// Package calibration measures how well each agent configuration's
// self-reported confidence matches outcomes (Brier score) and summarizes
// its reputation: verification, review and survival rates and total value.
package calibration

import (
	"sort"
	"strings"
	"time"

	"github.com/BTONGK/credence/daemon/store"
	"github.com/BTONGK/credence/spec"
)

// Stat is the reputation of one agent configuration (or repo, or harness).
type Stat struct {
	Key            string  `json:"key"`
	Harness        string  `json:"harness,omitempty"`
	Model          string  `json:"model,omitempty"`
	Sessions       int     `json:"sessions"`
	Claims         int     `json:"claims"`
	SelfClaims     int     `json:"self_claims"`
	FoundByAuditor int     `json:"found_by_auditor"`
	Verified       int     `json:"verified"`
	Corroborated   int     `json:"corroborated"`
	Contradicted   int     `json:"contradicted"`
	Unverified     int     `json:"unverified"`
	Accepted       int     `json:"accepted"`
	Edited         int     `json:"edited"`
	Disputed       int     `json:"disputed"`
	Rejected       int     `json:"rejected"`
	Surviving      int     `json:"surviving"`
	Decayed        int     `json:"decayed"`
	Reverted       int     `json:"reverted"`
	Value          float64 `json:"value"`
	Brier          float64 `json:"brier"`
	BrierN         int     `json:"brier_n"`
	Overconfidence float64 `json:"overconfidence"` // mean confidence minus mean outcome
	CostUSD        float64 `json:"cost_usd"`
	ValuePerDollar float64 `json:"value_per_dollar"`
}

// Outcome is the observed truth of a self-claim: 1 true, 0 false, -1 unknown.
func Outcome(r *store.ClaimRow) float64 {
	switch r.ReviewState {
	case spec.ReviewAccepted:
		return 1
	case spec.ReviewEdited:
		return 0.5
	case spec.ReviewDisputed, spec.ReviewRejected:
		return 0
	}
	switch r.Status {
	case spec.StatusVerified:
		return 1
	case spec.StatusContradicted, spec.StatusOverstated:
		return 0
	}
	return -1
}

// ConfigOf extracts cfg_xxxx from an agent contributor id.
func ConfigOf(actor string) string {
	if i := strings.LastIndex(actor, "@"); i >= 0 && strings.HasPrefix(actor, "agent:") {
		return actor[i+1:]
	}
	return ""
}

// GroupBy selects the reputation key.
type GroupBy string

const (
	ByAgent   GroupBy = "agent"
	ByRepo    GroupBy = "repo"
	ByHarness GroupBy = "harness"
	ByModel   GroupBy = "model"
)

// Compute aggregates active primary claims since a time.
func Compute(st *store.Store, by GroupBy, since time.Time) ([]*Stat, error) {
	rows, err := st.ListClaims(store.ClaimFilter{ActiveOnly: true, Since: since})
	if err != nil {
		return nil, err
	}
	sessions := map[string]*store.SessionRow{}
	stats := map[string]*Stat{}
	seenSess := map[string]map[string]bool{}
	var sumP, sumY = map[string]float64{}, map[string]float64{}
	for _, r := range rows {
		s, ok := sessions[r.Claim.SessionID]
		if !ok {
			s, _ = st.GetSession(r.Claim.SessionID)
			sessions[r.Claim.SessionID] = s
		}
		if s == nil {
			continue
		}
		key := ""
		switch by {
		case ByRepo:
			key = s.RepoName
		case ByHarness:
			key = s.Harness
		case ByModel:
			if len(s.Models) > 0 {
				key = s.Models[0]
			}
		default:
			// Claims found by the auditor still describe the session agent's work.
			key = r.Claim.Claimant
			if !strings.HasPrefix(key, "agent:") {
				key = s.AgentConfig
			}
		}
		if key == "" {
			key = "unknown"
		}
		x := stats[key]
		if x == nil {
			x = &Stat{Key: key, Harness: s.Harness}
			if len(s.Models) > 0 {
				x.Model = s.Models[0]
			}
			stats[key] = x
			seenSess[key] = map[string]bool{}
		}
		if !seenSess[key][s.ID] {
			seenSess[key][s.ID] = true
			x.Sessions++
			x.CostUSD += s.CostUSD
		}
		if !r.Primary {
			continue
		}
		x.Claims++
		if r.Claim.Origin == spec.OriginSelf {
			x.SelfClaims++
		}
		if r.Section == spec.SectionAuditor {
			x.FoundByAuditor++
		}
		switch r.Status {
		case spec.StatusVerified:
			x.Verified++
		case spec.StatusCorroborated:
			x.Corroborated++
		case spec.StatusContradicted, spec.StatusOverstated:
			x.Contradicted++
		default:
			x.Unverified++
		}
		switch r.ReviewState {
		case spec.ReviewAccepted:
			x.Accepted++
		case spec.ReviewEdited:
			x.Edited++
		case spec.ReviewDisputed:
			x.Disputed++
		case spec.ReviewRejected:
			x.Rejected++
		}
		switch r.SurvivalState {
		case spec.SurvivalSurviving:
			x.Surviving++
		case spec.SurvivalDecayed:
			x.Decayed++
		case spec.SurvivalReverted:
			x.Reverted++
		}
		x.Value += r.Value
		if r.Claim.Origin == spec.OriginSelf && r.Claim.SelfConfidence != nil {
			if y := Outcome(r); y >= 0 {
				p := *r.Claim.SelfConfidence
				x.Brier += (p - y) * (p - y)
				x.BrierN++
				sumP[key] += p
				sumY[key] += y
			}
		}
	}
	var out []*Stat
	for k, x := range stats {
		if x.BrierN > 0 {
			x.Brier /= float64(x.BrierN)
			x.Overconfidence = (sumP[k] - sumY[k]) / float64(x.BrierN)
		}
		if x.CostUSD > 0 {
			x.ValuePerDollar = x.Value / x.CostUSD
		}
		out = append(out, x)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Value != out[j].Value {
			return out[i].Value > out[j].Value
		}
		return out[i].Key < out[j].Key
	})
	return out, nil
}

// For returns the stat for one key, or nil.
func For(stats []*Stat, key string) *Stat {
	for _, s := range stats {
		if s.Key == key {
			return s
		}
	}
	return nil
}
