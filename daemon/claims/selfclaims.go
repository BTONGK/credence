package claims

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/BTONGK/credence/internal/gitx"
	"github.com/BTONGK/credence/spec"
)

var (
	shaRe    = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	callIDRe = regexp.MustCompile(`^(toolu_|call_|tool_|fc_|srvtoolu_)[A-Za-z0-9_\-]+$`)
)

// ExtractSelfClaims turns credence_claim tool calls captured in the
// transcript into claims. Evidence hints are resolved against events the
// daemon captured itself; a hint that resolves to nothing is kept as an
// unresolved target so tier 0 can block the claim.
func ExtractSelfClaims(d *Data) []*spec.Claim {
	var out []*spec.Claim
	add := func(sessionID string, e *spec.Event, data *Data) {
		in, _ := Payload(e)["input"].(map[string]any)
		if in == nil || rejected(data, e) {
			return
		}
		c := buildSelfClaim(sessionID, e, in, data)
		if c != nil {
			out = append(out, c)
		}
	}
	for i := range d.Events {
		if d.Events[i].Kind == spec.KindClaimFiled {
			add(d.Session.ID, &d.Events[i], d)
		}
	}
	// Subagents can file claims too; they belong to the parent's report.
	for _, child := range d.Children {
		cd := &Data{Session: child, Events: d.ChildEvents[child.ID], Commits: d.Commits, Policy: d.Policy}
		cd.Session.Repo = d.Session.Repo
		cd.index()
		for i := range cd.Events {
			if cd.Events[i].Kind == spec.KindClaimFiled {
				in, _ := Payload(&cd.Events[i])["input"].(map[string]any)
				if in == nil || rejected(cd, &cd.Events[i]) {
					continue
				}
				if c := buildSelfClaim(child.ID, &cd.Events[i], in, cd); c != nil {
					c.SessionID = d.Session.ID
					c.AssignID()
					out = append(out, c)
				}
			}
		}
	}
	return out
}

// rejected reports whether the MCP server refused the claim (the agent
// usually retries with a corrected one).
func rejected(d *Data, e *spec.Event) bool {
	r := d.Result(e)
	if r == nil {
		return false
	}
	ok, _ := Payload(r)["ok"].(bool)
	return !ok && strings.Contains(PayloadStr(r, "output"), "Claim not recorded")
}

