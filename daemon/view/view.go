// Package view builds the read models shown by the UI and the CLI: the
// sessions inbox, a session's credit report with its timeline episodes,
// and claim cards.
package view

import (
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/BTONGK/credence/daemon/attribution"
	"github.com/BTONGK/credence/daemon/claims"
	"github.com/BTONGK/credence/daemon/store"
	"github.com/BTONGK/credence/spec"
)

// SectionInfo describes a report section.
type SectionInfo struct {
	Key         string `json:"key"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

// Sections in display order.
var Sections = []SectionInfo{
	{spec.SectionVerified, "Verified", "Claimed by the agent and confirmed by checks"},
	{spec.SectionUnverified, "Unverified", "Claimed and plausible, waiting on a machine check"},
	{spec.SectionContradicted, "Contradicted", "Claimed, and the evidence disagrees"},
	{spec.SectionAuditor, "Found by auditor", "Work the agent did beyond what it claimed"},
	{spec.SectionFailures, "Reported failures", "Approaches the agent tried and set aside"},
}

// SessionItem is one inbox row.
type SessionItem struct {
	*store.SessionRow
	DurationSec int            `json:"duration_sec"`
	Counts      map[string]int `json:"counts"`
	Claims      int            `json:"claims"`
	Pending     int            `json:"pending"`
	Value       float64        `json:"value"`
	Summary     string         `json:"summary"`
	Model       string         `json:"model"`
	Muted       bool           `json:"muted"`
	// Tally counts the agent's claims by verdict (backed, unchecked,
	// contradicted); Noticed counts what the auditor found besides.
	Tally   map[string]int `json:"tally"`
	Noticed int            `json:"noticed"`
}

// Inbox lists sessions with claim summaries.
func Inbox(st *store.Store, f store.SessionFilter) ([]SessionItem, error) {
	rows, err := st.ListSessions(f)
	if err != nil {
		return nil, err
	}
	out := make([]SessionItem, 0, len(rows))
	for _, r := range rows {
		cs, _ := st.SessionClaims(r.ID)
		out = append(out, Item(r, cs))
	}
	return out, nil
}

// Item summarizes one session.
func Item(r *store.SessionRow, cs []*store.ClaimRow) SessionItem {
	it := SessionItem{SessionRow: r, Counts: map[string]int{}, Tally: map[string]int{}}
	end := r.LastActivity
	if r.EndedAt != nil && r.EndedAt.After(r.StartedAt) {
		end = *r.EndedAt
	}
	if !r.StartedAt.IsZero() && end.After(r.StartedAt) {
		it.DurationSec = int(end.Sub(r.StartedAt).Seconds())
	}
	if len(r.Models) > 0 {
		it.Model = r.Models[0]
	}
	for _, c := range cs {
		if !c.Primary {
			continue
		}
		it.Claims++
		it.Counts[c.Section]++
		switch {
		case c.Section == spec.SectionAuditor || c.Section == spec.SectionFailures:
			it.Noticed++
		case c.Section == spec.SectionContradicted:
			it.Tally[VerdictContradicted]++
		case c.Status == spec.StatusVerified || c.Status == spec.StatusCorroborated:
			it.Tally[VerdictBacked]++
		default:
			it.Tally[VerdictUnchecked]++
		}
		it.Value += c.Value
		if c.ReviewState == spec.ReviewPending {
			it.Pending++
		}
	}
	it.Summary = SummaryLine(it.Claims, it.Counts)
	it.Muted = r.Skipped || it.Claims == 0
	if r.Skipped && r.SkipReason != "" {
		it.Summary = "conversation only · " + r.SkipReason
	}
	return it
}

// SummaryLine renders "5 claims · 3 verified · 1 flagged · 1 found by auditor".
func SummaryLine(n int, counts map[string]int) string {
	if n == 0 {
		return "0 claims"
	}
	parts := []string{plural(n, "claim")}
	if v := counts[spec.SectionVerified]; v > 0 {
		parts = append(parts, fmt.Sprintf("%d verified", v))
	}
	if v := counts[spec.SectionUnverified] + counts[spec.SectionContradicted]; v > 0 {
		parts = append(parts, fmt.Sprintf("%d flagged", v))
	}
	if v := counts[spec.SectionAuditor]; v > 0 {
		parts = append(parts, fmt.Sprintf("%d found by auditor", v))
	}
	if v := counts[spec.SectionFailures]; v > 0 {
		parts = append(parts, plural(v, "reported failure"))
	}
	return strings.Join(parts, " · ")
}

func plural(n int, w string) string {
	if n == 1 {
		return "1 " + w
	}
	return fmt.Sprintf("%d %ss", n, w)
}

// EvidenceView is an evidence chip.
type EvidenceView struct {
	spec.Evidence
	Kind    string `json:"kind"` // events, commit, file, test_run, ci, pr, issue, unresolved
	Label   string `json:"label"`
	Session string `json:"session,omitempty"`
	From    int    `json:"from,omitempty"`
	To      int    `json:"to,omitempty"`
	Commit  string `json:"commit,omitempty"`
	Path    string `json:"path,omitempty"`
	ID      string `json:"id,omitempty"`
}

// CheckView is a verifier result in plain words.
type CheckView struct {
	Tier      int             `json:"tier"`
	Verifier  string          `json:"verifier"`
	Check     string          `json:"check"`
	Result    string          `json:"result"`
	Outcome   string          `json:"outcome"`
	Plain     string          `json:"plain"`
	Details   string          `json:"details"`
	Artifacts []spec.Artifact `json:"artifacts,omitempty"`
	Duration  int64           `json:"duration_ms"`
}

// Member is another claim in the same cluster.
type Member struct {
	ID       string `json:"id"`
	Origin   string `json:"origin"`
	Claimant string `json:"claimant"`
	Title    string `json:"title"`
	Status   string `json:"status"`
}

// ClaimCard is everything a claim card shows.
type ClaimCard struct {
	*store.ClaimRow
	DisplayTitle     string            `json:"display_title"`
	DisplayStatement string            `json:"display_statement"`
	ClusterOrigin    string            `json:"cluster_origin"`
	Evidence         []EvidenceView    `json:"evidence"`
	Checks           []CheckView       `json:"checks"`
	Attribution      *spec.Attribution `json:"attribution,omitempty"`
	Credit           string            `json:"credit"`
	Members          []Member          `json:"members"`
	SelfConfidence   *float64          `json:"self_confidence,omitempty"`
	// Verdict is the plain answer to "is this true?": backed, partly,
	// unchecked, contradicted or pending. Reason says why in one line.
	Verdict string `json:"verdict"`
	Reason  string `json:"reason"`
}

// Episode is a compressed run of timeline events.
type Episode struct {
	Session  string `json:"session"`
	From     int    `json:"from"`
	To       int    `json:"to"`
	Kind     string `json:"kind"`
	Actor    string `json:"actor"`
	Label    string `json:"label"`
	Detail   string `json:"detail,omitempty"`
	Count    int    `json:"count"`
	Failures int    `json:"failures,omitempty"`
	TS       string `json:"ts"`
}

// Section is a report section with its claims.
type Section struct {
	SectionInfo
	Claims []*ClaimCard `json:"claims"`
}

// Report is a session's credit report.
type Report struct {
	Session  SessionItem         `json:"session"`
	Sections []Section           `json:"sections"`
	Episodes []Episode           `json:"episodes"`
	Children []*store.SessionRow `json:"children"`
	Hidden   []*ClaimCard        `json:"duplicates"`
}

// BuildReport assembles a session's report.
func BuildReport(st *store.Store, sessionID string) (*Report, error) {
	d, err := claims.Load(st, sessionID)
	if err != nil {
		return nil, err
	}
	rows, err := st.SessionClaims(sessionID)
	if err != nil {
		return nil, err
	}
	clusters, _ := st.Clusters(sessionID)
	rep := &Report{Session: Item(d.Session, rows), Children: d.Children, Episodes: Episodes(d)}
	cards := map[string]*ClaimCard{}
	for _, r := range rows {
		cards[r.Claim.ID] = Card(st, r)
	}
	for _, cl := range clusters {
		for _, m := range cl.Members {
			c := cards[m]
			if c == nil {
				continue
			}
			c.ClusterOrigin = cl.Origin
			for _, o := range cl.Members {
				if o == m || cards[o] == nil {
					continue
				}
				oc := cards[o]
				c.Members = append(c.Members, Member{ID: o, Origin: oc.Claim.Origin, Claimant: oc.Claim.Claimant, Title: oc.Claim.Title, Status: oc.Status})
			}
		}
	}
	bySection := map[string][]*ClaimCard{}
	for _, r := range rows {
		c := cards[r.Claim.ID]
		if !r.Primary {
			rep.Hidden = append(rep.Hidden, c)
			continue
		}
		bySection[r.Section] = append(bySection[r.Section], c)
	}
	for _, s := range Sections {
		cs := bySection[s.Key]
		sort.SliceStable(cs, func(i, j int) bool {
			if cs[i].Value != cs[j].Value {
				return cs[i].Value > cs[j].Value
			}
			return cs[i].Claim.CreatedAt.Before(cs[j].Claim.CreatedAt)
		})
		if cs == nil {
			cs = []*ClaimCard{}
		}
		rep.Sections = append(rep.Sections, Section{SectionInfo: s, Claims: cs})
	}
	if rep.Hidden == nil {
		rep.Hidden = []*ClaimCard{}
	}
	if rep.Children == nil {
		rep.Children = []*store.SessionRow{}
	}
	return rep, nil
}

// Card builds one claim card.
func Card(st *store.Store, r *store.ClaimRow) *ClaimCard {
	c := &ClaimCard{ClaimRow: r, DisplayTitle: r.Claim.Title, DisplayStatement: r.Claim.Statement, SelfConfidence: r.Claim.SelfConfidence, Members: []Member{}}
	if r.Review != nil && r.Review.Decision == spec.ReviewEdited {
		if r.Review.EditedTitle != "" {
			c.DisplayTitle = r.Review.EditedTitle
		}
		if r.Review.EditedStatement != "" {
			c.DisplayStatement = r.Review.EditedStatement
		}
	}
	for _, ev := range r.Claim.Evidence {
		c.Evidence = append(c.Evidence, Chip(ev))
	}
	if c.Evidence == nil {
		c.Evidence = []EvidenceView{}
	}
	vs, _ := st.Verdicts(r.Claim.ID, -1)
	for _, v := range vs {
		c.Checks = append(c.Checks, CheckView{Tier: v.Tier, Verifier: v.Verifier, Check: v.Check, Result: v.Result, Outcome: v.Outcome,
			Plain: Plain(v), Details: v.Details, Artifacts: v.Artifacts, Duration: v.DurationMs})
	}
	if c.Checks == nil {
		c.Checks = []CheckView{}
	}
	c.Attribution, _ = st.GetAttribution(r.Claim.ID)
	c.Credit = CreditLine(c.Attribution)
	c.Verdict, c.Reason = VerdictOf(r.Status, c.Checks, r.Notes)
	return c
}

// Verdicts in the order a reader cares about them.
const (
	VerdictContradicted = "contradicted"
	VerdictBacked       = "backed"
	VerdictPartly       = "partly"
	VerdictUnchecked    = "unchecked"
	VerdictPending      = "pending"
)

// VerdictOf reduces a claim's status and checks to a plain answer and the
// one line that explains it.
func VerdictOf(status string, checks []CheckView, notes []string) (string, string) {
	var pass, fail, open []string
	partial := false // some check found related work it couldn't tie to the claim
	for _, k := range checks {
		if k.Verifier == "resolve" && k.Result == spec.ResultPass {
			continue
		}
		if k.Outcome == "unmatched" {
			partial = true
		}
		switch k.Result {
		case spec.ResultPass:
			pass = append(pass, k.Plain)
		case spec.ResultFail:
			fail = append(fail, k.Plain)
		default:
			open = append(open, k.Plain)
		}
	}
	first := func(xs ...[]string) string {
		for _, x := range xs {
			if len(x) > 0 {
				return x[0]
			}
		}
		return ""
	}
	switch status {
	case spec.StatusProposed:
		return VerdictPending, "Checking against the session record…"
	case spec.StatusContradicted, spec.StatusOverstated:
		return VerdictContradicted, first(fail, notes, open)
	case spec.StatusVerified, spec.StatusCorroborated:
		return VerdictBacked, strings.Join(capList(pass, 2), " ")
	}
	if len(pass) > 0 {
		return VerdictPartly, strings.Join(append(capList(pass, 1), capList(append(open, notes...), 1)...), " ")
	}
	if partial {
		return VerdictPartly, first(open, notes)
	}
	why := first(open, notes)
	if why == "" {
		why = "This claim goes beyond the session record."
	}
	return VerdictUnchecked, why
}

func capList(xs []string, n int) []string {
	var out []string
	seen := map[string]bool{}
	for _, x := range xs {
		if x != "" && !seen[x] && len(out) < n {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// Chip labels an evidence target.
func Chip(ev spec.Evidence) EvidenceView {
	t := spec.ParseTarget(ev.Target)
	v := EvidenceView{Evidence: ev, Session: t.Session, From: t.From, To: t.To, Commit: t.Commit, Path: t.Path, ID: t.ID}
	switch t.Scheme {
	case "event":
		v.Kind = "events"
		if t.To > t.From {
			v.Label = fmt.Sprintf("events #%d–%d", t.From, t.To)
		} else {
			v.Label = fmt.Sprintf("event #%d", t.From)
		}
		if strings.Contains(ev.Note, "subagent") {
			v.Label = "subagent " + v.Label
		}
	case "git":
		v.Kind = "commit"
		v.Label = "commit " + short(t.Commit)
	case "file":
		v.Kind = "file"
		v.Label = t.Path
		if t.From > 0 {
			v.Label += fmt.Sprintf(":%d-%d", t.From, t.To)
		}
		if t.Commit != "" && t.Commit != "HEAD" {
			v.Label += " @" + short(t.Commit)
		}
	case "test_run":
		v.Kind, v.Label = "test_run", "test run "+t.ID
	case "ci":
		v.Kind, v.Label = "ci", "CI "+t.ID
	case "pr":
		v.Kind, v.Label = "pr", "PR "+t.ID
	case "issue":
		v.Kind, v.Label = "issue", "issue "+t.ID
	default:
		v.Kind, v.Label = "unresolved", "unresolved: "+strings.TrimPrefix(t.Raw, "unresolved:")
	}
	if ev.Relation == spec.RelInformed {
		v.Label += " (context)"
	}
	return v
}

// Plain renders a verdict in plain words.
func Plain(v spec.Verdict) string {
	switch v.Verifier {
	case "resolve":
		switch v.Result {
		case spec.ResultPass:
			return "Evidence resolves to captured events and artifacts"
		case spec.ResultBlocked:
			return "Evidence lookup failed: " + v.Details
		}
		return "Evidence failed an integrity check: " + v.Details
	case "judge":
		return "Blind judge: " + v.Outcome + " — " + v.Details
	case "mutation":
		return "Mutation check: " + v.Details
	}
	first := v.Details
	if i := strings.IndexByte(first, '\n'); i > 0 {
		first = first[:i]
	}
	return capitalize(first)
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// CreditLine renders "agent 70% · Maya 30%: identified the failing test".
func CreditLine(a *spec.Attribution) string {
	if a == nil || len(a.Shares) == 0 {
		return ""
	}
	var parts []string
	var why string
	for _, s := range a.Shares {
		parts = append(parts, fmt.Sprintf("%s %d%%", ShortContributor(s.Contributor), int(s.Share*100+0.5)))
		if strings.HasPrefix(s.Contributor, "human:") && strings.HasPrefix(s.Basis, "direction: ") && why == "" {
			why = ShortContributor(s.Contributor) + " " + strings.TrimPrefix(s.Basis, "direction: ")
		}
	}
	line := strings.Join(parts, " · ")
	if len(a.Shares) == 1 {
		line = ShortContributor(a.Shares[0].Contributor) + " 100%"
	}
	if why != "" {
		line += ": " + why
	}
	return line
}

// ShortContributor shortens contributor ids for display.
func ShortContributor(id string) string {
	switch {
	case strings.HasPrefix(id, "human:"):
		n := strings.TrimPrefix(id, "human:")
		if n == "" {
			return "human"
		}
		return strings.ToUpper(n[:1]) + n[1:]
	case strings.HasPrefix(id, "agent:"):
		return "agent"
	case strings.HasPrefix(id, "subagent:"):
		return "subagent " + path.Base(id)
	}
	return id
}

// Episodes compresses a session's events into readable steps.
func Episodes(d *claims.Data) []Episode {
	var out []Episode
	var cur *Episode
	var files []string
	flush := func() {
		if cur == nil {
			return
		}
		switch cur.Kind {
		case "read":
			cur.Label = fmt.Sprintf("read %s%s", plural(cur.Count, "file"), dirOf(files))
		case "search":
			cur.Label = fmt.Sprintf("searched %s", plural(cur.Count, "time"))
		case "tool":
			if names := uniqStrings(files); len(names) == 1 {
				cur.Label = "used " + toolName(names[0])
			} else {
				cur.Label = fmt.Sprintf("used %s", plural(len(names), "tool"))
			}
		case "test":
			cur.Label = fmt.Sprintf("ran tests %s", times(cur.Count))
			if cur.Failures > 0 {
				cur.Label += fmt.Sprintf(", %s", plural(cur.Failures, "failure"))
			}
		case "command":
			cur.Label = fmt.Sprintf("ran %s", plural(cur.Count, "command"))
			if cur.Failures > 0 {
				cur.Label += fmt.Sprintf(", %d failed", cur.Failures)
			}
		case "edit":
			cur.Label = fmt.Sprintf("edited %s", fileList(uniqStrings(files)))
		}
		out = append(out, *cur)
		cur = nil
		files = nil
	}
	group := func(e *spec.Event, kind string) {
		if cur != nil && cur.Kind == kind && cur.Actor == e.Actor {
			cur.To = e.Seq
			cur.Count++
			return
		}
		flush()
		cur = &Episode{Session: d.Session.ID, From: e.Seq, To: e.Seq, Kind: kind, Actor: e.Actor, Count: 1, TS: e.TS.Format(time.RFC3339)}
	}
	single := func(e *spec.Event, kind, label, detail string) {
		flush()
		out = append(out, Episode{Session: d.Session.ID, From: e.Seq, To: e.Seq, Kind: kind, Actor: e.Actor, Label: label, Detail: detail, Count: 1, TS: e.TS.Format(time.RFC3339)})
	}
	children := map[int]*store.SessionRow{}
	ci := 0
	for i := range d.Events {
		e := &d.Events[i]
		p := claims.Payload(e)
		s := func(k string) string { v, _ := p[k].(string); return v }
		switch e.Kind {
		case spec.KindPrompt:
			who := ShortContributor(e.Actor)
			single(e, "prompt", who+": "+clip(s("text"), 140), s("text"))
		case spec.KindMessage:
			single(e, "message", clip(s("text"), 160), s("text"))
		case spec.KindFileRead:
			group(e, "read")
			files = append(files, d.RelPath(s("path")))
		case spec.KindSearch:
			group(e, "search")
		case spec.KindFileEdit:
			group(e, "edit")
			files = append(files, d.RelPath(s("path")))
		case spec.KindCommand:
			r := d.Result(e)
			failed := r != nil && !boolOf(claims.Payload(r)["ok"])
			if _, isTest := p["test"]; isTest {
				group(e, "test")
			} else if strings.HasPrefix(strings.TrimSpace(s("command")), "git commit") || strings.Contains(s("command"), "&& git commit") {
				single(e, "commit", "$ "+clip(s("command"), 120), "")
				continue
			} else {
				group(e, "command")
			}
			if cur != nil {
				if failed {
					cur.Failures++
				}
				if cur.Detail != "" {
					cur.Detail += "\n"
				}
				cur.Detail += "$ " + clip(s("command"), 200)
			}
		case spec.KindSubagentStart:
			label := "spawned subagent: " + clip(orStr(s("description"), s("agent_type")), 100)
			single(e, "subagent", label, s("prompt"))
			if ci < len(d.Children) {
				children[e.Seq] = d.Children[ci]
				out[len(out)-1].Detail = d.Children[ci].ID
				ci++
			}
		case spec.KindCommit:
			single(e, "commit", fmt.Sprintf("committed %s %q", short(s("sha")), s("subject")), "")
		case spec.KindClaimFiled:
			in, _ := p["input"].(map[string]any)
			t, _ := in["title"].(string)
			single(e, "claim", "filed claim: "+clip(t, 100), "")
		case spec.KindCompaction:
			single(e, "compaction", "context compacted", "")
		case spec.KindToolCall:
			group(e, "tool")
			files = append(files, s("tool"))
		}
	}
	flush()
	if out == nil {
		out = []Episode{}
	}
	return out
}

// toolName names a tool for people: "mcp__google_business__update_hours" is
// "the google business update hours tool".
func toolName(name string) string {
	if !strings.HasPrefix(name, "mcp__") {
		return "the " + orStr(name, "a") + " tool"
	}
	parts := strings.Split(strings.TrimPrefix(name, "mcp__"), "__")
	server := strings.ReplaceAll(strings.TrimPrefix(strings.TrimPrefix(parts[0], "claude_ai_"), "plugin_"), "_", " ")
	if len(parts) > 1 {
		return "the " + server + " " + strings.ReplaceAll(parts[len(parts)-1], "_", " ") + " tool"
	}
	return "the " + server + " tool"
}

func boolOf(v any) bool { b, _ := v.(bool); return b }

func times(n int) string {
	switch n {
	case 1:
		return "once"
	case 2:
		return "twice"
	}
	return fmt.Sprintf("%d times", n)
}

func dirOf(files []string) string {
	files = uniqStrings(files)
	if len(files) == 0 {
		return ""
	}
	dir := path.Dir(files[0])
	for _, f := range files[1:] {
		for dir != "." && !strings.HasPrefix(f, dir+"/") {
			dir = path.Dir(dir)
		}
	}
	if dir == "." || dir == "/" {
		if len(files) <= 2 {
			return ": " + strings.Join(files, ", ")
		}
		return ""
	}
	return " in " + dir + "/"
}

func uniqStrings(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if x != "" && !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

func fileList(files []string) string {
	switch len(files) {
	case 0:
		return "files"
	case 1:
		return files[0]
	case 2:
		return files[0] + " and " + files[1]
	}
	return fmt.Sprintf("%s, %s and %d more", files[0], files[1], len(files)-2)
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	cut := strings.LastIndex(s[:n], " ")
	if cut < n/2 {
		cut = n
	}
	return s[:cut] + "…"
}

func orStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func short(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

// Describe re-exports attribution.Describe for CLI use.
func Describe(a *spec.Attribution) string { return attribution.Describe(a) }
