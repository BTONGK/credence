package verifiers

import (
	"testing"

	"github.com/BTONGK/credence/policy"
	"github.com/BTONGK/credence/spec"
)

func v(tier int, verifier, check, result, outcome string) spec.Verdict {
	return spec.Verdict{Tier: tier, Verifier: verifier, Check: check, Result: result, Outcome: outcome, Details: verifier + " " + outcome}
}

var resolved = v(0, "resolve", "evidence_resolves", spec.ResultPass, "resolved")

func TestDecide(t *testing.T) {
	pol := policy.Default()
	cases := []struct {
		name         string
		typ          string
		origin       string
		vs           []spec.Verdict
		judge        bool
		status, sect string
		wantFlag     string
		wantMissing  string
	}{
		{name: "fix with a pre-existing test that transitions is verified", typ: spec.TypeFix,
			vs:     []spec.Verdict{resolved, v(1, "tests", "test_fails_before_passes_after", spec.ResultPass, "transition")},
			status: spec.StatusVerified, sect: spec.SectionVerified},
		{name: "fix proven only by a test written in the same session is corroborated", typ: spec.TypeFix,
			vs:     []spec.Verdict{resolved, v(1, "tests", "test_fails_before_passes_after", spec.ResultPass, "transition_born_in_session")},
			status: spec.StatusCorroborated, sect: spec.SectionVerified, wantFlag: "test_born_in_session"},
		{name: "a same-session test that kills the fix's mutants verifies it", typ: spec.TypeFix,
			vs: []spec.Verdict{resolved, v(1, "tests", "test_fails_before_passes_after", spec.ResultPass, "transition_born_in_session"),
				v(1, "mutation", "mutation_score", spec.ResultPass, "mutation_strong")},
			status: spec.StatusVerified, sect: spec.SectionVerified, wantFlag: "test_born_in_session"},
		{name: "a weak same-session test stays corroborated and is flagged", typ: spec.TypeFix,
			vs: []spec.Verdict{resolved, v(1, "tests", "test_passes", spec.ResultPass, "passes_born_in_session"),
				v(1, "mutation", "mutation_score", spec.ResultInconclusive, "mutation_weak")},
			status: spec.StatusCorroborated, sect: spec.SectionVerified, wantFlag: "weak_test"},
		{name: "a test claim may be proven by its own new test", typ: spec.TypeTest,
			vs:     []spec.Verdict{resolved, v(1, "tests", "test_fails_before_passes_after", spec.ResultPass, "transition_born_in_session")},
			status: spec.StatusVerified, sect: spec.SectionVerified},
		{name: "tier-0 integrity failure contradicts and nothing later can rescue it", typ: spec.TypeFix,
			vs: []spec.Verdict{v(0, "resolve", "evidence_resolves", spec.ResultFail, "integrity"),
				v(1, "tests", "test_passes", spec.ResultPass, "passes"), v(2, "judge", "judge", spec.ResultPass, "supported")},
			status: spec.StatusContradicted, sect: spec.SectionContradicted},
		{name: "tier-0 blocked evidence blocks", typ: spec.TypeFix,
			vs:     []spec.Verdict{v(0, "resolve", "evidence_resolves", spec.ResultBlocked, "no_evidence")},
			status: spec.StatusBlocked, sect: spec.SectionUnverified},
		{name: "a tier-1 failure contradicts even with a supportive judge", typ: spec.TypeTest,
			vs: []spec.Verdict{resolved, v(1, "tests", "test_passes", spec.ResultFail, "fails_after"),
				v(2, "judge", "judge", spec.ResultPass, "supported")},
			status: spec.StatusContradicted, sect: spec.SectionContradicted},
		{name: "test that also passed before is unverified", typ: spec.TypeTest,
			vs:     []spec.Verdict{resolved, v(1, "tests", "test_fails_before_passes_after", spec.ResultInconclusive, "no_failure_before")},
			status: spec.StatusUnverified, sect: spec.SectionUnverified, wantMissing: "tests"},
		{name: "diagnosis backed by a repro transition satisfies the judge requirement", typ: spec.TypeDiagnosis,
			vs:     []spec.Verdict{resolved, v(1, "tests", "command_fails_before_passes_after", spec.ResultPass, "transition")},
			status: spec.StatusVerified, sect: spec.SectionVerified},
		{name: "judge-only support is capped at corroborated", typ: spec.TypeResearch, judge: true,
			vs:     []spec.Verdict{resolved, v(2, "judge", "judge", spec.ResultPass, "supported")},
			status: spec.StatusCorroborated, sect: spec.SectionVerified},
		{name: "judge ruling overstated downgrades", typ: spec.TypeDiagnosis, judge: true,
			vs:     []spec.Verdict{resolved, v(2, "judge", "judge", spec.ResultFail, "overstated")},
			status: spec.StatusOverstated, sect: spec.SectionContradicted},
		{name: "negative results go to reported failures", typ: spec.TypeNegativeResult,
			vs:     []spec.Verdict{resolved},
			status: spec.StatusUnverified, sect: spec.SectionFailures, wantMissing: "judge"},
		{name: "auditor-only work is found by auditor even when verified", typ: spec.TypeOps, origin: spec.OriginAuditor,
			vs:     []spec.Verdict{resolved, v(1, "git", "commit_touches", spec.ResultPass, "touches")},
			status: spec.StatusVerified, sect: spec.SectionAuditor},
		{name: "contradicted beats found by auditor", typ: spec.TypeOps, origin: spec.OriginAuditor,
			vs:     []spec.Verdict{resolved, v(1, "git", "commit_touches", spec.ResultFail, "untouched_paths")},
			status: spec.StatusContradicted, sect: spec.SectionContradicted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &spec.Claim{Type: tc.typ, Origin: spec.OriginSelf}
			origin := tc.origin
			if origin == "" {
				origin = spec.OriginSelf
			}
			d := Decide(c, tc.vs, pol, origin, tc.judge)
			if d.Status != tc.status || d.Section != tc.sect {
				t.Fatalf("got %s/%s, want %s/%s (notes %v)", d.Status, d.Section, tc.status, tc.sect, d.Notes)
			}
			if tc.wantFlag != "" && !contains(d.Flags, tc.wantFlag) {
				t.Errorf("flags %v, want %s", d.Flags, tc.wantFlag)
			}
			if tc.wantMissing != "" && !contains(d.Missing, tc.wantMissing) {
				t.Errorf("missing %v, want %s", d.Missing, tc.wantMissing)
			}
		})
	}
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func TestValueOrdering(t *testing.T) {
	pol := policy.Default()
	verified := Value(pol, spec.TypeFix, spec.StatusVerified, spec.ReviewAccepted, "")
	corroborated := Value(pol, spec.TypeFix, spec.StatusCorroborated, spec.ReviewAccepted, "")
	unverified := Value(pol, spec.TypeFix, spec.StatusUnverified, spec.ReviewPending, "")
	disputed := Value(pol, spec.TypeFix, spec.StatusVerified, spec.ReviewDisputed, "")
	reverted := Value(pol, spec.TypeFix, spec.StatusVerified, spec.ReviewAccepted, spec.SurvivalReverted)
	if !(verified > corroborated && corroborated > unverified) {
		t.Errorf("value should fall with weaker evidence: %v %v %v", verified, corroborated, unverified)
	}
	if disputed != 0 || reverted != 0 {
		t.Errorf("disputed (%v) and reverted (%v) work should earn nothing", disputed, reverted)
	}
	if neg := Value(pol, spec.TypeNegativeResult, spec.StatusUnverified, spec.ReviewPending, ""); neg <= 0 {
		t.Errorf("reported failures should earn a little credit, got %v", neg)
	}
}
