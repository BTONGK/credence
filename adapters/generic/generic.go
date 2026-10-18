// Package generic parses chat-style JSONL transcripts (Anthropic or OpenAI
// message shapes) and provides the generic transcript-file adapter used for
// harnesses without a dedicated adapter.
package generic

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BTONGK/credence/adapters"
	"github.com/BTONGK/credence/internal/home"
	"github.com/BTONGK/credence/spec"
)

// Name is the harness id.
const Name = "generic"

// ChatParser parses role/content JSONL transcripts.
type ChatParser struct {
	tracker   *adapters.CallTracker
	sessionID string
	started   bool
	seq       int
	// AgentRole is the role used for assistant turns ("agent" or "subagent").
	AgentRole string
}

// NewChatParser returns a parser; sessionID is used when records carry none.
func NewChatParser(sessionID string) *ChatParser {
	return &ChatParser{tracker: adapters.NewCallTracker(), sessionID: sessionID, AgentRole: "agent"}
}

// Parse handles one record.
func (p *ChatParser) Parse(line []byte, lineNo int) (adapters.Parsed, error) {
	var out adapters.Parsed
	var m map[string]any
	if err := json.Unmarshal(line, &m); err != nil {
		// Plain-text transcript line: keep as a message.
		t := strings.TrimSpace(string(line))
		if t != "" {
			out.Meta.HarnessSessionID = p.sessionID
			out.Drafts = append(out.Drafts, adapters.Draft{TS: time.Now().UTC(), Kind: spec.KindMessage, Role: p.AgentRole, SrcLine: lineNo,
				Payload: map[string]any{"text": adapters.Truncate(t, adapters.MaxText)}})
		}
		return out, nil
	}
	sid := adapters.Str(m, "session_id", "sessionId", "conversation_id", "conversationId", "thread_id")
	if sid == "" {
		sid = p.sessionID
	}
	out.Meta.HarnessSessionID = sid
	out.Meta.Harness = adapters.Str(m, "harness")
	out.Meta.Cwd = adapters.Str(m, "cwd", "workspace", "workspace_root")
	out.Meta.Model = adapters.Str(m, "model")
	ts := adapters.ParseTime(adapters.Str(m, "timestamp", "ts", "created_at", "time"))
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	msg := m
	if inner := adapters.Obj(m, "message"); inner != nil {
		msg = inner
		if out.Meta.Model == "" {
			out.Meta.Model = adapters.Str(inner, "model")
		}
	}
	role := adapters.Str(msg, "role")
	if role == "" {
		role = adapters.Str(m, "type", "role")
	}
	if !p.started {
		p.started = true
		out.Meta.StartedAt = ts
		out.Drafts = append(out.Drafts, adapters.Draft{TS: ts, Kind: spec.KindSessionStart, Role: "system", SrcLine: lineNo,
			Payload: map[string]any{"cwd": out.Meta.Cwd}})
	}
	if u := adapters.Obj(msg, "usage"); u != nil {
		out.Meta.TokensIn += int64(adapters.Num(u, "input_tokens") + adapters.Num(u, "prompt_tokens"))
		out.Meta.TokensOut += int64(adapters.Num(u, "output_tokens") + adapters.Num(u, "completion_tokens"))
	}
	// OpenAI Responses-style items.
	switch adapters.Str(m, "type") {
	case "function_call":
		var input map[string]any
		_ = json.Unmarshal([]byte(adapters.Str(m, "arguments")), &input)
		out.Drafts = append(out.Drafts, p.tracker.Call(adapters.Str(m, "call_id", "id"), adapters.Str(m, "name"), input, ts, p.AgentRole, lineNo)...)
		return out, nil
	case "function_call_output":
		out.Drafts = append(out.Drafts, p.tracker.Result(adapters.Str(m, "call_id"), adapters.ResultInfo{Output: adapters.Str(m, "output")}, ts, lineNo)...)
		return out, nil
	}
	switch role {
	case "user", "human":
		p.content(msg["content"], ts, lineNo, "human", &out)
	case "assistant", "ai", "model", "agent":
		p.content(msg["content"], ts, lineNo, p.AgentRole, &out)
		for _, tc := range adapters.Arr(msg, "tool_calls") {
			c, _ := tc.(map[string]any)
			fn := adapters.Obj(c, "function")
			var input map[string]any
			_ = json.Unmarshal([]byte(adapters.Str(fn, "arguments")), &input)
			out.Drafts = append(out.Drafts, p.tracker.Call(adapters.Str(c, "id"), adapters.Str(fn, "name"), input, ts, p.AgentRole, lineNo)...)
		}
	case "tool":
		out.Drafts = append(out.Drafts, p.tracker.Result(adapters.Str(msg, "tool_call_id"), adapters.ResultInfo{Output: text(msg["content"])}, ts, lineNo)...)
	}
	return out, nil
}

