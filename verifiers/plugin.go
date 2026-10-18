package verifiers

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/BTONGK/credence/internal/gitx"
	"github.com/BTONGK/credence/internal/home"
	"github.com/BTONGK/credence/spec"
)

// Plugin is an external verifier speaking JSON-RPC 2.0 over stdio, one
// request and response per line. Plugins are registered in the user's
// config, never by a repository.
//
//	-> {"jsonrpc":"2.0","id":1,"method":"describe"}
//	<- {"jsonrpc":"2.0","id":1,"result":{"name":"todos","tier":1,"checks":["no_new_todos"]}}
//	-> {"jsonrpc":"2.0","id":2,"method":"verify","params":{"claim_id":"clm_…","assertion":{...},
//	     "workspace":"/…/wt-3f2e9c1","budget_seconds":300,"claim":{...},"repo":"/path","commit":"…"}}
//	<- {"jsonrpc":"2.0","id":2,"result":{"result":"pass","outcome":"no_todos","details":"…",
//	     "artifacts":[{"kind":"report","locator":"file:todos.json"}],"verifier":"credence/todos@0.1.0"}}
//
// The workspace is a clean, detached worktree of the assertion's commit that
// Credence creates before the call and removes after it.
type Plugin struct {
	PluginName string
	Command    []string
	PluginTier int
	Checks     map[string]bool
	Timeout    time.Duration
}

// Name is the verifier id on the plugin's verdicts and in policy
// requirements: plugin:<name>, so a plugin can never pass for a built-in.
func (p *Plugin) Name() string { return "plugin:" + p.PluginName }
func (p *Plugin) Tier() int    { return p.PluginTier }

type rpcReq struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResp struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// RPCError is a JSON-RPC error a plugin returned.
type RPCError struct {
	Plugin  string
	Code    int
	Message string
}

func (e *RPCError) Error() string { return fmt.Sprintf("plugin %s: %s", e.Plugin, e.Message) }

// call starts the plugin, sends one request and reads its response.
func (p *Plugin) call(ctx context.Context, method string, params any, out any) error {
	if len(p.Command) == 0 {
		return errors.New("plugin has no command")
	}
	timeout := p.Timeout
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, p.Command[0], p.Command[1:]...)
	cmd.Env = TestEnv()
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	b, _ := json.Marshal(rpcReq{JSONRPC: "2.0", ID: 1, Method: method, Params: params})
	if _, err := in.Write(append(b, '\n')); err != nil {
		return err
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var r rpcResp
		if json.Unmarshal(sc.Bytes(), &r) != nil || r.ID != 1 {
			continue
		}
		if r.Error != nil {
			return &RPCError{Plugin: p.PluginName, Code: r.Error.Code, Message: r.Error.Message}
		}
		return json.Unmarshal(r.Result, out)
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return fmt.Errorf("plugin %s exited without a response", p.PluginName)
}

var pluginName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// LoadPlugin asks a plugin to describe itself.
func LoadPlugin(ctx context.Context, name string, command []string) (*Plugin, error) {
	p := &Plugin{PluginName: name, Command: command, PluginTier: 1, Checks: map[string]bool{}}
	var d struct {
		Name   string   `json:"name"`
		Tier   int      `json:"tier"`
		Checks []string `json:"checks"`
	}
	if err := p.call(ctx, "describe", nil, &d); err != nil {
		return nil, err
	}
	if pluginName.MatchString(d.Name) {
		p.PluginName = d.Name
	}
	if d.Tier == 1 || d.Tier == 2 {
		p.PluginTier = d.Tier
	}
	for _, c := range d.Checks {
		p.Checks[c] = true
	}
	return p, nil
}

func (p *Plugin) Verify(ctx context.Context, vc *Context, c *spec.Claim) []spec.Verdict {
	var out []spec.Verdict
	for _, a := range c.Assertions {
		if p.Checks[a.Check()] {
			out = append(out, p.verifyOne(ctx, vc, c, a))
		}
	}
	return out
}

// pluginResult is the result of a verify call.
type pluginResult struct {
	Result    string          `json:"result"`
	Outcome   string          `json:"outcome"`
	Details   string          `json:"details"`
	Verifier  string          `json:"verifier"`
	Artifacts []spec.Artifact `json:"artifacts"`
}

// verifyParams are the params of a verify call; workspace is added when
// there is a commit to check out.
func verifyParams(c *spec.Claim, a spec.Assertion, repo, commit string, budget int) map[string]any {
	return map[string]any{"claim_id": c.ID, "assertion": a, "budget_seconds": budget, "claim": c, "repo": repo, "commit": commit}
}

func (p *Plugin) verifyOne(ctx context.Context, vc *Context, c *spec.Claim, a spec.Assertion) spec.Verdict {
	start := time.Now()
	name := p.Name()
	repo := vc.Data.Session.Repo
	commit := claimCommit(vc.Data, c, a)
	budget := vc.Data.Policy.Tests.BudgetSeconds
	if budget <= 0 {
		budget = 300
	}
	params := verifyParams(c, a, repo, commit, budget)
	if commit != "" && repo != "" {
		wt, err := gitx.AddWorktree(ctx, repo, commit, home.Worktrees())
		if err != nil {
			return verdict(vc, c, name, p.PluginTier, a.Check(), spec.ResultInconclusive, "environment", "worktree: "+err.Error(), a, start)
		}
		defer wt.Remove()
		params["workspace"] = wt.Dir
	}
	if p.Timeout == 0 {
		p.Timeout = time.Duration(budget+30) * time.Second
	}
	var r pluginResult
	if err := p.call(ctx, "verify", params, &r); err != nil {
		return verdict(vc, c, name, p.PluginTier, a.Check(), spec.ResultInconclusive, "plugin_error", err.Error(), a, start)
	}
	switch r.Result {
	case spec.ResultPass, spec.ResultFail, spec.ResultInconclusive:
	default:
		r.Result = spec.ResultInconclusive
	}
	if r.Outcome == "" {
		r.Outcome = r.Result
	}
	details := strings.TrimSpace(r.Details)
	if r.Verifier != "" {
		details += " [" + r.Verifier + "]"
	}
	v := verdict(vc, c, name, p.PluginTier, a.Check(), r.Result, r.Outcome, details, a, start)
	if len(r.Artifacts) > 0 {
		v.Artifacts = r.Artifacts
		v.AssignID()
	}
	return v
}
