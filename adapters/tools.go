package adapters

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/BTONGK/credence/spec"
)

// Output size limits for stored payloads.
const (
	MaxOutput = 6000
	MaxText   = 12000
	MaxInput  = 4000
)

// ToolCall is a tool invocation awaiting its result.
type ToolCall struct {
	ID      string
	Name    string
	Kind    string
	Input   map[string]any
	Command string
	Paths   []string
	Test    *TestInfo
	TS      time.Time
	Role    string
}

// CallTracker pairs tool calls with their results across records.
type CallTracker struct {
	calls map[string]*ToolCall
}

// NewCallTracker returns an empty tracker.
func NewCallTracker() *CallTracker { return &CallTracker{calls: map[string]*ToolCall{}} }

// Lookup returns a pending or completed call.
func (t *CallTracker) Lookup(id string) *ToolCall { return t.calls[id] }

// ToolKind classifies a tool name into an event kind.
func ToolKind(name string) string {
	n := strings.ToLower(name)
	if strings.HasSuffix(n, "credence_claim") {
		return spec.KindClaimFiled
	}
	if i := strings.LastIndex(n, "__"); i >= 0 && strings.HasPrefix(n, "mcp__") {
		n = n[i+2:]
	}
	switch n {
	case "bash", "shell", "exec_command", "run_terminal_cmd", "terminal", "local_shell", "local_shell_call",
		"execute_command", "run_command", "container.exec", "run_shell_command", "bashoutput":
		return spec.KindCommand
	case "edit", "multiedit", "write", "notebookedit", "edit_file", "search_replace", "write_file", "create_file",
		"apply_patch", "str_replace_editor", "str_replace_based_edit_tool", "delete_file", "replace", "write_to_file", "replace_in_file":
		return spec.KindFileEdit
	case "read", "read_file", "view", "notebookread", "open_file", "read_many_files", "view_image":
		return spec.KindFileRead
	case "grep", "glob", "ls", "list_dir", "list_directory", "codebase_search", "glob_file_search", "find", "search",
		"search_file_content", "grep_search", "file_search":
		return spec.KindSearch
	case "task", "agent", "spawn_agent", "subagent", "dispatch_agent", "delegate":
		return spec.KindSubagentStart
	}
	return spec.KindToolCall
}

// Call records a tool call and returns its event drafts.
func (t *CallTracker) Call(id, name string, input map[string]any, ts time.Time, role string, line int) []Draft {
	if input == nil {
		input = map[string]any{}
	}
	kind := ToolKind(name)
	c := &ToolCall{ID: id, Name: name, Kind: kind, Input: input, TS: ts, Role: role}
	if id != "" {
		t.calls[id] = c
	}
	base := map[string]any{"tool_call_id": id, "tool": name}
	switch kind {
	case spec.KindCommand:
		c.Command = commandString(input)
		p := clone(base)
		p["command"] = Truncate(c.Command, MaxInput)
		if d := Str(input, "description"); d != "" {
			p["description"] = d
		}
		if w := Str(input, "workdir", "cwd", "working_directory"); w != "" {
			p["workdir"] = w
		}
		if ti := DetectTest(c.Command); ti != nil {
			c.Test = ti
			p["test"] = ti
		}
		if ops := GitOps(c.Command); len(ops) > 0 {
			p["git_ops"] = ops
		}
		return []Draft{{TS: ts, Kind: kind, Role: role, Payload: p, SrcLine: line}}
	case spec.KindFileEdit:
		edits := editsFromInput(name, input)
		var ds []Draft
		for _, e := range edits {
			c.Paths = append(c.Paths, e.Path)
			p := clone(base)
			p["path"] = e.Path
			p["op"] = e.Op
			p["added"] = e.Added
			p["removed"] = e.Removed
			if len(e.LineHashes) > 0 {
				p["added_line_hashes"] = e.LineHashes
			}
			if e.MoveTo != "" {
				p["move_to"] = e.MoveTo
			}
			ds = append(ds, Draft{TS: ts, Kind: kind, Role: role, Payload: p, SrcLine: line})
		}
		if len(ds) == 0 {
			p := clone(base)
			p["input"] = truncJSON(input)
			ds = append(ds, Draft{TS: ts, Kind: spec.KindToolCall, Role: role, Payload: p, SrcLine: line})
		}
		return ds
	case spec.KindFileRead:
		path := Str(input, "file_path", "path", "target_file", "filePath", "absolute_path")
		c.Paths = []string{path}
		p := clone(base)
		p["path"] = path
		return []Draft{{TS: ts, Kind: kind, Role: role, Payload: p, SrcLine: line}}
	case spec.KindSearch:
		p := clone(base)
		if v := Str(input, "pattern", "query", "glob_pattern", "regex"); v != "" {
			p["pattern"] = Truncate(v, 300)
		}
		if v := Str(input, "path", "target_directory", "dir_path"); v != "" {
			p["path"] = v
		}
		return []Draft{{TS: ts, Kind: kind, Role: role, Payload: p, SrcLine: line}}
	case spec.KindSubagentStart:
		p := clone(base)
		p["agent_type"] = Str(input, "subagent_type", "agent_type", "type", "agent")
		p["description"] = Truncate(Str(input, "description", "task", "name"), 300)
		p["prompt"] = Truncate(Str(input, "prompt", "instructions", "message"), 1500)
		return []Draft{{TS: ts, Kind: kind, Role: role, Payload: p, SrcLine: line}}
	case spec.KindClaimFiled:
		p := clone(base)
		p["input"] = input
		return []Draft{{TS: ts, Kind: kind, Role: role, Payload: p, SrcLine: line}}
	}
	p := clone(base)
	p["input"] = truncJSON(input)
	if strings.Contains(strings.ToLower(name), "web") || strings.Contains(strings.ToLower(name), "fetch") {
		p["category"] = "research"
	}
	if n := strings.ToLower(name); n == "todowrite" || n == "todo_write" || n == "update_plan" || n == "exitplanmode" {
		p["category"] = "plan"
	}
	return []Draft{{TS: ts, Kind: kind, Role: role, Payload: p, SrcLine: line}}
}