func text(v any) string {
	switch c := v.(type) {
	case string:
		return c
	case []any:
		var parts []string
		for _, b := range c {
			if m, ok := b.(map[string]any); ok {
				if t := adapters.Str(m, "text"); t != "" {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func (p *ChatParser) content(v any, ts time.Time, lineNo int, role string, out *adapters.Parsed) {
	kind := spec.KindMessage
	if role == "human" {
		kind = spec.KindPrompt
	}
	switch c := v.(type) {
	case string:
		if t := strings.TrimSpace(c); t != "" {
			out.Drafts = append(out.Drafts, adapters.Draft{TS: ts, Kind: kind, Role: role, SrcLine: lineNo, Payload: map[string]any{"text": adapters.Truncate(t, adapters.MaxText)}})
		}
	case []any:
		for _, b := range c {
			blk, _ := b.(map[string]any)
			switch adapters.Str(blk, "type") {
			case "text", "input_text", "output_text":
				if t := strings.TrimSpace(adapters.Str(blk, "text")); t != "" {
					out.Drafts = append(out.Drafts, adapters.Draft{TS: ts, Kind: kind, Role: role, SrcLine: lineNo, Payload: map[string]any{"text": adapters.Truncate(t, adapters.MaxText)}})
				}
			case "thinking", "reasoning":
				if t := strings.TrimSpace(adapters.Str(blk, "thinking", "text")); t != "" {
					out.Drafts = append(out.Drafts, adapters.Draft{TS: ts, Kind: spec.KindReasoning, Role: role, SrcLine: lineNo, Payload: map[string]any{"text": adapters.Truncate(t, 3000)}})
				}
			case "tool_use", "tool-call", "tool_call":
				input, _ := blk["input"].(map[string]any)
				if input == nil {
					input, _ = blk["args"].(map[string]any)
				}
				id := adapters.Str(blk, "id", "toolCallId", "tool_call_id")
				name := adapters.Str(blk, "name", "toolName", "tool_name")
				out.Drafts = append(out.Drafts, p.tracker.Call(id, name, input, ts, p.AgentRole, lineNo)...)
			case "tool_result", "tool-result":
				isErr, _ := blk["is_error"].(bool)
				res := text(blk["content"])
				if res == "" {
					if r, ok := blk["result"].(string); ok {
						res = r
					} else if blk["result"] != nil {
						b, _ := json.Marshal(blk["result"])
						res = string(b)
					}
				}
				out.Drafts = append(out.Drafts, p.tracker.Result(adapters.Str(blk, "tool_use_id", "toolCallId", "tool_call_id"), adapters.ResultInfo{Output: res, IsError: isErr}, ts, lineNo)...)
			}
		}
	}
}

// Adapter watches $CREDENCE_HOME/transcripts/<harness>/*.jsonl and any
// configured extra roots for chat-style transcripts.
type Adapter struct{}

func init() { adapters.Register(Adapter{}) }

// Name returns the harness id.
func (Adapter) Name() string { return Name }

// Detect is always true: the generic adapter needs no harness.
func (Adapter) Detect() adapters.Detection {
	return adapters.Detection{Installed: true, Notes: "drop JSONL transcripts into " + Root()}
}

// Root is the generic drop directory.
func Root() string { return home.Sub("transcripts") }

// Install is a no-op; there are no hooks to register.
func (Adapter) Install(adapters.InstallOptions) (adapters.InstallReport, error) {
	return adapters.InstallReport{Harness: Name, Actions: []string{"no hooks needed; watching " + Root()}}, nil
}

// Uninstall is a no-op.
func (Adapter) Uninstall(adapters.InstallOptions) (adapters.InstallReport, error) {
	return adapters.InstallReport{Harness: Name}, nil
}

// Doctor reports the drop directory.
func (Adapter) Doctor(adapters.InstallOptions) []adapters.Health {
	_, err := os.Stat(Root())
	return []adapters.Health{{Check: "generic transcript drop dir", OK: err == nil, Detail: Root()}}
}

// TranscriptRoots returns the drop directory.
func (Adapter) TranscriptRoots() []string { return []string{Root()} }

// IsTranscript reports whether path is in the drop directory.
func (Adapter) IsTranscript(path string) bool {
	return strings.HasSuffix(path, ".jsonl") && adapters.UnderDir(path, Root())
}

// LocateTranscript is unused (no hooks).
func (Adapter) LocateTranscript(h adapters.HookRecord) string { return h.TranscriptPath }

// Boundaries uses the idle timeout only.
func (Adapter) Boundaries() adapters.Boundaries {
	return adapters.Boundaries{IdleTimeout: 10 * time.Minute}
}

// Capabilities declares what generic transcripts usually provide.
func (Adapter) Capabilities() adapters.Capabilities {
	return adapters.Capabilities{EventKinds: []string{spec.KindPrompt, spec.KindMessage, spec.KindCommand, spec.KindToolResult, spec.KindFileEdit}}
}

// HookEventName is unused.
func (Adapter) HookEventName(raw map[string]any) string { return adapters.Str(raw, "hook_event_name") }

// NewParser returns a chat parser keyed by the file name.
func (Adapter) NewParser(path string) adapters.Parser {
	return NewChatParser(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
}
