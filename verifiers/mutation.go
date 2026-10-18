package verifiers

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BTONGK/credence/internal/gitx"
	"github.com/BTONGK/credence/spec"
)

// Mutation is tier 1: it mutates the lines a commit added and re-runs a
// test against each mutant in a clean worktree. A test the agent wrote in
// the same session can only corroborate its own fix; one that also kills
// most mutants of the changed code is evidence it actually exercises it.
//
// It runs for explicit mutation_score assertions, and on its own for code
// changes whose only passing test was born in the session (when
// tests.mutation.enabled is set, the default).
type Mutation struct{}

func (*Mutation) Name() string { return "mutation" }
func (*Mutation) Tier() int    { return 1 }

func (m *Mutation) Verify(ctx context.Context, vc *Context, c *spec.Claim) []spec.Verdict {
	if hasFailure(vc.prior) {
		return nil // a weak or strong test cannot change a factual failure
	}
	var out []spec.Verdict
	explicit := false
	for _, a := range c.Assertions {
		if a.Check() == "mutation_score" {
			explicit = true
			out = append(out, m.check(ctx, vc, c, a, true))
		}
	}
	if explicit || !vc.Data.Policy.Tests.Mutation.On() || !codeChange[c.Type] {
		return out
	}
	// Implicit: only when every passing test was written in this session,
	// which is exactly when the status would otherwise be capped.
	var born []spec.Assertion
	for _, v := range vc.prior {
		if v.Verifier != "tests" || v.Result != spec.ResultPass {
			continue
		}
		if !strings.Contains(v.Outcome, "born_in_session") {
			return out
		}
		born = append(born, v.Assertion)
	}
	if len(born) > 0 {
		a := spec.Assertion{"check": "mutation_score"}
		for _, k := range []string{"test", "command", "commit"} {
			if s := born[0].Str(k); s != "" {
				a[k] = s
			}
		}
		out = append(out, m.check(ctx, vc, c, a, false))
	}
	return out
}

func (m *Mutation) check(ctx context.Context, vc *Context, c *spec.Claim, a spec.Assertion, explicit bool) spec.Verdict {
	start := time.Now()
	d := vc.Data
	repo := d.Session.Repo
	pol := d.Policy.Tests
	inconclusive := func(outcome, details string) spec.Verdict {
		return verdict(vc, c, "mutation", 1, "mutation_score", spec.ResultInconclusive, outcome, details, a, start)
	}
	if repo == "" {
		return inconclusive("no_repo", "session has no git repository")
	}
	commit := claimCommit(d, c, a)
	if commit == "" {
		return inconclusive("uncommitted", "no commit to mutate")
	}
	if a.Str("test") == "" && a.Str("command") == "" {
		return inconclusive("no_test", "mutation_score needs the test or command that should catch the mutants")
	}
	cmd, origin, workdir, err := resolveCommand(d, a, commit)
	if err != nil {
		return inconclusive("command_not_allowed", err.Error())
	}
	skip := map[string]bool{}
	for _, p := range testPaths(a, cmd) {
		skip[d.RelPath(p)] = true
	}
	limit := a.Int("max_mutants", pol.Mutation.MaxMutants)
	if limit <= 0 {
		limit = 8
	}
	ms, err := Mutants(repo, commit, skip, limit)
	if err != nil {
		return inconclusive("environment", err.Error())
	}
	if len(ms) == 0 {
		return inconclusive("no_mutants", fmt.Sprintf("no mutable operators in the source lines %s added", short(commit)))
	}
	min := pol.Mutation.MinScore
	if s := a.Str("min"); s != "" {
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			min = f
		}
	}
	if min <= 0 {
		min = 0.6
	}
	budget := time.Duration(pol.BudgetSeconds) * time.Second
	if budget <= 0 {
		budget = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	t := &Tests{}
	local := uncommittedInputs(d, a, cmd, commit)
	var killed, ran int
	var survived []string
	var arts []spec.Artifact
	for _, mu := range ms {
		if ctx.Err() != nil {
			break
		}
		mu := mu
		r := t.run(ctx, vc, c, runSpec{commit: commit, cmd: cmd, workdir: workdir, runs: 1, label: "mutant", local: local,
			mutant: mu.ID(), apply: mu.Apply})
		if r.err != nil {
			return inconclusive("environment", "could not run a mutant: "+r.err.Error())
		}
		if r.runs == 0 {
			// The run hit the time budget: a mutant that hangs the test
			// counts as caught only if there is time left for others.
			if ctx.Err() != nil {
				break
			}
			killed++
			ran++
			continue
		}
		ran++
		if r.id != "" {
			arts = append(arts, spec.Artifact{Kind: "test_run", Locator: "test_run:" + r.id, Repo: repo})
		}
		if r.passes == 0 {
			killed++
		} else {
			survived = append(survived, mu.String())
		}
	}
	if ran < min3(len(ms)) {
		return inconclusive("budget", fmt.Sprintf("ran out of time after %d of %d mutants", ran, len(ms)))
	}
	score := float64(killed) / float64(ran)
	desc := fmt.Sprintf("`%s` (%s) caught %d/%d mutants of the lines %s added (score %.2f, needs %.2f)", clipCmd(cmd), origin, killed, ran, short(commit), score, min)
	if len(survived) > 0 {
		desc += "; survived: " + strings.Join(firstN(survived, 4), "; ")
	}
	result, outcome := spec.ResultPass, "mutation_strong"
	if score < min {
		outcome = "mutation_weak"
		// A claim that its test is strong is contradicted by a weak score;
		// for a fix, a weak test is just not extra evidence.
		result = spec.ResultInconclusive
		if explicit {
			result = spec.ResultFail
		}
	}
	v := verdict(vc, c, "mutation", 1, "mutation_score", result, outcome, desc, a, start)
	v.Artifacts = arts
	v.AssignID()
	return v
}

