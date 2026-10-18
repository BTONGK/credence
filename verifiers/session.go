package verifiers

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/BTONGK/credence/daemon/claims"
	"github.com/BTONGK/credence/spec"
)

// Session is tier 1 for what an agent said in its replies: each statement
// is checked against the session's own record, which the harness wrote,
// not the agent. A failed test run, or a published link no tool ever
// returned, contradicts the statement; nothing relevant in the record
// leaves it unchecked.
type Session struct{}

func (Session) Name() string { return "session" }
func (Session) Tier() int    { return 1 }

func (Session) Verify(ctx context.Context, vc *Context, c *spec.Claim) []spec.Verdict {
	var out []spec.Verdict
	for _, a := range c.Assertions {
		start := time.Now()
		var rs []sessionResult
		switch a.Check() {
		case "session_edits":
			rs = checkEdits(vc.Data, a, c.Statement)
		case "session_run":
			rs = []sessionResult{checkRun(vc.Data, a)}
		case "session_link":
			rs = []sessionResult{checkLink(vc.Data, a)}
		default:
			continue
		}
		for _, r := range rs {
			out = append(out, verdict(vc, c, "session", 1, a.Check(), r.result, r.outcome, r.details, a, start))
		}
	}
	return out
}

type sessionResult struct{ result, outcome, details string }

func passed(outcome, format string, args ...any) sessionResult {
	return sessionResult{spec.ResultPass, outcome, fmt.Sprintf(format, args...)}
}

func failed(outcome, format string, args ...any) sessionResult {
	return sessionResult{spec.ResultFail, outcome, fmt.Sprintf(format, args...)}
}

func unknown(outcome, format string, args ...any) sessionResult {
	return sessionResult{spec.ResultInconclusive, outcome, fmt.Sprintf(format, args...)}
}

// step is one tool call in the record and how it ended.
type step struct {
	call  *spec.Event
	res   *spec.Event
	ok    bool
	label string
}

func stepOf(d *claims.Data, e *spec.Event) step {
	s := step{call: e, res: d.Result(e), ok: true}
	if s.res != nil {
		p := claims.Payload(s.res)
		s.ok, _ = p["ok"].(bool)
		if to, ok := p["test_outcome"].(map[string]any); ok {
			st, _ := to["status"].(string)
			s.ok = st == "pass"
		}
	}
	switch e.Kind {
	case spec.KindCommand:
		s.label = "`" + cmdLabel(claims.PayloadStr(e, "command")) + "`"
	case spec.KindCommit:
		s.label = "commit " + short(claims.PayloadStr(e, "sha"))
	default:
		s.label = toolLabel(claims.PayloadStr(e, "tool"))
	}
	return s
}

// An assignment from a command substitution, IP=$(dig …), is a command and
// stays.
var cdPrefixRe = regexp.MustCompile(`^(?:(?:cd\s+(?:"[^"]*"|'[^']*'|\S+)|[A-Za-z_][A-Za-z0-9_]*=(?:"[^"$]*"|'[^']*'|\$\{?\w+\}?[^\s;&]*|[^\s$` + "`" + `(;&][^\s;&]*|\s))\s*(?:&&|;)?\s*)+`)

// head is the command a shell line runs, without a leading cd or variable
// assignments and without
// the body of a heredoc, so a script that mentions "wrangler deploy" in a
// string isn't mistaken for a deploy.
func head(cmd string) string {
	line := strings.TrimSpace(cmd)
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	return cdPrefixRe.ReplaceAllString(line, "")
}

func cmdLabel(cmd string) string {
	h := head(cmd)
	if len(h) > 60 {
		h = strings.TrimSpace(h[:57]) + "…"
	} else if strings.Contains(strings.TrimSpace(cmd), "\n") {
		h += " …"
	}
	return h
}