// ResultInfo carries what a harness knows about a tool result.
type ResultInfo struct {
	Output   string
	IsError  bool
	ExitCode *int
	Extra    map[string]any
}

// Result records a tool result and returns its drafts (tool_result, plus a
// commit event when a `git commit` succeeded, plus subagent_stop).
func (t *CallTracker) Result(id string, r ResultInfo, ts time.Time, line int) []Draft {
	c := t.calls[id]
	p := map[string]any{"tool_call_id": id}
	callKind := spec.KindToolCall
	name := ""
	if c != nil {
		callKind = c.Kind
		name = c.Name
		p["tool"] = c.Name
	}
	p["call_kind"] = callKind
	exit := 0
	known := false
	if r.ExitCode != nil {
		exit, known = *r.ExitCode, true
	} else if n, ok := ExitCodeFromText(r.Output); ok {
		exit, known = n, true
	}
	if !known && r.IsError {
		exit = 1
	}
	ok := !r.IsError && exit == 0
	p["ok"] = ok
	if callKind == spec.KindCommand {
		p["exit_code"] = exit
	}
	p["output"] = Truncate(r.Output, MaxOutput)
	p["output_len"] = len(r.Output)
	var drafts []Draft
	if c != nil && callKind == spec.KindCommand {
		if c.Test != nil {
			p["test_outcome"] = ParseTestOutcome(c.Test.Runner, r.Output, exit)
			p["test"] = c.Test
		}
		if ci := DetectCommit(c.Command, r.Output); ci != nil && ok {
			p["commit"] = ci
			drafts = append(drafts, Draft{TS: ts, Kind: spec.KindCommit, Role: c.Role, SrcLine: line, Payload: map[string]any{
				"sha": ci.SHA, "branch": ci.Branch, "subject": ci.Subject, "source": "command", "tool_call_id": id,
			}})
		}
	}
	if c != nil && callKind == spec.KindFileEdit && r.Extra != nil {
		if sp, ok := r.Extra["structuredPatch"].([]any); ok {
			p["ranges"] = rangesFromPatch(sp)
		}
		if typ := Str(r.Extra, "type"); typ != "" {
			p["edit_type"] = typ
		}
	}
	for k, v := range r.Extra {
		if k == "structuredPatch" || k == "originalFile" || k == "file" || k == "content" || k == "oldString" || k == "newString" || k == "stdout" || k == "stderr" {
			continue
		}
		if _, exists := p[k]; !exists && isSmall(v) {
			p["x_"+k] = v
		}
	}
	res := Draft{TS: ts, Kind: spec.KindToolResult, Role: "tool", Payload: p, SrcLine: line}
	drafts = append([]Draft{res}, drafts...)
	if c != nil && callKind == spec.KindSubagentStart {
		sp := map[string]any{"tool_call_id": id, "tool": name, "ok": ok, "summary": Truncate(r.Output, 2000)}
		if r.Extra != nil {
			if aid := Str(r.Extra, "agentId", "agent_id"); aid != "" {
				sp["agent_id"] = aid
			}
			if n := Num(r.Extra, "totalTokens"); n > 0 {
				sp["tokens"] = n
			}
			if n := Num(r.Extra, "totalDurationMs"); n > 0 {
				sp["duration_ms"] = n
			}
		}
		drafts = append(drafts, Draft{TS: ts, Kind: spec.KindSubagentStop, Role: c.Role, Payload: sp, SrcLine: line})
	}
	return drafts
}

func isSmall(v any) bool {
	switch t := v.(type) {
	case bool, float64, nil:
		return true
	case string:
		return len(t) < 200
	}
	return false
}

func clone(m map[string]any) map[string]any {
	out := make(map[string]any, len(m)+4)
	for k, v := range m {
		out[k] = v
	}
	return out
}

func truncJSON(v any) any {
	b, err := json.Marshal(v)
	if err != nil || len(b) <= MaxInput {
		return v
	}
	return Truncate(string(b), MaxInput)
}

