// Package cursor is the Cursor harness adapter.
//
// Cursor hooks live in ~/.cursor/hooks.json (user) or .cursor/hooks.json
// (project) and every hook payload carries transcript_path (null when
// transcripts are off) plus agent_transcript_path per subagent. Cloud agents
// don't fire sessionEnd, so stop plus the idle timeout bound sessions.
package cursor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/BTONGK/credence/adapters"
	"github.com/BTONGK/credence/adapters/generic"
	"github.com/BTONGK/credence/internal/home"
	"github.com/BTONGK/credence/spec"
)

// Name is the harness id.
const Name = "cursor"

// HookEvents are the hooks Credence registers.
var HookEvents = []string{"sessionStart", "sessionEnd", "postToolUse", "afterFileEdit", "subagentStart", "subagentStop", "stop"}

// Adapter implements adapters.Adapter for Cursor.
type Adapter struct{}

func init() { adapters.Register(Adapter{}) }

// Name returns the harness id.
func (Adapter) Name() string { return Name }

// Dir is Cursor's user config directory.
func Dir() string { return filepath.Join(home.UserHome(), ".cursor") }

// Detect checks for cursor-agent or the config directory.
func (Adapter) Detect() adapters.Detection {
	for _, bin := range []string{"cursor-agent", "cursor"} {
		if p, err := exec.LookPath(bin); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			out, _ := exec.CommandContext(ctx, p, "--version").Output()
			cancel()
			return adapters.Detection{Installed: true, Path: p, Version: strings.TrimSpace(string(out))}
		}
	}
	if _, err := os.Stat(Dir()); err == nil {
		return adapters.Detection{Installed: true, Notes: "~/.cursor found"}
	}
	return adapters.Detection{}
}

func hooksPath(opts adapters.InstallOptions) string {
	if opts.Scope == adapters.ScopeUser || opts.Repo == "" {
		return filepath.Join(Dir(), "hooks.json")
	}
	return filepath.Join(opts.Repo, ".cursor", "hooks.json")
}

func strip(v any) []any {
	l, _ := v.([]any)
	var out []any
	for _, e := range l {
		m, _ := e.(map[string]any)
		if m != nil && adapters.IsManaged(adapters.Str(m, "command")) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// Install writes hooks.json entries.
func (Adapter) Install(opts adapters.InstallOptions) (adapters.InstallReport, error) {
	rep := adapters.InstallReport{Harness: Name}
	path := hooksPath(opts)
	m, err := adapters.ReadJSONFile(path)
	if err != nil {
		return rep, fmt.Errorf("%s: %w", path, err)
	}
	if _, ok := m["version"]; !ok {
		m["version"] = 1
	}
	hooks, _ := m["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	cmd := adapters.HookCommand(opts.Binary, Name)
	for _, ev := range HookEvents {
		hooks[ev] = append(strip(hooks[ev]), map[string]any{"command": cmd})
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
		mcp := filepath.Join(Dir(), "mcp.json")
		if opts.Scope != adapters.ScopeUser && opts.Repo != "" {
			mcp = filepath.Join(opts.Repo, ".cursor", "mcp.json")
		}
		mm, err := adapters.ReadJSONFile(mcp)
		if err == nil {
			servers, _ := mm["mcpServers"].(map[string]any)
			if servers == nil {
				servers = map[string]any{}
			}
			servers["credence"] = map[string]any{"command": opts.Binary, "args": []any{"mcp"}}
			mm["mcpServers"] = servers
			if !opts.DryRun {
				err = adapters.WriteJSONFile(mcp, mm)
			}
		}
		if err != nil {
			rep.Warn = append(rep.Warn, "mcp.json: "+err.Error())
		} else {
			rep.Files = append(rep.Files, mcp)
			rep.Actions = append(rep.Actions, "registered credence MCP server in "+mcp)
		}
	}
	return rep, nil
}

// Uninstall removes Credence hooks.
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
		if !opts.DryRun {
			if err := adapters.WriteJSONFile(path, m); err != nil {
				return rep, err
			}
		}
		rep.Actions = append(rep.Actions, "removed hooks from "+path)
	}
	return rep, nil
}

// Doctor checks static configuration.
func (a Adapter) Doctor(opts adapters.InstallOptions) []adapters.Health {
	d := a.Detect()
	hs := []adapters.Health{{Check: "cursor installed", OK: d.Installed, Detail: d.Version + d.Notes}}
	m, _ := adapters.ReadJSONFile(hooksPath(opts))
	hooks, _ := m["hooks"].(map[string]any)
	n := 0
	for _, ev := range HookEvents {
		l, _ := hooks[ev].([]any)
		if len(l) > len(strip(hooks[ev])) {
			n++
		}
	}
	hs = append(hs, adapters.Health{Check: "cursor hooks registered", OK: n == len(HookEvents),
		Detail: fmt.Sprintf("%d/%d hooks in %s; enable agent transcripts in Cursor settings", n, len(HookEvents), hooksPath(opts)), Fix: "run `credence init --harness cursor`"})
	return hs
}

// TranscriptRoots returns Cursor's projects directory; most transcript paths
// arrive via hooks.
func (Adapter) TranscriptRoots() []string { return []string{filepath.Join(Dir(), "projects")} }

// IsTranscript reports whether path is a Cursor agent transcript.
func (a Adapter) IsTranscript(path string) bool {
	if !(strings.HasSuffix(path, ".jsonl") || strings.HasSuffix(path, ".txt")) {
		return false
	}
	return adapters.UnderDir(path, Dir()) && strings.Contains(filepath.ToSlash(path), "transcripts")
}

// LocateTranscript returns the hook's transcript path.
func (Adapter) LocateTranscript(h adapters.HookRecord) string {
	if len(h.Extra) > 0 {
		var x map[string]any
		if json.Unmarshal(h.Extra, &x) == nil {
			if p := adapters.Str(x, "agent_transcript_path"); p != "" && h.Event == "subagentStop" {
				return p
			}
		}
	}
	return h.TranscriptPath
}

// Boundaries declares session signals.
func (Adapter) Boundaries() adapters.Boundaries {
	return adapters.Boundaries{Start: []string{"sessionStart"}, End: []string{"sessionEnd"}, TurnEnd: []string{"stop"}, IdleTimeout: 10 * time.Minute}
}

// Capabilities declares what this adapter provides.
func (Adapter) Capabilities() adapters.Capabilities {
	return adapters.Capabilities{
		EventKinds:    []string{spec.KindPrompt, spec.KindMessage, spec.KindCommand, spec.KindToolResult, spec.KindFileEdit, spec.KindSubagentStart},
		SubagentLinks: true, SelfClaims: true, Hooks: true,
	}
}

// HookEventName returns the native event name.
func (Adapter) HookEventName(raw map[string]any) string { return adapters.Str(raw, "hook_event_name") }

// NewParser returns a chat parser keyed by the transcript file name.
func (Adapter) NewParser(path string) adapters.Parser {
	p := generic.NewChatParser(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
	if strings.Contains(filepath.ToSlash(path), "/subagents/") {
		p.AgentRole = "subagent"
	}
	return p
}
