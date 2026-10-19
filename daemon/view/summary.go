package view

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/BTONGK/credence/daemon/store"
	"github.com/BTONGK/credence/spec"
)

// Summary sums up what agents did over a period in the words a person
// uses: emails sent, files written, tests run, sites published. It reads
// the session record itself; the agent's own claims appear only where the
// record contradicts them.
type Summary struct {
	Since     time.Time `json:"since"`
	Sessions  int       `json:"sessions"`
	ActiveSec int       `json:"active_sec"`
	CostUSD   float64   `json:"cost_usd"`
	Tiles     []Tile    `json:"tiles"`
	Apps      []AppUse  `json:"apps"`
	Well      []Note    `json:"well"`
	Look      []Note    `json:"look"`
	Requests  []Request `json:"requests"`
}

// Tile is one headline number: "342 emails sent".
type Tile struct {
	Key    string   `json:"key"`
	Label  string   `json:"label"`
	Value  int      `json:"value"`
	Detail string   `json:"detail,omitempty"`
	Items  []string `json:"items,omitempty"`
}

// AppUse counts the actions taken in one app or service.
type AppUse struct {
	Name    string `json:"name"`
	Actions int    `json:"actions"`
}

// Note is one line of "went well" or "needs a look".
type Note struct {
	Text     string   `json:"text"`
	Sessions []Ref    `json:"sessions,omitempty"`
	Details  []Detail `json:"details,omitempty"`
}

// Detail backs a note with one concrete case.
type Detail struct {
	Text    string `json:"text"`
	Said    string `json:"said,omitempty"` // the agent's words, for a contradicted claim
	Session string `json:"session,omitempty"`
}

// Ref points at a session.
type Ref struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// Request is one session: what was asked for and what came of it.
type Request struct {
	SessionID string    `json:"session_id"`
	Title     string    `json:"title"`
	When      time.Time `json:"when"`
	ActiveSec int       `json:"active_sec"`
	Facts     []string  `json:"facts"`
	Look      bool      `json:"look"`
}

// work is what the record shows a session did.
type work struct {
	cwd              string
	files            map[string]string // path -> create | edit | delete
	added, removed   int
	emailsSent       int
	emailsFailed     int
	drafts           int
	recipients       map[string]bool
	messages         int
	calendar         int
	onlineDocs       int
	sites            map[string]bool
	browserSteps     int
	pagesRead        int
	searches         int
	testRuns         int
	testFails        int
	lastTest         *testOutcome
	published        map[string]bool
	deploys          int
	commits          map[string]bool
	pushes           int
	apps             map[string]int
	interrupts       int
	corrections      []string
	stuck            []string // steps that failed 3+ times in a row
	activeSec        int
	lastTS           time.Time
	lastFailed       string
	failStreak       int
	pending          map[string]call
	commitsWithoutID int
}

type call struct {
	tool    string
	command string
	desc    string
	gitOps  []string
	input   map[string]any
}

// payload holds the event fields the summary reads.
type payload struct {
	Tool        string          `json:"tool"`
	ToolCallID  string          `json:"tool_call_id"`
	CallKind    string          `json:"call_kind"`
	Path        string          `json:"path"`
	Op          string          `json:"op"`
	Added       int             `json:"added"`
	Removed     int             `json:"removed"`
	Command     string          `json:"command"`
	Description string          `json:"description"`
	GitOps      []string        `json:"git_ops"`
	OK          *bool           `json:"ok"`
	Output      string          `json:"output"`
	Text        string          `json:"text"`
	SHA         string          `json:"sha"`
	TestOutcome *testOutcome    `json:"test_outcome"`
	Input       json.RawMessage `json:"input"`
}

// testOutcome is a test run's result as the adapters record it.
type testOutcome struct {
	Status string `json:"status"`
	Passed int    `json:"passed"`
	Failed int    `json:"failed"`
}

// passed trusts the parsed counts; the exit code decides only without them
// (a pipeline can exit nonzero after every test passed).
func (t testOutcome) passed() bool {
	if t.Passed+t.Failed > 0 {
		return t.Failed == 0
	}
	return t.Status == "pass"
}

