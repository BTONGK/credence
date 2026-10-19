package main

import (
	"strings"
	"testing"

	"github.com/BTONGK/credence/daemon/quick"
	"github.com/BTONGK/credence/daemon/view"
)

func result(cs ...quick.Claim) *quick.Result {
	r := &quick.Result{Claims: cs, Counts: map[string]int{}}
	for _, c := range cs {
		r.Counts[c.Verdict]++
	}
	return r
}

func TestNoteText(t *testing.T) {
	if got := noteText(result()); got != "" {
		t.Errorf("a reply without claims gets a note: %q", got)
	}
	backed := quick.Claim{Statement: "I added the holiday hours to `index.html`.", Verdict: view.VerdictBacked, Reason: "The session changed index.html."}
	if got := noteText(result(backed)); got != "Credence: 1 claim in this reply, backed by evidence. Type /credence for the reasons." {
		t.Errorf("one backed claim: %q", got)
	}
	if got := noteText(result(backed, backed)); !strings.Contains(got, "2 claims in this reply, all backed by evidence.") {
		t.Errorf("two backed claims: %q", got)
	}
	unchecked := quick.Claim{Statement: "I updated your holiday hours on Google.", Verdict: view.VerdictUnchecked, Reason: "The work went through the google business update hours tool."}
	wrong := quick.Claim{Statement: "All 6 tests pass.", Verdict: view.VerdictContradicted, Reason: "The last test run failed: 5 passed, 1 failed."}
	got := noteText(result(backed, unchecked, wrong))
	want := "Credence: 3 claims in this reply: 1 backed by evidence, 1 yours to confirm, 1 contradicted. ✗ “All 6 tests pass.” The last test run failed: 5 passed, 1 failed. Type /credence for every claim."
	if got != want {
		t.Errorf("mixed reply:\n got %q\nwant %q", got, want)
	}
	if got := noteText(result(backed, unchecked)); !strings.Contains(got, "? “I updated your holiday hours on Google.”") {
		t.Errorf("the note leads with the claim to confirm: %q", got)
	}
}

func TestCheckText(t *testing.T) {
	r := result(
		quick.Claim{Statement: "I added the form.", Verdict: view.VerdictBacked, Reason: "The session changed contact.html."},
		quick.Claim{Statement: "All 6 tests pass.", Verdict: view.VerdictContradicted, Reason: "The last test run failed."},
	)
	r.Title = "Bakery website"
	got := checkText(r)
	for _, want := range []string{
		"Credence checked 2 claims in this session (Bakery website) against the session record.",
		"✗ Contradicted: “All 6 tests pass.”\n  The last test run failed.\n✓ Backed by evidence: “I added the form.”",
		"Totals: 1 backed by evidence, 1 contradicted.",
		"Full report: run `credence` in a terminal.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("check text lacks %q:\n%s", want, got)
		}
	}
	if got := checkText(result()); !strings.Contains(got, "Full report: run `credence` in a terminal.") {
		t.Errorf("an empty check ends without the next step:\n%s", got)
	}
}

func TestWindowWords(t *testing.T) {
	for in, want := range map[string]string{"7d": "7 days", "1d": "day", "30d": "30 days", "2w": "2w"} {
		if got := windowWords(in); got != want {
			t.Errorf("windowWords(%q) = %q, want %q", in, got, want)
		}
	}
	if got := joinAnd([]string{"Claude Code", "Codex", "Cursor"}); got != "Claude Code, Codex and Cursor" {
		t.Errorf("joinAnd = %q", got)
	}
}
