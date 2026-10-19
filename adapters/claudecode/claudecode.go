// Package claudecode is the Claude Code harness adapter.
//
// Hooks append a small record to the spool and exit 0; the real capture is
// the session transcript (~/.claude/projects/<slug>/<session>.jsonl), which
// Claude Code writes asynchronously. Subagent runs live in
// <session>/subagents/agent-<id>.jsonl and become child sessions.
package claudecode

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
const Name = "claude-code"

// HookEvents are the hooks Credence registers.
var HookEvents = []string{"SessionStart", "UserPromptSubmit", "PostToolUse", "Stop", "SubagentStop", "SessionEnd"}

// Adapter implements adapters.Adapter for Claude Code.
type Adapter struct{}

func init() { adapters.Register(Adapter{}) }

// Name returns the harness id.
func (Adapter) Name() string { return Name }

// ConfigDir is Claude Code's config directory.
func ConfigDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	return filepath.Join(home.UserHome(), ".claude")
}

// Detect checks for the claude binary.
func (Adapter) Detect() adapters.Detection {
	p, err := exec.LookPath("claude")
	if err != nil {
		if _, err := os.Stat(ConfigDir()); err == nil {
			return adapters.Detection{Installed: true, Notes: "config dir found; claude binary not on PATH"}
		}
		return adapters.Detection{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, p, "--version").Output()
	return adapters.Detection{Installed: true, Path: p, Version: strings.TrimSpace(string(out))}
}

func settingsPath(opts adapters.InstallOptions) string {
	switch opts.Scope {
	case adapters.ScopeProject:
		return filepath.Join(opts.Repo, ".claude", "settings.json")
	case adapters.ScopeLocal:
		return filepath.Join(opts.Repo, ".claude", "settings.local.json")
	}
	return filepath.Join(ConfigDir(), "settings.json")
}

// Install merges Credence's hooks into settings.json and registers the MCP server.
func (a Adapter) Install(opts adapters.InstallOptions) (adapters.InstallReport, error) {
	rep := adapters.InstallReport{Harness: Name}
	path := settingsPath(opts)
	settings, err := adapters.ReadJSONFile(path)
	if err != nil {
		return rep, fmt.Errorf("%s: %w", path, err)
	}
	MergeHooks(settings, adapters.HookCommand(opts.Binary, Name))
	if !opts.DryRun {
		if err := adapters.WriteJSONFile(path, settings); err != nil {
			return rep, err
		}
	}
	rep.Files = append(rep.Files, path)
	rep.Actions = append(rep.Actions, fmt.Sprintf("registered %d hooks in %s", len(HookEvents), path))
	if opts.Scope == adapters.ScopeUser || opts.Scope == "" {
		act, err := InstallSkill(opts.Binary, opts.DryRun)
		if err != nil {
			rep.Warn = append(rep.Warn, "the /credence skill: "+err.Error())
		} else {
			rep.Files = append(rep.Files, SkillFile())
			rep.Actions = append(rep.Actions, act)
		}
	}
	if opts.WithMCP {
		act, warn := registerMCP(opts)
		if act != "" {
			rep.Actions = append(rep.Actions, act)
		}
		if warn != "" {
			rep.Warn = append(rep.Warn, warn)
		}
	}
	return rep, nil
}

// MergeHooks adds Credence hook entries to a settings object, replacing older ones.
func MergeHooks(settings map[string]any, command string) {
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	for _, ev := range HookEvents {
		entries := removeManaged(asList(hooks[ev]))
		entry := map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command, "timeout": 5}}}
		if ev == "PostToolUse" {
			entry["matcher"] = "*"
		}
		hooks[ev] = append(entries, entry)
	}
	settings["hooks"] = hooks
}

// RemoveHooks strips Credence hook entries from a settings object.
func RemoveHooks(settings map[string]any) int {
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		return 0
	}
	removed := 0
	for ev, v := range hooks {
		before := countManaged(asList(v))
		entries := removeManaged(asList(v))
		removed += before
		if len(entries) == 0 {
			delete(hooks, ev)
		} else {
			hooks[ev] = entries
		}
	}
	if len(hooks) == 0 {
		delete(settings, "hooks")
	}
	return removed
}

