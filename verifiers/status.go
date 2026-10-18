package verifiers

import (
	"fmt"
	"strings"

	"github.com/BTONGK/credence/policy"
	"github.com/BTONGK/credence/spec"
)

// Decision is a claim's computed state after verification.
type Decision struct {
	Status  string
	Section string
	Notes   []string
	Flags   []string
	Missing []string
}

// Decide applies the tier rules: tier 0 gates everything, a factual failure
// at tier 0 or 1 is final, the judge can only confirm or downgrade, and a
// test written in the same session cannot by itself fully verify a fix.
func Decide(c *spec.Claim, vs []spec.Verdict, pol *policy.Policy, clusterOrigin string, judgeAvailable bool) Decision {
	d := Decision{Status: spec.StatusProposed}
	var t0 *spec.Verdict
	var t1Fail, t2 []spec.Verdict
	passBy := map[string]bool{}
	t1Pass, t1Inconclusive := 0, []spec.Verdict{}
	bornOnly := true
	transitionPass := false
	for i := range vs {
		v := &vs[i]
		switch v.Tier {
		case 0:
			t0 = v
		case 1:
			switch v.Result {
			case spec.ResultFail:
				t1Fail = append(t1Fail, *v)
			case spec.ResultPass:
				t1Pass++
				if v.Verifier == "tests" && !strings.Contains(v.Outcome, "born_in_session") {
					bornOnly = false
				}
				if v.Verifier == "tests" && strings.HasPrefix(v.Outcome, "transition") {
					transitionPass = true
				}
			default:
				t1Inconclusive = append(t1Inconclusive, *v)
			}
		case 2:
			t2 = append(t2, *v)
		}
		if v.Result == spec.ResultPass {
			passBy[v.Verifier] = true
			if v.Check == "ci_green" {
				passBy["ci"] = true
			}
		}
		if strings.Contains(v.Outcome, "born_in_session") {
			d.Flags = appendOnce(d.Flags, "test_born_in_session")
		}
		if v.Outcome == "mutation_weak" {
			d.Flags = appendOnce(d.Flags, "weak_test")
		}
		if strings.HasPrefix(v.Outcome, "flaky") {
			d.Flags = appendOnce(d.Flags, "flaky")
		}
	}
	switch {
	case t0 == nil:
		d.Status = spec.StatusProposed
	case t0.Result == spec.ResultFail:
		d.Status = spec.StatusContradicted
		d.Notes = append(d.Notes, t0.Details)
	case t0.Result == spec.ResultBlocked:
		d.Status = spec.StatusBlocked
		d.Notes = append(d.Notes, t0.Details)
	case len(t1Fail) > 0:
		d.Status = spec.StatusContradicted
		for _, v := range t1Fail {
			d.Notes = append(d.Notes, v.Details)
		}
	default:
		d.Status = spec.StatusResolved
		if t0.Outcome == "partial" {
			d.Notes = append(d.Notes, t0.Details)
		}
		judgeOutcome := ""
		for _, v := range t2 {
			judgeOutcome = v.Outcome
			if v.Result != spec.ResultPass {
				d.Notes = append(d.Notes, "judge: "+v.Details)
			}
		}
		switch judgeOutcome {
		case "overstated":
			d.Status = spec.StatusOverstated
			return finishSection(d, c, clusterOrigin)
		case "contradicted":
			d.Status = spec.StatusContradicted
			return finishSection(d, c, clusterOrigin)
		}
		if c.Origin == spec.OriginStated {
			return decideStated(d, c, vs, passBy, t1Inconclusive, clusterOrigin)
		}
		for _, r := range pol.Type(c.Type).Require {
			ok := false
			switch r {
			case "resolve":
				ok = true
			case "judge":
				// A deterministic before/after check is stronger evidence than
				// a model's reading, so it satisfies a judge requirement.
				ok = passBy["judge"] || transitionPass
			default:
				ok = passBy[r]
			}
			if !ok {
				d.Missing = append(d.Missing, r)
			}
		}
		for _, v := range t1Inconclusive {
			d.Notes = append(d.Notes, v.Details)
		}
		if len(d.Missing) > 0 {
			d.Status = spec.StatusUnverified
			if judgeOutcome == "unsupported" {
				d.Status = spec.StatusUnsupported
			}
			for _, m := range d.Missing {
				d.Notes = append(d.Notes, missingNote(m, c, judgeAvailable, vs))
			}
			break
		}
		d.Status = spec.StatusVerified
		testsPassed := passBy["tests"]
		switch {
		case testsPassed && bornOnly && codeChange[c.Type] && !passBy["mutation"]:
			// The agent wrote the test that proves its own code change, and
			// no mutation check showed the test exercises that change.
			d.Status = spec.StatusCorroborated
		case t1Pass == 0 && passBy["judge"]:
			// Only a model's reading supports it.
			d.Status = spec.StatusCorroborated
		}
	}
	return finishSection(d, c, clusterOrigin)
}

