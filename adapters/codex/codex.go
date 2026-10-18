// Package codex is the Codex CLI harness adapter.
//
// Codex writes a rollout JSONL per session under $CODEX_HOME/sessions
// (default ~/.codex/sessions/YYYY/MM/DD/rollout-<ts>-<id>.jsonl). Records are
// {timestamp, type, payload} with types session_meta, turn_context,
// response_item and event_msg. Project hooks live in .codex/hooks.json.
// Ephemeral runs may have no rollout file; hooks plus the idle timeout still
// bound those sessions.
package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/BTONGK/credence/adapters"
	"github.com/BTONGK/credence/internal/home"
	"github.com/BTONGK/credence/spec"
)

// Name is the harness id.
const Name = "codex"

// HookEvents are the hooks Credence registers.
var HookEvents = []string{"SessionStart", "UserPromptSubmit", "PostToolUse", "Stop"}

// Adapter implements adapters.Adapter for Codex.
type Adapter struct{}

func init() { adapters.Register(Adapter{}) }

// Name returns the harness id.
func (Adapter) Name() string { return Name }

// Home is CODEX_HOME.
func Home() string {
	if d := os.Getenv("CODEX_HOME"); d != "" {
		return d
	}
	return filepath.Join(home.UserHome(), ".codex")
}