func asList(v any) []any {
	if l, ok := v.([]any); ok {
		return l
	}
	return nil
}

func countManaged(entries []any) int {
	n := 0
	for _, e := range entries {
		m, _ := e.(map[string]any)
		for _, h := range asList(m["hooks"]) {
			hm, _ := h.(map[string]any)
			if adapters.IsManaged(adapters.Str(hm, "command")) {
				n++
			}
		}
	}
	return n
}

func removeManaged(entries []any) []any {
	var out []any
	for _, e := range entries {
		m, ok := e.(map[string]any)
		if !ok {
			out = append(out, e)
			continue
		}
		var kept []any
		for _, h := range asList(m["hooks"]) {
			hm, _ := h.(map[string]any)
			if !adapters.IsManaged(adapters.Str(hm, "command")) {
				kept = append(kept, h)
			}
		}
		if len(kept) > 0 {
			m["hooks"] = kept
			out = append(out, m)
		}
	}
	return out
}

func registerMCP(opts adapters.InstallOptions) (string, string) {
	if opts.Scope == adapters.ScopeProject || opts.Scope == adapters.ScopeLocal || opts.SkipMCPCLI {
		path := filepath.Join(opts.Repo, ".mcp.json")
		if opts.Scope == adapters.ScopeUser {
			return "", "MCP server not registered: claude CLI unavailable; run `claude mcp add --scope user credence -- credence mcp`"
		}
		m, err := adapters.ReadJSONFile(path)
		if err != nil {
			return "", err.Error()
		}
		servers, _ := m["mcpServers"].(map[string]any)
		if servers == nil {
			servers = map[string]any{}
		}
		servers["credence"] = map[string]any{"command": opts.Binary, "args": []any{"mcp"}}
		m["mcpServers"] = servers
		if !opts.DryRun {
			if err := adapters.WriteJSONFile(path, m); err != nil {
				return "", err.Error()
			}
		}
		return "registered credence MCP server in " + path, ""
	}
	claude, err := exec.LookPath("claude")
	if err != nil {
		return "", "MCP server not registered: claude CLI not on PATH; run `claude mcp add --scope user credence -- credence mcp`"
	}
	if opts.DryRun {
		return "would run: claude mcp add --scope user credence -- " + opts.Binary + " mcp", ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, claude, "mcp", "remove", "--scope", "user", "credence").Run()
	out, err := exec.CommandContext(ctx, claude, "mcp", "add", "--scope", "user", "credence", "--", opts.Binary, "mcp").CombinedOutput()
	if err != nil {
		return "", "claude mcp add failed: " + strings.TrimSpace(string(out))
	}
	return "registered credence MCP server (user scope) via claude mcp add", ""
}

// Uninstall removes Credence's hooks and MCP registration.
func (a Adapter) Uninstall(opts adapters.InstallOptions) (adapters.InstallReport, error) {
	rep := adapters.InstallReport{Harness: Name}
	path := settingsPath(opts)
	settings, err := adapters.ReadJSONFile(path)
	if err != nil {
		return rep, err
	}
	n := RemoveHooks(settings)
	if n > 0 && !opts.DryRun {
		if err := adapters.WriteJSONFile(path, settings); err != nil {
			return rep, err
		}
	}
	rep.Files = append(rep.Files, path)
	rep.Actions = append(rep.Actions, fmt.Sprintf("removed %d hooks from %s", n, path))
	if (opts.Scope == adapters.ScopeUser || opts.Scope == "") && SkillInstalled() && !opts.DryRun {
		if os.RemoveAll(filepath.Dir(SkillFile())) == nil {
			rep.Actions = append(rep.Actions, "removed the /credence skill")
		}
	}
	if opts.WithMCP && opts.Scope == adapters.ScopeUser && !opts.DryRun {
		if claude, err := exec.LookPath("claude"); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if exec.CommandContext(ctx, claude, "mcp", "remove", "--scope", "user", "credence").Run() == nil {
				rep.Actions = append(rep.Actions, "removed credence MCP server")
			}
		}
	}
	return rep, nil
}

