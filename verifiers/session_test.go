package verifiers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/BTONGK/credence/daemon/claims"
	"github.com/BTONGK/credence/daemon/store"
	"github.com/BTONGK/credence/policy"
	"github.com/BTONGK/credence/spec"
)

// sessionData builds an indexed session from (kind, actor, payload) rows.
func sessionData(t *testing.T, rows ...[3]any) *claims.Data {
	t.Helper()
	start := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	var evs []spec.Event
	prev := ""
	for i, r := range rows {
		b, err := json.Marshal(r[2])
		if err != nil {
			t.Fatal(err)
		}
		e := spec.Event{SessionID: "ses_0000000001", Seq: i + 1, TS: start.Add(time.Duration(i) * time.Minute),
			Kind: r[0].(string), Actor: r[1].(string), Payload: b, PayloadRef: spec.HashBytes(b), Prev: prev}
		e.Hash = e.ChainHash()
		prev = e.Hash
		evs = append(evs, e)
	}
	d := &claims.Data{Session: &store.SessionRow{Session: spec.Session{ID: "ses_0000000001", Cwd: "/r", StartedAt: start}},
		Events: evs, Policy: policy.Default()}
	return d.Subset(func(int) bool { return true }) // indexes calls and results
}

type m = map[string]any

const (
	human = "human:pat"
	agent = "agent:claude-code/test"
	tool  = "tool:harness"
)

func verifyStated(t *testing.T, d *claims.Data) map[string]Decision {
	t.Helper()
	out := map[string]Decision{}
	vc := &Context{Data: d}
	for _, c := range claims.ExtractStated(d) {
		vs := append(Resolve{}.Verify(context.Background(), vc, c), Session{}.Verify(context.Background(), vc, c)...)
		dec := Decide(c, vs, d.Policy, spec.OriginStated, false)
		var why []string
		for _, v := range vs {
			if v.Verifier == "session" {
				why = append(why, v.Result+": "+v.Details)
			}
		}
		dec.Notes = why
		out[c.Statement] = dec
	}
	return out
}

func TestStatedClaimsAgainstTheRecord(t *testing.T) {
	d := sessionData(t,
		[3]any{spec.KindPrompt, human, m{"text": "build the app and ship it"}},
		[3]any{spec.KindFileEdit, agent, m{"tool_call_id": "t1", "tool": "Edit", "path": "/r/src/app.js", "op": "edit"}},
		[3]any{spec.KindToolResult, tool, m{"tool_call_id": "t1", "call_kind": "file_edit", "ok": true}},
		[3]any{spec.KindCommand, agent, m{"tool_call_id": "t2", "tool": "Bash", "command": "npm test", "test": m{"runner": "npm"}}},
		[3]any{spec.KindToolResult, tool, m{"tool_call_id": "t2", "call_kind": "command", "ok": true, "output": "Tests: 12 passed, 12 total",
			"test_outcome": m{"status": "pass", "passed": 12, "failed": 0}}},
		[3]any{spec.KindCommand, agent, m{"tool_call_id": "t3", "tool": "Bash", "command": "cd /r && npx wrangler deploy"}},
		[3]any{spec.KindToolResult, tool, m{"tool_call_id": "t3", "call_kind": "command", "ok": true, "output": "Published to https://app.example.dev"}},
		[3]any{spec.KindMessage, agent, m{"text": "I built the app. All 12 tests pass. It's live at https://app.example.dev. I also updated `src/other.js`."}},
		[3]any{spec.KindPrompt, human, m{"text": "and the count?"}},
		[3]any{spec.KindMessage, agent, m{"text": "All 13 tests pass."}},
	)
	got := verifyStated(t, d)
	for statement, want := range map[string]string{
		"I built the app.":                      spec.StatusCorroborated,
		"All 12 tests pass.":                    spec.StatusCorroborated,
		"It's live at https://app.example.dev.": spec.StatusCorroborated,
		"I also updated `src/other.js`.":        spec.StatusContradicted,
		"All 13 tests pass.":                    spec.StatusUnverified, // the run shows 12
	} {
		dec, ok := got[statement]
		if !ok {
			t.Errorf("%q was not extracted; got %v", statement, keys(got))
			continue
		}
		if dec.Status != want {
			t.Errorf("%q: status %s, want %s (%v)", statement, dec.Status, want, dec.Notes)
		}
	}
	if why := strings.Join(got["All 12 tests pass."].Notes, " "); !strings.Contains(why, "`npm test` passed at 3:00 with 12 tests passing") {
		t.Errorf("test evidence: %s", why)
	}
	if why := strings.Join(got["It's live at https://app.example.dev."].Notes, " "); !strings.Contains(why, "came back from `npx wrangler deploy`") {
		t.Errorf("link evidence: %s", why)
	}
}