// toolLabel names a tool for people: "mcp__cloudflare__workers_deploy" is
// "the cloudflare workers_deploy tool".
func toolLabel(name string) string {
	if strings.HasPrefix(name, "mcp__") {
		parts := strings.Split(strings.TrimPrefix(name, "mcp__"), "__")
		server := strings.TrimPrefix(strings.TrimPrefix(parts[0], "claude_ai_"), "plugin_")
		// plugin_cloudflare_cloudflare is the cloudflare plugin's cloudflare server.
		var words []string
		for _, w := range strings.Split(server, "_") {
			if len(words) == 0 || !strings.EqualFold(words[len(words)-1], w) {
				words = append(words, w)
			}
		}
		server = strings.Join(words, " ")
		if len(parts) > 1 {
			return "the " + server + " " + strings.ReplaceAll(parts[len(parts)-1], "_", " ") + " tool"
		}
		return "the " + server + " tool"
	}
	if name == "" {
		return "a tool"
	}
	return "the " + name + " tool"
}

func span(a spec.Assertion) (int, int) { return a.Int("from", 1), a.Int("to", 1<<30) }

// at says where in the session an event happened, as the timeline shows it.
func at(d *claims.Data, e *spec.Event) string {
	if e == nil || d.Session.StartedAt.IsZero() || e.TS.Before(d.Session.StartedAt) {
		return ""
	}
	t := int(e.TS.Sub(d.Session.StartedAt).Seconds())
	if t >= 3600 {
		return fmt.Sprintf(" at %d:%02d:%02d", t/3600, t/60%60, t%60)
	}
	return fmt.Sprintf(" at %d:%02d", t/60, t%60)
}

var (
	shellWriteRe = regexp.MustCompile(`(?:^|[^>2&])>{1,2}\s*[^\s&|>]|\bsed\s+-i\b|\btee\b|\bperl\s+-pi\b|\bcp\s|\bmv\s|\bpatch\b|\bgit\s+apply\b|open\([^)]*['"]w`)
	testCmdRe    = regexp.MustCompile(`\b(?:pytest|unittest|jest|vitest|mocha|ava|go\s+test|cargo\s+test|(?:npm|pnpm|yarn|bun)\s+(?:run\s+)?test|rspec|phpunit|tox|nox|make\s+test|deno\s+test|playwright\s+test|swift\s+test|mvn\s+test|gradle\w*\s+test)\b`)
	buildCmdRe   = regexp.MustCompile(`\b(?:build|tsc|typecheck|type-check|lint|eslint|vet|compile|cargo\s+check|make)\b`)
	deployCmdRe  = regexp.MustCompile(`\b(?:wrangler\s+(?:pages\s+)?deploy|vercel|netlify\s+deploy|fly\s+deploy|firebase\s+deploy|gh-pages|git\s+push|npm\s+publish|docker\s+push|kubectl\s+apply|terraform\s+apply|gcloud\s+\S+\s+deploy|aws\s+s3\s+(?:sync|cp)|rsync|scp)\b`)
	fetchCmdRe   = regexp.MustCompile(`(?:^|[;&|(]\s*|\bdo\s+)(?:curl|wget|http|https|dig|nslookup|host|whois|openssl\s+s_client|gh\s+api|gh\s+\w+\s+view)\b`)
	scriptRe     = regexp.MustCompile(`^(?:python3?|node|ruby|perl|bash|sh|deno|bun)\b`)
	bodyFetchRe  = regexp.MustCompile(`urlopen|urllib\.request|requests\.(?:get|post)|httpx\.|fetch\(|http\.client|curl `)
	// Harness bookkeeping tools are not evidence of anything.
	harnessTool = regexp.MustCompile(`^(?:ToolSearch|TodoWrite|TodoRead|Task\w*|AskUserQuestion|ExitPlanMode|EnterPlanMode|Skill|SendMessage|ScheduleWakeup|Monitor|ListMcpResourcesTool|ReadMcpResourceTool)$`)
	deployTool  = regexp.MustCompile(`(?i)artifact|deploy|publish|upload|release`)
	lookTool    = regexp.MustCompile(`(?i)get|list|read|search|fetch|query|find|describe|lookup|view|navigate|browse|screenshot|snapshot|page|computer|batch|evaluate`)
	runTool     = regexp.MustCompile(`(?i)screenshot|navigate|snapshot|read_page|get_page|computer|evaluate|batch`)
	localOpenRe = regexp.MustCompile(`(?i)^(?:curl|wget|open)\b|navigate|screenshot|snapshot`)
	serverRe    = regexp.MustCompile(`\b(?:npm|pnpm|yarn|node|npx|python3?|uvicorn|gunicorn|flask|vite|next|serve|go\s+run|deno|bun|php|ruby|rails)\b`)
	// Commands that only look around say nothing about whether the work runs.
	lookCmdRe = regexp.MustCompile(`^(?:ls|cat|head|tail|less|grep|rg|find|echo|pwd|which|type|wc|sed\s+-n|awk|sort|uniq|` +
		`git\s+(?:status|diff|log|show|branch|remote|add)|kill|pkill|lsof|ps|sleep|open|mkdir|rm|cp|mv|touch|chmod|export|true|date|whoami|env|tree|du|df|stat|file|jq)\b`)
)

