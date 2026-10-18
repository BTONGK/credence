package claims

import (
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/BTONGK/credence/adapters"
	"github.com/BTONGK/credence/spec"
)

// What a statement says happened decides how the session record checks it.
const (
	StateChange   = "change"   // I built / fixed / updated …
	StateTests    = "tests"    // all 98 tests pass
	StateBuild    = "build"    // the build passes
	StateRuns     = "runs"     // it works end to end; I ran it
	StateDeploy   = "deploy"   // deployed, published, live at <url>
	StateCommit   = "commit"   // committed
	StateResearch = "research" // I read / checked / looked through …
)

// A long summary shouldn't bury the report.
const (
	statedPerReply   = 4
	statedPerSession = 12
)

// Statement is one checkable sentence from an agent's reply.
type Statement struct {
	Text  string
	Kinds []string
	Paths []string // files or directories the sentence names
	URLs  []string
	Count int // a stated number of passing tests
	// Agent says the agent did it ("I fixed …") rather than describing a
	// state ("the server is set up"); doing nothing contradicts only the first.
	Agent bool
}

// ExtractStated turns what the agent told the human it had done into
// claims. Agents rarely file claims with the credence_claim tool; they
// write "I fixed the scheduler and all 14 tests pass" in the reply that
// ends a turn. Each such statement becomes a claim with origin "stated"
// whose session_* assertions the session's own record (edits, command
// results, links tools returned) can back or contradict.
func ExtractStated(d *Data) []*spec.Claim {
	var out []*spec.Claim
	seen := map[string]bool{}
	turnStart := 0
	var reply *spec.Event
	flush := func() {
		if reply == nil {
			return
		}
		for _, st := range Statements(PayloadStr(reply, "text")) {
			if len(out) >= statedPerSession {
				break
			}
			key := strings.ToLower(st.Text)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, statedClaim(d, st, turnStart, reply))
		}
		reply = nil
	}
	for i := range d.Events {
		e := &d.Events[i]
		switch {
		case e.Kind == spec.KindPrompt && strings.HasPrefix(e.Actor, "human:"):
			flush()
			// A background task finishing is the harness talking, not the
			// human: the next reply still reports on the human's request.
			if !harnessPromptRe.MatchString(PayloadStr(e, "text")) {
				turnStart = e.Seq
			}
		case e.Kind == spec.KindMessage && strings.HasPrefix(e.Actor, "agent:"):
			reply = e
		}
	}
	flush()
	return out
}

// harnessPromptRe matches user-role messages the harness wrote.
var harnessPromptRe = regexp.MustCompile(`^\s*<(?:task-notification|local-command-stdout|local-command-caveat|system-reminder)>`)

func statedClaim(d *Data, st Statement, turnStart int, reply *spec.Event) *spec.Claim {
	sid := d.Session.ID
	from, to := turnStart+1, reply.Seq
	if !workBetween(d, from, to) {
		// A reply with no work since the human spoke is about earlier work.
		from = 1
	}
	c := &spec.Claim{SessionID: sid, Claimant: reply.Actor, Origin: spec.OriginStated, Type: statedType(st),
		Title: clipText(st.Text, 140), Statement: st.Text, SourceEvent: reply.Seq, CreatedAt: reply.TS,
		Assertions: []spec.Assertion{},
		Evidence:   []spec.Evidence{{Target: spec.EventTarget(sid, to, to), Relation: spec.RelInformed, Note: "the agent's reply"}},
	}
	if from < to {
		c.Evidence = append(c.Evidence, spec.Evidence{Target: spec.EventTarget(sid, from, to-1), Relation: spec.RelProduced, Note: "the work the reply reports on"})
	}
	run := func(kind string) spec.Assertion {
		return spec.Assertion{"check": "session_run", "kind": kind, "from": from, "to": to}
	}
	for _, k := range st.Kinds {
		switch k {
		case StateChange:
			a := spec.Assertion{"check": "session_edits", "from": from, "to": to}
			if len(st.Paths) > 0 {
				a["paths"] = st.Paths
			}
			if st.Agent {
				a["by_agent"] = true
			}
			c.Assertions = append(c.Assertions, a)
		case StateTests:
			a := run("test")
			if st.Count > 0 {
				a["count"] = st.Count
			}
			c.Assertions = append(c.Assertions, a)
		case StateBuild:
			c.Assertions = append(c.Assertions, run("build"))
		case StateRuns:
			c.Assertions = append(c.Assertions, run("run"))
		case StateDeploy:
			if len(st.URLs) == 0 {
				c.Assertions = append(c.Assertions, run("deploy"))
			}
			for i, u := range st.URLs {
				if i == 2 {
					break
				}
				a := spec.Assertion{"check": "session_link", "url": u, "from": from, "to": to}
				if deployRe.MatchString(st.Text) {
					a["published"] = true
				}
				c.Assertions = append(c.Assertions, a)
			}
		case StateCommit:
			c.Assertions = append(c.Assertions, run("commit"))
		case StateResearch:
			c.Assertions = append(c.Assertions, run("research"))
		}
	}
	c.AssignID()
	return c
}