func min3(n int) int {
	if n < 3 {
		return n
	}
	return 3
}

func firstN(xs []string, n int) []string {
	if len(xs) > n {
		return append(xs[:n:n], fmt.Sprintf("%d more", len(xs)-n))
	}
	return xs
}

// Mutant is one operator replacement on one added line.
type Mutant struct {
	Path string
	Line int // 1-based, in the commit's version of the file
	Col  int // byte offset of the operator in the line
	From string
	To   string
}

// ID is path:line:col:from→to; the UI parses it from test run targets.
func (m Mutant) ID() string {
	return fmt.Sprintf("%s:%d:%d:%s→%s", m.Path, m.Line, m.Col, m.From, m.To)
}

func (m Mutant) String() string {
	return fmt.Sprintf("%s:%d `%s` → `%s`", m.Path, m.Line, strings.TrimSpace(m.From), strings.TrimSpace(m.To))
}

// Apply rewrites the mutant's line in a worktree.
func (m Mutant) Apply(dir string) error {
	p := filepath.Join(dir, m.Path)
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	lines := strings.SplitAfter(string(b), "\n")
	if m.Line < 1 || m.Line > len(lines) {
		return fmt.Errorf("%s has no line %d", m.Path, m.Line)
	}
	l := lines[m.Line-1]
	if m.Col+len(m.From) > len(l) || l[m.Col:m.Col+len(m.From)] != m.From {
		return fmt.Errorf("%s:%d changed", m.Path, m.Line)
	}
	lines[m.Line-1] = l[:m.Col] + m.To + l[m.Col+len(m.From):]
	return os.WriteFile(p, []byte(strings.Join(lines, "")), 0o644)
}

// operators are tried in order; the first that matches outside a string
// literal or comment wins for each position. Each entry is a regexp for
// the operator in context and its replacement.
var operators = []struct {
	re *regexp.Regexp
	to string
}{
	{regexp.MustCompile(`===`), "!=="},
	{regexp.MustCompile(`!==`), "==="},
	{regexp.MustCompile(`==`), "!="},
	{regexp.MustCompile(`!=`), "=="},
	{regexp.MustCompile(`<=`), "<"},
	{regexp.MustCompile(`>=`), ">"},
	{regexp.MustCompile(` < `), " <= "},
	{regexp.MustCompile(` > `), " >= "},
	{regexp.MustCompile(`&&`), "||"},
	{regexp.MustCompile(`\|\|`), "&&"},
	{regexp.MustCompile(`\band\b`), "or"},
	{regexp.MustCompile(`\bor\b`), "and"},
	{regexp.MustCompile(`\bnot `), ""},
	{regexp.MustCompile(`\btrue\b`), "false"},
	{regexp.MustCompile(`\bfalse\b`), "true"},
	{regexp.MustCompile(`\bTrue\b`), "False"},
	{regexp.MustCompile(`\bFalse\b`), "True"},
	{regexp.MustCompile(`\+=`), "-="},
	{regexp.MustCompile(`-=`), "+="},
	{regexp.MustCompile(` \+ 1\b`), " - 1"},
	{regexp.MustCompile(` - 1\b`), " + 1"},
}

