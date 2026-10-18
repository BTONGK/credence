// Package survival re-checks accepted claims after their survival windows
// (7d and 30d by default): is the work still there, has it decayed, or was
// it reverted?
package survival

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/BTONGK/credence/adapters"
	"github.com/BTONGK/credence/daemon/ledger"
	"github.com/BTONGK/credence/daemon/store"
	"github.com/BTONGK/credence/internal/gitx"
	"github.com/BTONGK/credence/policy"
	"github.com/BTONGK/credence/spec"
)

// Check measures how much of a claim's committed work survives at ref.
func Check(repo string, commits []string, ref string) spec.SurvivalCheck {
	sc := spec.SurvivalCheck{Result: spec.SurvivalSurviving, Ratio: 1}
	if ref == "" {
		ref = gitx.DefaultBranch(repo)
	}
	total, alive := 0, 0
	var notes []string
	for _, sha := range commits {
		if rv := gitx.RevertsOf(repo, sha); len(rv) > 0 {
			sc.Result, sc.Ratio = spec.SurvivalReverted, 0
			sc.Details = fmt.Sprintf("commit %s was reverted by %s", short(sha), short(rv[0]))
			return sc
		}
		added, err := gitx.AddedLines(repo, sha)
		if err != nil {
			notes = append(notes, "commit "+short(sha)+" is gone")
			continue
		}
		for file, lines := range added {
			cur, err := gitx.FileAt(repo, ref, file)
			present := map[string]bool{}
			if err == nil {
				for _, h := range adapters.LineHashes(cur) {
					present[h] = true
				}
			}
			for _, l := range lines {
				h := adapters.LineHash(l)
				if h == "" {
					continue
				}
				total++
				if present[h] {
					alive++
				}
			}
		}
	}
	if total == 0 {
		if len(notes) > 0 {
			sc.Result, sc.Ratio, sc.Details = spec.SurvivalDecayed, 0, strings.Join(notes, "; ")
		} else {
			sc.Details = "no added lines to track"
		}
		return sc
	}
	sc.Ratio = float64(alive) / float64(total)
	switch {
	case alive == 0:
		sc.Result = spec.SurvivalReverted
		sc.Details = fmt.Sprintf("none of %d added lines remain on %s", total, ref)
	case sc.Ratio < 0.5:
		sc.Result = spec.SurvivalDecayed
		sc.Details = fmt.Sprintf("%d of %d added lines remain on %s", alive, total, ref)
	default:
		sc.Details = fmt.Sprintf("%d of %d added lines remain on %s", alive, total, ref)
	}
	return sc
}

// Worker runs due survival checks.
type Worker struct {
	Store  *store.Store
	Ledger *ledger.Ledger
	Now    func() time.Time
}

// RunDue checks every accepted claim whose window has elapsed. It returns
// the number of checks recorded.
func (w *Worker) RunDue(ctx context.Context) (int, error) {
	now := time.Now().UTC()
	if w.Now != nil {
		now = w.Now()
	}
	rows, err := w.Store.ListClaims(store.ClaimFilter{ActiveOnly: true})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range rows {
		if ctx.Err() != nil {
			break
		}
		if r.Review == nil || (r.ReviewState != spec.ReviewAccepted && r.ReviewState != spec.ReviewEdited) {
			continue
		}
		var commits []string
		for _, ev := range r.Claim.Evidence {
			if c := spec.ParseTarget(ev.Target).Commit; c != "" && c != "HEAD" && ev.Relation != spec.RelInformed {
				commits = append(commits, c)
			}
		}
		if len(commits) == 0 {
			continue
		}
		sess, err := w.Store.GetSession(r.Claim.SessionID)
		if err != nil || sess.Repo == "" {
			continue
		}
		pol, _ := policy.Load(sess.Repo)
		if pol == nil {
			pol = policy.Default()
		}
		done := map[string]bool{}
		entries, _ := w.Store.LedgerEntries(store.LedgerFilter{ClaimID: r.Claim.ID, Kind: "survival"})
		for _, e := range entries {
			if e.Survival != nil {
				done[e.Survival.Window] = true
			}
		}
		for _, win := range pol.Survival.Windows {
			d, err := policy.ParseWindow(win)
			if err != nil || done[win] || now.Before(r.Review.At.Add(d)) {
				continue
			}
			sc := Check(sess.Repo, commits, "")
			sc.Window = win
			if _, err := w.Ledger.Survival(ctx, r.Claim.ID, sc); err != nil {
				return n, err
			}
			n++
			break // one window per pass; the next runs later
		}
	}
	return n, nil
}

func short(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}