var (
	statedFixRe      = regexp.MustCompile(`(?i)\b(fix|fixes|fixed|bug|crash|regress\w*|broken|flaky)\b`)
	statedRefactorRe = regexp.MustCompile(`(?i)\b(refactor\w*|renamed|cleaned up|simplif\w*|reorgani\w*|restructur\w*)\b`)
)

func workBetween(d *Data, from, to int) bool {
	for i := range d.Events {
		e := &d.Events[i]
		if e.Seq < from || e.Seq >= to {
			continue
		}
		switch e.Kind {
		case spec.KindCommand, spec.KindFileEdit, spec.KindFileRead, spec.KindSearch, spec.KindToolCall, spec.KindSubagentStart, spec.KindCommit:
			return true
		}
	}
	return false
}

func statedType(st Statement) string {
	has := func(k string) bool {
		for _, x := range st.Kinds {
			if x == k {
				return true
			}
		}
		return false
	}
	switch {
	case has(StateChange) && statedFixRe.MatchString(st.Text):
		return spec.TypeFix
	case has(StateChange) && statedRefactorRe.MatchString(st.Text):
		return spec.TypeRefactor
	case has(StateChange) && len(st.Paths) > 0 && allDocs(st.Paths):
		return spec.TypeDocs
	case has(StateChange):
		return spec.TypeFeature
	case has(StateTests):
		return spec.TypeTest
	case has(StateDeploy) || has(StateCommit) || has(StateBuild):
		return spec.TypeOps
	case has(StateRuns):
		return spec.TypeFeature
	}
	return spec.TypeResearch
}

func allDocs(paths []string) bool {
	for _, p := range paths {
		if !adapters.IsDocPath(p) {
			return false
		}
	}
	return true
}

