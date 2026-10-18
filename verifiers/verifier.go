// Package verifiers checks claims against evidence in tiers. Tier 0
// resolves every evidence target; tier 1 runs deterministic checks (git,
// tests in a clean worktree, CI); tier 2 is the blind model judge; tier 3
// is the human. A later tier never overrules an earlier factual failure.
package verifiers

import (
	"context"
	"log"
	"time"

	"github.com/BTONGK/credence/daemon/claims"
	"github.com/BTONGK/credence/daemon/redact"
	"github.com/BTONGK/credence/daemon/store"
	"github.com/BTONGK/credence/internal/config"
	"github.com/BTONGK/credence/llm"
	"github.com/BTONGK/credence/spec"
)

// Context is what verifiers may use. Verifiers never see who made a claim
// beyond what the claim's evidence shows.
type Context struct {
	Store    *store.Store
	Data     *claims.Data
	Config   *config.Config
	Redactor *redact.Redactor
	Judge    llm.Provider // nil when no judge is configured
	GitHub   *GitHub
	Plugins  []*Plugin
	Run      int
	Rerun    bool // ignore cached test runs
	Log      *log.Logger
	Now      func() time.Time

	prior []spec.Verdict // verdicts earlier verifiers produced for the claim
}

func (vc *Context) now() time.Time {
	if vc.Now != nil {
		return vc.Now().UTC()
	}
	return time.Now().UTC()
}

func (vc *Context) logf(format string, args ...any) {
	if vc.Log != nil {
		vc.Log.Printf(format, args...)
	}
}

// Verifier checks some aspect of a claim.
type Verifier interface {
	Name() string
	Tier() int
	Verify(ctx context.Context, vc *Context, c *spec.Claim) []spec.Verdict
}

func verdict(vc *Context, c *spec.Claim, verifier string, tier int, check, result, outcome, details string, a spec.Assertion, started time.Time) spec.Verdict {
	v := spec.Verdict{ClaimID: c.ID, Verifier: verifier, Tier: tier, Check: check, Result: result, Outcome: outcome,
		Details: details, Assertion: a, CreatedAt: vc.now()}
	if !started.IsZero() {
		v.DurationMs = time.Since(started).Milliseconds()
	}
	v.AssignID()
	return v
}

// Pipeline is the ordered set of verifiers.
func Pipeline(vc *Context) []Verifier {
	vs := []Verifier{Resolve{}, Git{}, Session{}, &Tests{}, &Mutation{}, GitHubChecks{}}
	for _, p := range vc.Plugins {
		vs = append(vs, p)
	}
	vs = append(vs, &Judge{})
	return vs
}

// VerifyClaim runs every tier in order and stops early on blocking or
// failing results where a later tier could not change the outcome.
func VerifyClaim(ctx context.Context, vc *Context, c *spec.Claim, only string, maxTier int) []spec.Verdict {
	var all []spec.Verdict
	for _, v := range Pipeline(vc) {
		if only != "" && v.Name() != only && v.Tier() != 0 {
			continue
		}
		if v.Tier() > maxTier {
			continue
		}
		if v.Tier() == 2 && hasFailure(all) {
			// Tier 2 cannot rescue a factual failure; skip the model call.
			continue
		}
		vc.prior = all
		vs := v.Verify(ctx, vc, c)
		all = append(all, vs...)
		if v.Tier() == 0 && blocked(vs) {
			break
		}
		if ctx.Err() != nil {
			break
		}
	}
	return all
}

func blocked(vs []spec.Verdict) bool {
	for _, v := range vs {
		if v.Result == spec.ResultBlocked || (v.Tier == 0 && v.Result == spec.ResultFail) {
			return true
		}
	}
	return false
}

func hasFailure(vs []spec.Verdict) bool {
	for _, v := range vs {
		if v.Result == spec.ResultFail && v.Tier <= 1 {
			return true
		}
	}
	return false
}