func newWork() *work {
	return &work{files: map[string]string{}, recipients: map[string]bool{}, sites: map[string]bool{}, published: map[string]bool{},
		commits: map[string]bool{}, apps: map[string]int{}, pending: map[string]call{}}
}

var (
	// Commands that put work somewhere others can see it.
	deployCmd  = regexp.MustCompile(`\b(?:wrangler\s+(?:pages\s+)?deploy|vercel(?:\s+deploy|\s+--prod)?|netlify\s+deploy|fly\s+deploy|firebase\s+deploy|gh-pages|npm\s+publish|docker\s+push|kubectl\s+apply|terraform\s+apply|gcloud\s+\S+\s+deploy|aws\s+s3\s+(?:sync|cp))\b`)
	deployTool = regexp.MustCompile(`(?i)deploy|publish|release`)
	elapsedRe  = regexp.MustCompile(` at \d+:\d{2}(?::\d{2})?\b`)
	urlRe      = regexp.MustCompile(`https?://[A-Za-z0-9.-]+\.[A-Za-z]{2,}(?::\d+)?`)
	// A file written with a heredoc: cat > f <<EOF, cat <<EOF > f, tee f <<EOF.
	heredocA = regexp.MustCompile(`^\s*cat\s*>{1,2}\s*(\S+)\s*<<-?\s*['"]?(\w+)['"]?\s*$`)
	heredocB = regexp.MustCompile(`^\s*cat\s*<<-?\s*['"]?(\w+)['"]?\s*>{1,2}\s*(\S+)\s*$`)
	heredocC = regexp.MustCompile(`^\s*tee\s+(?:-a\s+)?(\S+)\s*<<-?\s*['"]?(\w+)['"]?`)
	// Words people use when the agent got something wrong.
	correctionRe = regexp.MustCompile(`(?i)\b(?:still (?:not|isn'?t|doesn'?t|broken|failing|wrong|the same)|(?:it|this|that) (?:doesn'?t|didn'?t|does not|did not|isn'?t) work|not working|that'?s (?:wrong|not (?:it|right|what))|you (?:didn'?t|forgot|missed|broke)|wrong (?:file|one|answer|number))`)
	docExt       = map[string]bool{".md": true, ".mdx": true, ".txt": true, ".rst": true, ".doc": true, ".docx": true, ".pdf": true, ".rtf": true,
		".odt": true, ".pages": true, ".tex": true, ".adoc": true, ".pptx": true, ".key": true, ".xlsx": true, ".xls": true, ".csv": true}
	appNames = map[string]string{"claude in chrome": "Chrome", "gmail": "Gmail", "google calendar": "Google Calendar", "google drive": "Google Drive",
		"google slides": "Google Slides", "claude docs": "Claude Docs", "playwright": "Playwright browser", "slack": "Slack", "notion": "Notion",
		"cloudflare": "Cloudflare", "vercel": "Vercel", "github": "GitHub", "linear": "Linear", "supabase": "Supabase", "apollo io": "Apollo"}
)

// scan adds one session's events to w.
func (w *work) scan(evs []spec.Event) {
	for i := range evs {
		e := &evs[i]
		if !w.lastTS.IsZero() && e.TS.After(w.lastTS) {
			if gap := e.TS.Sub(w.lastTS); gap < 5*time.Minute {
				w.activeSec += int(gap.Seconds())
			}
		}
		if e.TS.After(w.lastTS) {
			w.lastTS = e.TS
		}
		var p payload
		_ = json.Unmarshal(e.Payload, &p)
		switch e.Kind {
		case spec.KindFileEdit:
			if w.keep(p.Path) {
				w.file(p.Path, p.Op)
				w.added += p.Added
				w.removed += p.Removed
			}
		case spec.KindCommand:
			w.pending[p.ToolCallID] = call{command: p.Command, desc: p.Description, gitOps: p.GitOps}
		case spec.KindToolCall:
			w.pending[p.ToolCallID] = call{tool: p.Tool, input: inputMap(p.Input)}
		case spec.KindToolResult:
			c, found := w.pending[p.ToolCallID]
			if !found {
				continue
			}
			delete(w.pending, p.ToolCallID)
			ok := p.OK == nil || *p.OK
			if c.command != "" || p.CallKind == spec.KindCommand {
				w.command(c, p, ok)
			} else {
				w.tool(c, ok)
			}
		case spec.KindCommit:
			if p.SHA != "" {
				w.commits[p.SHA] = true
			} else {
				w.commitsWithoutID++
			}
		case spec.KindPrompt:
			t := strings.TrimSpace(p.Text)
			switch {
			case strings.HasPrefix(t, "[Request interrupted"):
				w.interrupts++
			case strings.HasPrefix(e.Actor, "human:") && !strings.HasPrefix(t, "<") && len(t) < 600 && correctionRe.MatchString(t):
				flat := strings.Join(strings.Fields(t), " ")
				if c := around(flat, correctionRe.FindStringIndex(flat), 160); !contains(w.corrections, c) {
					w.corrections = append(w.corrections, c)
				}
			}
		}
	}
}

