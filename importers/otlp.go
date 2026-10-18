package importers

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/BTONGK/credence/adapters"
	"github.com/BTONGK/credence/adapters/generic"
	"github.com/BTONGK/credence/daemon/ingest"
	"github.com/BTONGK/credence/daemon/store"
)

// OTLP telemetry (OTLP/JSON logs and traces) is converted into generic
// chat-transcript records, one file per session under
// $CREDENCE_HOME/transcripts/otlp, which the generic adapter captures.
// Supported sources: Claude Code and Codex OpenTelemetry events
// (claude_code.user_prompt, claude_code.tool_result, claude_code.api_request
// and their codex.* equivalents) and OpenTelemetry GenAI spans and events
// (gen_ai.operation.name chat/execute_tool, gen_ai.input/output.messages).
//
// Telemetry is much thinner than a transcript: prompts and tool inputs are
// only present when the harness was told to export them. Sessions already
// captured from transcripts are skipped.

// OTLPDir is where converted telemetry is written.
func OTLPDir() string { return filepath.Join(generic.Root(), "otlp") }

type otlpRecord struct {
	ts time.Time
	m  map[string]any
}

type otlpSession struct {
	id, harness, cwd string
	records          []otlpRecord
}

type otlpConverter struct {
	repo       string
	sessions   map[string]*otlpSession
	lastPrompt map[string]string
	calls      map[string]bool
}

func newOTLPConverter(repo string) *otlpConverter {
	return &otlpConverter{repo: repo, sessions: map[string]*otlpSession{}, lastPrompt: map[string]string{}, calls: map[string]bool{}}
}

type otlpKV struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

type otlpResource struct {
	Attributes []otlpKV `json:"attributes"`
}

type otlpRequest struct {
	ResourceLogs []struct {
		Resource  otlpResource `json:"resource"`
		ScopeLogs []struct {
			LogRecords []struct {
				TimeUnixNano         string          `json:"timeUnixNano"`
				ObservedTimeUnixNano string          `json:"observedTimeUnixNano"`
				Body                 json.RawMessage `json:"body"`
				Attributes           []otlpKV        `json:"attributes"`
				TraceID              string          `json:"traceId"`
			} `json:"logRecords"`
		} `json:"scopeLogs"`
	} `json:"resourceLogs"`
	ResourceSpans []struct {
		Resource   otlpResource `json:"resource"`
		ScopeSpans []struct {
			Spans []struct {
				TraceID           string   `json:"traceId"`
				Name              string   `json:"name"`
				StartTimeUnixNano string   `json:"startTimeUnixNano"`
				EndTimeUnixNano   string   `json:"endTimeUnixNano"`
				Attributes        []otlpKV `json:"attributes"`
				Events            []struct {
					Name         string   `json:"name"`
					TimeUnixNano string   `json:"timeUnixNano"`
					Attributes   []otlpKV `json:"attributes"`
				} `json:"events"`
				Status struct {
					Code    any    `json:"code"`
					Message string `json:"message"`
				} `json:"status"`
			} `json:"spans"`
		} `json:"scopeSpans"`
	} `json:"resourceSpans"`
}

// anyValue decodes an OTLP AnyValue.
func anyValue(raw json.RawMessage) any {
	var v map[string]json.RawMessage
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	for k, x := range v {
		switch k {
		case "stringValue", "bytesValue":
			var s string
			_ = json.Unmarshal(x, &s)
			return s
		case "boolValue":
			var b bool
			_ = json.Unmarshal(x, &b)
			return b
		case "intValue":
			var s any
			_ = json.Unmarshal(x, &s)
			switch n := s.(type) {
			case string:
				i, _ := strconv.ParseInt(n, 10, 64)
				return float64(i)
			case float64:
				return n
			}
		case "doubleValue":
			var f float64
			_ = json.Unmarshal(x, &f)
			return f
		case "arrayValue":
			var a struct {
				Values []json.RawMessage `json:"values"`
			}
			_ = json.Unmarshal(x, &a)
			out := make([]any, 0, len(a.Values))
			for _, e := range a.Values {
				out = append(out, anyValue(e))
			}
			return out
		case "kvlistValue":
			var kv struct {
				Values []otlpKV `json:"values"`
			}
			_ = json.Unmarshal(x, &kv)
			return attrs(kv.Values)
		}
	}
	return nil
}

