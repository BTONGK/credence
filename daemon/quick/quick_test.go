package quick

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/BTONGK/credence/daemon/store"
	"github.com/BTONGK/credence/daemon/view"
	"github.com/BTONGK/credence/demo"
	"github.com/BTONGK/credence/internal/config"
)

func TestCheckReplyAndSession(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("CREDENCE_HOME", homeDir)
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	ctx := context.Background()
	sc, err := demo.Build(ctx, dir, time.Now().Add(-5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(homeDir, "credence.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := config.Default()
	sid, err := demo.Ingest(ctx, st, cfg, sc, nil)
	if err != nil {
		t.Fatal(err)
	}

	// The last reply, as the Stop hook hands it over.
	res, err := Check(ctx, st, cfg, sid, "I updated your holiday hours on Google.")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Claims) != 1 || res.Claims[0].Verdict != view.VerdictUnchecked {
		t.Fatalf("Google reply: %+v", res.Claims)
	}

	// A reply the transcript doesn't hold yet is checked against the record so far.
	res, err = Check(ctx, st, cfg, sid, "All 6 tests pass. The site is live at https://sunrise-bakery.pages.dev.")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, c := range res.Claims {
		got[c.Statement] = c.Verdict
		t.Logf("%-12s %s — %s", c.Verdict, c.Statement, c.Reason)
	}
	if got["All 6 tests pass."] != view.VerdictContradicted || got["The site is live at https://sunrise-bakery.pages.dev."] != view.VerdictBacked {
		t.Fatalf("new reply: %v", got)
	}

	// The whole session matches the full report's verdicts.
	res, err = Check(ctx, st, cfg, sid, "")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{view.VerdictBacked: 3, view.VerdictPartly: 1, view.VerdictUnchecked: 1, view.VerdictContradicted: 2}
	for k, n := range want {
		if res.Counts[k] != n {
			t.Errorf("session counts %v, want %v", res.Counts, want)
			break
		}
	}
}
