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
	"github.com/BTONGK/credence/daemon/view"
	"github.com/BTONGK/credence/internal/config"
	"github.com/BTONGK/credence/internal/keys"
)

func TestDemoEndToEnd(t *testing.T) {
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
	if _, err := pe.Report(ctx, sid, pipeline.Options{Final: true}); err != nil {
		t.Fatal(err)
	}
	rep, err := view.BuildReport(st, sid)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, c := range Claims(st, rep) {
		t.Logf("%-12s %s\n             %s", c.Verdict, c.DisplayTitle, c.Reason)
		got[c.Claim.Statement] = c.Verdict
	}
	want := map[string]string{
		"I added the holiday hours to `index.html`.":            view.VerdictBacked,
		"I made the homepage easier to read on phones.":         view.VerdictPartly,
		"I also fixed the Instagram link in `footer.html`.":     view.VerdictContradicted,
		"I added a cake order form to `contact.html`.":          view.VerdictBacked,
		"All 6 tests pass.":                                     view.VerdictContradicted,
		"The site is live at https://sunrise-bakery.pages.dev.": view.VerdictBacked,
		"I updated your holiday hours on Google.":               view.VerdictUnchecked,
	}
	for s, v := range want {
		if got[s] != v {
			t.Errorf("%q: verdict %q, want %q", s, got[s], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("%d claims, want %d", len(got), len(want))
	}
}