func attrs(kvs []otlpKV) map[string]any {
	m := make(map[string]any, len(kvs))
	for _, kv := range kvs {
		m[kv.Key] = anyValue(kv.Value)
	}
	return m
}

func nanoTime(vals ...string) time.Time {
	for _, v := range vals {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return time.Unix(0, n).UTC()
		}
	}
	return time.Time{}
}

// str returns the first non-empty string (or number) attribute.
func str(m map[string]any, keys ...string) string {
	for _, k := range keys {
		switch v := m[k].(type) {
		case string:
			if v != "" {
				return v
			}
		case float64:
			return strconv.FormatFloat(v, 'f', -1, 64)
		case bool:
			return strconv.FormatBool(v)
		}
	}
	return ""
}

func num(m map[string]any, keys ...string) float64 {
	for _, k := range keys {
		switch v := m[k].(type) {
		case float64:
			return v
		case string:
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				return f
			}
		}
	}
	return 0
}

func harnessOf(service string) string {
	s := strings.ToLower(service)
	switch {
	case strings.Contains(s, "claude"):
		return "claude-code"
	case strings.Contains(s, "codex"):
		return "codex"
	case s == "":
		return "otlp"
	}
	return ingest.Slug(s)
}

var sessionKeys = []string{"session.id", "session_id", "gen_ai.conversation.id", "conversation.id", "conversation_id", "thread.id"}
var cwdKeys = []string{"credence.repo", "cwd", "process.cwd", "workspace", "vcs.repository.path"}

func (c *otlpConverter) session(res, a map[string]any, traceID string) *otlpSession {
	id := str(a, sessionKeys...)
	if id == "" {
		id = str(res, sessionKeys...)
	}
	if id == "" {
		id = traceID
	}
	if id == "" {
		id = "unknown"
	}
	h := harnessOf(str(res, "service.name"))
	key := h + ":" + id
	s := c.sessions[key]
	if s == nil {
		cwd := str(a, cwdKeys...)
		if cwd == "" {
			cwd = str(res, cwdKeys...)
		}
		if cwd == "" {
			cwd = c.repo
		}
		s = &otlpSession{id: id, harness: h, cwd: cwd}
		c.sessions[key] = s
	}
	return s
}

func (c *otlpConverter) emit(s *otlpSession, ts time.Time, m map[string]any) {
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	m["session_id"] = "otlp:" + s.id
	m["harness"] = s.harness
	m["timestamp"] = ts.Format(time.RFC3339Nano)
	if s.cwd != "" {
		m["cwd"] = s.cwd
	}
	s.records = append(s.records, otlpRecord{ts: ts, m: m})
}

func (c *otlpConverter) prompt(s *otlpSession, ts time.Time, text string) {
	text = strings.TrimSpace(text)
	if text == "" || c.lastPrompt[s.id] == text {
		return
	}
	c.lastPrompt[s.id] = text
	c.emit(s, ts, map[string]any{"role": "user", "content": text})
}

func (c *otlpConverter) toolCall(s *otlpSession, ts time.Time, id, name string, args map[string]any) {
	key := s.id + "/" + id
	if c.calls[key] {
		return
	}
	c.calls[key] = true
	b, _ := json.Marshal(args)
	c.emit(s, ts, map[string]any{"type": "function_call", "call_id": id, "name": name, "arguments": string(b)})
}

