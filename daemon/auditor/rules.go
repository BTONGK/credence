package auditor

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/BTONGK/credence/adapters"
	"github.com/BTONGK/credence/daemon/claims"
	"github.com/BTONGK/credence/internal/gitx"
	"github.com/BTONGK/credence/spec"
)

// RulesID identifies the deterministic auditor.
const RulesID = "auditor:rules@0.9"

type testRun struct {
	seq, resultSeq int
	cmd            string
	key            string
	targets        []string
	status         string
}

type commitInfo struct {
	sha, subject string
	files        []string
	added, del   int
	seq          int
}

type edit struct {
	seq  int
	path string
	op   string
}

// facts are deterministic observations both auditors start from.
type facts struct {
	tests   []testRun
	edits   []edit
	commits []commitInfo
	created map[string]bool // files created in this session
}

func normCmd(c string) string { return strings.Join(strings.Fields(c), " ") }

func gather(d *claims.Data) facts {
	f := facts{created: map[string]bool{}}
	for i := range d.Events {
		e := &d.Events[i]
		switch e.Kind {
		case spec.KindCommand:
			p := claims.Payload(e)
			ti, ok := p["test"].(map[string]any)
			if !ok {
				continue
			}
			tr := testRun{seq: e.Seq, cmd: claims.PayloadStr(e, "command")}
			tr.key = normCmd(tr.cmd)
			for _, t := range adaptersStrs(ti["targets"]) {
				tr.targets = append(tr.targets, t)
			}
			if r := d.Result(e); r != nil {
				tr.resultSeq = r.Seq
				rp := claims.Payload(r)
				if to, ok := rp["test_outcome"].(map[string]any); ok {
					tr.status, _ = to["status"].(string)
				} else if okv, _ := rp["ok"].(bool); okv {
					tr.status = "pass"
				} else {
					tr.status = "fail"
				}
			}
			f.tests = append(f.tests, tr)
		case spec.KindFileEdit:
			if !d.EditOK(e) {
				continue
			}
			ed := edit{seq: e.Seq, path: d.RelPath(claims.PayloadStr(e, "path")), op: claims.PayloadStr(e, "op")}
			if ed.op == "create" && d.Session.StartCommit != "" && !gitx.FileExistsAt(d.Session.Repo, d.Session.StartCommit, ed.path) {
				f.created[ed.path] = true
			} else if ed.op == "create" && d.Session.StartCommit == "" {
				f.created[ed.path] = true
			}
			f.edits = append(f.edits, ed)
		}
	}
	for _, c := range d.Commits {
		seq := d.CommitSeq(c.SHA)
		if seq == 0 {
			// Commit joined from git history: place it after the last event before it.
			for _, e := range d.Events {
				if !e.TS.After(c.CommitTime) {
					seq = e.Seq
				}
			}
		}
		f.commits = append(f.commits, commitInfo{sha: c.SHA, subject: c.Subject, files: c.Files, added: c.Added, del: c.Deleted, seq: seq})
	}
	sort.Slice(f.commits, func(i, j int) bool { return f.commits[i].seq < f.commits[j].seq })
	return f
}