var (
	changeRe = regexp.MustCompile(`(?i)\b(?:I|I've|I have|we|we've|we have)\s+(?:also\s+|now\s+|just\s+|then\s+|already\s+|finally\s+)?` +
		`(?:built|added|fixed|wrote|written|created|implemented|updated|changed|rewrote|rewritten|replaced|removed|deleted|moved|renamed|` +
		`refactored|redesigned|reworked|split|merged|cleaned up|set up|wired|hooked up|made|turned|switched|corrected|restored|reverted|` +
		`upgraded|bumped|patched|converted|migrated|edited|drafted|saved|generated|exported|configured|installed|enabled|disabled|` +
		`connected|integrated|extended|simplified|trimmed|shortened|tightened|reorganized|restructured)\b`)
	passiveRe = regexp.MustCompile(`(?i)\b(?:is|are|was|were|has been|have been|it's|they're)\s+(?:now\s+|all\s+|both\s+|also\s+)?` +
		`(?:updated|fixed|added|written|done|finished|complete|completed|in place|rewritten|removed|renamed|set up|wired up|` +
		`configured|installed|connected|resolved|merged|implemented)\b|\bnow (?:has|shows|says|reads|uses|includes|supports|handles|returns|opens|links)\b`)
	testsRe = regexp.MustCompile(`(?i)\b(?:all\s+)?(?:(\d[\d,]*)\s+)?(?:of the\s+)?(?:new\s+|existing\s+|unit\s+|integration\s+|e2e\s+|end-to-end\s+)?` +
		`(?:tests?|specs?|test cases?)\b[^.;:]{0,30}?\b(?:pass(?:es|ed|ing)?|are green|is green|succeed(?:s|ed)?)\b`)
	buildRe = regexp.MustCompile(`(?i)\b(?:build|typecheck|type check|type-check|tsc|lint|linter|vet|compile|compilation)\s+(?:now\s+|still\s+)?` +
		`(?:passes|passed|succeeds|succeeded|is clean|are clean|is green|completes|completed)\b|\b(?:builds?|compiles?)\s+(?:cleanly|successfully|without errors)\b`)
	runsRe = regexp.MustCompile(`(?i)\b(?:it|this|that|everything|all of it|the [\w\-' ]{1,40}?)\s+(?:now\s+|still\s+|all\s+)?(?:works|worked|is working|are working)\b|` +
		`\b(?:runs?|ran|renders?|rendered|loads?|loaded|works?)\s+(?:end[ -]to[ -]end|correctly|cleanly|fine|as expected|without errors|successfully|properly)\b|` +
		`\bI\s+(?:also\s+|then\s+)?(?:ran|tested|verified|confirmed|exercised|smoke-tested)\b`)
	deployRe = regexp.MustCompile(`(?i)\b(?:I|we)\s+(?:also\s+|now\s+|just\s+)?(?:put|posted|uploaded|published|republished|deployed|redeployed|pushed|shipped|released)\b|` +
		`\b(?:is|are|it's|now)\s+(?:now\s+)?(?:live|deployed|published|online)\b|\b(?:is|it's|are)\s+(?:now\s+)?(?:running|up|available)\s+(?:at|on)\b`)
	linkWordRe = regexp.MustCompile(`(?i)\b(?:here|at|live|page|preview|running|deployed|published|available)\b`)
	mdLinkRe   = regexp.MustCompile(`\[([^\]]*)\]\((https?://[^)\s]+)\)`)
	// "That works because …" and "it works in your browser" describe; they don't report a result.
	describeRe = regexp.MustCompile(`(?i)^\s*(?:because|by|like|for|if|when|with|only|in|on|as|the same|similarly)\b`)
	commitRe   = regexp.MustCompile(`(?i)\b(?:I|we)\s+(?:also\s+)?committed\b|\b(?:is|are|was|were|has been|have been)\s+(?:now\s+)?committed\b`)
	researchRe = regexp.MustCompile(`(?i)\bI\s+(?:also\s+|first\s+)?(?:read|checked|looked (?:at|through|into|up)|searched|reviewed|went through|compared|` +
		`researched|scanned|audited|inspected|counted|measured|pulled|fetched|found)\b`)
	// Plans, conditions, negations and hedges are not claims of work done.
	hedgeRe = regexp.MustCompile(`(?i)\b(?:I'll|I will|I'd|I would|I can|I could|I may|I might|I need|let me|let's|we'll|we can|we could|if|once|unless|until|` +
		`whether|would|should|could|might|not|cannot|never|nothing|none|no longer|yet|plan to|going to|want to|need to|needs to|try to|hope|` +
		`expect|probably|likely|seems|appears|maybe|perhaps)\b|n't\b`)
	// Sentences about the human, or instructions to them.
	userRe = regexp.MustCompile(`(?i)^(?:you|your|here's|here is|open|click|go to|visit|run|paste|type|sign|approve|use|try|copy|re-?run|restart|` +
		`re-?seed|drag|pick|choose|select|press|say|tell|ask|please|note|remember|make sure|to|when|after|before|while|as soon as|how)\b`)
	quotedRe = regexp.MustCompile(`"[^"]*"|“[^”]*”`)
	leadRe   = regexp.MustCompile(`(?i)^(?:yes|no|done|ok|okay|great|sure|good news|update|fixed|result|summary)\s*[,.:!—–-]+\s*`)
	listRe   = regexp.MustCompile(`^(?:[-*•+]|\d+[.)])\s+`)
	urlRe    = regexp.MustCompile("https?://[^\\s)<>\\]\"'`*]+")
	tickRe   = regexp.MustCompile("`([^`]+)`")
	slashRe  = regexp.MustCompile(`(?:^|\s)((?:~|\.{1,2})?/?[\w.\-]+(?:/[\w.\-]+)+/?)`)
	fileRe   = regexp.MustCompile(`^(?:~|\.{1,2})?/?[\w.\-]+(?:/[\w.\-]+)*(?:\.[A-Za-z][\w]{0,5}|/)?$`)
	tldRe    = regexp.MustCompile(`(?i)\.(?:com|org|net|io|ai|dev|app|co|edu|gov|us|uk)$`)
)