func (w *work) file(p, op string) {
	if op == "" {
		op = "edit"
	}
	if prev, ok := w.files[p]; ok && prev == "create" && op == "edit" {
		return // still a new file
	}
	w.files[p] = op
}

func (w *work) command(c call, p payload, ok bool) {
	first := strings.SplitN(c.command, "\n", 2)[0]
	if p.TestOutcome != nil {
		w.testRuns++
		t := *p.TestOutcome
		w.lastTest = &t
		if !t.passed() {
			w.testFails++
		}
	}
	if ok && deployCmd.MatchString(first) {
		w.deploys++
		if hosts := urlRe.FindAllString(p.Output, -1); len(hosts) > 0 {
			w.published[hostOf(hosts[len(hosts)-1])] = true
		}
	}
	if ok {
		for _, g := range c.gitOps {
			if g == "push" {
				w.pushes++
			}
		}
		w.heredocs(c.command)
	}
	label := orStr(c.desc, clip(first, 80))
	if !ok {
		if c.command == w.lastFailed {
			w.failStreak++
			if w.failStreak == 3 {
				w.stuck = append(w.stuck, label)
			}
		} else {
			w.lastFailed, w.failStreak = c.command, 1
		}
	} else {
		w.lastFailed, w.failStreak = "", 0
	}
}

// heredocs counts files a shell command wrote with a heredoc.
func (w *work) heredocs(cmd string) {
	lines := strings.Split(cmd, "\n")
	for i := 0; i < len(lines); i++ {
		var file, tag string
		if m := heredocA.FindStringSubmatch(lines[i]); m != nil {
			file, tag = m[1], m[2]
		} else if m := heredocB.FindStringSubmatch(lines[i]); m != nil {
			tag, file = m[1], m[2]
		} else if m := heredocC.FindStringSubmatch(lines[i]); m != nil {
			file, tag = m[1], m[2]
		} else {
			continue
		}
		file = strings.Trim(file, `'"`)
		n := 0
		for i++; i < len(lines) && strings.TrimSpace(lines[i]) != tag; i++ {
			n++
		}
		if file == "/dev/null" || !w.keep(file) {
			continue
		}
		if _, seen := w.files[file]; !seen {
			w.files[file] = "create"
		}
		w.added += n
	}
}

func (w *work) tool(c call, ok bool) {
	server, action := splitTool(c.tool)
	if app := appName(server, c.tool); app != "" {
		w.apps[app]++
	}
	s, a := strings.ToLower(server), strings.ToLower(action)
	switch {
	case has(s, "gmail", "outlook", "mail") || has(a, "email"):
		switch {
		case has(a, "draft"):
			if ok {
				w.drafts++
			}
		case has(a, "send", "reply", "forward"):
			if !ok {
				w.emailsFailed++
				break
			}
			w.emailsSent++
			for _, r := range recipients(c.input) {
				w.recipients[r] = true
			}
		}
	case has(s, "slack", "teams", "discord", "telegram", "whatsapp", "imessage"):
		if ok && has(a, "send", "post", "reply", "message") {
			w.messages++
		}
	case has(s, "calendar"):
		if ok && has(a, "create", "insert", "update", "add") {
			w.calendar++
		}
	case has(s, "docs", "drive", "notion", "confluence", "sheets", "slides"):
		if ok && has(a, "create", "write", "update", "append", "insert", "batch", "copy") {
			w.onlineDocs++
		}
	case has(s, "chrome", "playwright", "browser", "puppeteer"):
		w.browserSteps++
		if has(a, "navigate", "goto", "open") {
			if h := hostOf(str(c.input, "url")); h != "" {
				w.sites[h] = true
			}
		}
	case c.tool == "WebFetch" || a == "web_fetch" || a == "fetch":
		w.pagesRead++
		if h := hostOf(str(c.input, "url")); h != "" {
			w.sites[h] = true
		}
	case c.tool == "WebSearch" || a == "web_search":
		w.searches++
	case server != "" && ok && deployTool.MatchString(action):
		w.deploys++
	}
}

