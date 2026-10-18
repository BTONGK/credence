package verifiers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/BTONGK/credence/adapters"
	"github.com/BTONGK/credence/daemon/claims"
	"github.com/BTONGK/credence/daemon/store"
	"github.com/BTONGK/credence/internal/gitx"
	"github.com/BTONGK/credence/internal/home"
	"github.com/BTONGK/credence/policy"
	"github.com/BTONGK/credence/spec"
)

// Tests is tier 1: re-runs tests or commands in a clean worktree, before and
// after the claimed commit. Commands come only from the repo's policy, from
// commands the agent itself ran that are recognizable test runs, or from
// fixed templates Credence builds for a test path.
type Tests struct{}

func (*Tests) Name() string { return "tests" }
func (*Tests) Tier() int    { return 1 }

var testChecks = map[string]bool{
	"test_fails_before_passes_after":    true,
	"test_passes":                       true,
	"command_fails_before_passes_after": true,
	"command_passes":                    true,
}

// IsTestCheck reports whether a check is run by the test verifier.
func IsTestCheck(check string) bool { return testChecks[check] }

func (t *Tests) Verify(ctx context.Context, vc *Context, c *spec.Claim) []spec.Verdict {
	var out []spec.Verdict
	for _, a := range c.Assertions {
		if testChecks[a.Check()] {
			out = append(out, t.check(ctx, vc, c, a))
		}
	}
	return out
}