func (c *otlpConverter) usage(s *otlpSession, ts time.Time, model string, in, out float64) {
	if in == 0 && out == 0 && model == "" {
		return
	}
	c.emit(s, ts, map[string]any{"model": model, "message": map[string]any{"role": "assistant", "content": "", "model": model,
		"usage": map[string]any{"input_tokens": in, "output_tokens": out}}})
}

// toolArgs normalizes exported tool parameters into tool input.
func toolArgs(v any) map[string]any {
	var m map[string]any
	switch x := v.(type) {
	case map[string]any:
		m = x
	case string:
		if json.Unmarshal([]byte(x), &m) != nil && x != "" {
			m = map[string]any{"input": x}
		}
	}
	if m == nil {
		m = map[string]any{}
	}
	if _, ok := m["command"]; !ok {
		if cmd := str(m, "full_command", "bash_command", "cmd"); cmd != "" {
			m["command"] = cmd
		}
	}
	return m
}

func callID(s *otlpSession, ts time.Time, parts ...string) string {
	sum := sha256.Sum256([]byte(s.id + ts.String() + strings.Join(parts, "\x00")))
	return "call_" + hex.EncodeToString(sum[:6])
}

// event handles one log record or span event.
func (c *otlpConverter) event(s *otlpSession, name string, a map[string]any, body any, ts time.Time) {
	// Claude Code puts "claude_code.user_prompt" in the body and
	// "user_prompt" in event.name; prefer the qualified one.
	if bs, ok := body.(string); ok && (name == "" || (strings.Contains(bs, ".") && !strings.Contains(name, "."))) {
		name = bs
	}
	is := func(event string) bool { return name == event || strings.HasSuffix(name, "."+event) }
	bm, _ := body.(map[string]any)
	switch {
	case is("user_prompt") || name == "gen_ai.user.message":
		text := str(a, "prompt", "gen_ai.prompt", "content")
		if text == "" {
			text = str(bm, "content")
		}
		if text == "" {
			if n := str(a, "prompt_length"); n != "" {
				text = fmt.Sprintf("(prompt not exported; %s characters)", n)
			}
		}
		c.prompt(s, ts, text)
	case is("tool_result") || name == "gen_ai.tool.message":
		tool := str(a, "tool_name", "gen_ai.tool.name", "tool", "name")
		args := toolArgs(firstNonNil(a["tool_parameters"], a["arguments"], a["tool_input"], a["gen_ai.tool.call.arguments"]))
		id := str(a, "tool_use_id", "call_id", "tool_call_id", "gen_ai.tool.call.id", "id")
		if id == "" {
			id = str(bm, "id")
		}
		if id == "" {
			id = callID(s, ts, tool)
		}
		if tool != "" {
			c.toolCall(s, ts, id, tool, args)
		}
		out := str(a, "output", "tool_output", "result", "gen_ai.tool.call.result")
		if out == "" {
			out = str(bm, "content")
		}
		if ok := str(a, "success"); ok == "false" {
			out = strings.TrimSpace("error: " + str(a, "error") + "\n" + out)
		}
		c.emit(s, ts, map[string]any{"type": "function_call_output", "call_id": id, "output": out})
	case is("api_request"):
		c.usage(s, ts, str(a, "model", "gen_ai.request.model"), num(a, "input_tokens"), num(a, "output_tokens"))
	case name == "gen_ai.assistant.message" || name == "gen_ai.choice":
		msg := bm
		if inner, ok := bm["message"].(map[string]any); ok {
			msg = inner
		}
		if t := str(msg, "content"); t != "" {
			c.emit(s, ts, map[string]any{"role": "assistant", "content": t})
		}
		if tcs, ok := msg["tool_calls"].([]any); ok {
			for _, tc := range tcs {
				m, _ := tc.(map[string]any)
				fn, _ := m["function"].(map[string]any)
				c.toolCall(s, ts, orStr(str(m, "id"), callID(s, ts, str(fn, "name"))), str(fn, "name"), toolArgs(fn["arguments"]))
			}
		}
	}
}

