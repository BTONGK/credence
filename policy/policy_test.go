package policy

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	p, err := Parse([]byte(`
claim_types:
  fix:
    weight: 5
    require: [resolve, git, tests, plugin:bench]
tests:
  command: go test -run {test} ./...
  allow_commands: ["make check"]
survival:
  windows: [7d, 90d]
`), "credence.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if fix := p.Type("fix"); fix.Weight != 5 || !p.Requires("fix", "plugin:bench") || fix.HumanReview == "" {
		t.Errorf("fix: %+v", fix)
	}
	if p.Type("docs").Weight == 0 {
		t.Error("unlisted claim types lost their defaults")
	}
	if p.Tests.Command == "" || len(p.Tests.AllowCommands) != 1 || p.Survival.Windows[1] != "90d" {
		t.Errorf("tests/survival not merged: %+v %+v", p.Tests, p.Survival)
	}

	for _, bad := range []struct{ yaml, want string }{
		{"version: 9", "unsupported policy version"},
		{"claim_types: {bugfix: {weight: 1}}", "unknown claim type"},
		{"claim_types: {fix: {require: [vibes]}}", "unknown verifier"},
		{"claim_types: {fix: {human_review: sometimes}}", "human_review"},
		{"survival: {windows: [soon]}", ""},
		{"redaction: {extra_patterns: ['(']}", "redaction pattern"},
	} {
		if _, err := Parse([]byte(bad.yaml), "x"); err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%q: err = %v, want %q", bad.yaml, err, bad.want)
		}
	}
}

func TestParseWindow(t *testing.T) {
	for w, hours := range map[string]float64{"7d": 168, "12h": 12, " 30d ": 720} {
		d, err := ParseWindow(w)
		if err != nil || d.Hours() != hours {
			t.Errorf("%q: %v %v", w, d, err)
		}
	}
}