func commandString(input map[string]any) string {
	if s := Str(input, "command", "cmd", "script"); s != "" {
		return s
	}
	if arr := Arr(input, "command"); len(arr) > 0 {
		parts := make([]string, 0, len(arr))
		for _, a := range arr {
			if s, ok := a.(string); ok {
				parts = append(parts, s)
			}
		}
		// ["bash","-lc","..."] -> the script.
		if len(parts) == 3 && (parts[1] == "-lc" || parts[1] == "-c") {
			return parts[2]
		}
		return strings.Join(parts, " ")
	}
	if arr := Arr(input, "cmd"); len(arr) > 0 {
		var parts []string
		for _, a := range arr {
			if s, ok := a.(string); ok {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, " ")
	}
	return ""
}

// FileEdit is one file change derived from a tool input.
type FileEdit struct {
	Path       string
	Op         string // create | edit | delete | move
	Added      int
	Removed    int
	LineHashes []string
	MoveTo     string
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(strings.TrimRight(s, "\n"), "\n") + 1
}

func editsFromInput(name string, in map[string]any) []FileEdit {
	n := strings.ToLower(name)
	if i := strings.LastIndex(n, "__"); i >= 0 && strings.HasPrefix(n, "mcp__") {
		n = n[i+2:]
	}
	path := Str(in, "file_path", "path", "target_file", "filePath", "notebook_path", "absolute_path")
	switch n {
	case "apply_patch":
		patch := Str(in, "input", "patch", "content")
		if patch == "" {
			if s, ok := in["_raw"].(string); ok {
				patch = s
			}
		}
		return ParseApplyPatch(patch)
	case "write", "write_file", "create_file", "write_to_file":
		content := Str(in, "content", "contents", "file_text", "text")
		return []FileEdit{{Path: path, Op: "create", Added: countLines(content), LineHashes: LineHashes(content)}}
	case "delete_file":
		return []FileEdit{{Path: path, Op: "delete"}}
	case "multiedit":
		var added, removed int
		var hashes []string
		for _, e := range Arr(in, "edits") {
			if m, ok := e.(map[string]any); ok {
				ns, os := Str(m, "new_string"), Str(m, "old_string")
				added += countLines(ns)
				removed += countLines(os)
				hashes = append(hashes, LineHashes(ns)...)
			}
		}
		return []FileEdit{{Path: path, Op: "edit", Added: added, Removed: removed, LineHashes: hashes}}
	case "str_replace_editor", "str_replace_based_edit_tool":
		cmd := Str(in, "command")
		if cmd == "create" {
			c := Str(in, "file_text")
			return []FileEdit{{Path: path, Op: "create", Added: countLines(c), LineHashes: LineHashes(c)}}
		}
		ns := Str(in, "new_str")
		return []FileEdit{{Path: path, Op: "edit", Added: countLines(ns), Removed: countLines(Str(in, "old_str")), LineHashes: LineHashes(ns)}}
	}
	ns := Str(in, "new_string", "new_str", "code_edit", "replacement", "new_source", "content", "contents")
	os := Str(in, "old_string", "old_str", "search")
	if path == "" {
		return nil
	}
	return []FileEdit{{Path: path, Op: "edit", Added: countLines(ns), Removed: countLines(os), LineHashes: LineHashes(ns)}}
}

var patchHeader = regexp.MustCompile(`^\*\*\* (Add|Update|Delete) File: (.+)$`)

// ParseApplyPatch parses Codex's apply_patch format into per-file edits.
func ParseApplyPatch(patch string) []FileEdit {
	var res []FileEdit
	var cur *FileEdit
	var added []string
	flush := func() {
		if cur != nil {
			cur.LineHashes = LineHashes(strings.Join(added, "\n"))
			res = append(res, *cur)
		}
		cur, added = nil, nil
	}
	for _, l := range strings.Split(patch, "\n") {
		if m := patchHeader.FindStringSubmatch(l); m != nil {
			flush()
			op := map[string]string{"Add": "create", "Update": "edit", "Delete": "delete"}[m[1]]
			cur = &FileEdit{Path: strings.TrimSpace(m[2]), Op: op}
			continue
		}
		if cur == nil {
			continue
		}
		switch {
		case strings.HasPrefix(l, "*** Move to: "):
			cur.MoveTo = strings.TrimSpace(strings.TrimPrefix(l, "*** Move to: "))
			cur.Op = "move"
		case strings.HasPrefix(l, "*** "):
		case strings.HasPrefix(l, "+"):
			cur.Added++
			added = append(added, l[1:])
		case strings.HasPrefix(l, "-"):
			cur.Removed++
		}
	}
	flush()
	return res
}

func rangesFromPatch(sp []any) [][2]int {
	var rs [][2]int
	for _, h := range sp {
		m, ok := h.(map[string]any)
		if !ok {
			continue
		}
		start := int(Num(m, "newStart"))
		n := int(Num(m, "newLines"))
		if start > 0 {
			end := start + n - 1
			if end < start {
				end = start
			}
			rs = append(rs, [2]int{start, end})
		}
	}
	return rs
}
