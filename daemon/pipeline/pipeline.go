// Package pipeline turns a captured session into its credit report:
// self-claims and auditor claims are extracted, reconciled into clusters,
// verified tier by tier, attributed and valued. Provisional reports run
// after each turn; the final report runs when the session closes.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/BTONGK/credence/daemon/attribution"
	"github.com/BTONGK/credence/daemon/auditor"
	"github.com/BTONGK/credence/daemon/calibration"
	"github.com/BTONGK/credence/daemon/claims"
	"github.com/BTONGK/credence/daemon/ledger"
	"github.com/BTONGK/credence/daemon/redact"
	"github.com/BTONGK/credence/daemon/store"
	"github.com/BTONGK/credence/internal/config"
	"github.com/BTONGK/credence/llm"
	"github.com/BTONGK/credence/policy"
	"github.com/BTONGK/credence/spec"
	"github.com/BTONGK/credence/verifiers"
)

// Engine runs report pipelines.
type Engine struct {
	Store    *store.Store
	Cfg      *config.Config
	Ledger   *ledger.Ledger
	Redactor *redact.Redactor
	GitHub   *verifiers.GitHub
	Plugins  []*verifiers.Plugin
	Log      *log.Logger
	// Changed is called after a report is written.
	Changed func(sessionID string)

	locks sync.Map
}

// Summary describes a report run.
type Summary struct {
	SessionID string         `json:"session_id"`
	State     string         `json:"state"`
	Auditor   string         `json:"auditor"`
	Fallback  string         `json:"fallback,omitempty"`
	Skipped   string         `json:"skipped,omitempty"`
	Claims    int            `json:"claims"`
	Sections  map[string]int `json:"sections"`
	Duration  time.Duration  `json:"duration"`
}

func (e *Engine) logf(format string, args ...any) {
	if e.Log != nil {
		e.Log.Printf(format, args...)
	}
}