func TestMadeUpLinkAndFailedTests(t *testing.T) {
	d := sessionData(t,
		[3]any{spec.KindPrompt, human, m{"text": "fix it"}},
		[3]any{spec.KindFileEdit, agent, m{"tool_call_id": "t1", "tool": "Edit", "path": "/r/a.py", "op": "edit"}},
		[3]any{spec.KindToolResult, tool, m{"tool_call_id": "t1", "call_kind": "file_edit", "ok": true}},
		[3]any{spec.KindCommand, agent, m{"tool_call_id": "t2", "tool": "Bash", "command": "pytest -q", "test": m{"runner": "pytest"}}},
		[3]any{spec.KindToolResult, tool, m{"tool_call_id": "t2", "call_kind": "command", "ok": false, "output": "1 failed, 9 passed",
			"test_outcome": m{"status": "fail", "passed": 9, "failed": 1}}},
		[3]any{spec.KindMessage, agent, m{"text": "I fixed the parser and all tests pass. I published the docs at https://docs.example.org/parser."}},
	)
	got := verifyStated(t, d)
	if dec := got["I fixed the parser and all tests pass."]; dec.Status != spec.StatusContradicted ||
		!strings.Contains(strings.Join(dec.Notes, " "), "failed: 9 passed, 1 failed") {
		t.Errorf("failed tests should contradict: %+v", dec)
	}
	if dec := got["I published the docs at https://docs.example.org/parser."]; dec.Status != spec.StatusContradicted {
		t.Errorf("a published link nothing returned should contradict: %+v", dec)
	}
}

func TestChangeMustMatchTheClaim(t *testing.T) {
	d := sessionData(t,
		[3]any{spec.KindPrompt, human, m{"text": "move the domain over"}},
		[3]any{spec.KindCommand, agent, m{"tool_call_id": "t1", "tool": "Bash", "command": "cat > /r/notes/dns_records.txt <<'EOF'\nA 1.2.3.4\nEOF"}},
		[3]any{spec.KindToolResult, tool, m{"tool_call_id": "t1", "call_kind": "command", "ok": true}},
		[3]any{spec.KindToolCall, agent, m{"tool_call_id": "t2", "tool": "mcp__cloudflare__execute"}},
		[3]any{spec.KindToolResult, tool, m{"tool_call_id": "t2", "call_kind": "tool_call", "ok": true}},
		[3]any{spec.KindMessage, agent, m{"text": "I added the domain to your Cloudflare account. I saved the current DNS records."}},
	)
	got := verifyStated(t, d)
	if dec := got["I saved the current DNS records."]; dec.Status != spec.StatusCorroborated {
		t.Errorf("the records file backs the records claim: %+v", dec)
	}
	dec := got["I added the domain to your Cloudflare account."]
	if why := strings.Join(dec.Notes, " "); dec.Status != spec.StatusUnverified || !strings.Contains(why, "cloudflare execute tool") {
		t.Errorf("an unrelated file must not back the Cloudflare claim: %s %s", dec.Status, why)
	}
}

func TestNotificationDoesNotSplitTheTurn(t *testing.T) {
	d := sessionData(t,
		[3]any{spec.KindPrompt, human, m{"text": "fix the caption"}},
		[3]any{spec.KindFileEdit, agent, m{"tool_call_id": "t1", "tool": "Edit", "path": "/r/work/caption.py", "op": "edit"}},
		[3]any{spec.KindToolResult, tool, m{"tool_call_id": "t1", "call_kind": "file_edit", "ok": true}},
		[3]any{spec.KindMessage, agent, m{"text": "Rendering now."}},
		[3]any{spec.KindPrompt, human, m{"text": "<task-notification>\n<task-id>b1</task-id>\n</task-notification>"}},
		[3]any{spec.KindCommand, agent, m{"tool_call_id": "t2", "tool": "Bash", "command": "ls -la /r/out.mp4"}},
		[3]any{spec.KindToolResult, tool, m{"tool_call_id": "t2", "call_kind": "command", "ok": true}},
		[3]any{spec.KindMessage, agent, m{"text": "I fixed it."}},
	)
	if dec := verifyStated(t, d)["I fixed it."]; dec.Status != spec.StatusCorroborated {
		t.Errorf("the fix before the notification backs the reply after it: %s %v", dec.Status, dec.Notes)
	}

	d = sessionData(t,
		[3]any{spec.KindPrompt, human, m{"text": "fix it"}},
		[3]any{spec.KindCommand, agent, m{"tool_call_id": "t1", "tool": "Bash", "command": "ls"}},
		[3]any{spec.KindToolResult, tool, m{"tool_call_id": "t1", "call_kind": "command", "ok": true}},
		[3]any{spec.KindMessage, agent, m{"text": "I fixed it."}},
	)
	dec := verifyStated(t, d)["I fixed it."]
	if why := strings.Join(dec.Notes, " "); dec.Status != spec.StatusContradicted || !strings.Contains(why, "only read and looked at things") {
		t.Errorf("looking around is not a fix: %s %s", dec.Status, why)
	}
}

func keys(m map[string]Decision) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestHead(t *testing.T) {
	for in, want := range map[string]string{
		"cd /r && npm test":                       "npm test",
		"S=/tmp/x; $S/bin/python run.py":          "$S/bin/python run.py",
		"FOO=\"a b\" BAR=$HOME/x go test ./...":   "go test ./...",
		"IP=$(dig +short A example.com); curl -s": "IP=$(dig +short A example.com); curl -s",
		"X= make build":                           "make build",
	} {
		if got := head(in); got != want {
			t.Errorf("head(%q) = %q, want %q", in, got, want)
		}
	}
}