// edits are the files changed between from and to, by edit tools or by a
// subagent working in that span.
func edits(d *claims.Data, from, to int) []string {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	subagents := 0
	for i := range d.Events {
		e := &d.Events[i]
		if e.Seq < from || e.Seq > to {
			continue
		}
		switch e.Kind {
		case spec.KindFileEdit:
			if d.EditOK(e) {
				add(claims.PayloadStr(e, "path"))
			}
		case spec.KindSubagentStart:
			subagents++
		}
	}
	if subagents > 0 {
		for _, evs := range d.ChildEvents {
			for i := range evs {
				if evs[i].Kind == spec.KindFileEdit {
					add(claims.PayloadStr(&evs[i], "path"))
				}
			}
		}
	}
	return out
}

func checkEdits(d *claims.Data, a spec.Assertion, statement string) []sessionResult {
	from, to := span(a)
	one := func(r sessionResult) []sessionResult { return []sessionResult{r} }
	if named := a.Strs("paths"); len(named) > 0 {
		// A named file counts if it was changed anywhere before the reply.
		all := edits(d, 1, to)
		var hit, miss []string
		for _, p := range named {
			if pathChanged(d, p, all, to) {
				hit = append(hit, p)
			} else {
				miss = append(miss, p)
			}
		}
		switch {
		case len(hit) == 0:
			return one(failed("path_not_changed", "The session left %s unchanged.", joinNames(miss)))
		case len(miss) > 0:
			return one(unknown("some_paths", "%s %s changed; %s stayed the same.", joinNames(hit), wasWere(len(hit)), joinNames(miss)))
		}
		return one(passed("paths_changed", "%s %s changed in the session.", joinNames(hit), wasWere(len(hit))))
	}

	// Without a file name, the change has to match what the sentence is
	// about: "I added the domain to Cloudflare" is not backed by a notes
	// file written in the same turn.
	topic := topicWords(statement)
	w := work(d, from, to)
	if len(topic) == 0 {
		switch {
		case len(w.changed) > 0:
			return one(passed("edited", "%s changed here: %s.", countNoun(len(w.changed), "file was", "files were"), joinNames(w.changed)))
		case len(w.writes) > 0:
			return one(passed("shell_writes", "Files were written by %s (%s).", countNoun(len(w.writes), "shell command", "shell commands"), last(w.writes).label))
		}
	} else {
		if hit := matching(topic, w.changed); len(hit) > 0 {
			return one(passed("edited", "%s changed here: %s.", countNoun(len(hit), "file was", "files were"), joinNames(hit)))
		}
		if hit := matchingSteps(topic, w.writes); len(hit) > 0 {
			return one(passed("shell_writes", "Files were written by %s (%s).", countNoun(len(hit), "shell command", "shell commands"), last(hit).label))
		}
		if hit := matchingSteps(topic, w.actions); len(hit) > 0 {
			return one(unknown("other_tools", "The work went through %s.", last(hit).label))
		}
		// Work happened, but nothing names what the sentence is about. The
		// "unmatched" outcome tells readers it is partial support.
		switch {
		case len(w.changed) > 0:
			return one(unknown("unmatched", "%s changed here (%s). A quick look will match %s to this claim.",
				countNoun(len(w.changed), "file was", "files were"), joinNames(w.changed), pronoun(len(w.changed))))
		case len(w.writes) > 0:
			return one(unknown("unmatched", "Files were written by %s (%s). A quick look will confirm they match this claim.",
				countNoun(len(w.writes), "shell command", "shell commands"), last(w.writes).label))
		}
	}
	if len(w.actions) > 0 {
		return one(unknown("other_tools", "The work went through %s (last: %s).",
			countNoun(len(w.actions), "other step", "other steps"), last(w.actions).label))
	}
	// Nothing since the human spoke: the reply may be about earlier work.
	if from > 1 {
		before := work(d, 1, from-1)
		if e := before.lastChange(); e != nil {
			return one(unknown("earlier_change", "This most likely refers to the change made earlier%s (%s).",
				at(d, e.call), e.label))
		}
	}
	if a["by_agent"] == true {
		if len(w.looks) > 0 {
			return one(failed("no_changes", "Up to this point the agent only read and looked at things (last: %s).", last(w.looks).label))
		}
		return one(failed("no_changes", "The session record shows zero edits, commands or tool calls up to this point."))
	}
	return one(unknown("no_changes", "This describes a state outside the session record."))
}

