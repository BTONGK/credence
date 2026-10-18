package conformance

import (
	"strings"
	"testing"
)

// checkSemantics states what each fixture is meant to prove, so a golden
// file cannot quietly encode a wrong answer.
func checkSemantics(t *testing.T, harness string, s Session) {
	t.Helper()
	must := func(ok bool, what string) {
		if !ok {
			t.Errorf("%s: %s", harness, what)
		}
	}
	one := func(kind string, want map[string]string, what string) {
		must(len(find(s, kind, want)) == 1, what)
	}
	must(count(s, "session_start") == 1, "exactly one session_start")
	must(count(s, "prompt") == 1, "exactly one prompt (no duplicates, no injected context)")
	for _, e := range s.Events {
		if e.Kind == "prompt" && strings.HasPrefix(strings.TrimSpace(e.Payload["text"].(string)), "<") {
			t.Errorf("%s: harness-injected context recorded as a prompt: %v", harness, e.Payload["text"])
		}
	}
	switch harness {
	case "claude-code":
		must(s.Model == "claude-opus-5-5" && s.Branch == "main" && s.Cwd == "/work/billing", "session metadata")
		must(s.Tokens == [4]int64{9600, 1440, 32000, 0}, "tokens summed per assistant message")
		must(count(s, "reasoning") == 1, "thinking becomes reasoning")
		one("file_read", map[string]string{"path": "billing/invoice.py"}, "Read becomes file_read")
		one("tool_result", map[string]string{"test_outcome": `"status":"fail"`}, "the first test run fails")
		one("tool_result", map[string]string{"test_outcome": `"status":"pass"`}, "the second test run passes")
		one("file_edit", map[string]string{"path": "billing/invoice.py", "removed": "1"}, "Edit becomes file_edit with line counts")
		one("commit", map[string]string{"sha": "3c2a9f1"}, "git commit output becomes a commit event")
		one("claim_filed", map[string]string{"input": `"type":"fix"`}, "credence_claim becomes claim_filed with its input")
	case "codex":
		must(s.Model == "gpt-5.5" && s.Branch == "fix/rounding" && s.Version == "0.60.0", "session metadata")
		must(s.Tokens == [4]int64{3000, 700, 6000, 0}, "cumulative token counter, cached input split out")
		one("prompt", map[string]string{"text": "Round invoice totals"}, "user_message and its response_item are one prompt")
		one("tool_result", map[string]string{"tool_call_id": "call_A1", "exit_code": "1"}, "exec_command_end and function_call_output are one result")
		one("file_edit", map[string]string{"path": "billing/money.py", "tool": "apply_patch"}, "apply_patch becomes file_edit")
		one("claim_filed", map[string]string{"input": "Invoice totals round half-up"}, "MCP credence_claim becomes claim_filed")
	case "cursor":
		must(s.Model == "gpt-5.5" && s.Cwd == "/work/billing", "session metadata")
		one("file_edit", map[string]string{"path": "billing/export.py"}, "edit_file becomes file_edit")
		one("tool_result", map[string]string{"test_outcome": `"passed":3`}, "test output is parsed")
	case "generic":
		must(s.ReportedHarness == "aider" && s.Tokens == [4]int64{800, 60, 0, 0}, "harness and OpenAI usage fields")
		one("file_edit", map[string]string{"path": "README.md", "op": "create"}, "an OpenAI tool call becomes file_edit")
		must(count(s, "message") == 2, "two assistant messages")
	}
}