func str(m map[string]any, k string) string {
	switch v := m[k].(type) {
	case string:
		return strings.TrimSpace(v)
	case nil:
		return ""
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func buildSelfClaim(sessionID string, e *spec.Event, in map[string]any, d *Data) *spec.Claim {
	typ := strings.ToLower(str(in, "type"))
	title := str(in, "title")
	statement := str(in, "statement")
	if title == "" && statement == "" {
		return nil
	}
	if title == "" {
		title = statement
	}
	if len(title) > 100 {
		title = title[:97] + "…"
	}
	if statement == "" {
		statement = title
	}
	if typ == "negative" || typ == "failure" || typ == "failed_attempt" {
		typ = spec.TypeNegativeResult
	}
	if !spec.IsClaimType(typ) {
		typ = spec.TypeFeature
	}
	c := &spec.Claim{
		SessionID: sessionID, Claimant: e.Actor, Origin: spec.OriginSelf, Type: typ, Title: title,
		Statement: statement, Rationale: str(in, "rationale"), SourceEvent: e.Seq, CreatedAt: e.TS,
		Assertions: []spec.Assertion{}, Evidence: []spec.Evidence{},
	}
	if v, ok := in["confidence"].(float64); ok {
		if v < 0 {
			v = 0
		}
		if v > 1 {
			v = 1
		}
		c.SelfConfidence = &v
	}
	for _, a := range asList(in["assertions"]) {
		if m, ok := a.(map[string]any); ok && str(m, "check") != "" {
			c.Assertions = append(c.Assertions, spec.Assertion(m))
		}
	}
	for _, cc := range asList(in["co_contributors"]) {
		switch v := cc.(type) {
		case string:
			c.CoContributors = append(c.CoContributors, spec.CoContributor{ID: normalizeContributor(v)})
		case map[string]any:
			if id := str(v, "id"); id != "" {
				c.CoContributors = append(c.CoContributors, spec.CoContributor{ID: normalizeContributor(id), Basis: str(v, "basis")})
			}
		}
	}
	for _, h := range asList(in["evidence_hints"]) {
		if s, ok := h.(string); ok && strings.TrimSpace(s) != "" {
			c.EvidenceHints = append(c.EvidenceHints, strings.TrimSpace(s))
		}
	}
	c.Evidence = ResolveHints(d, sessionID, c.EvidenceHints, e.Seq)
	c.AssignID()
	return c
}

func asList(v any) []any {
	switch t := v.(type) {
	case []any:
		return t
	case string:
		// Some harnesses pass arrays as JSON strings.
		var l []any
		if json.Unmarshal([]byte(t), &l) == nil {
			return l
		}
		if t != "" {
			return []any{t}
		}
	}
	return nil
}

func normalizeContributor(s string) string {
	s = strings.TrimSpace(s)
	if strings.Contains(s, ":") {
		return s
	}
	return "human:" + strings.ToLower(strings.TrimPrefix(s, "@"))
}

// ResolveHints maps evidence hints (file paths, commit shas, tool call ids)
// to evidence targets. Events after the claim was filed are not eligible.
func ResolveHints(d *Data, sessionID string, hints []string, before int) []spec.Evidence {
	var ev []spec.Evidence
	seen := map[string]bool{}
	addEv := func(target, rel, note string) {
		if seen[target] {
			return
		}
		seen[target] = true
		ev = append(ev, spec.Evidence{Target: target, Relation: rel, Note: note})
	}
	for _, h := range hints {
		hint := strings.TrimSpace(strings.Trim(h, "`'\""))
		resolved := false
		switch {
		case callIDRe.MatchString(hint):
			if c, r, ok := d.Call(hint); ok && (before == 0 || c < before) {
				if r == 0 {
					r = c
				}
				addEv(spec.EventTarget(sessionID, c, r), spec.RelProduced, "tool call "+hint)
				resolved = true
			}
		case shaRe.MatchString(strings.ToLower(hint)):
			if c := d.CommitBySHA(strings.ToLower(hint)); c != nil {
				addEv(spec.CommitTarget(c.SHA), spec.RelProduced, "")
				resolved = true
			} else if d.Session.Repo != "" {
				if full, err := gitx.ResolveCommit(d.Session.Repo, strings.ToLower(hint)); err == nil {
					addEv(spec.CommitTarget(full), spec.RelInformed, "commit not made in this session")
					resolved = true
				}
			}
		default:
			path := hint
			if i := strings.Index(path, "::"); i > 0 {
				path = path[:i]
			}
			if i := strings.LastIndex(path, ":"); i > 0 && !strings.Contains(path[i:], "/") {
				path = path[:i] // strip :line suffixes
			}
			rel := d.RelPath(path)
			var seqs []int
			for i := range d.Events {
				e := &d.Events[i]
				if before > 0 && e.Seq >= before {
					break
				}
				if e.Kind == spec.KindFileEdit && d.RelPath(PayloadStr(e, "path")) == rel && d.EditOK(e) {
					seqs = append(seqs, e.Seq)
				}
				if e.Kind == spec.KindCommand && rel != "" && strings.Contains(PayloadStr(e, "command"), rel) {
					seqs = append(seqs, e.Seq)
					if r := d.Result(e); r != nil {
						seqs = append(seqs, r.Seq)
					}
				}
			}
			for _, span := range Spans(seqs, 3) {
				addEv(spec.EventTarget(sessionID, span[0], span[1]), spec.RelProduced, rel)
				resolved = true
			}
			for _, c := range d.Commits {
				for _, f := range c.Files {
					if f == rel {
						addEv(spec.FileTarget(rel, c.SHA, 0, 0), spec.RelProduced, "")
						resolved = true
					}
				}
			}
			if !resolved && d.Session.Repo != "" && rel != "" && gitx.FileExistsAt(d.Session.Repo, "HEAD", rel) {
				addEv(spec.FileTarget(rel, "HEAD", 0, 0), spec.RelInformed, "file exists but was not changed in this session")
				resolved = true
			}
		}
		if !resolved {
			addEv("unresolved:"+hint, spec.RelProduced, "hint did not match any captured event or artifact")
		}
	}
	return ev
}

// Spans groups sorted seqs into contiguous [from, to] ranges, merging gaps
// up to maxGap.
func Spans(seqs []int, maxGap int) [][2]int {
	if len(seqs) == 0 {
		return nil
	}
	sorted := append([]int(nil), seqs...)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j] < sorted[j-1]; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	var res [][2]int
	cur := [2]int{sorted[0], sorted[0]}
	for _, s := range sorted[1:] {
		if s <= cur[1]+maxGap {
			if s > cur[1] {
				cur[1] = s
			}
			continue
		}
		res = append(res, cur)
		cur = [2]int{s, s}
	}
	return append(res, cur)
}