// spanWork sorts the steps in a span by what they could have changed.
type spanWork struct {
	changed []string // files changed by edit tools
	edits   []step
	writes  []step // shell commands that write files
	actions []step // other commands and tools that act on something
	looks   []step // commands and tools that only look
}

func work(d *claims.Data, from, to int) spanWork {
	w := spanWork{changed: edits(d, from, to)}
	for i := range d.Events {
		e := &d.Events[i]
		if e.Seq < from || e.Seq > to {
			continue
		}
		if e.Kind == spec.KindFileEdit && d.EditOK(e) {
			w.edits = append(w.edits, stepOf(d, e))
			continue
		}
		if e.Kind != spec.KindCommand && e.Kind != spec.KindToolCall && e.Kind != spec.KindFileRead && e.Kind != spec.KindSearch {
			continue
		}
		s := stepOf(d, e)
		if !s.ok {
			continue
		}
		cmd := claims.PayloadStr(e, "command")
		tool := claims.PayloadStr(e, "tool")
		switch {
		case e.Kind == spec.KindCommand && shellWriteRe.MatchString(devNullRe.ReplaceAllString(cmd, "")):
			w.writes = append(w.writes, s)
		case e.Kind == spec.KindCommand && !lookCmdRe.MatchString(head(cmd)) && !fetches(cmd):
			w.actions = append(w.actions, s)
		case e.Kind == spec.KindToolCall && claims.PayloadStr(e, "category") == "" && !lookTool.MatchString(tool) && !harnessTool.MatchString(tool):
			w.actions = append(w.actions, s)
		case e.Kind == spec.KindToolCall && harnessTool.MatchString(tool):
		default:
			w.looks = append(w.looks, s)
		}
	}
	return w
}

// lastChange is the latest step in the span that changed something.
func (w spanWork) lastChange() *step {
	var best *step
	for _, ss := range [][]step{w.edits, w.writes, w.actions} {
		for i := range ss {
			if best == nil || ss[i].call.Seq > best.call.Seq {
				best = &ss[i]
			}
		}
	}
	if best != nil && best.call.Kind == spec.KindFileEdit {
		best.label = filepath.Base(claims.PayloadStr(best.call, "path"))
	}
	return best
}

func last(ss []step) step { return ss[len(ss)-1] }