// decideStated settles a statement from the agent's reply once nothing
// contradicted it: an independent re-run or git check verifies it, the
// session record alone corroborates it, and anything the record couldn't
// speak to leaves it unverified.
func decideStated(d Decision, c *spec.Claim, vs []spec.Verdict, passBy map[string]bool, inconclusive []spec.Verdict, clusterOrigin string) Decision {
	for _, v := range inconclusive {
		d.Notes = append(d.Notes, v.Details)
	}
	switch {
	case len(inconclusive) > 0:
		d.Status = spec.StatusUnverified
	case passBy["tests"] || passBy["git"] || passBy["ci"]:
		d.Status = spec.StatusVerified
	case passBy["session"] || passBy["judge"]:
		d.Status = spec.StatusCorroborated
	default:
		d.Status = spec.StatusUnverified
		d.Notes = append(d.Notes, "This claim goes beyond the session record.")
	}
	return finishSection(d, c, clusterOrigin)
}

// codeChange types are capped when their only test proof was written in the
// same session. A test or diagnosis claim is about the test or repro itself.
var codeChange = map[string]bool{spec.TypeFix: true, spec.TypeFeature: true, spec.TypeRefactor: true, spec.TypeOps: true}

func missingNote(r string, c *spec.Claim, judgeAvailable bool, vs []spec.Verdict) string {
	switch r {
	case "tests":
		for _, v := range vs {
			if v.Verifier == "tests" {
				return "needs a passing test re-run"
			}
		}
		return "needs a test assertion to re-run"
	case "judge":
		if !judgeAvailable {
			return "needs a human review, or a blind judge once one is configured"
		}
		return "the blind judge asked for more evidence"
	case "ci":
		return "needs green CI"
	case "git":
		return "needs a commit assertion for a git check"
	}
	return fmt.Sprintf("needs %s", r)
}

func finishSection(d Decision, c *spec.Claim, clusterOrigin string) Decision {
	switch {
	case d.Status == spec.StatusContradicted || d.Status == spec.StatusOverstated:
		d.Section = spec.SectionContradicted
	case c.Type == spec.TypeNegativeResult:
		d.Section = spec.SectionFailures
	case clusterOrigin == spec.OriginAuditor:
		d.Section = spec.SectionAuditor
	case d.Status == spec.StatusVerified || d.Status == spec.StatusCorroborated:
		d.Section = spec.SectionVerified
	default:
		d.Section = spec.SectionUnverified
	}
	if d.Flags == nil {
		d.Flags = []string{}
	}
	return d
}

func appendOnce(xs []string, x string) []string {
	for _, y := range xs {
		if y == x {
			return xs
		}
	}
	return append(xs, x)
}

// StatusFactor is how much of a claim's weight its verification earns.
func StatusFactor(status string) float64 {
	switch status {
	case spec.StatusVerified:
		return 1
	case spec.StatusCorroborated:
		return 0.6
	case spec.StatusResolved, spec.StatusUnverified, spec.StatusProposed:
		return 0.25
	}
	return 0
}

// Value is the credit a claim carries given its status, review and survival.
func Value(pol *policy.Policy, typ, status, review, survival string) float64 {
	w := pol.Type(typ).Weight
	f := StatusFactor(status)
	switch review {
	case spec.ReviewAccepted, spec.ReviewEdited:
		if f < 0.6 && f > 0 {
			f = 0.6 // a human vouched for it
		}
	case spec.ReviewDisputed, spec.ReviewRejected:
		f = 0
	}
	switch survival {
	case spec.SurvivalReverted:
		if pol.Survival.OnRevert != "keep" {
			f = 0
		}
	case spec.SurvivalDecayed:
		f *= 0.5
	}
	return float64(int(w*f*100+0.5)) / 100
}
