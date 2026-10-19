package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/BTONGK/credence/daemon/quick"
	"github.com/BTONGK/credence/daemon/view"
	"github.com/BTONGK/credence/demo"
)

// TestCheckAndBackfill drives the two endpoints the reply note, the
// /credence skill and guided setup use, against the bakery example sitting
// in a Claude Code projects folder.
func TestCheckAndBackfill(t *testing.T) {
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("CREDENCE_HOME", filepath.Join(dir, "credence"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "claude"))
	t.Setenv("GIT_CEILING_DIRECTORIES", dir)
	if _, err := demo.Build(context.Background(), dir, time.Now().Add(-40*time.Minute)); err != nil {
		t.Fatal(err)
	}
	d, err := Open(Options{Log: io.Discard, NoScan: true})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.Info = &Info{Token: "test-token"}
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()
	post := func(path string, body any, out any) {
		t.Helper()
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest("POST", srv.URL+path, bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer test-token")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			msg, _ := io.ReadAll(resp.Body)
			t.Fatalf("%s: %s %s", path, resp.Status, msg)
		}
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}

	// The skill names only the session; the daemon finds its transcript.
	var res quick.Result
	post("/api/check", CheckRequest{Harness: "claude-code", Session: "5d1e8b2a-7c4f-4e9a-b6d3-2a9f1c7e0b44"}, &res)
	if len(res.Claims) != 7 || res.Counts[view.VerdictContradicted] != 2 {
		t.Fatalf("session check: %d claims, counts %v", len(res.Claims), res.Counts)
	}

	// The Stop hook adds the reply text.
	post("/api/check", CheckRequest{Harness: "claude-code", Session: "5d1e8b2a-7c4f-4e9a-b6d3-2a9f1c7e0b44", Reply: "All 6 tests pass."}, &res)
	if len(res.Claims) != 1 || res.Claims[0].Verdict != view.VerdictContradicted {
		t.Fatalf("reply check: %+v", res.Claims)
	}

	// The session went quiet 40 minutes ago, so backfill closes it and
	// queues its final report; a second backfill finds it queued already.
	var bf BackfillResult
	post("/api/backfill", map[string]string{"since": "7d"}, &bf)
	if bf.Queued != 1 {
		t.Fatalf("backfill queued %d reports, want 1", bf.Queued)
	}
	post("/api/backfill", map[string]string{"since": "7d"}, &bf)
	if bf.Queued != 0 {
		t.Fatalf("second backfill queued %d reports, want 0", bf.Queued)
	}
}