var (
	devNullRe = regexp.MustCompile(`[0-9&]?>{1,2}\s*/dev/null`)
	wordRe    = regexp.MustCompile(`[A-Za-z][A-Za-z0-9]*`)
	camelRe   = regexp.MustCompile(`([a-z0-9])([A-Z])`)
	pathSepRe = regexp.MustCompile(`[/.\s]+`)
)

// genericWords say that something changed without saying what.
var genericWords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`a an the and or but so of to in on at for with by from into onto as is are was were be been
		it its it's this that these those them they their there here then than now just also still again already only all each every
		both any some more most other same new old one two three your you yours my our we i me he she his her him what which who
		whom whose how when where why can could will would should may might must do does did done has have had having not no yes
		up down out off over under back about after before because while through via per
		fix fixed fixes fixing update updated updates updating change changed changes changing add added adds adding make made
		makes making build built builds building write wrote written writes writing create created creates creating remove removed
		removes removing delete deleted deletes deleting move moved moves moving rename renamed set sets put puts save saved saves
		replace replaced replaces rewrite rewrote rewritten edit edited edits implement implemented refactor refactored clean
		cleaned cleanup tidy tidied tweak tweaked adjust adjusted wire wired hook hooked swap swapped switch switched turn turned
		bug bugs issue issues problem problems error errors thing things stuff everything something anything code file files
		change work worked working version versions place places line lines part parts way bit few lot lots com org net io dev
		properly correctly now fully completely right correct working`) {
		genericWords[w] = true
	}
}

// topicWords are the content words of a claim, stemmed.
func topicWords(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, w := range wordRe.FindAllString(stripURLs(s), -1) {
		lw := strings.ToLower(w)
		if len(lw) < 3 || genericWords[lw] {
			continue
		}
		if st := stem(lw); !seen[st] {
			seen[st] = true
			out = append(out, st)
		}
	}
	return out
}

var urlRe = regexp.MustCompile(`https?://(\S+)`)

// stripURLs keeps a link's host and path words but drops the scheme.
func stripURLs(s string) string {
	return urlRe.ReplaceAllString(s, " $1 ")
}

func stem(w string) string {
	for _, suf := range []string{"ing", "ed", "es", "s"} {
		if strings.HasSuffix(w, suf) && len(w)-len(suf) >= 4 {
			return w[:len(w)-len(suf)]
		}
	}
	return w
}

// wordsOf splits a path, command or tool name into stemmed words.
func wordsOf(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range wordRe.FindAllString(camelRe.ReplaceAllString(s, "$1 $2"), -1) {
		if lw := strings.ToLower(w); len(lw) >= 3 {
			out[stem(lw)] = true
		}
	}
	// Joined words too, so "acmecorp" meets "acme_corp".
	for _, part := range pathSepRe.Split(strings.ToLower(s), -1) {
		if j := strings.NewReplacer("_", "", "-", "").Replace(part); len(j) >= 5 {
			out[stem(j)] = true
		}
	}
	return out
}

func related(topic []string, s string) bool {
	ws := wordsOf(s)
	for _, t := range topic {
		if ws[t] {
			return true
		}
	}
	return false
}

// matching keeps the paths whose last few parts name something in topic.
func matching(topic, paths []string) []string {
	var out []string
	for _, p := range paths {
		parts := strings.Split(strings.Trim(p, "/"), "/")
		if len(parts) > 3 {
			parts = parts[len(parts)-3:]
		}
		if related(topic, strings.Join(parts, "/")) {
			out = append(out, p)
		}
	}
	return out
}

func matchingSteps(topic []string, ss []step) []step {
	var out []step
	for _, s := range ss {
		name := claims.PayloadStr(s.call, "tool")
		if s.call.Kind == spec.KindCommand {
			name = head(claims.PayloadStr(s.call, "command"))
		} else {
			name = strings.NewReplacer("mcp__", "", "claude_ai_", "", "plugin_", "").Replace(name)
		}
		if related(topic, name) {
			out = append(out, s)
		}
	}
	return out
}