func adaptersStrs(v any) []string {
	l, _ := v.([]any)
	var out []string
	for _, x := range l {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

var (
	fixRe       = regexp.MustCompile(`(?i)\b(fix|fixes|fixed|bug|race|crash|regress\w*|issue|error|broken|flaky|hotfix|patch)\b`)
	refactorRe  = regexp.MustCompile(`(?i)\b(refactor\w*|rename\w*|cleanup|clean up|simplif\w*|extract\w*|reorgani\w*|restructur\w*|tidy)\b`)
	opsRe       = regexp.MustCompile(`(?i)\b(ci|build|deploy\w*|infra|docker\w*|workflow|release|bump|dependenc\w*|upgrade)\b`)
	reproRe     = regexp.MustCompile(`(?i)(repro|reproduce|minimal|debug_|bisect)[\w\-]*\.(py|js|ts|sh|go|rb|mjs)`)
	rootCauseRe = regexp.MustCompile(`(?i)(root cause (?:is|was)|the (?:bug|culprit) (?:is|was)|is caused by|was caused by|caused by a|race condition (?:in|between|where))`)
	negativeRe  = regexp.MustCompile(`(?i)(didn't work|doesn't work|did not (?:fix|help|work)|does not fix|still fail\w*|no luck|ruled out|not the cause|isn't the cause|wasn't the cause|that approach (?:failed|didn't)|revert(?:ing|ed)? (?:my|the|that) (?:change|attempt|approach)|dead end)`)
	reviewRe    = regexp.MustCompile(`(?i)\b(review|pr\b|pull request|diff|audit)\b`)
	youRe       = regexp.MustCompile(`(?i)\byou(?:'ve|'re|r)?\b`)
)

// substantive reports whether a sentence says enough to be a claim:
// "Underneath it are three root causes: 1." does not.
func substantive(s string) bool {
	s = strings.TrimSpace(s)
	if len(strings.Fields(s)) < 6 {
		return false
	}
	last := s[len(s)-1]
	return !(last == ':' || (last == '.' && len(s) > 1 && s[len(s)-2] >= '0' && s[len(s)-2] <= '9'))
}

// scratch reports paths that are not project work: temp files, harness
// state and memory notes.
func scratch(p string) bool {
	for _, s := range []string{"/tmp/", "/var/folders/", "/.claude/", "/scratchpad/", "/.playwright-mcp/", "/.cache/"} {
		if strings.Contains("/"+p, s) {
			return true
		}
	}
	return strings.HasPrefix(p, "/private/")
}

func classify(subject string, files []string) string {
	allTests, allDocs := len(files) > 0, len(files) > 0
	for _, f := range files {
		if !adapters.IsTestPath(f) {
			allTests = false
		}
		if !adapters.IsDocPath(f) {
			allDocs = false
		}
	}
	switch {
	case allTests:
		return spec.TypeTest
	case allDocs:
		return spec.TypeDocs
	case fixRe.MatchString(subject):
		return spec.TypeFix
	case refactorRe.MatchString(subject):
		return spec.TypeRefactor
	case opsRe.MatchString(subject):
		return spec.TypeOps
	}
	return spec.TypeFeature
}

func firstSentence(s string, re *regexp.Regexp) string {
	s = strings.Join(strings.Fields(s), " ")
	loc := re.FindStringIndex(s)
	if loc == nil {
		return clip(s, 200)
	}
	start := strings.LastIndexAny(s[:loc[0]], ".!?\n")
	if start < 0 {
		start = 0
	} else {
		start++
	}
	end := strings.IndexAny(s[loc[1]:], ".!?\n")
	if end < 0 {
		end = len(s)
	} else {
		end = loc[1] + end + 1
	}
	return clip(strings.TrimSpace(s[start:end]), 240)
}

func capitalize(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// Rules is the deterministic, model-free auditor.
type Rules struct{}

// Audit proposes claims from captured events and git history.
func (Rules) Audit(d *claims.Data) []*spec.Claim {
	f := gather(d)
	sid := d.Session.ID
	var out []*spec.Claim
	mk := func(typ, title, statement string, ev []spec.Evidence, as []spec.Assertion) *spec.Claim {
		if len(title) > 100 {
			title = title[:97] + "…"
		}
		c := &spec.Claim{SessionID: sid, Claimant: RulesID, Origin: spec.OriginAuditor, Type: typ, Title: capitalize(title),
			Statement: statement, Evidence: ev, Assertions: as, CreatedAt: d.Session.StartedAt}
		if c.Assertions == nil {
			c.Assertions = []spec.Assertion{}
		}
		c.AssignID()
		out = append(out, c)
		return c
	}
	runs := d.Policy.Tests.DefaultRuns
	if runs <= 0 {
		runs = 3
	}
	committed := map[string]bool{}
	prevSeq := 0
	for _, c := range f.commits {
		for _, p := range c.files {
			committed[p] = true
		}
		inCommit := map[string]bool{}
		for _, p := range c.files {
			inCommit[p] = true
		}
		var editSeqs []int
		lastEdit := 0
		for _, e := range f.edits {
			if e.seq > prevSeq && e.seq <= c.seq && inCommit[e.path] {
				editSeqs = append(editSeqs, e.seq)
				if e.seq > lastEdit {
					lastEdit = e.seq
				}
			}
		}
		first := c.seq
		if len(editSeqs) > 0 {
			first = editSeqs[0]
		}
		ev := []spec.Evidence{{Target: spec.CommitTarget(c.sha), Relation: spec.RelProduced}}
		if c.seq > 0 {
			ev = append(ev, spec.Evidence{Target: spec.EventTarget(sid, first, c.seq), Relation: spec.RelProduced})
		}
		as := []spec.Assertion{{"check": "commit_touches", "commit": c.sha, "paths": capList(c.files, 10)}}
		typ := classify(c.subject, c.files)
		statement := fmt.Sprintf("Commit %s changes %s (+%d −%d)", c.sha[:7], fileList(c.files), c.added, c.del)
		if lastEdit == 0 {
			lastEdit = c.seq
		}
		if fail, pass := transition(f.tests, lastEdit); fail != nil && pass != nil {
			a := spec.Assertion{"check": "test_fails_before_passes_after", "command": pass.cmd, "commit": c.sha, "runs": runs}
			if len(pass.targets) > 0 {
				a["test"] = pass.targets[0]
			}
			as = append(as, a)
			statement = fmt.Sprintf("`%s` failed before commit %s (event #%d) and passes after it (event #%d)", clip(pass.cmd, 120), c.sha[:7], fail.seq, pass.seq)
			ev = append(ev, spec.Evidence{Target: spec.EventTarget(sid, fail.seq, max(fail.resultSeq, fail.seq)), Relation: spec.RelDemonstrates},
				spec.Evidence{Target: spec.EventTarget(sid, pass.seq, max(pass.resultSeq, pass.seq)), Relation: spec.RelDemonstrates})
		} else if pass := passAfter(f.tests, lastEdit); pass != nil {
			a := spec.Assertion{"check": "test_passes", "command": pass.cmd, "commit": c.sha, "runs": runs}
			if len(pass.targets) > 0 {
				a["test"] = pass.targets[0]
			}
			as = append(as, a)
			ev = append(ev, spec.Evidence{Target: spec.EventTarget(sid, pass.seq, max(pass.resultSeq, pass.seq)), Relation: spec.RelDemonstrates})
		}
		mk(typ, c.subject, statement, ev, as)
		// A commit that mixes code with brand-new test files also earns a test claim.
		if typ != spec.TypeTest {
			var newTests []string
			for _, p := range c.files {
				if adapters.IsTestPath(p) && f.created[p] {
					newTests = append(newTests, p)
				}
			}
			if len(newTests) > 0 {
				tev := []spec.Evidence{{Target: spec.FileTarget(newTests[0], c.sha, 0, 0), Relation: spec.RelProduced}}
				for _, e := range f.edits {
					if e.path == newTests[0] && e.seq <= c.seq && e.seq > prevSeq {
						tev = append(tev, spec.Evidence{Target: spec.EventTarget(sid, e.seq, e.seq), Relation: spec.RelProduced})
					}
				}
				tas := []spec.Assertion{{"check": "test_fails_before_passes_after", "test": newTests[0], "commit": c.sha, "runs": runs}}
				if r := runOf(f.tests, newTests[0]); r != nil {
					tas[0]["command"] = r.cmd
				}
				mk(spec.TypeTest, "Added "+fileList(newTests), fmt.Sprintf("%s is new in commit %s", fileList(newTests), c.sha[:7]), tev, tas)
			}
		}
		prevSeq = c.seq
	}
	// Uncommitted work still in the working tree.
	var dirty []string
	var dirtySeqs []int
	seenDirty := map[string]bool{}
	for _, e := range f.edits {
		if committed[e.path] || scratch(e.path) {
			continue
		}
		dirtySeqs = append(dirtySeqs, e.seq)
		if !seenDirty[e.path] {
			seenDirty[e.path] = true
			dirty = append(dirty, e.path)
		}
	}
	if len(dirty) > 0 {
		var ev []spec.Evidence
		for _, sp := range claims.Spans(dirtySeqs, 8) {
			ev = append(ev, spec.Evidence{Target: spec.EventTarget(sid, sp[0], sp[1]), Relation: spec.RelProduced})
		}
		typ := classify("", dirty)
		var as []spec.Assertion
		if pass := passAfter(f.tests, dirtySeqs[len(dirtySeqs)-1]); pass != nil {
			ev = append(ev, spec.Evidence{Target: spec.EventTarget(sid, pass.seq, max(pass.resultSeq, pass.seq)), Relation: spec.RelDemonstrates})
		}
		if d.Session.Repo == "" {
			mk(typ, "Changed "+fileList(dirty), fmt.Sprintf("Edited %s in this session (not in a git repository)", fileList(dirty)), ev, as)
		} else {
			mk(typ, "Uncommitted changes to "+fileList(dirty), fmt.Sprintf("Edited %s in this session without committing", fileList(dirty)), ev, as)
		}
	}
	// Diagnosis: a repro that fails before a commit and passes after it.
	diagnosed := false
	coveredChild := map[string]bool{}
	for i, t := range allCommands(d) {
		if !reproRe.MatchString(t.cmd) || t.status != "fail" {
			continue
		}
		for _, later := range allCommands(d)[i+1:] {
			if later.key != t.key || later.status != "pass" {
				continue
			}
			c := commitBetween(f.commits, t.seq, later.seq)
			if c == nil {
				c = commitAfter(f.commits, later.seq)
			}
			if c == nil {
				break
			}
			as := []spec.Assertion{{"check": "command_fails_before_passes_after", "command": t.cmd, "commit": c.sha}}
			if script := reproRe.FindString(t.cmd); script != "" {
				for p := range f.created {
					if strings.HasSuffix(p, script) {
						as[0]["overlay"] = []string{p}
					}
				}
			}
			ev := []spec.Evidence{
				{Target: spec.EventTarget(sid, t.seq, max(t.resultSeq, t.seq)), Relation: spec.RelDemonstrates},
				{Target: spec.EventTarget(sid, later.seq, max(later.resultSeq, later.seq)), Relation: spec.RelDemonstrates},
			}
			dc := mk(spec.TypeDiagnosis, "Reproduced the bug with "+reproRe.FindString(t.cmd),
				fmt.Sprintf("`%s` fails before commit %s and passes after it", clip(t.cmd, 120), c.sha[:7]), ev, as)
			// Credit a subagent that wrote the repro on this claim rather than separately.
			script := reproRe.FindString(t.cmd)
			for _, child := range d.Children {
				cevs := d.ChildEvents[child.ID]
				for _, p := range childEdits(d, child.ID) {
					if strings.HasSuffix(p, script) && len(cevs) > 0 {
						dc.Evidence = append(dc.Evidence, spec.Evidence{Target: spec.EventTarget(child.ID, cevs[0].Seq, cevs[len(cevs)-1].Seq), Relation: spec.RelProduced})
						dc.CoContributors = append(dc.CoContributors, spec.CoContributor{ID: subagentID(child.ParentSessionID, child.AgentType, cevs), Basis: "wrote " + p})
						coveredChild[child.ID] = true
					}
				}
			}
			dc.AssignID()
			diagnosed = true
			break
		}
		if diagnosed {
			break
		}
	}
	if !diagnosed {
		for i := range d.Events {
			e := &d.Events[i]
			if e.Kind != spec.KindMessage || role(e.Actor) == "human" {
				continue
			}
			text := claims.PayloadStr(e, "text")
			if !rootCauseRe.MatchString(text) || !substantive(firstSentence(text, rootCauseRe)) {
				continue
			}
			from := max(1, e.Seq-12)
			mk(spec.TypeDiagnosis, "Diagnosed: "+headClip(firstSentence(text, rootCauseRe), 80), firstSentence(text, rootCauseRe),
				[]spec.Evidence{{Target: spec.EventTarget(sid, from, e.Seq), Relation: spec.RelProduced}}, nil)
			break
		}
	}
	// Reported failures.
	negatives := 0
	for i := range d.Events {
		e := &d.Events[i]
		if negatives >= 2 || e.Kind != spec.KindMessage || role(e.Actor) == "human" {
			continue
		}
		text := claims.PayloadStr(e, "text")
		if !negativeRe.MatchString(text) {
			continue
		}
		s := firstSentence(text, negativeRe)
		if !substantive(s) || youRe.MatchString(s) {
			continue // "Reddit didn't work." or "the option you already ruled out"
		}
		negatives++
		mk(spec.TypeNegativeResult, "Ruled out: "+headClip(s, 80), s,
			[]spec.Evidence{{Target: spec.EventTarget(sid, max(1, e.Seq-8), e.Seq), Relation: spec.RelProduced}}, nil)
	}
	// Subagent work.
	starts := []*spec.Event{}
	for i := range d.Events {
		if d.Events[i].Kind == spec.KindSubagentStart {
			starts = append(starts, &d.Events[i])
		}
	}
	for i, child := range d.Children {
		evs := d.ChildEvents[child.ID]
		if len(evs) == 0 || coveredChild[child.ID] {
			continue
		}
		desc := child.Title
		var ev []spec.Evidence
		if i < len(starts) {
			st := starts[i]
			if dd := claims.PayloadStr(st, "description"); dd != "" {
				desc = dd
			}
			end := st.Seq
			if r := d.Result(st); r != nil {
				end = r.Seq
			}
			ev = append(ev, spec.Evidence{Target: spec.EventTarget(sid, st.Seq, end), Relation: spec.RelProduced})
		}
		ev = append(ev, spec.Evidence{Target: spec.EventTarget(child.ID, evs[0].Seq, evs[len(evs)-1].Seq), Relation: spec.RelProduced})
		typ := spec.TypeResearch
		at := strings.ToLower(child.AgentType + " " + desc)
		switch {
		case strings.Contains(at, "test") || strings.Contains(at, "repro"):
			typ = spec.TypeTest
		case strings.Contains(at, "review"):
			typ = spec.TypeReview
		}
		if typ == spec.TypeTest && strings.Contains(at, "repro") {
			typ = spec.TypeDiagnosis
		}
		c := mk(typ, "Subagent: "+orDash(desc), fmt.Sprintf("A %s subagent ran %d steps for: %s", orDash(child.AgentType), len(evs), clip(desc, 160)), ev, nil)
		c.CoContributors = []spec.CoContributor{{ID: subagentID(child.ParentSessionID, child.AgentType, evs), Basis: "did the work"}}
		c.AssignID()
	}
	// Review / research sessions that changed nothing.
	if len(f.edits) == 0 && len(f.commits) == 0 {
		reads, research := 0, 0
		var firstPrompt string
		var lastMsg *spec.Event
		for i := range d.Events {
			e := &d.Events[i]
			switch e.Kind {
			case spec.KindFileRead, spec.KindSearch:
				reads++
			case spec.KindToolCall:
				if claims.PayloadStr(e, "category") == "research" {
					research++
				}
			case spec.KindPrompt:
				if t := strings.TrimSpace(claims.PayloadStr(e, "text")); firstPrompt == "" && role(e.Actor) == "human" && !strings.HasPrefix(t, "/") {
					firstPrompt = t
				}
			case spec.KindMessage:
				lastMsg = e
			}
		}
		if lastMsg != nil && firstPrompt != "" && len(claims.PayloadStr(lastMsg, "text")) > 200 {
			ev := []spec.Evidence{{Target: spec.EventTarget(sid, lastMsg.Seq, lastMsg.Seq), Relation: spec.RelProduced}}
			switch {
			case reviewRe.MatchString(firstPrompt) && reads >= 2:
				mk(spec.TypeReview, "Reviewed: "+headClip(titleOf(firstPrompt), 80), fmt.Sprintf("Read %d files and reported findings (event #%d)", reads, lastMsg.Seq), ev, nil)
			case research >= 2 || reads >= 6:
				mk(spec.TypeResearch, "Researched: "+headClip(titleOf(firstPrompt), 80), fmt.Sprintf("Consulted %d sources/files and summarized the answer (event #%d)", reads+research, lastMsg.Seq), ev, nil)
			}
		}
	}
	return out
}

func subagentID(parent, agentType string, evs []spec.Event) string {
	for _, e := range evs {
		if r := role(e.Actor); r == "subagent" || r == "agent" {
			return e.Actor
		}
	}
	if agentType == "" {
		agentType = "general"
	}
	return "subagent:" + parent + "/" + strings.ToLower(agentType)
}

func titleOf(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i > 0 {
		s = s[:i]
	}
	return s
}

func allCommands(d *claims.Data) []testRun {
	var res []testRun
	for i := range d.Events {
		e := &d.Events[i]
		if e.Kind != spec.KindCommand {
			continue
		}
		t := testRun{seq: e.Seq, cmd: claims.PayloadStr(e, "command")}
		t.key = normCmd(t.cmd)
		if r := d.Result(e); r != nil {
			t.resultSeq = r.Seq
			if ok, _ := claims.Payload(r)["ok"].(bool); ok {
				t.status = "pass"
			} else {
				t.status = "fail"
			}
		}
		res = append(res, t)
	}
	return res
}

// transition finds a test command that failed before lastEdit and passed after it.
func transition(tests []testRun, lastEdit int) (*testRun, *testRun) {
	for i := len(tests) - 1; i >= 0; i-- {
		pass := &tests[i]
		if pass.seq <= lastEdit || pass.status != "pass" {
			continue
		}
		for j := range tests {
			fail := &tests[j]
			if fail.seq < lastEdit && fail.status == "fail" && (fail.key == pass.key || sameTarget(fail, pass)) {
				return fail, pass
			}
		}
	}
	return nil, nil
}

func sameTarget(a, b *testRun) bool {
	for _, x := range a.targets {
		for _, y := range b.targets {
			if x == y {
				return true
			}
		}
	}
	return false
}

func passAfter(tests []testRun, seq int) *testRun {
	for i := range tests {
		if tests[i].seq > seq && tests[i].status == "pass" {
			return &tests[i]
		}
	}
	return nil
}

func runOf(tests []testRun, path string) *testRun {
	for i := len(tests) - 1; i >= 0; i-- {
		if strings.Contains(tests[i].cmd, path) {
			return &tests[i]
		}
	}
	return nil
}

func commitBetween(cs []commitInfo, a, b int) *commitInfo {
	for i := range cs {
		if cs[i].seq > a && cs[i].seq < b {
			return &cs[i]
		}
	}
	return nil
}

func commitAfter(cs []commitInfo, a int) *commitInfo {
	for i := range cs {
		if cs[i].seq > a {
			return &cs[i]
		}
	}
	return nil
}

func capList(xs []string, n int) []string {
	if len(xs) > n {
		return xs[:n]
	}
	if xs == nil {
		return []string{}
	}
	return xs
}

func fileList(files []string) string {
	switch len(files) {
	case 0:
		return "no files"
	case 1:
		return files[0]
	case 2:
		return files[0] + " and " + files[1]
	}
	return fmt.Sprintf("%s, %s and %d more", files[0], files[1], len(files)-2)
}

// Facts summarizes deterministic observations for the model auditor so it
// can attach checkable assertions.
func Facts(d *claims.Data) string {
	f := gather(d)
	var b strings.Builder
	for _, c := range f.commits {
		fmt.Fprintf(&b, "- commit %s at event #%d %q files=%s\n", c.sha[:min(12, len(c.sha))], c.seq, c.subject, strings.Join(capList(c.files, 8), ","))
	}
	for _, t := range f.tests {
		fmt.Fprintf(&b, "- test run at event #%d: `%s` → %s\n", t.seq, clip(t.cmd, 160), orDash(t.status))
	}
	for p := range f.created {
		fmt.Fprintf(&b, "- file created this session: %s\n", p)
	}
	return b.String()
}

// headClip shortens s at a word boundary for use in a title.
func headClip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return strings.TrimRight(s, ".")
	}
	cut := strings.LastIndex(s[:n], " ")
	if cut < n/2 {
		cut = n
	}
	return strings.TrimRight(s[:cut], " ,;:.") + "…"
}

// childEdits lists files a subagent session edited.
func childEdits(d *claims.Data, childID string) []string {
	var out []string
	seen := map[string]bool{}
	for _, e := range d.ChildEvents[childID] {
		if e.Kind == spec.KindFileEdit {
			p := d.RelPath(claims.PayloadStr(&e, "path"))
			if p != "" && !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}