func firstNonNil(vs ...any) any {
	for _, v := range vs {
		if v != nil {
			return v
		}
	}
	return nil
}

// messages parses gen_ai.input/output.messages (JSON string or value).
func messages(v any) []map[string]any {
	if s, ok := v.(string); ok {
		var out []map[string]any
		_ = json.Unmarshal([]byte(s), &out)
		return out
	}
	var out []map[string]any
	if arr, ok := v.([]any); ok {
		for _, x := range arr {
			if m, ok := x.(map[string]any); ok {
				out = append(out, m)
			}
		}
	}
	return out
}

func partsText(m map[string]any) string {
	if t, ok := m["content"].(string); ok {
		return t
	}
	var b []string
	parts, _ := m["parts"].([]any)
	for _, p := range parts {
		pm, _ := p.(map[string]any)
		if adapters.Str(pm, "type") == "text" {
			b = append(b, adapters.Str(pm, "content", "text"))
		}
	}
	return strings.Join(b, "\n")
}

func (c *otlpConverter) span(s *otlpSession, a map[string]any, start, end time.Time, failed bool, statusMsg string) {
	switch op := str(a, "gen_ai.operation.name"); op {
	case "chat", "text_completion", "generate_content", "invoke_agent":
		in := messages(a["gen_ai.input.messages"])
		for i := len(in) - 1; i >= 0; i-- {
			if adapters.Str(in[i], "role") == "user" {
				c.prompt(s, start, partsText(in[i]))
				break
			}
		}
		for _, m := range messages(a["gen_ai.output.messages"]) {
			if t := strings.TrimSpace(partsText(m)); t != "" {
				c.emit(s, end, map[string]any{"role": "assistant", "content": t})
			}
			parts, _ := m["parts"].([]any)
			for _, p := range parts {
				pm, _ := p.(map[string]any)
				if adapters.Str(pm, "type") == "tool_call" {
					name := adapters.Str(pm, "name")
					c.toolCall(s, end, orStr(adapters.Str(pm, "id"), callID(s, end, name)), name, toolArgs(pm["arguments"]))
				}
			}
		}
		c.usage(s, end, str(a, "gen_ai.response.model", "gen_ai.request.model"), num(a, "gen_ai.usage.input_tokens"), num(a, "gen_ai.usage.output_tokens"))
	case "execute_tool":
		name := str(a, "gen_ai.tool.name")
		id := str(a, "gen_ai.tool.call.id")
		if id == "" {
			id = callID(s, start, name)
		}
		c.toolCall(s, start, id, name, toolArgs(a["gen_ai.tool.call.arguments"]))
		out := str(a, "gen_ai.tool.call.result")
		if failed {
			out = strings.TrimSpace("error: " + statusMsg + "\n" + out)
		}
		c.emit(s, end, map[string]any{"type": "function_call_output", "call_id": id, "output": out})
	}
}

// add converts one OTLP/JSON export request.
func (c *otlpConverter) add(body []byte) error {
	var req otlpRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return err
	}
	for _, rl := range req.ResourceLogs {
		res := attrs(rl.Resource.Attributes)
		for _, sl := range rl.ScopeLogs {
			for _, lr := range sl.LogRecords {
				a := attrs(lr.Attributes)
				ts := nanoTime(lr.TimeUnixNano, lr.ObservedTimeUnixNano)
				if t := adapters.ParseTime(str(a, "event.timestamp")); !t.IsZero() {
					ts = t
				}
				var body any
				if len(lr.Body) > 0 {
					body = anyValue(lr.Body)
				}
				c.event(c.session(res, a, lr.TraceID), str(a, "event.name"), a, body, ts)
			}
		}
	}
	for _, rs := range req.ResourceSpans {
		res := attrs(rs.Resource.Attributes)
		for _, ss := range rs.ScopeSpans {
			for _, sp := range ss.Spans {
				a := attrs(sp.Attributes)
				s := c.session(res, a, sp.TraceID)
				start, end := nanoTime(sp.StartTimeUnixNano), nanoTime(sp.EndTimeUnixNano)
				failed := fmt.Sprint(sp.Status.Code) == "2" || fmt.Sprint(sp.Status.Code) == "STATUS_CODE_ERROR"
				c.span(s, a, start, end, failed, sp.Status.Message)
				for _, ev := range sp.Events {
					c.event(s, ev.Name, attrs(ev.Attributes), nil, nanoTime(ev.TimeUnixNano))
				}
			}
		}
	}
	return nil
}