// Summarize sums up the sessions active since the given time (zero: all).
func Summarize(st *store.Store, since time.Time) (*Summary, error) {
	rows, err := st.ListSessions(store.SessionFilter{Since: since, IncludeKids: true})
	if err != nil {
		return nil, err
	}
	kids := map[string][]string{}
	var tops []*store.SessionRow
	for _, r := range rows {
		if r.EventCount == 0 || (r.Skipped && r.SkipReason == "internal") || r.SkipReason == "excluded_repo" {
			continue
		}
		if r.ParentSessionID != "" {
			kids[r.ParentSessionID] = append(kids[r.ParentSessionID], r.ID)
			continue
		}
		tops = append(tops, r)
	}
	sum := &Summary{Since: since, Tiles: []Tile{}, Apps: []AppUse{}, Well: []Note{}, Look: []Note{}, Requests: []Request{}}
	type sess struct {
		row          *store.SessionRow
		w            *work
		tally        map[string]int
		contradicted []Detail
	}
	var ss []sess
	all := newWork()
	tally := map[string]int{}
	for _, r := range tops {
		w := newWork()
		w.cwd = r.Cwd
		for _, id := range append([]string{r.ID}, kids[r.ID]...) {
			evs, err := st.Events(id, 0, 0)
			if err != nil {
				return nil, err
			}
			w.lastTS = time.Time{}
			w.scan(evs)
		}
		s := sess{row: r, w: w, tally: map[string]int{}}
		if cs, err := st.SessionClaims(r.ID); err == nil {
			s.tally = Item(r, cs).Tally
		}
		if s.tally[VerdictContradicted] > 0 {
			if rep, err := BuildReport(st, r.ID); err == nil {
				for _, sec := range rep.Sections {
					for _, c := range sec.Claims {
						if c.Verdict == VerdictContradicted {
							s.contradicted = append(s.contradicted, Detail{Text: elapsedRe.ReplaceAllString(c.Reason, ""), Said: clip(c.Claim.Statement, 160), Session: r.ID})
						}
					}
				}
			}
		}
		for k, n := range s.tally {
			tally[k] += n
		}
		all.merge(w)
		sum.Sessions++
		sum.ActiveSec += w.activeSec
		sum.CostUSD += r.CostUSD
		ss = append(ss, s)
	}

	// Headline numbers.
	created, edited, deleted := 0, 0, 0
	var docs []string
	for p, op := range all.files {
		switch op {
		case "create":
			created++
		case "delete":
			deleted++
		default:
			edited++
		}
		if op != "delete" && isDoc(p) {
			docs = append(docs, path.Base(p))
		}
	}
	docs = uniqStrings(docs)
	sort.Strings(docs)
	commits := len(all.commits) + all.commitsWithoutID
	tile := func(key, label string, v int, detail string, items ...string) {
		if v > 0 {
			sum.Tiles = append(sum.Tiles, Tile{Key: key, Label: label, Value: v, Detail: detail, Items: items})
		}
	}
	tile("emails", "Emails sent", all.emailsSent, joinParts(
		countIf(len(all.recipients), "to %d people", "to 1 person"), countIf(all.emailsFailed, "%d failed", "1 failed"), countIf(all.drafts, "%d drafts", "1 draft")))
	tile("files", "Files created or changed", created+edited, joinParts(countIf(created, "%d new", "1 new"), countIf(edited, "%d changed", "1 changed"), countIf(deleted, "%d deleted", "1 deleted")))
	tile("lines", "Lines written", all.added, countIf(all.removed, "%d removed", "1 removed"))
	tile("docs", "Documents written", len(docs)+all.onlineDocs, countIf(all.onlineDocs, "%d online", "1 online"), firstN(docs, 8)...)
	pubs := sortedKeys(all.published)
	if len(pubs) > 0 {
		label := "Sites published"
		if len(pubs) == 1 {
			label = "Site published"
		}
		tile("published", label, len(pubs), countIf(all.deploys, "deployed %d times", "deployed once"), pubs...)
	} else {
		tile("published", "Deploys", all.deploys, "")
	}
	tile("tests", "Test runs", all.testRuns, joinParts(countIf(all.testRuns-all.testFails, "%d passed", "1 passed"), countIf(all.testFails, "%d failed", "1 failed")))
	tile("commits", "Commits", commits, countIf(all.pushes, "pushed %d times", "pushed once"))
	tile("messages", "Messages posted", all.messages, "")
	tile("calendar", "Calendar events", all.calendar, "")
	tile("web", "Websites visited", len(all.sites), joinParts(countIf(all.pagesRead, "%d pages read", "1 page read"),
		countIf(all.searches, "%d searches", "1 search"), countIf(all.browserSteps, "%d browser steps", "1 browser step")))
	for name, n := range all.apps {
		sum.Apps = append(sum.Apps, AppUse{Name: name, Actions: n})
	}
	sort.Slice(sum.Apps, func(i, j int) bool {
		if sum.Apps[i].Actions != sum.Apps[j].Actions {
			return sum.Apps[i].Actions > sum.Apps[j].Actions
		}
		return sum.Apps[i].Name < sum.Apps[j].Name
	})

	refs := func(f func(s sess) bool) []Ref {
		var out []Ref
		for _, s := range ss {
			if f(s) {
				out = append(out, Ref{ID: s.row.ID, Title: titleOf(s.row)})
			}
		}
		return out
	}

	// Went well.
	well := func(text string, rs []Ref) { sum.Well = append(sum.Well, Note{Text: text, Sessions: rs}) }
	if all.emailsSent > 0 {
		well("Sent "+plural(all.emailsSent, "email")+orStr(countIf(len(all.recipients), " to %d people", " to 1 person"), ""),
			refs(func(s sess) bool { return s.w.emailsSent > 0 }))
	}
	if len(pubs) > 0 {
		well("Published "+listWords(pubs, 3), refs(func(s sess) bool { return len(s.w.published) > 0 }))
	} else if all.deploys > 0 {
		well("Published work "+times(all.deploys), refs(func(s sess) bool { return s.w.deploys > 0 }))
	}
	if created+edited > 0 {
		t := "Created " + plural(created, "file")
		if created == 0 {
			t = "Changed " + plural(edited, "file")
		} else if edited > 0 {
			t += " and changed " + fmt.Sprint(edited)
		}
		if all.added > 0 {
			t += fmt.Sprintf(" (%s lines written)", thousands(all.added))
		}
		well(t, refs(func(s sess) bool { return len(s.w.files) > 0 }))
	}
	if len(docs)+all.onlineDocs > 0 {
		t := "Wrote " + plural(len(docs)+all.onlineDocs, "document")
		if len(docs) > 0 {
			t += ": " + listWords(docs, 3)
		}
		well(t, refs(func(s sess) bool { return s.w.onlineDocs > 0 || hasDoc(s.w.files) }))
	}
	if all.messages > 0 {
		well("Posted "+plural(all.messages, "message"), refs(func(s sess) bool { return s.w.messages > 0 }))
	}
	if all.calendar > 0 {
		well("Added or updated "+plural(all.calendar, "calendar event"), refs(func(s sess) bool { return s.w.calendar > 0 }))
	}
	tested, passing := 0, 0
	for _, s := range ss {
		if s.w.lastTest != nil {
			tested++
			if s.w.lastTest.passed() {
				passing++
			}
		}
	}
	if passing > 0 {
		t := "Tests passing at the end of " + plural(passing, "session")
		if tested > passing {
			t = fmt.Sprintf("Tests passing at the end of %d of %d sessions that ran them", passing, tested)
		}
		well(t, refs(func(s sess) bool { return s.w.lastTest != nil && s.w.lastTest.passed() }))
	}
	if commits > 0 {
		t := "Saved the work in " + plural(commits, "commit")
		if all.pushes > 0 {
			t += " and pushed it " + times(all.pushes)
		}
		well(t, refs(func(s sess) bool { return len(s.w.commits)+s.w.commitsWithoutID > 0 }))
	}
	if all.pagesRead+all.searches > 0 {
		t := "Researched the web: " + joinWords(countIf(all.pagesRead, "%d pages read", "1 page read"), countIf(all.searches, "%d searches", "1 search"))
		well(t, refs(func(s sess) bool { return s.w.pagesRead+s.w.searches > 0 }))
	}
	if n := tally[VerdictBacked]; n > 0 {
		checked := tally[VerdictBacked] + tally[VerdictContradicted] + tally[VerdictUnchecked]
		well(fmt.Sprintf("Backed %d of its %d claims with evidence", n, checked), nil)
	}

	// Needs a look.
	look := func(n Note) { sum.Look = append(sum.Look, n) }
	if all.emailsFailed > 0 {
		look(Note{Text: plural(all.emailsFailed, "email") + " failed to send", Sessions: refs(func(s sess) bool { return s.w.emailsFailed > 0 })})
	}
	var failing []Detail
	for _, s := range ss {
		if t := s.w.lastTest; t != nil && !t.passed() {
			d := "The last test run failed."
			if t.Passed+t.Failed > 0 {
				d = fmt.Sprintf("%d of %d tests passed in the last run.", t.Passed, t.Passed+t.Failed)
			}
			failing = append(failing, Detail{Text: d, Session: s.row.ID})
		}
	}
	if len(failing) > 0 {
		look(Note{Text: "Tests were failing when " + plural(len(failing), "session") + " ended", Details: failing})
	}
	var wrong []Detail
	for _, s := range ss {
		wrong = append(wrong, s.contradicted...)
	}
	if len(wrong) > 0 {
		what := "thing"
		look(Note{Text: fmt.Sprintf("Said %s %s done that the record contradicts", plural(len(wrong), what), wasWere(len(wrong))), Details: wrong})
	}
	var stuck []Detail
	for _, s := range ss {
		for _, step := range s.w.stuck {
			stuck = append(stuck, Detail{Text: "“" + step + "” failed 3 or more times in a row.", Session: s.row.ID})
		}
	}
	if len(stuck) > 0 {
		look(Note{Text: "Got stuck repeating a failing step " + times(len(stuck)), Details: stuck})
	}
	if all.interrupts > 0 {
		look(Note{Text: "You stopped it mid-task " + times(all.interrupts), Sessions: refs(func(s sess) bool { return s.w.interrupts > 0 })})
	}
	var fixes []Detail
	for _, s := range ss {
		for _, c := range s.w.corrections {
			fixes = append(fixes, Detail{Text: "“" + c + "”", Session: s.row.ID})
		}
	}
	if len(fixes) > 0 {
		look(Note{Text: "You had to correct it " + times(len(fixes)), Details: fixes})
	}

	// What was asked for, and what came of it.
	flagged := map[string]bool{}
	for _, n := range sum.Look {
		for _, r := range n.Sessions {
			flagged[r.ID] = true
		}
		for _, d := range n.Details {
			flagged[d.Session] = true
		}
	}
	for _, s := range ss {
		sum.Requests = append(sum.Requests, Request{SessionID: s.row.ID, Title: titleOf(s.row), When: s.row.LastActivity,
			ActiveSec: s.w.activeSec, Facts: s.w.facts(), Look: flagged[s.row.ID]})
	}
	return sum, nil
}

