package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BTONGK/credence/daemon/view"
	"github.com/BTONGK/credence/demo"
)

// TestSummary sums up the bakery example in plain numbers: the files it
// wrote, the test run that failed, the site it published and the app it
// worked in.
func TestSummary(t *testing.T) {
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
	call := func(method, path, body string, out any) {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
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

	// A check reads the transcript into the store.
	var res map[string]any
	call("POST", "/api/check", `{"harness":"claude-code","session":"5d1e8b2a-7c4f-4e9a-b6d3-2a9f1c7e0b44"}`, &res)

	var sum view.Summary
	call("GET", "/api/summary?range=all", "", &sum)
	if sum.Sessions != 1 || len(sum.Requests) != 1 {
		t.Fatalf("summary covers %d sessions, %d requests", sum.Sessions, len(sum.Requests))
	}
	tiles := map[string]view.Tile{}
	for _, tl := range sum.Tiles {
		tiles[tl.Key] = tl
	}
	if tiles["files"].Value == 0 || tiles["lines"].Value == 0 {
		t.Errorf("files and lines tiles: %+v %+v", tiles["files"], tiles["lines"])
	}
	if tl := tiles["tests"]; tl.Value != 1 || !strings.Contains(tl.Detail, "1 failed") {
		t.Errorf("tests tile: %+v", tl)
	}
	if tl := tiles["published"]; tl.Value != 1 || len(tl.Items) != 1 || !strings.Contains(tl.Items[0], "sunrise-bakery") {
		t.Errorf("published tile: %+v", tl)
	}
	var looks []string
	for _, n := range sum.Look {
		looks = append(looks, n.Text)
	}
	if !contains(looks, "Tests were failing when 1 session ended") {
		t.Errorf("needs a look: %q", looks)
	}
	if r := sum.Requests[0]; !r.Look || len(r.Facts) == 0 {
		t.Errorf("request: %+v", r)
	}

	// The default period is the last 7 days, and the session falls inside it.
	call("GET", "/api/summary", "", &sum)
	if sum.Sessions != 1 {
		t.Errorf("7-day summary covers %d sessions", sum.Sessions)
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