func (e *Engine) lock(id string) func() {
	m, _ := e.locks.LoadOrStore(id, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// Options tune a report run.
type Options struct {
	Final bool
	Rerun bool // re-run tests even when cached
	// Reverify forces verification of claims that were already verified.
	Reverify bool
}

// Report builds or refreshes a session's credit report.
func (e *Engine) Report(ctx context.Context, sessionID string, o Options) (*Summary, error) {
	start := time.Now()
	unlock := e.lock(sessionID)
	defer unlock()
	d, err := claims.Load(e.Store, sessionID)
	if err != nil {
		return nil, err
	}
	if d.Session.ParentSessionID != "" {
		// Subagent work is reported in its parent's report.
		return nil, fmt.Errorf("session %s is a subagent of %s", sessionID, d.Session.ParentSessionID)
	}
	state := "provisional"
	if o.Final {
		state = "final"
	}
	sum := &Summary{SessionID: sessionID, State: state, Sections: map[string]int{}}
	if d.PolicyEdited {
		_ = e.Store.AddSessionFlag(sessionID, "policy_edited")
	}

	self := claims.ExtractSelfClaims(d)
	var aud auditor.Result
	if o.Final {
		aud = auditor.Run(ctx, e.Store, e.Cfg, d)
	} else {
		// Provisional reports never spend model tokens.
		aud = auditor.Result{Auditor: auditor.RulesID}
		if r := auditor.Skip(d); r != "" {
			aud.Skipped = r
		} else {
			aud.Claims = auditor.Rules{}.Audit(d)
		}
	}
	sum.Auditor, sum.Fallback, sum.Skipped = aud.Auditor, aud.Fallback, aud.Skipped
	if aud.Fallback != "" {
		e.logf("session %s: model auditor unavailable (%s); used rules", sessionID, aud.Fallback)
	}
	if aud.Skipped != "" && len(self) == 0 {
		_ = e.Store.SetSkipped(sessionID, true, aud.Skipped)
		_ = e.Store.DeactivateClaims(sessionID, spec.OriginAuditor, nil)
		_ = e.Store.SetReportState(sessionID, state, "")
		e.changed(sessionID)
		sum.Duration = time.Since(start)
		return sum, nil
	}
	_ = e.Store.SetSkipped(sessionID, false, "")

	keepSelf, keepAud := map[string]bool{}, map[string]bool{}
	for _, c := range self {
		if _, err := e.Store.InsertClaim(c); err != nil {
			return nil, err
		}
		keepSelf[c.ID] = true
	}
	for _, c := range aud.Claims {
		if _, err := e.Store.InsertClaim(c); err != nil {
			return nil, err
		}
		keepAud[c.ID] = true
	}
	if err := e.Store.DeactivateClaims(sessionID, spec.OriginSelf, keepSelf); err != nil {
		return nil, err
	}
	if err := e.Store.DeactivateClaims(sessionID, spec.OriginAuditor, keepAud); err != nil {
		return nil, err
	}
	rows, err := e.Store.SessionClaims(sessionID)
	if err != nil {
		return nil, err
	}
	all := make([]*spec.Claim, 0, len(rows))
	byID := map[string]*store.ClaimRow{}
	for _, r := range rows {
		c := r.Claim
		all = append(all, &c)
		byID[c.ID] = r
	}
	clusters := claims.Reconcile(d, all)
	if err := e.Store.ReplaceClusters(ctx, sessionID, clusters); err != nil {
		return nil, err
	}
	clusterOf := map[string]*store.Cluster{}
	for i := range clusters {
		cl := &clusters[i]
		for _, m := range cl.Members {
			clusterOf[m] = cl
			_ = e.Store.SetClaimCluster(m, cl.ID, m == cl.PrimaryClaimID)
		}
	}

	judge := e.judge(d)
	vc := &verifiers.Context{Store: e.Store, Data: d, Config: e.Cfg, Redactor: e.Redactor, Judge: judge, GitHub: e.GitHub,
		Plugins: e.Plugins, Rerun: o.Rerun, Log: e.Log}
	prov := attribution.SessionProvenance(d)
	if d.Session.Repo != "" {
		if arts, err := e.Store.ArtifactsByKind("line_provenance", d.Session.Repo); err == nil {
			prov.Merge(arts)
		}
	}
	var stats []*calibration.Stat
	primaryDecision := map[string]verifiers.Decision{}
	// Verify primaries first so members can inherit their section.
	sort.SliceStable(all, func(i, j int) bool {
		return clusterOf[all[i].ID].PrimaryClaimID == all[i].ID && clusterOf[all[j].ID].PrimaryClaimID != all[j].ID
	})
	for _, c := range all {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		row := byID[c.ID]
		cl := clusterOf[c.ID]
		primary := cl != nil && cl.PrimaryClaimID == c.ID
		var vs []spec.Verdict
		if row.VerifyRun > 0 && !o.Reverify && !o.Final {
			vs, _ = e.Store.Verdicts(c.ID, -1)
		} else {
			maxTier := 1
			if o.Final && primary {
				maxTier = 2
			}
			run, err := e.Store.BumpVerifyRun(c.ID)
			if err != nil {
				return nil, err
			}
			vc.Run = run
			vs = verifiers.VerifyClaim(ctx, vc, c, "", maxTier)
			for i := range vs {
				if err := e.Store.InsertVerdict(&vs[i], run); err != nil {
					return nil, err
				}
			}
		}
		origin := ""
		var flags []string
		if cl != nil {
			origin = cl.Origin
			flags = append(flags, cl.Flags...)
		}
		dec := verifiers.Decide(c, vs, d.Policy, origin, judge != nil)
		if primary {
			primaryDecision[cl.ID] = dec
		} else if cl != nil {
			if pd, ok := primaryDecision[cl.ID]; ok {
				dec.Section = pd.Section
			}
			flags = append(flags, "duplicate_of:"+cl.PrimaryClaimID)
		}
		flags = append(flags, dec.Flags...)
		if d.PolicyEdited {
			flags = append(flags, "policy_edited")
		}
		a := attribution.Compute(d, clusterView(c, cl, byID), prov, d.Policy.Attribution.DirectionShare)
		_ = e.Store.PutAttribution(a, false)
		value := verifiers.Value(d.Policy, c.Type, dec.Status, row.ReviewState, row.SurvivalState)
		if !primary {
			value = 0 // credit is counted once per cluster
		}
		if err := e.Store.SetClaimState(c.ID, store.ClaimState{Status: dec.Status, Section: dec.Section, Flags: uniq(flags), Notes: dec.Notes, Value: value}); err != nil {
			return nil, err
		}
		if primary {
			sum.Claims++
			sum.Sections[dec.Section]++
		}
		if o.Final && primary && row.ReviewState == spec.ReviewPending {
			if stats == nil {
				stats, _ = calibration.Compute(e.Store, calibration.ByAgent, time.Time{})
			}
			e.autoAccept(ctx, d.Policy, c, dec, stats)
		}
	}
	_ = e.Store.SetReportState(sessionID, state, reportHash(e.Store, sessionID))
	e.changed(sessionID)
	sum.Duration = time.Since(start)
	e.logf("session %s: %s report, %d claims %v (%s)", sessionID, state, sum.Claims, sum.Sections, sum.Duration.Round(time.Millisecond))
	return sum, nil
}

func (e *Engine) changed(id string) {
	if e.Changed != nil {
		e.Changed(id)
	}
}

// judge builds the tier-2 provider when configured and allowed by the repo.
func (e *Engine) judge(d *claims.Data) llm.Provider {
	if e.Cfg == nil {
		return nil
	}
	mc := auditor.Effective(e.Cfg.Judge, d.Policy.Judge)
	p, err := llm.New(mc)
	if err != nil {
		if !errors.Is(err, llm.ErrDisabled) {
			e.logf("judge unavailable: %v", err)
		}
		return nil
	}
	return p
}

// autoAccept accepts verified low-stakes claims from well-calibrated agent
// configurations when the policy allows it.
func (e *Engine) autoAccept(ctx context.Context, pol *policy.Policy, c *spec.Claim, dec verifiers.Decision, stats []*calibration.Stat) {
	ct := pol.Type(c.Type)
	if ct.AutoAccept == nil || ct.HumanReview == "required" || dec.Status != spec.StatusVerified || c.Origin != spec.OriginSelf || e.Ledger == nil {
		return
	}
	cfg := calibration.ConfigOf(c.Claimant)
	st := calibration.For(stats, cfg)
	if st == nil || st.BrierN < ct.AutoAccept.MinSamples || st.Brier > ct.AutoAccept.MaxBrier {
		return
	}
	_, err := e.Ledger.Decide(ctx, c.ID, spec.HumanDecision{Decision: spec.ReviewAccepted, By: "policy:auto_accept",
		Note: fmt.Sprintf("%s has Brier %.3f over %d claims (policy max %.2f)", cfg, st.Brier, st.BrierN, ct.AutoAccept.MaxBrier)})
	if err != nil {
		e.logf("auto-accept %s: %v", c.ID, err)
	}
}

func reportHash(st *store.Store, sessionID string) string {
	rows, _ := st.SessionClaims(sessionID)
	var parts []string
	for _, r := range rows {
		parts = append(parts, r.Claim.ID+":"+r.Status+":"+r.Section)
	}
	sort.Strings(parts)
	return spec.HashBytes([]byte(strings.Join(parts, ",")))
}

// clusterView is the primary claim with its cluster's evidence and
// co-contributors merged in, so credit reflects everything the cluster found.
func clusterView(c *spec.Claim, cl *store.Cluster, byID map[string]*store.ClaimRow) *spec.Claim {
	if cl == nil || cl.PrimaryClaimID != c.ID || len(cl.Members) < 2 {
		return c
	}
	v := *c
	v.Evidence = append([]spec.Evidence{}, c.Evidence...)
	v.CoContributors = append([]spec.CoContributor{}, c.CoContributors...)
	seenEv := map[string]bool{}
	for _, e := range v.Evidence {
		seenEv[e.Target] = true
	}
	seenCo := map[string]bool{}
	for _, cc := range v.CoContributors {
		seenCo[cc.ID] = true
	}
	for _, m := range cl.Members {
		r := byID[m]
		if r == nil || m == c.ID {
			continue
		}
		for _, e := range r.Claim.Evidence {
			if !seenEv[e.Target] && !strings.HasPrefix(e.Target, "unresolved:") {
				seenEv[e.Target] = true
				v.Evidence = append(v.Evidence, e)
			}
		}
		for _, cc := range r.Claim.CoContributors {
			if !seenCo[cc.ID] {
				seenCo[cc.ID] = true
				v.CoContributors = append(v.CoContributors, cc)
			}
		}
	}
	return &v
}

func uniq(xs []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, x := range xs {
		if x != "" && !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// VerifyOne re-verifies a single claim on demand (`credence verify`).
func (e *Engine) VerifyOne(ctx context.Context, claimID string, rerun bool) ([]spec.Verdict, *verifiers.Decision, error) {
	row, err := e.Store.GetClaim(claimID)
	if err != nil {
		return nil, nil, err
	}
	unlock := e.lock(row.Claim.SessionID)
	defer unlock()
	d, err := claims.Load(e.Store, row.Claim.SessionID)
	if err != nil {
		return nil, nil, err
	}
	judge := e.judge(d)
	vc := &verifiers.Context{Store: e.Store, Data: d, Config: e.Cfg, Redactor: e.Redactor, Judge: judge, GitHub: e.GitHub,
		Plugins: e.Plugins, Rerun: rerun, Log: e.Log}
	run, err := e.Store.BumpVerifyRun(row.Claim.ID)
	if err != nil {
		return nil, nil, err
	}
	vc.Run = run
	c := row.Claim
	maxTier := 1
	if row.Primary {
		maxTier = 2
	}
	vs := verifiers.VerifyClaim(ctx, vc, &c, "", maxTier)
	for i := range vs {
		_ = e.Store.InsertVerdict(&vs[i], run)
	}
	origin, primary := "", ""
	var flags []string
	cls, _ := e.Store.Clusters(row.Claim.SessionID)
	for _, cl := range cls {
		if cl.ID == row.ClusterID {
			origin, primary = cl.Origin, cl.PrimaryClaimID
			flags = append(flags, cl.Flags...)
		}
	}
	dec := verifiers.Decide(&c, vs, d.Policy, origin, judge != nil)
	if !row.Primary {
		dec.Section = row.Section
		if primary != "" {
			flags = append(flags, "duplicate_of:"+primary)
		}
	}
	value := verifiers.Value(d.Policy, c.Type, dec.Status, row.ReviewState, row.SurvivalState)
	if !row.Primary {
		value = 0
	}
	flags = append(flags, dec.Flags...)
	_ = e.Store.SetClaimState(c.ID, store.ClaimState{Status: dec.Status, Section: dec.Section, Flags: uniq(flags), Notes: dec.Notes, Value: value})
	e.changed(row.Claim.SessionID)
	return vs, &dec, nil
}
