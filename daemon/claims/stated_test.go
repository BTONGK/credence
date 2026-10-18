package claims

import (
	"reflect"
	"testing"
)

func TestStatements(t *testing.T) {
	reply := "I built the parser, and it works: the sample files load end to end. Running it showed two problems.\n\n" +
		"**It's running now at http://localhost:5050.** The code is in `/work/parser`. Rebuild with `make`.\n\n" +
		"## What's in it\n\n" +
		"- **A tokenizer** that handles 40 input formats.\n" +
		"- I updated `src/lexer.go` and `src/parse.go`.\n\n" +
		"| Step | Status |\n|---|---|\n| I fixed everything | done |\n\n" +
		"All 64 tests pass, including 9 new ones; nothing is committed yet.\n\n" +
		"I couldn't reach the mirror, so I checked the cached copy instead.\n\n" +
		"If you want, I'll push it to GitHub. You can share the page from the Share menu. Open this link: https://example.com/a?b=c"

	got := Statements(reply)
	type row struct {
		Text  string
		Kinds []string
	}
	var rows []row
	for _, s := range got {
		rows = append(rows, row{s.Text, s.Kinds})
	}
	want := []row{
		{"It's running now at http://localhost:5050.", []string{StateDeploy}},
		{"I updated `src/lexer.go` and `src/parse.go`.", []string{StateChange}},
		{"All 64 tests pass, including 9 new ones", []string{StateTests}},
		{"I built the parser, and it works: the sample files load end to end.", []string{StateRuns, StateChange}},
	}
	// The strongest four, in reply order.
	want = []row{want[3], want[0], want[1], want[2]}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("statements:\n got %+v\nwant %+v", rows, want)
	}
	if got[2].Paths == nil || !reflect.DeepEqual(got[2].Paths, []string{"src/lexer.go", "src/parse.go"}) {
		t.Errorf("paths = %v", got[2].Paths)
	}
	if got[3].Count != 64 {
		t.Errorf("count = %d, want 64", got[3].Count)
	}
	if !reflect.DeepEqual(got[1].URLs, []string{"http://localhost:5050"}) {
		t.Errorf("urls = %v", got[1].URLs)
	}
}

func TestStatementFilters(t *testing.T) {
	for _, s := range []string{
		"I'll deploy it once you approve.",
		"None of this is live yet.",
		"You connected the right account.",
		"Here's the homepage copy rewritten in plain language.",
		"Should I push the branch?",
		"The math checks out.",
		"Nothing is committed yet.",
		"It supports import/export of CSV files.",
	} {
		if st := Statements(s); len(st) != 0 {
			t.Errorf("%q: unexpected statement %+v", s, st)
		}
	}
	if st := Statements("I added import/export to the CLI."); len(st) != 1 || st[0].Paths != nil {
		t.Errorf("and/or-style words are not paths: %+v", st)
	}
	if st := Statements("I couldn't reach the mirror, so I checked the cached copy instead."); len(st) != 1 || st[0].Kinds[0] != StateResearch {
		t.Errorf("clause split: %+v", st)
	}
}