func (t *Tests) check(ctx context.Context, vc *Context, c *spec.Claim, a spec.Assertion) spec.Verdict {
	start := time.Now()
	d := vc.Data
	repo := d.Session.Repo
	check := a.Check()
	pol := d.Policy.Tests
	inconclusive := func(outcome, details string) spec.Verdict {
		return verdict(vc, c, "tests", 1, check, spec.ResultInconclusive, outcome, details, a, start)
	}
	if repo == "" {
		return inconclusive("no_repo", "session has no git repository")
	}
	commit := claimCommit(d, c, a)
	if commit == "" {
		return inconclusive("uncommitted", "no commit to test; commit the work and run `credence verify --rerun`")
	}
	cmd, origin, workdir, err := resolveCommand(d, a, commit)
	if err != nil {
		return inconclusive("command_not_allowed", err.Error())
	}
	runs := a.Int("runs", pol.DefaultRuns)
	if runs <= 0 {
		runs = 3
	}
	if pol.MaxRuns > 0 && runs > pol.MaxRuns {
		runs = pol.MaxRuns
	}
	budget := time.Duration(pol.BudgetSeconds) * time.Second
	if budget <= 0 {
		budget = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	transition := strings.HasSuffix(check, "fails_before_passes_after")
	local := uncommittedInputs(d, a, cmd, commit)
	after := t.run(ctx, vc, c, runSpec{commit: commit, cmd: cmd, workdir: workdir, runs: runs, label: "after", local: local})
	var arts []spec.Artifact
	if after.id != "" {
		arts = append(arts, spec.Artifact{Kind: "test_run", Locator: "test_run:" + after.id, Repo: repo})
	}
	finish := func(v spec.Verdict) spec.Verdict {
		v.Artifacts = arts
		v.AssignID()
		return v
	}
	if after.err != nil {
		return finish(inconclusive("environment", "could not run at "+short(commit)+": "+after.err.Error()))
	}
	desc := fmt.Sprintf("`%s` (%s)", clipCmd(cmd), origin)
	if len(local) > 0 {
		desc += fmt.Sprintf(" with uncommitted %s from your checkout", strings.Join(local, ", "))
	}
	if after.passes < after.runs {
		outcome, what := "fails_after", "fails"
		if after.passes > 0 {
			outcome, what = "flaky_after", "is flaky"
		}
		if after.timedOut {
			return finish(inconclusive("budget", fmt.Sprintf("%s ran out of time at %s after %d/%d runs", desc, short(commit), after.runs, runs)))
		}
		return finish(verdict(vc, c, "tests", 1, check, spec.ResultFail, outcome,
			fmt.Sprintf("%s %s at %s: passed %d/%d in a clean worktree\n%s", desc, what, short(commit), after.passes, after.runs, tail(after.output, 1200)), a, start))
	}
	born := bornInSession(d, testPaths(a, cmd))
	bornNote := ""
	if len(born) > 0 {
		bornNote = fmt.Sprintf("; %s written in this session, so this is corroboration, not independent proof", strings.Join(born, ", "))
	}
	if !transition {
		outcome := "passes"
		if len(born) > 0 {
			outcome = "passes_born_in_session"
		}
		return finish(verdict(vc, c, "tests", 1, check, spec.ResultPass, outcome,
			fmt.Sprintf("%s passed %d/%d at %s in a clean worktree%s", desc, after.passes, after.runs, short(commit), bornNote), a, start))
	}
	parent := gitx.Parent(repo, commit)
	if parent == "" {
		return finish(inconclusive("root_commit", "commit "+short(commit)+" has no parent to compare against"))
	}
	overlay := overlayPaths(d, a, cmd, commit, parent)
	before := t.run(ctx, vc, c, runSpec{commit: parent, overlayFrom: commit, overlay: overlay, cmd: cmd, workdir: workdir, runs: runs, label: "before", local: local})
	if before.id != "" {
		arts = append(arts, spec.Artifact{Kind: "test_run", Locator: "test_run:" + before.id, Repo: repo})
	}
	if before.err != nil {
		return finish(inconclusive("environment", "could not run before "+short(commit)+": "+before.err.Error()))
	}
	beforeDesc := "parent " + short(parent)
	if len(overlay) > 0 {
		beforeDesc += " with " + strings.Join(overlay, ", ") + " from the commit"
	}
	fails := before.runs - before.passes
	if fails == 0 {
		if before.timedOut {
			return finish(inconclusive("budget", "ran out of time before "+short(commit)))
		}
		return finish(inconclusive("no_failure_before", fmt.Sprintf("%s also passed %d/%d at %s, so it does not demonstrate the change (if the bug is intermittent, raise runs)",
			desc, before.runs, before.runs, beforeDesc)))
	}
	outcome := "transition"
	if len(born) > 0 {
		outcome = "transition_born_in_session"
	}
	return finish(verdict(vc, c, "tests", 1, check, spec.ResultPass, outcome,
		fmt.Sprintf("%s failed %d/%d at %s and passed %d/%d at %s%s", desc, fails, before.runs, beforeDesc, after.passes, after.runs, short(commit), bornNote), a, start))
}

func clipCmd(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 120 {
		return s[:117] + "…"
	}
	return s
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

// claimCommit picks the commit an assertion is about.
func claimCommit(d *claims.Data, c *spec.Claim, a spec.Assertion) string {
	if sha := a.Str("commit"); sha != "" {
		if cm := d.CommitBySHA(strings.ToLower(sha)); cm != nil {
			return cm.SHA
		}
		if full, err := gitx.ResolveCommit(d.Session.Repo, sha); err == nil {
			return full
		}
		return ""
	}
	var found string
	for _, ev := range c.Evidence {
		t := spec.ParseTarget(ev.Target)
		sha := t.Commit
		if sha == "" || sha == "HEAD" {
			continue
		}
		if cm := d.CommitBySHA(sha); cm != nil {
			found = cm.SHA // the latest cited session commit wins
		}
	}
	return found
}

var (
	cdPrefix  = regexp.MustCompile(`^cd\s+("[^"]+"|'[^']+'|\S+)\s*&&\s*(.+)$`)
	envPrefix = regexp.MustCompile(`^((?:[A-Z_][A-Z0-9_]*=[\w./:\-]*\s+)*)(.+)$`)
	shellMeta = regexp.MustCompile("[;&|><`$()\\\\\n]")
	scriptCmd = regexp.MustCompile(`^(?:python3?|node|bash|sh|ruby|deno run|bun|go run)\s+([\w./\-]+\.(?:py|js|mjs|ts|sh|rb|go))(?:\s+[\w./=:,\-]+)*$`)
)

// resolveCommand decides what may be run for an assertion.
func resolveCommand(d *claims.Data, a spec.Assertion, commit string) (cmd, origin, workdir string, err error) {
	pol := d.Policy.Tests
	want := strings.TrimSpace(a.Str("command"))
	test := strings.TrimSpace(a.Str("test"))
	if want != "" {
		if allowed(pol.AllowCommands, want) {
			return want, "policy", "", nil
		}
		ev := agentRan(d, want)
		if ev == nil {
			return "", "", "", fmt.Errorf("`%s` was not run by the agent in this session and is not in tests.allow_commands", clipCmd(want))
		}
		wd := relWorkdir(d, ev)
		if pol.TrustAgentCmds {
			return want, "agent", wd, nil
		}
		rest, dir := want, ""
		if m := cdPrefix.FindStringSubmatch(want); m != nil {
			dir, rest = strings.Trim(m[1], `"'`), m[2]
		}
		if m := envPrefix.FindStringSubmatch(rest); m != nil && shellMeta.MatchString(m[1]) {
			return "", "", "", fmt.Errorf("`%s` has an unsupported environment prefix", clipCmd(want))
		}
		if shellMeta.MatchString(rest) {
			return "", "", "", fmt.Errorf("`%s` is a compound shell command; add it to tests.allow_commands or set tests.trust_agent_commands", clipCmd(want))
		}
		if adapters.DetectTest(rest) == nil && !scriptCmd.MatchString(envPrefix.ReplaceAllString(rest, "$2")) {
			return "", "", "", fmt.Errorf("`%s` is not a recognized test or script command; add it to tests.allow_commands", clipCmd(want))
		}
		if dir != "" {
			wd = joinWorkdir(d, wd, dir)
		}
		return rest, "agent ran it", wd, nil
	}
	if test == "" {
		return "", "", "", errors.New("assertion names no test or command")
	}
	if pol.Command != "" {
		return strings.ReplaceAll(pol.Command, "{test}", shellQuote(test)), "policy", "", nil
	}
	if ev := agentTestRun(d, test); ev != nil {
		cmd := claims.PayloadStr(ev, "command")
		return resolveCommand(d, spec.Assertion{"command": cmd}, commit)
	}
	if inf := inferCommand(d.Session.Repo, commit, test); inf != "" {
		return inf, "inferred", "", nil
	}
	return "", "", "", fmt.Errorf("no way to run %s: set tests.command in credence.yaml", test)
}

func allowed(patterns []string, cmd string) bool {
	norm := strings.Join(strings.Fields(cmd), " ")
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if p == norm {
			return true
		}
		if strings.HasSuffix(p, "*") && strings.HasPrefix(norm, strings.TrimSuffix(p, "*")) && !shellMeta.MatchString(norm[len(p)-1:]) {
			return true
		}
	}
	return false
}

func agentRan(d *claims.Data, cmd string) *spec.Event {
	norm := strings.Join(strings.Fields(cmd), " ")
	check := func(evs []spec.Event) *spec.Event {
		for i := range evs {
			e := &evs[i]
			if e.Kind == spec.KindCommand && strings.Join(strings.Fields(claims.PayloadStr(e, "command")), " ") == norm {
				return e
			}
		}
		return nil
	}
	if e := check(d.Events); e != nil {
		return e
	}
	for _, evs := range d.ChildEvents {
		if e := check(evs); e != nil {
			return e
		}
	}
	return nil
}

func agentTestRun(d *claims.Data, test string) *spec.Event {
	path := test
	if i := strings.Index(path, "::"); i > 0 {
		path = path[:i]
	}
	var best *spec.Event
	for i := range d.Events {
		e := &d.Events[i]
		if e.Kind != spec.KindCommand {
			continue
		}
		ti, _ := claims.Payload(e)["test"].(map[string]any)
		if ti == nil {
			continue
		}
		cmd := claims.PayloadStr(e, "command")
		if strings.Contains(cmd, test) || strings.Contains(cmd, path) {
			best = e
		}
	}
	return best
}

func relWorkdir(d *claims.Data, e *spec.Event) string {
	wd := claims.PayloadStr(e, "workdir")
	if wd == "" {
		wd = d.Session.Cwd
	}
	return joinWorkdir(d, "", wd)
}

func joinWorkdir(d *claims.Data, base, dir string) string {
	repo := d.Session.Repo
	var abs string
	if filepath.IsAbs(dir) {
		abs = dir
	} else {
		abs = filepath.Join(repo, base, dir)
	}
	rel, err := filepath.Rel(repo, abs)
	if err != nil || strings.HasPrefix(rel, "..") || rel == "." {
		return ""
	}
	return rel
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// inferCommand builds a fixed command for a test id when nothing else is known.
func inferCommand(repo, commit, test string) string {
	path, rest, _ := strings.Cut(test, "::")
	exists := func(p string) bool { return gitx.FileExistsAt(repo, commit, p) }
	has := func(p, needle string) bool {
		b, err := gitx.FileAt(repo, commit, p)
		return err == nil && strings.Contains(b, needle)
	}
	switch {
	case strings.HasSuffix(path, ".py"):
		if exists("pytest.ini") || exists("conftest.py") || has("pyproject.toml", "[tool.pytest") || has("setup.cfg", "[tool:pytest]") || has("tox.ini", "[pytest]") {
			return "python3 -m pytest -q " + shellQuote(test)
		}
		mod := strings.ReplaceAll(strings.TrimSuffix(path, ".py"), "/", ".")
		if rest != "" {
			mod += "." + strings.ReplaceAll(rest, "::", ".")
		}
		return "python3 -m unittest -q " + shellQuote(mod)
	case regexp.MustCompile(`^[A-Za-z_][\w]*(\.[A-Za-z_][\w]*)+$`).MatchString(test) && !strings.Contains(test, "/"):
		return "python3 -m unittest -q " + shellQuote(test)
	case strings.HasSuffix(path, "_test.go"):
		dir := filepath.Dir(path)
		cmd := "go test ./" + strings.TrimPrefix(dir, "./")
		if rest != "" {
			cmd += " -run " + shellQuote("^"+rest+"$")
		}
		return cmd + " -count=1"
	case regexp.MustCompile(`\.(test|spec)\.[cm]?[jt]sx?$`).MatchString(path):
		if has("package.json", `"test"`) {
			return "npm test -- " + shellQuote(path)
		}
	case strings.HasPrefix(path, "tests/") && strings.HasSuffix(path, ".rs"):
		return "cargo test --test " + shellQuote(strings.TrimSuffix(filepath.Base(path), ".rs"))
	}
	return ""
}

// testPaths lists the files whose provenance caps the result.
func testPaths(a spec.Assertion, cmd string) []string {
	var ps []string
	if t := a.Str("test"); t != "" {
		ps = append(ps, testIDPaths(t)...)
	}
	ps = append(ps, a.Strs("overlay")...)
	if ti := adapters.DetectTest(cmd); ti != nil {
		for _, t := range ti.Targets {
			ps = append(ps, testIDPaths(t)...)
		}
	}
	if m := scriptCmd.FindStringSubmatch(cmd); m != nil {
		ps = append(ps, m[1])
	}
	return ps
}

var dottedID = regexp.MustCompile(`^[\w.]+$`)

// testIDPaths maps a test id to the files it may live in: a path
// (tests/test_x.py::test_y) is itself; a dotted unittest name
// (tests.test_x.T.test_y) may be any of its module prefixes.
func testIDPaths(id string) []string {
	p, _, _ := strings.Cut(id, "::")
	if strings.Contains(p, "/") || !dottedID.MatchString(p) {
		return []string{p}
	}
	if strings.HasSuffix(p, ".py") && !strings.Contains(strings.TrimSuffix(p, ".py"), ".") {
		return []string{p}
	}
	var ps []string
	parts := strings.Split(p, ".")
	for i := len(parts); i > 0; i-- {
		ps = append(ps, strings.Join(parts[:i], "/")+".py")
	}
	return append(ps, p)
}

// bornInSession returns the paths among ps that the session created or changed.
func bornInSession(d *claims.Data, ps []string) []string {
	changed := map[string]bool{}
	for _, c := range d.Commits {
		for _, f := range c.Files {
			changed[f] = true
		}
	}
	for i := range d.Events {
		e := &d.Events[i]
		if e.Kind == spec.KindFileEdit && d.EditOK(e) {
			changed[d.RelPath(claims.PayloadStr(e, "path"))] = true
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, p := range ps {
		p = d.RelPath(p)
		if changed[p] && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// overlayPaths are test files copied from the commit onto its parent so the
// "before" run executes the same test against the old code.
func overlayPaths(d *claims.Data, a spec.Assertion, cmd, commit, parent string) []string {
	repo := d.Session.Repo
	cm, err := gitx.ShowCommit(repo, commit)
	if err != nil {
		return nil
	}
	// Only the files the check itself runs are carried back; other test
	// files from the commit would add noise (or failures) to the old tree.
	want := map[string]bool{}
	for _, p := range testPaths(a, cmd) {
		want[d.RelPath(p)] = true
	}
	var out []string
	for p := range want {
		if p == "" || !gitx.FileExistsAt(repo, commit, p) {
			continue
		}
		// Only overlay files the commit changed; others are already identical.
		changed := false
		for _, f := range cm.Files {
			if f == p {
				changed = true
			}
		}
		if changed || !gitx.FileExistsAt(repo, parent, p) {
			out = append(out, p)
		}
	}
	sortStrings(out)
	return out
}

func sortStrings(xs []string) {
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0 && xs[j] < xs[j-1]; j-- {
			xs[j], xs[j-1] = xs[j-1], xs[j]
		}
	}
}

type runSpec struct {
	commit      string
	local       []string // uncommitted files copied from the user's checkout
	overlayFrom string
	overlay     []string
	cmd         string
	workdir     string
	runs        int
	label       string
	mutant      string                 // mutant id, part of the cache key
	apply       func(dir string) error // edits the worktree before running
}

type runResult struct {
	id       string
	runs     int
	passes   int
	output   string
	timedOut bool
	err      error
}

func (t *Tests) run(ctx context.Context, vc *Context, c *spec.Claim, rs runSpec) runResult {
	d := vc.Data
	repo := d.Session.Repo
	target := rs.label
	if len(rs.overlay) > 0 {
		target += "+overlay:" + strings.Join(rs.overlay, ",") + "@" + short(rs.overlayFrom)
	}
	if rs.workdir != "" {
		target += " in " + rs.workdir
	}
	if len(rs.local) > 0 {
		target += "+local:" + strings.Join(rs.local, ",")
		for _, p := range rs.local {
			target += "#" + spec.ShortHex(spec.HashBytes(readFile(filepath.Join(repo, p))), 8)
		}
	}
	if rs.mutant != "" {
		target += "+mutant:" + rs.mutant
	}
	if !vc.Rerun && vc.Store != nil {
		if tr, err := vc.Store.FindTestRun(repo, rs.commit, target, rs.cmd); err == nil && tr.Runs >= rs.runs {
			return runResult{id: tr.ID, runs: tr.Runs, passes: tr.Passes, output: tr.Output}
		}
	}
	wt, err := gitx.AddWorktree(ctx, repo, rs.commit, home.Worktrees())
	if err != nil {
		return runResult{err: fmt.Errorf("worktree: %w", err)}
	}
	defer wt.Remove()
	for _, p := range rs.overlay {
		content, err := gitx.FileAt(repo, rs.overlayFrom, p)
		if err != nil {
			continue
		}
		dst := filepath.Join(wt.Dir, p)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return runResult{err: err}
		}
		if err := os.WriteFile(dst, []byte(content), 0o644); err != nil {
			return runResult{err: err}
		}
	}
	for _, p := range rs.local {
		dst := filepath.Join(wt.Dir, p)
		_ = os.MkdirAll(filepath.Dir(dst), 0o755)
		if err := os.WriteFile(dst, readFile(filepath.Join(repo, p)), 0o644); err != nil {
			return runResult{err: err}
		}
	}
	if rs.apply != nil {
		if err := rs.apply(wt.Dir); err != nil {
			return runResult{err: err}
		}
	}
	linkDirs(repo, wt.Dir, rs.workdir, d.Policy.Tests)
	dir := filepath.Join(wt.Dir, rs.workdir)
	if setup := d.Policy.Tests.Setup; setup != "" {
		if code, out, _ := runShell(ctx, dir, setup); code != 0 {
			return runResult{err: fmt.Errorf("setup `%s` failed: %s", clipCmd(setup), tail(out, 400))}
		}
	}
	res := runResult{}
	var lastFail, lastPass string
	for i := 0; i < rs.runs; i++ {
		if ctx.Err() != nil {
			res.timedOut = true
			break
		}
		code, out, timedOut := runShell(ctx, dir, rs.cmd)
		if timedOut {
			res.timedOut = true
			break
		}
		res.runs++
		if code == 0 {
			res.passes++
			lastPass = out
		} else {
			lastFail = fmt.Sprintf("[run %d exit %d]\n%s", i+1, code, out)
		}
	}
	res.output = lastFail
	if res.output == "" {
		res.output = lastPass
	}
	res.output = tail(res.output, 4000)
	if res.runs == 0 {
		return res
	}
	if vc.Store != nil {
		tr := &store.TestRun{ClaimID: c.ID, Repo: repo, Commit: rs.commit, Target: target, Command: rs.cmd, Runs: res.runs, Passes: res.passes, Output: res.output}
		if err := vc.Store.InsertTestRun(tr); err == nil {
			res.id = tr.ID
		}
	}
	return res
}

func linkDirs(repo, wt, workdir string, pol policy.Tests) {
	bases := []string{""}
	if workdir != "" {
		bases = append(bases, workdir)
	}
	for _, b := range bases {
		for _, ld := range pol.LinkDirs {
			if ld == "" || strings.Contains(ld, "..") {
				continue
			}
			src := filepath.Join(repo, b, ld)
			dst := filepath.Join(wt, b, ld)
			if fi, err := os.Stat(src); err != nil || !fi.IsDir() {
				continue
			}
			if _, err := os.Lstat(dst); err == nil {
				continue
			}
			_ = os.MkdirAll(filepath.Dir(dst), 0o755)
			_ = os.Symlink(src, dst)
		}
	}
}

var secretEnv = regexp.MustCompile(`(?i)(API_KEY|_TOKEN|SECRET|PASSWORD|PASSWD|CREDENTIAL|^AWS_|^GH_|^GITHUB_|^ANTHROPIC_|^OPENAI_|SSH_AUTH_SOCK)`)

// TestEnv is the environment for re-runs: the user's, minus credentials.
func TestEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if secretEnv.MatchString(k) || strings.HasPrefix(k, "CLAUDE") || strings.HasPrefix(k, "CODEX") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "CREDENCE_INTERNAL=1", "GIT_TERMINAL_PROMPT=0", "PYTHONDONTWRITEBYTECODE=1")
}

// runShell runs cmd under sh in its own process group so a timeout kills
// everything it started.
func runShell(ctx context.Context, dir, cmd string) (int, string, bool) {
	c := exec.Command("/bin/sh", "-c", cmd)
	c.Dir = dir
	c.Env = TestEnv()
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var buf limitedBuffer
	buf.max = 256 << 10
	c.Stdout, c.Stderr = &buf, &buf
	c.Stdin = nil
	if err := c.Start(); err != nil {
		return -1, err.Error(), false
	}
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	select {
	case err := <-done:
		code := 0
		if err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				code = ee.ExitCode()
			} else {
				code = -1
			}
		}
		return code, buf.String(), false
	case <-ctx.Done():
		_ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
		<-done
		return -1, buf.String(), true
	}
}

