// Package quick checks what an agent said against the session record as it
// stands, without storing anything. It answers the one-line note Claude Code
// shows after a reply and the /credence skill; the full report still comes
// from the pipeline when the session finishes.
package quick

import (
	"context"
	"time"

	"github.com/BTONGK/credence/daemon/claims"
	"github.com/BTONGK/credence/daemon/store"
	"github.com/BTONGK/credence/daemon/view"
	"github.com/BTONGK/credence/internal/config"
	"github.com/BTONGK/credence/spec"
	"github.com/BTONGK/credence/verifiers"
)

// Claim is one checked statement.
type Claim struct {
	Statement string `json:"statement"`
	Verdict   string `json:"verdict"`
	Reason    string `json:"reason"`
}

// Result is a quick check of a reply or a whole session.
type Result struct {
	SessionID string         `json:"session_id"`
	Title     string         `json:"title"`
	Claims    []Claim        `json:"claims"`
	Counts    map[string]int `json:"counts"`
}

// Check verifies the claims in reply, or every claim the agent made in the
// session when reply is empty.
func Check(ctx context.Context, st *store.Store, cfg *config.Config, sessionID, reply string) (*Result, error) {
	d, err := claims.Load(st, sessionID)
	if err != nil {
		return nil, err
	}
	var cs []*spec.Claim
	if reply != "" {
		cs = claims.ReplyClaims(d, reply, time.Now())
	} else {
		cs = claims.ExtractStated(d)
	}
	res := &Result{SessionID: sessionID, Title: d.Session.Title, Claims: []Claim{}, Counts: map[string]int{}}
	vc := &verifiers.Context{Store: st, Data: d, Config: cfg}
	for _, c := range cs {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// The reply's evidence points at the record itself, so it resolves.
		vs := []spec.Verdict{{ClaimID: c.ID, Tier: 0, Verifier: "resolve", Check: "evidence", Result: spec.ResultPass}}
		vs = append(vs, verifiers.Session{}.Verify(ctx, vc, c)...)
		dec := verifiers.Decide(c, vs, d.Policy, "", false)
		var checks []view.CheckView
		for _, v := range vs {
			checks = append(checks, view.CheckView{Tier: v.Tier, Verifier: v.Verifier, Check: v.Check, Result: v.Result, Outcome: v.Outcome, Plain: view.Plain(v), Details: v.Details})
		}
		verdict, reason := view.VerdictOf(dec.Status, checks, dec.Notes)
		res.Claims = append(res.Claims, Claim{Statement: c.Statement, Verdict: verdict, Reason: reason})
		res.Counts[verdict]++
	}
	return res, nil
}