// Detect checks for the codex binary or home directory.
func (Adapter) Detect() adapters.Detection {
	p, err := exec.LookPath("codex")
	if err != nil {
		if _, err := os.Stat(filepath.Join(Home(), "sessions")); err == nil {
			return adapters.Detection{Installed: true, Notes: "sessions dir found; codex binary not on PATH"}
		}
		return adapters.Detection{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, p, "--version").Output()
	return adapters.Detection{Installed: true, Path: p, Version: strings.TrimSpace(string(out))}
}

func hooksPath(opts adapters.InstallOptions) string {
	if opts.Scope == adapters.ScopeUser || opts.Repo == "" {
		return filepath.Join(Home(), "hooks.json")
	}
	return filepath.Join(opts.Repo, ".codex", "hooks.json")
}

// Install writes hooks.json entries and registers the MCP server in config.toml.
func (Adapter) Install(opts adapters.InstallOptions) (adapters.InstallReport, error) {
	rep := adapters.InstallReport{Harness: Name}
	path := hooksPath(opts)
	m, err := adapters.ReadJSONFile(path)
	if err != nil {
		return rep, fmt.Errorf("%s: %w", path, err)
	}
	hooks, _ := m["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	cmd := adapters.HookCommand(opts.Binary, Name)
	for _, ev := range HookEvents {
		entries := strip(hooks[ev])
		hooks[ev] = append(entries, map[string]any{"hooks": []any{map[string]any{"type": "command", "command": cmd, "timeout": 5}}})
	}
	m["hooks"] = hooks
	if !opts.DryRun {
		if err := adapters.WriteJSONFile(path, m); err != nil {
			return rep, err
		}
	}
	rep.Files = append(rep.Files, path)
	rep.Actions = append(rep.Actions, fmt.Sprintf("registered %d hooks in %s", len(HookEvents), path))
	if opts.WithMCP {
		cfg := filepath.Join(Home(), "config.toml")
		if err := writeMCPBlock(cfg, opts.Binary, opts.DryRun); err != nil {
			rep.Warn = append(rep.Warn, "config.toml: "+err.Error())
		} else {
			rep.Files = append(rep.Files, cfg)
			rep.Actions = append(rep.Actions, "registered credence MCP server in "+cfg)
		}
	}
	return rep, nil
}

func strip(v any) []any {
	l, _ := v.([]any)
	var out []any
	for _, e := range l {
		m, ok := e.(map[string]any)
		if !ok {
			out = append(out, e)
			continue
		}
		var kept []any
		hs, _ := m["hooks"].([]any)
		for _, h := range hs {
			hm, _ := h.(map[string]any)
			if !adapters.IsManaged(adapters.Str(hm, "command")) {
				kept = append(kept, h)
			}
		}
		if adapters.IsManaged(adapters.Str(m, "command")) {
			continue
		}
		if len(kept) > 0 || len(hs) == 0 {
			if len(hs) > 0 {
				m["hooks"] = kept
			}
			out = append(out, m)
		}
	}
	return out
}

const (
	blockStart = "# >>> credence-managed"
	blockEnd   = "# <<< credence-managed"
)

var blockRe = regexp.MustCompile(`(?s)\n?# >>> credence-managed.*?# <<< credence-managed\n?`)

func writeMCPBlock(path, binary string, dry bool) error {
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	s := blockRe.ReplaceAllString(string(b), "\n")
	if strings.Contains(s, "[mcp_servers.credence]") {
		return fmt.Errorf("an unmanaged [mcp_servers.credence] table already exists")
	}
	block := fmt.Sprintf("%s\n[mcp_servers.credence]\ncommand = %q\nargs = [\"mcp\"]\n%s\n", blockStart, binary, blockEnd)
	s = strings.TrimRight(s, "\n") + "\n\n" + block
	if dry {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if len(b) > 0 {
		bak := path + ".credence-backup"
		if _, err := os.Stat(bak); os.IsNotExist(err) {
			_ = os.WriteFile(bak, b, 0o600)
		}
	}
	return os.WriteFile(path, []byte(strings.TrimLeft(s, "\n")), 0o644)
}

// Uninstall removes hooks and the MCP block.
func (Adapter) Uninstall(opts adapters.InstallOptions) (adapters.InstallReport, error) {
	rep := adapters.InstallReport{Harness: Name}
	path := hooksPath(opts)
	m, err := adapters.ReadJSONFile(path)
	if err != nil {
		return rep, err
	}
	if hooks, ok := m["hooks"].(map[string]any); ok {
		for ev, v := range hooks {
			if e := strip(v); len(e) == 0 {
				delete(hooks, ev)
			} else {
				hooks[ev] = e
			}
		}
		if len(hooks) == 0 {
			delete(m, "hooks")
		}
		if !opts.DryRun {
			if err := adapters.WriteJSONFile(path, m); err != nil {
				return rep, err
			}
		}
		rep.Actions = append(rep.Actions, "removed hooks from "+path)
	}
	if opts.WithMCP && !opts.DryRun {
		cfg := filepath.Join(Home(), "config.toml")
		if b, err := os.ReadFile(cfg); err == nil && blockRe.Match(b) {
			_ = os.WriteFile(cfg, blockRe.ReplaceAll(b, []byte("\n")), 0o644)
			rep.Actions = append(rep.Actions, "removed credence MCP server from "+cfg)
		}
	}
	return rep, nil
}

// Doctor checks static configuration.
func (a Adapter) Doctor(opts adapters.InstallOptions) []adapters.Health {
	d := a.Detect()
	hs := []adapters.Health{{Check: "codex installed", OK: d.Installed, Detail: d.Version + d.Notes, Fix: "install Codex CLI"}}
	m, _ := adapters.ReadJSONFile(hooksPath(opts))
	hooks, _ := m["hooks"].(map[string]any)
	n := 0
	for _, ev := range HookEvents {
		l, _ := hooks[ev].([]any)
		if len(l) > len(strip(hooks[ev])) {
			n++
		}
	}
	hs = append(hs, adapters.Health{Check: "codex hooks registered", OK: n == len(HookEvents),
		Detail: fmt.Sprintf("%d/%d hooks in %s (transcript watcher works without hooks)", n, len(HookEvents), hooksPath(opts)), Fix: "run `credence init`"})
	root := filepath.Join(Home(), "sessions")
	_, err := os.Stat(root)
	hs = append(hs, adapters.Health{Check: "codex rollouts readable", OK: err == nil, Detail: root})
	b, _ := os.ReadFile(filepath.Join(Home(), "config.toml"))
	hs = append(hs, adapters.Health{Check: "codex credence_claim MCP tool", OK: strings.Contains(string(b), "[mcp_servers.credence]"),
		Detail: "optional: enables self-claims", Fix: "run `credence init --mcp`"})
	return hs
}

// TranscriptRoots returns the sessions directory.
func (Adapter) TranscriptRoots() []string { return []string{filepath.Join(Home(), "sessions")} }

// IsTranscript reports whether path is a Codex rollout file.
func (a Adapter) IsTranscript(path string) bool {
	base := filepath.Base(path)
	if !strings.HasPrefix(base, "rollout-") || !strings.HasSuffix(base, ".jsonl") {
		return false
	}
	for _, r := range a.TranscriptRoots() {
		if adapters.UnderDir(path, r) {
			return true
		}
	}
	return false
}

// LocateTranscript returns the rollout path from a hook, if any.
func (Adapter) LocateTranscript(h adapters.HookRecord) string { return h.TranscriptPath }

// Boundaries declares session signals.
func (Adapter) Boundaries() adapters.Boundaries {
	return adapters.Boundaries{Start: []string{"SessionStart"}, TurnEnd: []string{"Stop"}, IdleTimeout: 10 * time.Minute}
}

// Capabilities declares what this adapter provides.
func (Adapter) Capabilities() adapters.Capabilities {
	return adapters.Capabilities{
		EventKinds: []string{spec.KindSessionStart, spec.KindPrompt, spec.KindMessage, spec.KindReasoning, spec.KindCommand,
			spec.KindToolResult, spec.KindFileEdit, spec.KindCommit, spec.KindClaimFiled, spec.KindSubagentStart, spec.KindCompaction},
		LineRanges: false, SubagentLinks: true, TokenCounts: true, SelfClaims: true, Hooks: true,
	}
}

// HookEventName returns the native event name.
func (Adapter) HookEventName(raw map[string]any) string {
	return adapters.Str(raw, "hook_event_name", "event", "hookEventName")
}

// NewParser returns a rollout parser.
func (Adapter) NewParser(path string) adapters.Parser {
	p := &Parser{tracker: adapters.NewCallTracker(), prompts: map[string]bool{}, results: map[string]bool{}}
	if m := rolloutID.FindStringSubmatch(filepath.Base(path)); m != nil {
		p.fileID = m[1]
	}
	return p
}

var rolloutID = regexp.MustCompile(`rollout-[0-9T:\-]+-([0-9a-fA-F-]{36})\.jsonl$`)

// Parser parses one rollout file.
type Parser struct {
	tracker *adapters.CallTracker
	fileID  string
	started bool
	model   string
	prompts map[string]bool
	results map[string]bool
	parent  string
}

// Parse handles one rollout line.
func (p *Parser) Parse(line []byte, lineNo int) (adapters.Parsed, error) {
	var out adapters.Parsed
	var rec map[string]any
	if err := json.Unmarshal(line, &rec); err != nil {
		return out, err
	}
	ts := adapters.ParseTime(adapters.Str(rec, "timestamp"))
	typ := adapters.Str(rec, "type")
	pl := adapters.Obj(rec, "payload")
	if pl == nil {
		// Very old rollouts wrote response items at the top level.
		pl, typ = rec, "response_item"
	}
	out.Meta.HarnessSessionID = p.fileID
	if p.model != "" {
		out.Meta.Model = p.model
	}
	switch typ {
	case "session_meta":
		p.sessionMeta(pl, ts, lineNo, &out)
	case "turn_context":
		out.Meta.Cwd = adapters.Str(pl, "cwd")
		if m := adapters.Str(pl, "model"); m != "" {
			p.model = m
			out.Meta.Model = m
		}
	case "response_item":
		p.responseItem(pl, ts, lineNo, &out)
	case "event_msg":
		p.eventMsg(pl, ts, lineNo, &out)
	case "compacted":
		out.Drafts = append(out.Drafts, adapters.Draft{TS: ts, Kind: spec.KindCompaction, Role: "system", SrcLine: lineNo, Payload: map[string]any{}})
	}
	return out, nil
}

func (p *Parser) sessionMeta(pl map[string]any, ts time.Time, lineNo int, out *adapters.Parsed) {
	if id := adapters.Str(pl, "id"); id != "" {
		p.fileID = id
		out.Meta.HarnessSessionID = id
	}
	out.Meta.Cwd = adapters.Str(pl, "cwd")
	out.Meta.Version = adapters.Str(pl, "cli_version")
	if g := adapters.Obj(pl, "git"); g != nil {
		out.Meta.Branch = adapters.Str(g, "branch")
	}
	if t := adapters.ParseTime(adapters.Str(pl, "timestamp")); !t.IsZero() {
		ts = t
	}
	// Subagent threads point at their parent.
	if src := adapters.Obj(pl, "source"); src != nil {
		if sa := adapters.Obj(src, "subagent"); sa != nil {
			if spawn := adapters.Obj(sa, "thread_spawn"); spawn != nil {
				p.parent = adapters.Str(spawn, "parent_thread_id")
			}
			if out.Meta.AgentType == "" {
				out.Meta.AgentType = adapters.Str(sa, "type", "agent_type")
			}
		}
	}
	if p.parent != "" {
		out.Meta.ParentID = p.parent
	}
	if adapters.Str(pl, "originator") == "credence_auditor" {
		out.Meta.Internal = true
	}
	if !p.started {
		p.started = true
		out.Meta.StartedAt = ts
		out.Drafts = append(out.Drafts, adapters.Draft{TS: ts, Kind: spec.KindSessionStart, Role: "system", SrcLine: lineNo,
			Payload: map[string]any{"cwd": out.Meta.Cwd, "branch": out.Meta.Branch, "version": out.Meta.Version, "originator": adapters.Str(pl, "originator")}})
	}
}

func contentText(pl map[string]any) string {
	var parts []string
	for _, c := range adapters.Arr(pl, "content") {
		if m, ok := c.(map[string]any); ok {
			if t := adapters.Str(m, "text"); t != "" {
				parts = append(parts, t)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func (p *Parser) prompt(text string, ts time.Time, lineNo int, out *adapters.Parsed) {
	t := strings.TrimSpace(text)
	if t == "" || strings.HasPrefix(t, "<environment_context>") || strings.HasPrefix(t, "<user_instructions>") ||
		strings.HasPrefix(t, "# AGENTS.md") || strings.HasPrefix(t, "<permissions") {
		return
	}
	key := spec.HashBytes([]byte(t))
	if p.prompts[key] {
		return
	}
	p.prompts[key] = true
	out.Drafts = append(out.Drafts, adapters.Draft{TS: ts, Kind: spec.KindPrompt, Role: "human", SrcLine: lineNo,
		Payload: map[string]any{"text": adapters.Truncate(t, adapters.MaxText)}})
}

func (p *Parser) responseItem(pl map[string]any, ts time.Time, lineNo int, out *adapters.Parsed) {
	switch adapters.Str(pl, "type") {
	case "message":
		switch adapters.Str(pl, "role") {
		case "user":
			p.prompt(contentText(pl), ts, lineNo, out)
		case "assistant":
			if t := strings.TrimSpace(contentText(pl)); t != "" {
				out.Drafts = append(out.Drafts, adapters.Draft{TS: ts, Kind: spec.KindMessage, Role: "agent", SrcLine: lineNo,
					Payload: map[string]any{"text": adapters.Truncate(t, adapters.MaxText), "model": p.model}})
			}
		}
	case "reasoning":
		var parts []string
		for _, s := range adapters.Arr(pl, "summary") {
			if m, ok := s.(map[string]any); ok {
				parts = append(parts, adapters.Str(m, "text"))
			}
		}
		if t := strings.TrimSpace(strings.Join(parts, "\n")); t != "" {
			out.Drafts = append(out.Drafts, adapters.Draft{TS: ts, Kind: spec.KindReasoning, Role: "agent", SrcLine: lineNo,
				Payload: map[string]any{"text": adapters.Truncate(t, 3000)}})
		}
	case "function_call":
		var input map[string]any
		args := adapters.Str(pl, "arguments")
		if json.Unmarshal([]byte(args), &input) != nil {
			input = map[string]any{"_raw": args}
		}
		name := adapters.Str(pl, "name")
		if name == "container.exec" {
			name = "shell"
		}
		out.Drafts = append(out.Drafts, p.tracker.Call(adapters.Str(pl, "call_id", "id"), name, input, ts, "agent", lineNo)...)
	case "custom_tool_call":
		name := adapters.Str(pl, "name")
		input := map[string]any{"input": adapters.Str(pl, "input")}
		out.Drafts = append(out.Drafts, p.tracker.Call(adapters.Str(pl, "call_id", "id"), name, input, ts, "agent", lineNo)...)
	case "local_shell_call":
		action := adapters.Obj(pl, "action")
		input := map[string]any{}
		if action != nil {
			input["command"] = action["command"]
			input["workdir"] = adapters.Str(action, "working_directory")
		}
		out.Drafts = append(out.Drafts, p.tracker.Call(adapters.Str(pl, "call_id", "id"), "local_shell", input, ts, "agent", lineNo)...)
	case "function_call_output", "custom_tool_call_output":
		id := adapters.Str(pl, "call_id")
		if p.results[id] {
			return
		}
		p.results[id] = true
		out.Drafts = append(out.Drafts, p.tracker.Result(id, parseOutput(pl["output"]), ts, lineNo)...)
	}
}

func parseOutput(v any) adapters.ResultInfo {
	var s string
	switch t := v.(type) {
	case string:
		s = t
	case map[string]any:
		b, _ := json.Marshal(t)
		s = string(b)
	}
	r := adapters.ResultInfo{Output: s}
	var obj map[string]any
	if json.Unmarshal([]byte(s), &obj) == nil {
		if o := adapters.Str(obj, "output"); o != "" || obj["output"] != nil {
			r.Output = o
		}
		if md := adapters.Obj(obj, "metadata"); md != nil {
			if _, ok := md["exit_code"]; ok {
				n := int(adapters.Num(md, "exit_code"))
				r.ExitCode = &n
			}
		}
	}
	if r.ExitCode == nil {
		if n, ok := adapters.ExitCodeFromText(s); ok {
			r.ExitCode = &n
		}
	}
	if r.ExitCode != nil && *r.ExitCode != 0 {
		r.IsError = true
	}
	return r
}

func (p *Parser) eventMsg(pl map[string]any, ts time.Time, lineNo int, out *adapters.Parsed) {
	switch adapters.Str(pl, "type") {
	case "user_message":
		p.prompt(adapters.Str(pl, "message"), ts, lineNo, out)
	case "token_count":
		if info := adapters.Obj(pl, "info"); info != nil {
			if tot := adapters.Obj(info, "total_token_usage"); tot != nil {
				out.Meta.AbsoluteTokens = true
				cached := int64(adapters.Num(tot, "cached_input_tokens"))
				out.Meta.TokensIn = int64(adapters.Num(tot, "input_tokens")) - cached
				out.Meta.CacheRead = cached
				out.Meta.TokensOut = int64(adapters.Num(tot, "output_tokens"))
			}
		}
	case "exec_command_end":
		id := adapters.Str(pl, "call_id")
		if id == "" || p.results[id] {
			return
		}
		p.results[id] = true
		n := int(adapters.Num(pl, "exit_code"))
		output := adapters.Str(pl, "aggregated_output", "formatted_output")
		if output == "" {
			output = adapters.Str(pl, "stdout") + adapters.Str(pl, "stderr")
		}
		out.Drafts = append(out.Drafts, p.tracker.Result(id, adapters.ResultInfo{Output: output, ExitCode: &n, IsError: n != 0}, ts, lineNo)...)
	case "mcp_tool_call_begin":
		inv := adapters.Obj(pl, "invocation")
		if inv == nil {
			return
		}
		id := adapters.Str(pl, "call_id")
		if p.tracker.Lookup(id) != nil {
			return
		}
		args, _ := inv["arguments"].(map[string]any)
		name := "mcp__" + adapters.Str(inv, "server") + "__" + adapters.Str(inv, "tool")
		out.Drafts = append(out.Drafts, p.tracker.Call(id, name, args, ts, "agent", lineNo)...)
	case "mcp_tool_call_end":
		id := adapters.Str(pl, "call_id")
		if id == "" || p.results[id] {
			return
		}
		p.results[id] = true
		b, _ := json.Marshal(pl["result"])
		isErr := strings.Contains(string(b), `"Err"`)
		out.Drafts = append(out.Drafts, p.tracker.Result(id, adapters.ResultInfo{Output: string(b), IsError: isErr}, ts, lineNo)...)
	}
}