// facts are the few things worth saying about one session's results.
func (w *work) facts() []string {
	out := []string{}
	if w.emailsSent > 0 {
		out = append(out, "sent "+plural(w.emailsSent, "email"))
	}
	if pubs := sortedKeys(w.published); len(pubs) > 0 {
		out = append(out, "published "+listWords(pubs, 2))
	} else if w.deploys > 0 {
		out = append(out, "published "+times(w.deploys))
	}
	created, edited := 0, 0
	for _, op := range w.files {
		switch op {
		case "create":
			created++
		case "edit", "move":
			edited++
		}
	}
	if created+edited > 0 {
		out = append(out, joinWords(countIf(created, "%d new files", "1 new file"), countIf(edited, "%d changed", "1 changed")))
	}
	if w.drafts > 0 {
		out = append(out, "drafted "+plural(w.drafts, "email"))
	}
	if w.messages > 0 {
		out = append(out, "posted "+plural(w.messages, "message"))
	}
	if t := w.lastTest; t != nil {
		if t.passed() {
			out = append(out, "tests passing")
		} else if t.Passed+t.Failed > 0 {
			out = append(out, fmt.Sprintf("tests failing (%d of %d passed)", t.Passed, t.Passed+t.Failed))
		} else {
			out = append(out, "tests failing")
		}
	}
	if n := len(w.commits) + w.commitsWithoutID; n > 0 {
		out = append(out, plural(n, "commit"))
	}
	if len(out) < 4 {
		sites := sortedKeys(w.sites)
		switch {
		case w.browserSteps > 0 && len(sites) > 0 && len(sites) <= 2:
			out = append(out, plural(w.browserSteps, "browser step")+" on "+joinWords(sites...))
		case w.browserSteps > 0:
			out = append(out, plural(w.browserSteps, "browser step"))
		case len(sites) > 0:
			out = append(out, "read "+plural(len(sites), "website"))
		case w.searches > 0:
			out = append(out, "searched the web "+times(w.searches))
		}
	}
	if len(out) > 4 {
		out = out[:4]
	}
	return out
}