func pathChanged(d *claims.Data, p string, changed []string, to int) bool {
	p = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSuffix(p, "/"), "~/"), "./")
	for _, c := range changed {
		if c == p || strings.HasSuffix(c, "/"+p) || strings.HasPrefix(c, p+"/") || strings.Contains(c, "/"+p+"/") {
			return true
		}
	}
	// A shell command that names the file and writes it.
	base := filepath.Base(p)
	for i := range d.Events {
		e := &d.Events[i]
		if e.Seq > to || e.Kind != spec.KindCommand {
			continue
		}
		cmd := claims.PayloadStr(e, "command")
		if strings.Contains(cmd, base) && shellWriteRe.MatchString(cmd) && stepOf(d, e).ok {
			return true
		}
	}
	return false
}

// fetches reports a command that downloads something: curl and friends, or
// a script that opens URLs.
func fetches(cmd string) bool {
	h := head(cmd)
	return fetchCmdRe.MatchString(h) || (scriptRe.MatchString(h) && bodyFetchRe.MatchString(cmd))
}

func matchesKind(e *spec.Event, kind string) bool {
	h := ""
	if e.Kind == spec.KindCommand {
		h = head(claims.PayloadStr(e, "command"))
	}
	tool := claims.PayloadStr(e, "tool")
	if e.Kind == spec.KindToolCall && harnessTool.MatchString(tool) {
		return false
	}
	switch kind {
	case "test":
		return h != "" && testCmdRe.MatchString(h)
	case "build":
		return h != "" && buildCmdRe.MatchString(h)
	case "deploy":
		return (h != "" && deployCmdRe.MatchString(h)) || (e.Kind == spec.KindToolCall && deployTool.MatchString(tool))
	case "commit":
		return e.Kind == spec.KindCommit
	case "research":
		switch e.Kind {
		case spec.KindFileRead, spec.KindSearch:
			return true
		case spec.KindToolCall:
			return claims.PayloadStr(e, "category") == "research" || lookTool.MatchString(tool)
		}
		return h != "" && fetches(claims.PayloadStr(e, "command"))
	case "run":
		if e.Kind == spec.KindToolCall {
			return runTool.MatchString(tool)
		}
		return h != "" && !lookCmdRe.MatchString(h)
	}
	return false
}

var runNoun = map[string]string{
	"test":     "test run",
	"build":    "build",
	"deploy":   "deploy step",
	"commit":   "commit",
	"research": "lookup",
	"run":      "run",
}

func checkRun(d *claims.Data, a spec.Assertion) sessionResult {
	from, to := span(a)
	kind := a.Str("kind")
	noun := runNoun[kind]
	lastEdit := 0
	var steps []step
	for i := range d.Events {
		e := &d.Events[i]
		if e.Seq < from || e.Seq > to {
			continue
		}
		if e.Kind == spec.KindFileEdit {
			lastEdit = e.Seq
		}
		if matchesKind(e, kind) {
			steps = append(steps, stepOf(d, e))
		}
	}
	if kind == "test" && len(steps) == 0 {
		// Tests from earlier in the session still count if nothing changed since.
		for i := range d.Events {
			e := &d.Events[i]
			if e.Seq >= from {
				break
			}
			switch {
			case e.Kind == spec.KindFileEdit:
				steps = nil
			case matchesKind(e, "test"):
				steps = []step{stepOf(d, e)}
			}
		}
	}
	if kind == "run" || kind == "test" || kind == "build" {
		// Only runs after the last change say anything about the result.
		var after []step
		for _, s := range steps {
			if s.call.Seq > lastEdit {
				after = append(after, s)
			}
		}
		if len(after) == 0 && len(steps) > 0 {
			return unknown("stale", "The last %s (%s) came before the last change. Re-run it to confirm.", noun, steps[len(steps)-1].label)
		}
		steps = after
	}
	if kind == "commit" && len(steps) == 0 {
		for _, c := range d.Commits {
			if seq := d.CommitSeq(c.SHA); seq >= from && seq <= to {
				return passed("committed", "Commit %s was made: %s.", short(c.SHA), clipCmd(c.Subject))
			}
		}
	}
	if len(steps) == 0 {
		where := "after the last change"
		if lastEdit == 0 || kind == "deploy" || kind == "commit" || kind == "research" {
			where = "here"
		}
		return unknown("none", "The session shows zero %ss %s.", noun, where)
	}
	last := steps[len(steps)-1]
	if !last.ok {
		detail := ""
		if kind == "test" {
			detail = testSummary(last)
		}
		return failed("last_failed", "The last %s, %s%s, failed%s.", noun, last.label, at(d, last.call), detail)
	}
	switch kind {
	case "test":
		if n := a.Int("count", 0); n > 0 {
			got := passedCount(last)
			switch {
			case got == n:
				return passed("count_matches", "%s passed%s with %d tests passing.", capitalizeFirst(last.label), at(d, last.call), got)
			case got > 0:
				return unknown("count_differs", "%s passed%s with %d tests passing; the reply says %d.", capitalizeFirst(last.label), at(d, last.call), got, n)
			}
		}
		return passed("passed", "%s passed%s%s.", capitalizeFirst(last.label), at(d, last.call), testSummary(last))
	case "research":
		return passed("looked", "The session looked things up %s here (last: %s%s).", times(len(steps)), last.label, at(d, last.call))
	}
	return passed("ok", "%s succeeded%s.", capitalizeFirst(last.label), at(d, last.call))
}