// Doctor checks static configuration.
func (a Adapter) Doctor(opts adapters.InstallOptions) []adapters.Health {
	var hs []adapters.Health
	d := a.Detect()
	hs = append(hs, adapters.Health{Check: "claude-code installed", OK: d.Installed, Detail: d.Version + d.Notes,
		Fix: "install Claude Code: https://docs.claude.com/claude-code"})
	found := 0
	where := ""
	for _, sc := range []adapters.Scope{adapters.ScopeUser, adapters.ScopeProject, adapters.ScopeLocal} {
		if sc != adapters.ScopeUser && opts.Repo == "" {
			continue
		}
		o := opts
		o.Scope = sc
		s, err := adapters.ReadJSONFile(settingsPath(o))
		if err != nil {
			continue
		}
		hooks, _ := s["hooks"].(map[string]any)
		n := 0
		for _, ev := range HookEvents {
			if countManaged(asList(hooks[ev])) > 0 {
				n++
			}
		}
		if n > found {
			found, where = n, settingsPath(o)
		}
	}
	hs = append(hs, adapters.Health{Check: "claude-code hooks registered", OK: found == len(HookEvents),
		Detail: fmt.Sprintf("%d/%d hooks in %s", found, len(HookEvents), orNone(where)), Fix: "run `credence init`"})
	root := filepath.Join(ConfigDir(), "projects")
	_, err := os.Stat(root)
	hs = append(hs, adapters.Health{Check: "claude-code transcripts readable", OK: err == nil, Detail: root})
	hs = append(hs, adapters.Health{Check: "/credence skill installed", OK: SkillInstalled(), Detail: SkillFile(),
		Fix: "run `credence setup`"})
	mcp := mcpRegistered(opts)
	hs = append(hs, adapters.Health{Check: "credence_claim MCP tool registered", OK: mcp, Detail: "optional: enables self-claims",
		Fix: "run `credence init` with the claude CLI on your PATH (auditor-only mode works without it)", Optional: true})
	return hs
}

// skillMarker identifies the skill file Credence wrote.
const skillMarker = "credence-managed"

// SkillFile is the /credence skill's path.
func SkillFile() string { return filepath.Join(ConfigDir(), "skills", "credence", "SKILL.md") }

// SkillInstalled reports whether Credence's /credence skill is in place.
func SkillInstalled() bool {
	b, err := os.ReadFile(SkillFile())
	return err == nil && strings.Contains(string(b), skillMarker)
}

// Skill is the /credence skill. Claude Code runs `credence check` for the
// current session when the skill loads, and Claude relays the result.
// Claude can also load it on its own when someone asks whether the work
// is really done.
func Skill(bin string) string {
	cmd := bin
	if strings.ContainsAny(bin, " '\"$`") {
		cmd = "'" + strings.ReplaceAll(bin, "'", `'\''`) + "'"
	}
	return `---
name: credence
description: Check what Claude said it did in this session against the session record. Credence marks each claim as backed by evidence, partly backed, contradicted, or yours to confirm.
when_to_use: Use when the user types /credence, asks whether the work is really done, asks to check or verify what was claimed, or asks for a Credence report.
allowed-tools: Bash(` + bin + ` check *)
---
<!-- ` + skillMarker + `: written by credence setup; credence uninstall removes it. -->

Credence checked this session's claims against the session record:

!` + "`" + cmd + ` check --harness claude-code --session=${CLAUDE_SESSION_ID}` + "`" + `

Show the user Credence's results above as they are: every claim with its verdict and reason, then the totals line and the line about the full report. Keep Credence's wording. When a claim is contradicted, say so plainly and offer to fix it. When a claim is yours to confirm, tell the user what to look at to confirm it.
`
}