func (w *work) merge(o *work) {
	for p, op := range o.files {
		w.file(p, op)
	}
	for r := range o.recipients {
		w.recipients[r] = true
	}
	for s := range o.sites {
		w.sites[s] = true
	}
	for h := range o.published {
		w.published[h] = true
	}
	for c := range o.commits {
		w.commits[c] = true
	}
	for a, n := range o.apps {
		w.apps[a] += n
	}
	w.added += o.added
	w.removed += o.removed
	w.emailsSent += o.emailsSent
	w.emailsFailed += o.emailsFailed
	w.drafts += o.drafts
	w.messages += o.messages
	w.calendar += o.calendar
	w.onlineDocs += o.onlineDocs
	w.browserSteps += o.browserSteps
	w.pagesRead += o.pagesRead
	w.searches += o.searches
	w.testRuns += o.testRuns
	w.testFails += o.testFails
	w.deploys += o.deploys
	w.pushes += o.pushes
	w.interrupts += o.interrupts
	w.commitsWithoutID += o.commitsWithoutID
}

// keep drops agent bookkeeping (memory, settings) and scratch files
// outside the session's working folder.
func (w *work) keep(p string) bool {
	if p == "" {
		return false
	}
	skips := []string{"/.claude/", "/.codex/", "/.credence/", "/.cursor/", "/node_modules/"}
	if w.cwd == "" || !strings.HasPrefix(p, strings.TrimSuffix(w.cwd, "/")+"/") {
		skips = append(skips, "/tmp/", "/private/var/folders/")
	}
	for _, skip := range skips {
		if strings.Contains(p, skip) || strings.HasPrefix(p, strings.TrimPrefix(skip, "/")) {
			return false
		}
	}
	return true
}