func testSummary(s step) string {
	if s.res == nil {
		return ""
	}
	to, ok := claims.Payload(s.res)["test_outcome"].(map[string]any)
	if !ok {
		return ""
	}
	p, _ := to["passed"].(float64)
	f, _ := to["failed"].(float64)
	switch {
	case p > 0 && f > 0:
		return fmt.Sprintf(": %d passed, %d failed", int(p), int(f))
	case f > 0:
		return fmt.Sprintf(": %d failed", int(f))
	case p > 0:
		return fmt.Sprintf(" (%d passed)", int(p))
	}
	return ""
}

var passedRe = regexp.MustCompile(`(?i)\b(\d+) (?:tests? )?passed\b|\bRan (\d+) tests?\b|\b(\d+) passing\b`)

func passedCount(s step) int {
	if s.res == nil {
		return 0
	}
	p := claims.Payload(s.res)
	if to, ok := p["test_outcome"].(map[string]any); ok {
		if n, _ := to["passed"].(float64); n > 0 {
			return int(n)
		}
	}
	out, _ := p["output"].(string)
	if m := passedRe.FindAllStringSubmatch(out, -1); len(m) > 0 {
		last := m[len(m)-1]
		for _, g := range last[1:] {
			if n, err := strconv.Atoi(g); err == nil {
				return n
			}
		}
	}
	return 0
}