var otlpMu sync.Mutex

// write appends new records to each session's file, skipping records it
// already holds and sessions captured from transcripts. It returns the
// files that changed.
func (c *otlpConverter) write(st *store.Store) ([]string, error) {
	otlpMu.Lock()
	defer otlpMu.Unlock()
	dir := OTLPDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(c.sessions))
	for k := range c.sessions {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var changed []string
	for _, k := range keys {
		s := c.sessions[k]
		if len(s.records) == 0 {
			continue
		}
		if st != nil {
			if row, err := st.GetSession(ingest.SessionID(s.harness, s.id)); err == nil && row.EventCount > 0 {
				continue // a transcript already captured this session
			}
		}
		sort.SliceStable(s.records, func(i, j int) bool { return s.records[i].ts.Before(s.records[j].ts) })
		path := filepath.Join(dir, safeName(s.harness+"-"+s.id)+".jsonl")
		seen := map[string]bool{}
		if f, err := os.Open(path); err == nil {
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 1<<20), 16<<20)
			for sc.Scan() {
				seen[recordKey(sc.Bytes())] = true
			}
			f.Close()
		}
		var buf bytes.Buffer
		for _, r := range s.records {
			b, err := json.Marshal(r.m)
			if err != nil {
				continue
			}
			if k := recordKey(b); !seen[k] {
				seen[k] = true
				buf.Write(b)
				buf.WriteByte('\n')
			}
		}
		if buf.Len() == 0 {
			continue
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return changed, err
		}
		_, err = f.Write(buf.Bytes())
		f.Close()
		if err != nil {
			return changed, err
		}
		changed = append(changed, path)
	}
	return changed, nil
}

func recordKey(b []byte) string {
	sum := sha256.Sum256(bytes.TrimSpace(b))
	return hex.EncodeToString(sum[:12])
}

// WriteOTLP converts one OTLP/JSON request body and appends it to the
// per-session files, returning the files that changed. repo is the
// default working directory for sessions that do not report one.
func WriteOTLP(st *store.Store, body []byte, repo string) ([]string, error) {
	c := newOTLPConverter(repo)
	if err := c.add(body); err != nil {
		return nil, err
	}
	return c.write(st)
}

// OTLP imports a file of OTLP/JSON export requests (one JSON document, or
// one per line as written by the collector's file exporter).
func OTLP(ctx context.Context, o Options) (*Result, error) {
	r := &Result{Kind: "otlp"}
	if o.Path == "" {
		return nil, errors.New("otlp: expected a file of OTLP/JSON logs or traces")
	}
	b, err := os.ReadFile(o.Path)
	if err != nil {
		return nil, err
	}
	c := newOTLPConverter(o.Repo)
	dec := json.NewDecoder(bytes.NewReader(b))
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("otlp: %w", err)
		}
		r.Records++
		if err := c.add(raw); err != nil {
			r.Skipped++
		}
	}
	files, err := c.write(o.Store)
	if err != nil {
		return nil, err
	}
	a, _ := adapters.Get(generic.Name)
	for _, f := range files {
		sids, err := ingestFile(ctx, o, f, a)
		if err != nil {
			r.note("%s: %v", f, err)
			continue
		}
		r.Sessions = append(r.Sessions, sids...)
	}
	if len(files) == 0 && len(c.sessions) > 0 {
		r.note("all %d sessions were already captured from transcripts or previously imported", len(c.sessions))
	}
	return r, nil
}
