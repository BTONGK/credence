package verifiers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/BTONGK/credence/internal/gitx"
	"github.com/BTONGK/credence/internal/home"
	"github.com/BTONGK/credence/spec"
)

// CheckStep is one plugin conformance check.
type CheckStep struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Warn   bool   `json:"warn,omitempty"` // advisory: the plugin works but deviates
	Detail string `json:"detail,omitempty"`
}

// builtinChecks are handled by Credence itself; a plugin that declares one
// runs alongside the built-in verifier rather than replacing it.
var builtinChecks = map[string]bool{
	"commit_in_session": true, "commit_exists": true, "commit_reachable": true, "commit_touches": true, "file_exists": true,
	"test_passes": true, "test_fails_before_passes_after": true, "command_passes": true, "command_fails_before_passes_after": true,
	"mutation_score": true, "ci_green": true, "pr_merged": true, "issue_closed": true,
}

// CheckPlugin runs the protocol conformance checks of spec section 11
// against a plugin command: describe, a verify call on a scratch repository
// and the response to an unknown method. The plugin is returned when its
// describe response is usable.
func CheckPlugin(ctx context.Context, command []string) ([]CheckStep, *Plugin) {
	var steps []CheckStep
	add := func(name string, ok bool, detail string) bool {
		steps = append(steps, CheckStep{Name: name, OK: ok, Detail: detail})
		return ok
	}
	warn := func(name, detail string) {
		steps = append(steps, CheckStep{Name: name, OK: true, Warn: true, Detail: detail})
	}

	probe := &Plugin{PluginName: "plugin", Command: command, Timeout: 10 * time.Second}
	var d struct {
		Name   string   `json:"name"`
		Tier   int      `json:"tier"`
		Checks []string `json:"checks"`
	}
	start := time.Now()
	if err := probe.call(ctx, "describe", nil, &d); err != nil {
		add("describe responds within 10s", false, err.Error())
		return steps, nil
	}
	add("describe responds within 10s", true, fmt.Sprintf("%d ms", time.Since(start).Milliseconds()))
	nameOK := add("name is a lowercase id", pluginName.MatchString(d.Name), fmt.Sprintf("%q", d.Name))
	tierOK := add("tier is 1 or 2", d.Tier == 1 || d.Tier == 2, fmt.Sprintf("tier %d", d.Tier))
	var checks []string
	for _, c := range d.Checks {
		if c = strings.TrimSpace(c); c != "" {
			checks = append(checks, c)
		}
	}
	checksOK := add("declares at least one check", len(checks) > 0, strings.Join(checks, ", "))
	if !nameOK || !tierOK || !checksOK {
		return steps, nil
	}
	for _, c := range checks {
		if builtinChecks[c] {
			warn("checks do not shadow built-ins", c+" is also verified by Credence; both verdicts will be recorded")
		}
	}
	p := &Plugin{PluginName: d.Name, Command: command, PluginTier: d.Tier, Checks: map[string]bool{}}
	if p.PluginTier != 2 {
		p.PluginTier = 1
	}
	for _, c := range checks {
		p.Checks[c] = true
	}

	// verify against a throwaway repository with one commit.
	repo, sha, err := scratchRepo()
	if err != nil {
		add("verify returns a result", false, "could not create a scratch repository: "+err.Error())
		return steps, p
	}
	defer os.RemoveAll(repo)
	a := spec.Assertion{"check": checks[0], "commit": sha}
	c := &spec.Claim{Spec: spec.ClaimSpec, SessionID: "ses_conformance", Claimant: "agent:conformance/test", Origin: spec.OriginSelf,
		Type: spec.TypeFix, Title: "Conformance probe", Statement: "The scratch commit adds hello.txt.",
		Assertions: []spec.Assertion{a}, Evidence: []spec.Evidence{{Target: "git:commit/" + sha[:7], Relation: spec.RelProduced}}, CreatedAt: time.Now().UTC()}
	c.AssignID()
	params := verifyParams(c, a, repo, sha, 30)
	wt, err := gitx.AddWorktree(ctx, repo, sha, home.Worktrees())
	if err != nil {
		add("verify returns a result", false, "worktree: "+err.Error())
		return steps, p
	}
	params["workspace"] = wt.Dir
	var r pluginResult
	p.Timeout = 40 * time.Second
	start = time.Now()
	err = p.call(ctx, "verify", params, &r)
	wt.Remove()
	if err != nil {
		add("verify returns a result within budget_seconds", false, err.Error())
		return steps, p
	}
	add("verify returns a result within budget_seconds", true, fmt.Sprintf("%s in %d ms", strings.TrimSuffix(r.Result+"/"+r.Outcome, "/"), time.Since(start).Milliseconds()))
	switch r.Result {
	case spec.ResultPass, spec.ResultFail, spec.ResultInconclusive:
		add("result is pass, fail or inconclusive", true, r.Result)
	default:
		add("result is pass, fail or inconclusive", false, fmt.Sprintf("%q (Credence records it as inconclusive)", r.Result))
	}
	if strings.TrimSpace(r.Details) == "" {
		warn("details explain the result", "details is empty; reviewers see only the result")
	} else {
		add("details explain the result", true, clipCmd(r.Details))
	}
	for _, art := range r.Artifacts {
		if art.Kind == "" || art.Locator == "" {
			add("artifacts have kind and locator", false, fmt.Sprintf("%+v", art))
		}
	}

	// Unknown methods should be JSON-RPC errors, not hangs.
	p.Timeout = 5 * time.Second
	var ignored any
	err = p.call(ctx, "credence.conformance/unknown", nil, &ignored)
	var rpc *RPCError
	switch {
	case errors.As(err, &rpc):
		add("unknown methods return a JSON-RPC error", true, fmt.Sprintf("code %d", rpc.Code))
	case err == nil:
		warn("unknown methods return a JSON-RPC error", "returned a result instead")
	default:
		warn("unknown methods return a JSON-RPC error", err.Error())
	}
	return steps, p
}

// Conforms reports whether every non-advisory step passed.
func Conforms(steps []CheckStep) bool {
	for _, s := range steps {
		if !s.OK {
			return false
		}
	}
	return len(steps) > 0
}

func scratchRepo() (string, string, error) {
	dir, err := os.MkdirTemp("", "credence-conformance-")
	if err != nil {
		return "", "", err
	}
	git := func(args ...string) (string, error) {
		cmd := exec.Command("git", append([]string{"-c", "user.name=Credence", "-c", "user.email=conformance@credence.invalid",
			"-c", "commit.gpgsign=false", "-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	steps := [][]string{{"init", "-q"}, {"commit", "-q", "--allow-empty", "-m", "base"}}
	for _, s := range steps {
		if out, err := git(s...); err != nil {
			return dir, "", fmt.Errorf("git %s: %s", s[0], out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello\n"), 0o644); err != nil {
		return dir, "", err
	}
	for _, s := range [][]string{{"add", "hello.txt"}, {"commit", "-q", "-m", "Add hello.txt"}} {
		if out, err := git(s...); err != nil {
			return dir, "", fmt.Errorf("git %s: %s", s[0], out)
		}
	}
	sha, err := git("rev-parse", "HEAD")
	return dir, sha, err
}