type limitedBuffer struct {
	buf bytes.Buffer
	max int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	l.buf.Write(p)
	if l.buf.Len() > l.max {
		b := l.buf.Bytes()
		keep := append([]byte(nil), b[len(b)-l.max/2:]...)
		l.buf.Reset()
		l.buf.Write(keep)
	}
	return n, nil
}

func (l *limitedBuffer) String() string { return l.buf.String() }

func readFile(p string) []byte {
	b, _ := os.ReadFile(p)
	return b
}

// uncommittedInputs are files a command needs that exist only in the
// user's checkout (a repro script the agent never committed, for example).
func uncommittedInputs(d *claims.Data, a spec.Assertion, cmd, commit string) []string {
	repo := d.Session.Repo
	var out []string
	seen := map[string]bool{}
	for _, p := range testPaths(a, cmd) {
		p = d.RelPath(p)
		if p == "" || seen[p] || strings.HasPrefix(p, "..") || filepath.IsAbs(p) {
			continue
		}
		seen[p] = true
		if gitx.FileExistsAt(repo, commit, p) {
			continue
		}
		if fi, err := os.Stat(filepath.Join(repo, p)); err == nil && fi.Mode().IsRegular() && fi.Size() < 1<<20 {
			out = append(out, p)
		}
	}
	sortStrings(out)
	return out
}