var sourceExt = map[string]bool{
	".py": true, ".go": true, ".js": true, ".mjs": true, ".cjs": true, ".ts": true, ".tsx": true, ".jsx": true,
	".rb": true, ".rs": true, ".java": true, ".kt": true, ".swift": true, ".c": true, ".h": true, ".cc": true,
	".cpp": true, ".hpp": true, ".cs": true, ".php": true, ".scala": true, ".sh": true,
}

var testFile = regexp.MustCompile(`(^|/)(tests?|__tests__|spec)/|_test\.go$|(^|/)test_[^/]*\.py$|_test\.py$|\.(test|spec)\.[cm]?[jt]sx?$|Test\.(java|kt)$`)

// Mutants lists up to limit mutants of the source lines commit added,
// spread across lines (one per line first), skipping test files and skip.
func Mutants(repo, commit string, skip map[string]bool, limit int) ([]Mutant, error) {
	diff, err := gitx.Run(repo, "show", "--format=", "--no-color", "--unified=0", "--no-renames", commit)
	if err != nil {
		return nil, err
	}
	type line struct {
		path string
		n    int
		text string
	}
	var added []line
	cur, n := "", 0
	hunk := regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)
	sc := bufio.NewScanner(strings.NewReader(diff))
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		l := sc.Text()
		switch {
		case strings.HasPrefix(l, "+++ "):
			cur = strings.TrimPrefix(strings.TrimPrefix(l, "+++ "), "b/")
			if cur == "/dev/null" || skip[cur] || testFile.MatchString(cur) || !sourceExt[filepath.Ext(cur)] {
				cur = ""
			}
		case strings.HasPrefix(l, "@@"):
			if m := hunk.FindStringSubmatch(l); m != nil {
				n, _ = strconv.Atoi(m[1])
			}
		case strings.HasPrefix(l, "+"):
			if cur != "" {
				added = append(added, line{cur, n, l[1:]})
			}
			n++
		}
	}
	perLine := make([][]Mutant, len(added))
	for i, l := range added {
		code := codeMask(l.text)
		taken := map[int]bool{}
		for _, op := range operators {
			for _, loc := range op.re.FindAllStringIndex(l.text, -1) {
				if !code[loc[0]] || taken[loc[0]] {
					continue
				}
				for j := loc[0]; j < loc[1]; j++ {
					taken[j] = true
				}
				perLine[i] = append(perLine[i], Mutant{Path: l.path, Line: l.n, Col: loc[0], From: l.text[loc[0]:loc[1]], To: op.to})
			}
		}
		sort.Slice(perLine[i], func(a, b int) bool { return perLine[i][a].Col < perLine[i][b].Col })
	}
	// Round-robin: the first mutant of every line, then the second, …
	var out []Mutant
	for round := 0; len(out) < limit; round++ {
		any := false
		for _, ms := range perLine {
			if round < len(ms) && len(out) < limit {
				out = append(out, ms[round])
				any = true
			}
		}
		if !any {
			break
		}
	}
	return out, nil
}

// codeMask marks the bytes of a line that are code: not inside a string
// literal and not in a trailing comment. It is a heuristic shared by the
// C family, Python, Ruby and shell.
func codeMask(s string) []bool {
	mask := make([]bool, len(s)+1)
	var quote byte
	t := strings.TrimLeft(s, " \t")
	if strings.HasPrefix(t, "//") || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "*") || strings.HasPrefix(t, "/*") || strings.HasPrefix(t, `"""`) {
		return mask
	}
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if quote != 0 {
			if ch == '\\' {
				i++
				continue
			}
			if ch == quote {
				quote = 0
			}
			continue
		}
		switch {
		case ch == '"' || ch == '\'' || ch == '`':
			quote = ch
			continue
		case ch == '#' || (ch == '/' && i+1 < len(s) && s[i+1] == '/'):
			return mask
		}
		mask[i] = true
	}
	return mask
}
