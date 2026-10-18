// Package mcp is Credence's stdio MCP server. It exposes credence_claim,
// the tool agents use to file claims. The server only validates and
// acknowledges: the daemon reads the call from the session transcript, so a
// claim is always bound to the session that made it and its evidence hints
// are resolved against events the daemon captured itself.
package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/BTONGK/credence/spec"
)

// ProtocolVersions the server speaks, newest first.
var ProtocolVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

// ToolDescription is shown to agents.
const ToolDescription = "File a claim about work you completed or attempted in this session. Make it specific and falsifiable " +
	"(\"test_x fails before commit abc123 and passes after\", not \"improved reliability\"). Report failed attempts too, as type " +
	"negative_result. Give evidence_hints: file paths, commit SHAs, or tool call ids from this session. Claims are verified " +
	"independently after the session; overstated claims are flagged."

// ClaimSchema is the input schema of credence_claim.
func ClaimSchema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"type", "title", "statement"},
		"properties": map[string]any{
			"type":      map[string]any{"type": "string", "enum": spec.ClaimTypes},
			"title":     map[string]any{"type": "string", "maxLength": 100, "description": "One line, under 100 characters."},
			"statement": map[string]any{"type": "string", "description": "A falsifiable statement of what is now true."},
			"rationale": map[string]any{"type": "string", "description": "Why this matters (optional)."},
			"assertions": map[string]any{"type": "array", "description": "Machine-checkable predicates, e.g. " +
				`{"check":"test_fails_before_passes_after","test":"tests/test_x.py::test_y","commit":"abc1234","runs":20}, ` +
				`{"check":"commit_touches","commit":"abc1234","paths":["src/x.py"]}, {"check":"command_fails_before_passes_after","command":"python3 repro.py","commit":"abc1234"}, ` +
				`{"check":"mutation_score","test":"tests/test_x.py","commit":"abc1234"} (the test catches broken versions of the change)`,
				"items": map[string]any{"type": "object"}},
			"evidence_hints": map[string]any{"type": "array", "items": map[string]any{"type": "string"},
				"description": "File paths, commit SHAs or tool call ids from this session that back the claim."},
			"co_contributors": map[string]any{"type": "array", "description": `Others who contributed, e.g. {"id":"maya","basis":"identified the failing test"}.`,
				"items": map[string]any{"type": "object"}},
			"confidence": map[string]any{"type": "number", "minimum": 0, "maximum": 1, "description": "Your probability that the claim will be verified."},
		},
	}
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Serve runs the server until in closes.
func Serve(in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 32<<20)
	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var req request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			_ = enc.Encode(response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: -32700, Message: "parse error"}})
			continue
		}
		if len(req.ID) == 0 {
			continue // notification
		}
		res, rerr := handle(req)
		_ = enc.Encode(response{JSONRPC: "2.0", ID: req.ID, Result: res, Error: rerr})
	}
	return sc.Err()
}

func handle(req request) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		v := ProtocolVersions[0]
		for _, pv := range ProtocolVersions {
			if pv == p.ProtocolVersion {
				v = pv
			}
		}
		return map[string]any{
			"protocolVersion": v,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "credence", "title": "Credence", "version": spec.Version},
			"instructions": "When you finish a piece of work (or abandon an approach), call credence_claim once per distinct claim. " +
				"Claims are checked against the session's own evidence after the session, never by you.",
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": []any{map[string]any{
			"name": "credence_claim", "title": "File a credit claim", "description": ToolDescription, "inputSchema": ClaimSchema(),
			"annotations": map[string]any{"readOnlyHint": true, "openWorldHint": false},
		}}}, nil
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{Code: -32602, Message: "invalid params"}
		}
		if p.Name != "credence_claim" {
			return nil, &rpcError{Code: -32602, Message: "unknown tool " + p.Name}
		}
		msg, ok := Validate(p.Arguments)
		return map[string]any{"content": []any{map[string]any{"type": "text", "text": msg}}, "isError": !ok}, nil
	case "resources/list":
		return map[string]any{"resources": []any{}}, nil
	case "prompts/list":
		return map[string]any{"prompts": []any{}}, nil
	}
	return nil, &rpcError{Code: -32601, Message: "method not found: " + req.Method}
}

// Validate checks a claim and returns the message the agent sees. Invalid
// claims are rejected so the agent can correct them; the daemon ignores
// calls whose result was an error.
func Validate(a map[string]any) (string, bool) {
	typ, _ := a["type"].(string)
	title, _ := a["title"].(string)
	statement, _ := a["statement"].(string)
	var problems []string
	if !spec.IsClaimType(typ) {
		problems = append(problems, fmt.Sprintf("type must be one of %s", strings.Join(spec.ClaimTypes, ", ")))
	}
	if strings.TrimSpace(title) == "" {
		problems = append(problems, "title is required")
	} else if len(title) > 100 {
		problems = append(problems, "title must be under 100 characters")
	}
	if strings.TrimSpace(statement) == "" {
		problems = append(problems, "statement is required")
	}
	if c, ok := a["confidence"]; ok {
		if f, ok := c.(float64); !ok || f < 0 || f > 1 {
			problems = append(problems, "confidence must be a number between 0 and 1")
		}
	}
	if as, ok := a["assertions"]; ok {
		l, ok := as.([]any)
		if !ok {
			problems = append(problems, "assertions must be an array of objects")
		}
		for _, x := range l {
			m, ok := x.(map[string]any)
			if !ok || m["check"] == nil {
				problems = append(problems, `each assertion needs a "check"`)
				break
			}
		}
	}
	if len(problems) > 0 {
		return "Claim not recorded: " + strings.Join(problems, "; ") + ".", false
	}
	var tips []string
	hints, _ := a["evidence_hints"].([]any)
	if len(hints) == 0 {
		tips = append(tips, "no evidence_hints were given, so the claim may stay unverified")
	}
	if as, _ := a["assertions"].([]any); len(as) == 0 && (typ == spec.TypeFix || typ == spec.TypeTest) {
		tips = append(tips, "a test assertion would let it be verified automatically")
	}
	msg := "Claim recorded. It will be verified against this session's evidence after the session ends."
	if len(tips) > 0 {
		msg += " Note: " + strings.Join(tips, "; ") + "."
	}
	return msg, true
}