func checkLink(d *claims.Data, a spec.Assertion) sessionResult {
	from, to := span(a)
	raw := a.Str("url")
	u, err := url.Parse(raw)
	if err != nil {
		return unknown("bad_url", "Open %s to confirm it.", raw)
	}
	if host := u.Hostname(); host == "localhost" || host == "127.0.0.1" || host == "0.0.0.0" {
		return checkLocal(d, u, to)
	}
	want := strings.TrimRight(raw, "/")
	inPrompt := false
	var opened *spec.Event
	for i := range d.Events {
		e := &d.Events[i]
		if e.Seq > to {
			break
		}
		switch e.Kind {
		case spec.KindPrompt:
			if strings.Contains(claims.PayloadStr(e, "text"), want) {
				inPrompt = true
			}
		case spec.KindToolResult:
			if ok, _ := claims.Payload(e)["ok"].(bool); ok && strings.Contains(claims.PayloadStr(e, "output"), want) {
				label := toolLabel(claims.PayloadStr(e, "tool"))
				if call, _, found := d.Call(claims.PayloadStr(e, "tool_call_id")); found && d.Event(call) != nil {
					label = stepOf(d, d.Event(call)).label
				}
				return passed("link_returned", "The link came back from %s%s.", label, at(d, e))
			}
		case spec.KindCommand:
			// A URL inside a script that edits a file was not opened.
			if cmd := claims.PayloadStr(e, "command"); opened == nil && (strings.Contains(head(cmd), want) || (fetches(cmd) && strings.Contains(cmd, want))) && stepOf(d, e).ok {
				opened = e
			}
		case spec.KindToolCall:
			if opened == nil && !harnessTool.MatchString(claims.PayloadStr(e, "tool")) && strings.Contains(string(e.Payload), want) && stepOf(d, e).ok {
				opened = e
			}
		}
	}
	if opened != nil {
		return passed("link_opened", "The session opened this link%s with %s.", at(d, opened), stepOf(d, opened).label)
	}
	if inPrompt {
		return unknown("link_from_human", "This link came from your own message.")
	}
	for i := range d.Events {
		e := &d.Events[i]
		if e.Seq >= from && e.Seq <= to && matchesKind(e, "deploy") && stepOf(d, e).ok {
			return unknown("link_unconfirmed", "A publish step ran (%s). Open the link to confirm it's live.", stepOf(d, e).label)
		}
	}
	if a["published"] == true {
		return failed("link_unseen", "This link appears only in the agent's reply, so it may be made up.")
	}
	return unknown("link_unseen", "This link appears only in the agent's reply.")
}

func checkLocal(d *claims.Data, u *url.URL, to int) sessionResult {
	port := u.Port()
	if port == "" {
		return unknown("no_port", "Open %s to confirm it.", u.String())
	}
	portRe := regexp.MustCompile(`\b` + regexp.QuoteMeta(port) + `\b`)
	var used []step
	for i := range d.Events {
		e := &d.Events[i]
		if e.Seq > to {
			break
		}
		if (e.Kind == spec.KindCommand && portRe.MatchString(claims.PayloadStr(e, "command"))) ||
			(e.Kind == spec.KindToolCall && portRe.MatchString(string(e.Payload))) {
			used = append(used, stepOf(d, e))
		}
	}
	// Opening the page is the best evidence, then starting a server, then anything else on the port.
	for _, pref := range []*regexp.Regexp{localOpenRe, serverRe, nil} {
		for i := len(used) - 1; i >= 0; i-- {
			s := used[i]
			what := head(claims.PayloadStr(s.call, "command")) + " " + claims.PayloadStr(s.call, "tool")
			if s.ok && (pref == nil || pref.MatchString(what)) {
				return passed("local_server", "%s used port %s%s and succeeded.", capitalizeFirst(s.label), port, at(d, s.call))
			}
		}
	}
	if len(used) > 0 {
		last := used[len(used)-1]
		return failed("local_failed", "Every command that used port %s failed; the last was %s%s.", port, last.label, at(d, last.call))
	}
	return unknown("local_unseen", "The session shows zero activity on port %s.", port)
}

func joinNames(ps []string) string {
	names := make([]string, 0, len(ps))
	seen := map[string]bool{}
	for _, p := range ps {
		b := filepath.Base(strings.TrimSuffix(p, "/"))
		if !seen[b] {
			seen[b] = true
			names = append(names, b)
		}
	}
	more := 0
	if len(names) > 3 {
		more = len(names) - 3
		names = names[:3]
	}
	s := strings.Join(names, ", ")
	if more > 0 {
		return s + " and " + strconv.Itoa(more) + " more"
	}
	if i := strings.LastIndex(s, ", "); i >= 0 {
		s = s[:i] + " and " + s[i+2:]
	}
	return s
}

func countNoun(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

func times(n int) string {
	if n == 1 {
		return "once"
	}
	return strconv.Itoa(n) + " times"
}

func pronoun(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}

func wasWere(n int) string {
	if n == 1 {
		return "was"
	}
	return "were"
}

func capitalizeFirst(s string) string {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