// InstallSkill writes the /credence skill; dry describes what it would do.
func InstallSkill(bin string, dry bool) (string, error) {
	p := SkillFile()
	if b, err := os.ReadFile(p); err == nil && !strings.Contains(string(b), skillMarker) {
		return "", fmt.Errorf("%s belongs to another tool; it stays as it is", p)
	}
	act := "installed the /credence skill in " + filepath.Dir(p)
	if dry {
		return act, nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	return act, os.WriteFile(p, []byte(Skill(bin)), 0o644)
}

func orNone(s string) string {
	if s == "" {
		return "no settings file"
	}
	return s
}

func mcpRegistered(opts adapters.InstallOptions) bool {
	if opts.Repo != "" {
		if m, err := adapters.ReadJSONFile(filepath.Join(opts.Repo, ".mcp.json")); err == nil {
			if s, ok := m["mcpServers"].(map[string]any); ok && s["credence"] != nil {
				return true
			}
		}
	}
	b, err := os.ReadFile(filepath.Join(home.UserHome(), ".claude.json"))
	if err != nil {
		return false
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return false
	}
	if s, ok := m["mcpServers"].(map[string]any); ok && s["credence"] != nil {
		return true
	}
	return false
}

// TranscriptRoots returns the projects directory.
func (Adapter) TranscriptRoots() []string { return []string{filepath.Join(ConfigDir(), "projects")} }

// IsTranscript reports whether path is a Claude Code transcript.
func (a Adapter) IsTranscript(path string) bool {
	if !strings.HasSuffix(path, ".jsonl") {
		return false
	}
	for _, r := range a.TranscriptRoots() {
		if adapters.UnderDir(path, r) {
			return true
		}
	}
	return false
}

// LocateTranscript returns the transcript named by a hook.
func (Adapter) LocateTranscript(h adapters.HookRecord) string {
	if len(h.Extra) > 0 {
		var x map[string]any
		if json.Unmarshal(h.Extra, &x) == nil {
			if p := adapters.Str(x, "agent_transcript_path"); p != "" && h.Event == "SubagentStop" {
				return p
			}
		}
	}
	return h.TranscriptPath
}

// Boundaries declares session signals.
func (Adapter) Boundaries() adapters.Boundaries {
	return adapters.Boundaries{Start: []string{"SessionStart"}, End: []string{"SessionEnd"}, TurnEnd: []string{"Stop"}, IdleTimeout: 10 * time.Minute}
}

// Capabilities declares what this adapter provides.
func (Adapter) Capabilities() adapters.Capabilities {
	return adapters.Capabilities{
		EventKinds: []string{spec.KindSessionStart, spec.KindPrompt, spec.KindMessage, spec.KindReasoning, spec.KindCommand,
			spec.KindToolResult, spec.KindFileEdit, spec.KindFileRead, spec.KindSearch, spec.KindSubagentStart,
			spec.KindSubagentStop, spec.KindCommit, spec.KindClaimFiled, spec.KindCompaction, spec.KindSessionEnd},
		LineRanges: true, SubagentLinks: true, TokenCounts: true, SelfClaims: true, Hooks: true,
	}
}

// HookEventName returns the native event name.
func (Adapter) HookEventName(raw map[string]any) string { return adapters.Str(raw, "hook_event_name") }

// NewParser returns a transcript parser.
func (Adapter) NewParser(path string) adapters.Parser {
	p := &Parser{tracker: adapters.NewCallTracker(), seenMsg: map[string]bool{}}
	if m := subagentPath.FindStringSubmatch(filepath.ToSlash(path)); m != nil {
		p.parentID, p.agentID = m[1], m[2]
	}
	return p
}

var subagentPath = regexp.MustCompile(`/([0-9a-fA-F-]{36})/subagents/agent-([^/]+)\.jsonl$`)

// Parser parses one Claude Code transcript.
type Parser struct {
	tracker   *adapters.CallTracker
	seenMsg   map[string]bool
	parentID  string
	agentID   string
	started   bool
	firstUser bool
}

func (p *Parser) agentRole(sidechain bool) string {
	if p.parentID != "" || sidechain {
		return "subagent"
	}
	return "agent"
}

// Parse handles one transcript line.
func (p *Parser) Parse(line []byte, lineNo int) (adapters.Parsed, error) {
	var out adapters.Parsed
	var m map[string]any
	if err := json.Unmarshal(line, &m); err != nil {
		return out, err
	}
	typ := adapters.Str(m, "type")
	ts := adapters.ParseTime(adapters.Str(m, "timestamp"))
	meta := &out.Meta
	if sid := adapters.Str(m, "sessionId", "session_id"); sid != "" {
		if p.parentID != "" {
			meta.HarnessSessionID = sid + ":" + p.agentID
			meta.ParentID = sid
		} else {
			meta.HarnessSessionID = sid
		}
	}
	if p.parentID != "" && meta.HarnessSessionID == "" {
		meta.HarnessSessionID = p.parentID + ":" + p.agentID
		meta.ParentID = p.parentID
	}
	meta.Cwd = adapters.Str(m, "cwd")
	meta.Branch = adapters.Str(m, "gitBranch")
	meta.Version = adapters.Str(m, "version")
	if at := adapters.Str(m, "agentType", "subagent_type"); at != "" {
		meta.AgentType = at
	}
	if !p.started && !ts.IsZero() && (typ == "user" || typ == "assistant") {
		p.started = true
		meta.StartedAt = ts
		out.Drafts = append(out.Drafts, adapters.Draft{TS: ts, Kind: spec.KindSessionStart, Role: "system", SrcLine: lineNo,
			Payload: map[string]any{"cwd": meta.Cwd, "branch": meta.Branch, "version": meta.Version}})
	}
	sidechain, _ := m["isSidechain"].(bool)
	switch typ {
	case "user":
		p.parseUser(m, ts, lineNo, sidechain, &out)
	case "assistant":
		p.parseAssistant(m, ts, lineNo, sidechain, &out)
	case "summary":
		meta.Title = adapters.Str(m, "summary")
	case "ai-title":
		meta.Title = adapters.Str(m, "aiTitle")
	case "system":
		if st := adapters.Str(m, "subtype"); st == "compact_boundary" {
			out.Drafts = append(out.Drafts, adapters.Draft{TS: ts, Kind: spec.KindCompaction, Role: "system", SrcLine: lineNo, Payload: map[string]any{}})
		}
	}
	return out, nil
}

var (
	commandName  = regexp.MustCompile(`<command-name>([^<]*)</command-name>`)
	commandArgs  = regexp.MustCompile(`<command-args>([\s\S]*?)</command-args>`)
	systemRemind = regexp.MustCompile(`<system-reminder>[\s\S]*?</system-reminder>`)
)

func (p *Parser) parseUser(m map[string]any, ts time.Time, lineNo int, sidechain bool, out *adapters.Parsed) {
	if meta, _ := m["isMeta"].(bool); meta {
		return
	}
	if cs, _ := m["isCompactSummary"].(bool); cs {
		out.Drafts = append(out.Drafts, adapters.Draft{TS: ts, Kind: spec.KindCompaction, Role: "system", SrcLine: lineNo, Payload: map[string]any{"summary": true}})
		return
	}
	msg := adapters.Obj(m, "message")
	if msg == nil {
		return
	}
	role := "human"
	if p.parentID != "" || sidechain {
		// In a subagent transcript the "user" turn is the parent agent's task.
		role = "agent"
	}
	extra := adapters.Obj(m, "toolUseResult")
	switch c := msg["content"].(type) {
	case string:
		p.userText(c, ts, lineNo, role, out)
	case []any:
		for _, b := range c {
			blk, _ := b.(map[string]any)
			switch adapters.Str(blk, "type") {
			case "text":
				p.userText(adapters.Str(blk, "text"), ts, lineNo, role, out)
			case "tool_result":
				isErr, _ := blk["is_error"].(bool)
				out.Drafts = append(out.Drafts, p.tracker.Result(adapters.Str(blk, "tool_use_id"), adapters.ResultInfo{
					Output: flatten(blk["content"]), IsError: isErr, Extra: extra,
				}, ts, lineNo)...)
			}
		}
	}
}

func (p *Parser) userText(text string, ts time.Time, lineNo int, role string, out *adapters.Parsed) {
	t := strings.TrimSpace(systemRemind.ReplaceAllString(text, ""))
	if t == "" {
		return
	}
	switch {
	case strings.HasPrefix(t, "<local-command-stdout>"), strings.HasPrefix(t, "<local-command-stderr>"),
		strings.HasPrefix(t, "<local-command-caveat>"), strings.HasPrefix(t, "Caveat:"), strings.HasPrefix(t, "<bash-stdout>"),
		strings.HasPrefix(t, "<bash-stderr>"):
		return
	case strings.HasPrefix(t, "[Request interrupted by user"):
		out.Drafts = append(out.Drafts, adapters.Draft{TS: ts, Kind: spec.KindPrompt, Role: "human", SrcLine: lineNo,
			Payload: map[string]any{"text": "(interrupted)", "interrupt": true}})
		return
	}
	payload := map[string]any{"text": adapters.Truncate(t, adapters.MaxText)}
	if cm := commandName.FindStringSubmatch(t); cm != nil {
		payload = map[string]any{"text": strings.TrimSpace(cm[1] + " " + firstMatch(commandArgs, t)), "slash_command": cm[1]}
	}
	if role == "agent" && !p.firstUser {
		payload["from_parent"] = true
	}
	p.firstUser = true
	out.Drafts = append(out.Drafts, adapters.Draft{TS: ts, Kind: spec.KindPrompt, Role: role, SrcLine: lineNo, Payload: payload})
}

func firstMatch(re *regexp.Regexp, s string) string {
	if m := re.FindStringSubmatch(s); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

func (p *Parser) parseAssistant(m map[string]any, ts time.Time, lineNo int, sidechain bool, out *adapters.Parsed) {
	msg := adapters.Obj(m, "message")
	if msg == nil {
		return
	}
	model := adapters.Str(msg, "model")
	if model != "" && model != "<synthetic>" {
		out.Meta.Model = model
	}
	if id := adapters.Str(msg, "id"); id != "" && !p.seenMsg[id] {
		p.seenMsg[id] = true
		if u := adapters.Obj(msg, "usage"); u != nil {
			out.Meta.TokensIn += int64(adapters.Num(u, "input_tokens"))
			out.Meta.TokensOut += int64(adapters.Num(u, "output_tokens"))
			out.Meta.CacheRead += int64(adapters.Num(u, "cache_read_input_tokens"))
			out.Meta.CacheWrite += int64(adapters.Num(u, "cache_creation_input_tokens"))
		}
	}
	role := p.agentRole(sidechain)
	blocks, _ := msg["content"].([]any)
	for _, b := range blocks {
		blk, _ := b.(map[string]any)
		switch adapters.Str(blk, "type") {
		case "text":
			if t := strings.TrimSpace(adapters.Str(blk, "text")); t != "" {
				out.Drafts = append(out.Drafts, adapters.Draft{TS: ts, Kind: spec.KindMessage, Role: role, SrcLine: lineNo,
					Payload: map[string]any{"text": adapters.Truncate(t, adapters.MaxText), "model": model}})
			}
		case "thinking":
			if t := strings.TrimSpace(adapters.Str(blk, "thinking")); t != "" {
				out.Drafts = append(out.Drafts, adapters.Draft{TS: ts, Kind: spec.KindReasoning, Role: role, SrcLine: lineNo,
					Payload: map[string]any{"text": adapters.Truncate(t, 3000)}})
			}
		case "tool_use", "server_tool_use":
			input, _ := blk["input"].(map[string]any)
			out.Drafts = append(out.Drafts, p.tracker.Call(adapters.Str(blk, "id"), adapters.Str(blk, "name"), input, ts, role, lineNo)...)
		}
	}
}

func flatten(v any) string {
	switch c := v.(type) {
	case string:
		return c
	case []any:
		var parts []string
		for _, x := range c {
			if m, ok := x.(map[string]any); ok {
				if t := adapters.Str(m, "text"); t != "" {
					parts = append(parts, t)
				} else if adapters.Str(m, "type") == "image" {
					parts = append(parts, "[image]")
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}
