package demo

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/BTONGK/credence/daemon/ledger"
	"github.com/BTONGK/credence/daemon/pipeline"
	"github.com/BTONGK/credence/daemon/store"
	"github.com/BTONGK/credence/internal/config"
	"github.com/BTONGK/credence/internal/keys"
	"github.com/BTONGK/credence/spec"
)

func TestDemoEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("runs real tests in worktrees")
	}
	homeDir := t.TempDir()
	t.Setenv("CREDENCE_HOME", homeDir)
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	ctx := context.Background()
	sc, err := Build(ctx, dir, time.Now().Add(-5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(homeDir, "credence.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := config.Default()
	logger := log.New(os.Stderr, "demo ", log.Ltime)
	sid, err := Ingest(ctx, st, cfg, sc, logger)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := keys.Load()
	if err != nil {
		t.Fatal(err)
	}
	pe := &pipeline.Engine{Store: st, Cfg: cfg, Ledger: &ledger.Ledger{Store: st, Signer: signer}, Log: logger}
	sum, err := pe.Report(ctx, sid, pipeline.Options{Final: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("summary: %+v", sum)
	rows, _ := st.SessionClaims(sid)
	bySection := map[string][]string{}
	for _, r := range rows {
		vs, _ := st.Verdicts(r.Claim.ID, -1)
		t.Logf("[%s] %-18s %-12s primary=%v %s/%s %q flags=%v notes=%v", r.Section, r.Claim.Origin, r.Claim.Type, r.Primary, r.Status, r.ReviewState, r.Claim.Title, r.Flags, r.Notes)
		for _, v := range vs {
			t.Logf("      t%d %s/%s %s %s: %s", v.Tier, v.Verifier, v.Check, v.Result, v.Outcome, v.Details)
		}
		if a, _ := st.GetAttribution(r.Claim.ID); a != nil {
			t.Logf("      attribution %s %+v", a.Method, a.Shares)
		}
		if r.Primary {
			bySection[r.Section] = append(bySection[r.Section], r.Claim.Title)
		}
	}
	want := map[string]string{
		"Fixed race condition in job scheduler":               spec.SectionVerified,
		"Diagnosed check-then-pop race in JobQueue._take":     spec.SectionVerified,
		"Added regression test for concurrent job completion": spec.SectionUnverified,
		"All tests pass after the fix":                        spec.SectionContradicted,
		"Retrying popleft in _take does not fix the flake":    spec.SectionFailures,
	}
	for _, r := range rows {
		if exp, ok := want[r.Claim.Title]; ok && r.Primary && r.Section != exp {
			t.Errorf("%q in %s, want %s", r.Claim.Title, r.Section, exp)
		}
	}
	if len(bySection[spec.SectionAuditor]) == 0 {
		t.Errorf("expected the CI change to be found by the auditor")
	}
}