// isDoc reports whether a file is a document a person would read; agent
// instructions and memory files are bookkeeping.
func isDoc(p string) bool {
	switch strings.ToUpper(path.Base(p)) {
	case "CLAUDE.MD", "AGENTS.MD", "GEMINI.MD", "MEMORY.MD":
		return false
	}
	return docExt[strings.ToLower(path.Ext(p))] && !strings.Contains(p, "/memory/")
}

func hasDoc(files map[string]string) bool {
	for p, op := range files {
		if op != "delete" && isDoc(p) {
			return true
		}
	}
	return false
}

// splitTool splits "mcp__claude_ai_Gmail__send_message" into its server
// and action; built-in tools have no server.
func splitTool(name string) (server, action string) {
	if !strings.HasPrefix(name, "mcp__") {
		return "", name
	}
	parts := strings.Split(strings.TrimPrefix(name, "mcp__"), "__")
	if len(parts) == 1 {
		return parts[0], ""
	}
	return parts[0], parts[len(parts)-1]
}

// appName names an app for people: "plugin_cloudflare_cloudflare" is
// "Cloudflare", "claude-in-chrome" is "Chrome".
func appName(server, tool string) string {
	if server == "" {
		if tool == "WebFetch" || tool == "WebSearch" {
			return "Web"
		}
		return ""
	}
	s := strings.TrimPrefix(strings.TrimPrefix(server, "claude_ai_"), "plugin_")
	words := strings.FieldsFunc(s, func(r rune) bool { return r == '_' || r == '-' || r == '.' })
	var out []string
	for i, w := range words {
		if i > 0 && strings.EqualFold(w, words[i-1]) {
			continue
		}
		out = append(out, w)
	}
	key := strings.ToLower(strings.Join(out, " "))
	if key == "credence" || key == "" {
		return ""
	}
	if n, ok := appNames[key]; ok {
		return n
	}
	for i, w := range out {
		if w == strings.ToLower(w) {
			out[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(out, " ")
}

func inputMap(raw json.RawMessage) map[string]any {
	var m map[string]any
	if json.Unmarshal(raw, &m) == nil {
		return m
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		_ = json.Unmarshal([]byte(s), &m)
	}
	return m
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

// recipients lists the addresses an email went to.
func recipients(in map[string]any) []string {
	var out []string
	add := func(s string) {
		for _, a := range strings.Split(s, ",") {
			// Stored addresses are redacted placeholders; each still names one person.
			if a = strings.ToLower(strings.TrimSpace(a)); strings.Contains(a, "@") || strings.Contains(a, ":email:") {
				out = append(out, a)
			}
		}
	}
	switch v := in["to"].(type) {
	case string:
		add(v)
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok {
				add(s)
			}
		}
	}
	return out
}

func hostOf(u string) string {
	pu, err := url.Parse(strings.TrimSpace(u))
	if err != nil || pu.Hostname() == "" {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(pu.Hostname()), "www.")
}

func has(s string, subs ...string) bool {
	for _, x := range subs {
		if strings.Contains(s, x) {
			return true
		}
	}
	return false
}

func titleOf(r *store.SessionRow) string { return orStr(clip(r.Title, 70), "Untitled session") }

func countIf(n int, many, one string) string {
	switch {
	case n <= 0:
		return ""
	case n == 1:
		return one
	}
	return fmt.Sprintf(many, n)
}

func joinParts(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " · ")
}

func joinWords(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	switch len(out) {
	case 0:
		return ""
	case 1:
		return out[0]
	}
	return strings.Join(out[:len(out)-1], ", ") + " and " + out[len(out)-1]
}

// listWords names up to n items: "a, b and 3 more".
func listWords(xs []string, n int) string {
	if len(xs) <= n {
		return joinWords(xs...)
	}
	return strings.Join(xs[:n], ", ") + fmt.Sprintf(" and %d more", len(xs)-n)
}

func firstN(xs []string, n int) []string {
	if len(xs) > n {
		return xs[:n]
	}
	return xs
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func wasWere(n int) string {
	if n == 1 {
		return "was"
	}
	return "were"
}

func thousands(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// around clips s to n characters, keeping the matched part in view.
func around(s string, m []int, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n || m == nil {
		return clip(s, n)
	}
	end := len([]rune(s[:min(m[1], len(s))])) + 40
	if end < n {
		return string(r[:n]) + "…"
	}
	end = min(end, len(r))
	start := max(0, end-n)
	out := string(r[start:end])
	if start > 0 {
		out = "…" + out
	}
	if end < len(r) {
		out += "…"
	}
	return out
}