// Statements extracts the checkable statements from a reply: the strongest
// few, in the order they were written.
func Statements(text string) []Statement {
	var cands []Statement
	for _, s := range sentences(text) {
		for _, cl := range clauses(s) {
			if st, ok := statement(cl); ok {
				cands = append(cands, st)
			}
		}
	}
	if len(cands) <= statedPerReply {
		return cands
	}
	idx := make([]int, len(cands))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return strength(cands[idx[a]]) > strength(cands[idx[b]]) })
	idx = idx[:statedPerReply]
	sort.Ints(idx)
	out := make([]Statement, 0, len(idx))
	for _, i := range idx {
		out = append(out, cands[i])
	}
	return out
}

func strength(st Statement) int {
	w := map[string]int{StateTests: 5, StateDeploy: 4, StateRuns: 4, StateBuild: 3, StateCommit: 3, StateChange: 2, StateResearch: 1}
	n := 0
	for _, k := range st.Kinds {
		n += w[k]
	}
	return n
}

func statement(s string) (Statement, bool) {
	if strings.HasSuffix(strings.TrimRight(strings.TrimSpace(s), "*_"), ":") {
		return Statement{}, false // a lead-in to a list or quote
	}
	s = cleanMarkup(s)
	if len(strings.Fields(mdLinkRe.ReplaceAllString(s, ""))) < 3 {
		return Statement{}, false // a list of sources, not a claim
	}
	s = mdLinkRe.ReplaceAllString(s, "$1 ($2)")
	s = leadRe.ReplaceAllString(s, "")
	words := len(strings.Fields(s))
	if words < 3 || words > 60 || strings.HasSuffix(s, "?") || userRe.MatchString(s) || labelled(s) {
		return Statement{}, false
	}
	// Judge the agent's words, not links or quoted titles: a URL can contain
	// "if", and a section called "How it works" is not a claim that it works.
	bare := quotedRe.ReplaceAllString(urlRe.ReplaceAllString(s, ""), `""`)
	if hedgeRe.MatchString(bare) {
		return Statement{}, false
	}
	st := Statement{Text: capitalizeFirst(clipText(s, 300)), URLs: urls(s)}
	add := func(k string) {
		for _, x := range st.Kinds {
			if x == k {
				return
			}
		}
		st.Kinds = append(st.Kinds, k)
	}
	if m := testsRe.FindStringSubmatch(bare); m != nil {
		add(StateTests)
		st.Count, _ = strconv.Atoi(strings.ReplaceAll(m[1], ",", ""))
	}
	if buildRe.MatchString(bare) {
		add(StateBuild)
	}
	if m := runsRe.FindStringIndex(bare); m != nil && !describeRe.MatchString(bare[m[1]:]) {
		add(StateRuns)
	}
	if deployRe.MatchString(bare) || (len(st.URLs) > 0 && linkWordRe.MatchString(bare)) {
		add(StateDeploy)
	}
	if commitRe.MatchString(bare) {
		add(StateCommit)
	}
	if changeRe.MatchString(bare) {
		add(StateChange)
		st.Agent = true
	} else if passiveRe.MatchString(bare) {
		add(StateChange)
	}
	if researchRe.MatchString(bare) {
		add(StateResearch)
	}
	if len(st.Kinds) == 0 {
		return Statement{}, false
	}
	st.Paths = paths(s)
	return st, true
}

// labelled reports "Hosting (Cloudflare): connected as …" style lines, which
// describe rather than claim, unless the agent is the subject.
func labelled(s string) bool {
	i := strings.Index(s, ": ")
	if i < 0 || strings.HasPrefix(s, "I ") || strings.HasPrefix(s, "I'") {
		return false
	}
	return len(strings.Fields(s[:i])) <= 4
}

