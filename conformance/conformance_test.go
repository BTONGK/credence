// Package conformance replays fixture sessions through the harness adapters
// and compares the normalized events with expected outputs. An adapter that
// reproduces these files is compatible; to add a harness, add a directory
// named after it with transcripts and run with CREDENCE_UPDATE_FIXTURES=1,
// then review the generated *.expected.json by hand.
package conformance

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BTONGK/credence/adapters"
	_ "github.com/BTONGK/credence/adapters/claudecode"
	_ "github.com/BTONGK/credence/adapters/codex"
	_ "github.com/BTONGK/credence/adapters/cursor"
	_ "github.com/BTONGK/credence/adapters/generic"
)

// Event is a normalized event as an adapter emits it.
type Event struct {
	Line    int            `json:"line"`
	TS      time.Time      `json:"ts"`
	Kind    string         `json:"kind"`
	Role    string         `json:"role"`
	Payload map[string]any `json:"payload"`
}

// Session is the expected output for one transcript.
type Session struct {
	Harness          string   `json:"harness"`
	HarnessSessionID string   `json:"harness_session_id"`
	ReportedHarness  string   `json:"reported_harness,omitempty"`
	Cwd              string   `json:"cwd,omitempty"`
	Branch           string   `json:"branch,omitempty"`
	Model            string   `json:"model,omitempty"`
	Version          string   `json:"version,omitempty"`
	StartedAt        string   `json:"started_at,omitempty"`
	Tokens           [4]int64 `json:"tokens"` // input, output, cache read, cache write
	Events           []Event  `json:"events"`
}

func replay(t *testing.T, harness, path string) Session {
	t.Helper()
	a, ok := adapters.Get(harness)
	if !ok {
		t.Fatalf("no adapter %q", harness)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	// Adapters key some behaviour on where a transcript lives (subagent
	// directories, rollout names), so parse it under its own name.
	p := a.NewParser("/fixtures/" + harness + "/" + filepath.Base(path))
	s := Session{Harness: harness, Events: []Event{}}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for n := 1; sc.Scan(); n++ {
		res, err := p.Parse(sc.Bytes(), n)
		if err != nil {
			t.Fatalf("%s:%d: %v", path, n, err)
		}
		m := res.Meta
		set := func(dst *string, v string) {
			if v != "" {
				*dst = v
			}
		}
		set(&s.HarnessSessionID, m.HarnessSessionID)
		set(&s.ReportedHarness, m.Harness)
		set(&s.Cwd, m.Cwd)
		set(&s.Branch, m.Branch)
		set(&s.Model, m.Model)
		set(&s.Version, m.Version)
		if !m.StartedAt.IsZero() && s.StartedAt == "" {
			s.StartedAt = m.StartedAt.UTC().Format(time.RFC3339)
		}
		tok := [4]int64{m.TokensIn, m.TokensOut, m.CacheRead, m.CacheWrite}
		for i := range tok {
			if m.AbsoluteTokens {
				if tok[i] != 0 || i < 3 {
					s.Tokens[i] = tok[i]
				}
			} else {
				s.Tokens[i] += tok[i]
			}
		}
		for _, d := range res.Drafts {
			s.Events = append(s.Events, Event{Line: d.SrcLine, TS: d.TS.UTC(), Kind: d.Kind, Role: d.Role, Payload: roundTrip(d.Payload)})
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return s
}

// roundTrip normalizes payload values to their JSON forms.
func roundTrip(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	b, _ := json.Marshal(m)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

func encode(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return buf.Bytes()
}

func TestSessions(t *testing.T) {
	dirs, _ := filepath.Glob("sessions/*")
	if len(dirs) == 0 {
		t.Fatal("no fixture sessions")
	}
	update := os.Getenv("CREDENCE_UPDATE_FIXTURES") == "1"
	for _, dir := range dirs {
		harness := filepath.Base(dir)
		files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
		for _, f := range files {
			t.Run(harness+"/"+filepath.Base(f), func(t *testing.T) {
				got := replay(t, harness, f)
				checkSemantics(t, harness, got)
				want := strings.TrimSuffix(f, ".jsonl") + ".expected.json"
				b := encode(got)
				if update {
					if err := os.WriteFile(want, b, 0o644); err != nil {
						t.Fatal(err)
					}
					return
				}
				exp, err := os.ReadFile(want)
				if err != nil {
					t.Fatalf("%v (run with CREDENCE_UPDATE_FIXTURES=1 to create it)", err)
				}
				if !bytes.Equal(exp, b) {
					t.Errorf("%s differs from the adapter's output; diff it against:\n%s", want, firstDiff(exp, b))
				}
			})
		}
	}
}

func firstDiff(a, b []byte) string {
	al, bl := strings.Split(string(a), "\n"), strings.Split(string(b), "\n")
	for i := 0; i < len(al) || i < len(bl); i++ {
		var x, y string
		if i < len(al) {
			x = al[i]
		}
		if i < len(bl) {
			y = bl[i]
		}
		if x != y {
			return "line " + itoa(i+1) + ":\n  expected: " + x + "\n  got:      " + y
		}
	}
	return ""
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// find returns the events of a kind whose payload contains every pair.
func find(s Session, kind string, want map[string]string) []Event {
	var out []Event
	for _, e := range s.Events {
		if e.Kind != kind {
			continue
		}
		ok := true
		for k, v := range want {
			if got, _ := json.Marshal(e.Payload[k]); !strings.Contains(string(got), v) {
				ok = false
			}
		}
		if ok {
			out = append(out, e)
		}
	}
	return out
}

func count(s Session, kind string) int { return len(find(s, kind, nil)) }