// sentences splits a markdown reply into sentences, skipping code, tables,
// quotes and headings, and treating each list item as its own sentence.
func sentences(text string) []string {
	var out, para []string
	flush := func() {
		if len(para) > 0 {
			out = append(out, splitSentences(strings.Join(para, " "))...)
			para = nil
		}
	}
	fence := false
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "```") {
			fence = !fence
			flush()
			continue
		}
		if fence || t == "" || strings.HasPrefix(t, "|") || strings.HasPrefix(t, ">") || strings.HasPrefix(t, "#") {
			flush()
			continue
		}
		if m := listRe.FindStringIndex(t); m != nil {
			flush()
			para = append(para, t[m[1]:])
			flush()
			continue
		}
		para = append(para, t)
	}
	flush()
	return out
}

var abbrevs = map[string]bool{"e.g": true, "i.e": true, "vs": true, "etc": true, "approx": true, "no": true, "mr": true, "dr": true}

func splitSentences(p string) []string {
	var out []string
	start := 0
	for i := 0; i < len(p); i++ {
		if p[i] != '.' && p[i] != '!' && p[i] != '?' {
			continue
		}
		j := i + 1
		for j < len(p) && strings.IndexByte(`*"')_`, p[j]) >= 0 {
			j++
		}
		if j >= len(p) || p[j] != ' ' {
			continue
		}
		k := j
		for k < len(p) && p[k] == ' ' {
			k++
		}
		if k >= len(p) {
			break
		}
		n := p[k]
		if !(n >= 'A' && n <= 'Z' || strings.IndexByte("*`\"_[", n) >= 0 || n >= 0x80) {
			continue
		}
		words := strings.Fields(p[start:i])
		if len(words) > 0 && abbrevs[strings.ToLower(strings.Trim(words[len(words)-1], "*_("))] {
			continue
		}
		out = append(out, strings.TrimSpace(p[start:j]))
		start = k
		i = k - 1
	}
	if s := strings.TrimSpace(p[start:]); s != "" {
		out = append(out, s)
	}
	return out
}

// clauses splits where one sentence holds separate statements:
// "I couldn't open the console, so I checked the DNS records instead".
func clauses(s string) []string {
	parts := []string{s}
	for _, sep := range []string{"; ", " — ", " – ", ", so ", ", but "} {
		var next []string
		for _, p := range parts {
			next = append(next, strings.Split(p, sep)...)
		}
		parts = next
	}
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func cleanMarkup(s string) string {
	s = strings.NewReplacer("**", "", "__", "").Replace(s)
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimRight(strings.TrimSpace(s), ":")
}

func urls(s string) []string {
	var out []string
	for _, u := range urlRe.FindAllString(s, -1) {
		u = strings.TrimRight(u, ".,;:!")
		if _, err := url.Parse(u); err == nil {
			out = append(out, u)
		}
	}
	return out
}

// paths are files or directories a sentence names in backticks or as a
// slash-separated path; bare words like "Node.js" are not paths.
func paths(s string) []string {
	s = urlRe.ReplaceAllString(s, "")
	var out []string
	seen := map[string]bool{}
	add := func(p string, quoted bool) {
		p = strings.TrimRight(p, ".,;:")
		if p == "" || seen[p] || tldRe.MatchString(p) || !fileRe.MatchString(p) {
			return
		}
		last := p[strings.LastIndexByte(strings.TrimRight(p, "/"), '/')+1:]
		hasExt := strings.Contains(last, ".")
		rooted := strings.HasPrefix(p, "/") || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, "./") || strings.HasPrefix(p, "../")
		switch {
		case quoted && !strings.Contains(p, "/") && !hasExt:
			return
		case !quoted && !rooted && !hasExt:
			return // "and/or", "input/output"
		}
		seen[p] = true
		out = append(out, p)
	}
	for _, m := range tickRe.FindAllStringSubmatch(s, -1) {
		if !strings.ContainsAny(m[1], " =()") {
			add(m[1], true)
		}
	}
	for _, m := range slashRe.FindAllStringSubmatch(tickRe.ReplaceAllString(s, ""), -1) {
		add(m[1], false)
	}
	return out
}

func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := strings.LastIndexByte(s[:n], ' ')
	if cut < n/2 {
		cut = n
	}
	return strings.TrimRight(s[:cut], " ,;:") + "…"
}

func capitalizeFirst(s string) string {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
