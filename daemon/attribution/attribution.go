// Package attribution splits a claim's credit among contributors: the
// agent, its subagents and the human. Code claims use line provenance
// (which captured edit produced each added line of the commit); other
// claims use authorship of the cited events. Humans who directed the work
// receive a configurable direction share.
package attribution

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/BTONGK/credence/adapters"
	"github.com/BTONGK/credence/daemon/claims"
	"github.com/BTONGK/credence/internal/gitx"
	"github.com/BTONGK/credence/spec"
)

// Provenance maps line hashes to the contributor that wrote them. External
// importers (Agent Trace, git-ai) can add entries before Compute runs.
type Provenance map[string]string

// SessionProvenance collects line hashes from captured edits.
func SessionProvenance(d *claims.Data) Provenance {
	p := Provenance{}
	add := func(evs []spec.Event, dd *claims.Data) {
		for i := range evs {
			e := &evs[i]
			if e.Kind != spec.KindFileEdit || (dd != nil && !dd.EditOK(e)) {
				continue
			}
			hs, _ := claims.Payload(e)["added_line_hashes"].([]any)
			for _, h := range hs {
				if s, ok := h.(string); ok {
					if _, seen := p[s]; !seen {
						p[s] = e.Actor
					}
				}
			}
		}
	}
	add(d.Events, d)
	for _, evs := range d.ChildEvents {
		add(evs, nil)
	}
	return p
}

// ImportedProvenance is line provenance imported from other tools (Agent
// Trace, git-ai), stored as line_provenance artifacts.
type ImportedProvenance struct {
	Source string            `json:"source"`
	Commit string            `json:"commit"`
	Lines  map[string]string `json:"lines"` // line hash -> contributor id
}

// Merge adds imported provenance for lines the session did not capture.
func (p Provenance) Merge(arts []*spec.Artifact) {
	for _, a := range arts {
		var ip ImportedProvenance
		if json.Unmarshal(a.Meta, &ip) != nil {
			continue
		}
		for h, who := range ip.Lines {
			if _, ok := p[h]; !ok {
				p[h] = who
			}
		}
	}
}

// MainAgent is the agent contributor with the most events in the session.
func MainAgent(d *claims.Data) string {
	n := map[string]int{}
	for _, e := range d.Events {
		if strings.HasPrefix(e.Actor, "agent:") {
			n[e.Actor]++
		}
	}
	best, bn := "", 0
	for a, c := range n {
		if c > bn || (c == bn && a < best) {
			best, bn = a, c
		}
	}
	return best
}

// Compute returns the attribution for a claim.
func Compute(d *claims.Data, c *spec.Claim, prov Provenance, directionShare float64) *spec.Attribution {
	w := map[string]float64{}
	basis := map[string][]string{}
	add := func(who string, n float64, why string) {
		if who == "" || n <= 0 {
			return
		}
		w[who] += n
		if why != "" && !contains(basis[who], why) {
			basis[who] = append(basis[who], why)
		}
	}
	human := d.Session.Human
	if human == "" {
		human = "human:unknown"
	}
	agent := MainAgent(d)
	method := "event_authorship"
	commits := commitsOf(d, c)
	shellTouched := shellWrittenPaths(d)
	if len(commits) > 0 && d.Session.Repo != "" {
		for _, sha := range commits {
			added, err := gitx.AddedLines(d.Session.Repo, sha)
			if err != nil {
				continue
			}
			for file, lines := range added {
				for _, l := range lines {
					h := adapters.LineHash(l)
					if h == "" {
						continue
					}
					switch who, ok := prov[h]; {
					case ok:
						add(who, 1, "wrote lines in "+short(sha))
					case shellTouched[file] && agent != "":
						add(agent, 1, "wrote lines in "+short(sha)+" via shell")
					default:
						add(human, 1, "lines in "+short(sha)+" not written through the agent")
					}
				}
			}
		}
		if len(w) > 0 {
			method = "line_provenance"
		}
	}
	if len(w) == 0 {
		for _, ev := range c.Evidence {
			t := spec.ParseTarget(ev.Target)
			if t.Scheme != "event" {
				continue
			}
			evs := d.Events
			if t.Session != d.Session.ID {
				evs = d.ChildEvents[t.Session]
			}
			for i := range evs {
				e := &evs[i]
				if e.Seq < t.From || e.Seq > t.To {
					continue
				}
				switch e.Kind {
				case spec.KindFileEdit:
					add(e.Actor, 3, "edits")
				case spec.KindCommand, spec.KindSubagentStart:
					add(e.Actor, 1.5, "ran commands")
				case spec.KindMessage:
					add(e.Actor, 1, "analysis")
				case spec.KindFileRead, spec.KindSearch, spec.KindToolCall:
					add(e.Actor, 0.5, "investigation")
				case spec.KindPrompt:
					// Human prompts count as direction below, not authorship.
				}
			}
		}
	}
	if len(w) == 0 && agent != "" {
		add(agent, 1, "session agent")
	}
	// Direction: humans and others named as co-contributors.
	var directors []spec.CoContributor
	for _, cc := range c.CoContributors {
		if strings.HasPrefix(cc.ID, "human:") && cc.Basis != "did the work" {
			directors = append(directors, cc)
		} else if cc.ID != "" && w[cc.ID] == 0 {
			add(cc.ID, 0.25*total(w), orStr(cc.Basis, "named as co-contributor"))
		}
	}
	if len(directors) > 0 && directionShare > 0 {
		t := total(w)
		for k := range w {
			w[k] = w[k] / t * (1 - directionShare)
		}
		for _, dcc := range directors {
			add(dcc.ID, directionShare/float64(len(directors)), "direction: "+orStr(dcc.Basis, "steered the approach"))
		}
		method += "+direction"
	}
	t := total(w)
	a := &spec.Attribution{ClaimID: c.ID, Method: method}
	for who, v := range w {
		a.Shares = append(a.Shares, spec.Share{Contributor: who, Share: v / t, Basis: strings.Join(basis[who], "; ")})
	}
	sort.Slice(a.Shares, func(i, j int) bool {
		if a.Shares[i].Share != a.Shares[j].Share {
			return a.Shares[i].Share > a.Shares[j].Share
		}
		return a.Shares[i].Contributor < a.Shares[j].Contributor
	})
	roundShares(a.Shares)
	return a
}

// roundShares rounds to 0.01 and keeps the sum at exactly 1.
func roundShares(s []spec.Share) {
	if len(s) == 0 {
		return
	}
	sum := 0.0
	for i := range s {
		s[i].Share = math.Round(s[i].Share*100) / 100
		sum += s[i].Share
	}
	s[0].Share = math.Round((s[0].Share+1-sum)*100) / 100
}

func total(w map[string]float64) float64 {
	t := 0.0
	for _, v := range w {
		t += v
	}
	if t == 0 {
		return 1
	}
	return t
}

func commitsOf(d *claims.Data, c *spec.Claim) []string {
	seen := map[string]bool{}
	var out []string
	addSHA := func(sha string) {
		if sha == "" || sha == "HEAD" {
			return
		}
		if cm := d.CommitBySHA(strings.ToLower(sha)); cm != nil && !seen[cm.SHA] {
			seen[cm.SHA] = true
			out = append(out, cm.SHA)
		}
	}
	for _, ev := range c.Evidence {
		if ev.Relation == spec.RelInformed {
			continue
		}
		addSHA(spec.ParseTarget(ev.Target).Commit)
	}
	for _, a := range c.Assertions {
		if a.Check() == "commit_touches" {
			addSHA(a.Str("commit"))
		}
	}
	return out
}

// shellWrittenPaths are files the agent wrote through shell commands
// (heredocs, sed -i, formatters), which produce no edit events.
func shellWrittenPaths(d *claims.Data) map[string]bool {
	out := map[string]bool{}
	var files []string
	for _, c := range d.Commits {
		files = append(files, c.Files...)
	}
	for i := range d.Events {
		e := &d.Events[i]
		if e.Kind != spec.KindCommand {
			continue
		}
		cmd := claims.PayloadStr(e, "command")
		writes := strings.Contains(cmd, ">") || strings.Contains(cmd, "sed -i") || strings.Contains(cmd, "tee ") ||
			strings.Contains(cmd, "fmt") || strings.Contains(cmd, "prettier") || strings.Contains(cmd, "black") || strings.Contains(cmd, "--fix")
		if !writes {
			continue
		}
		for _, f := range files {
			if strings.Contains(cmd, f) || strings.Contains(cmd, " .") {
				out[f] = true
			}
		}
	}
	return out
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func orStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// Describe renders shares for humans: "agent 70% · human 30%".
func Describe(a *spec.Attribution) string {
	if a == nil {
		return ""
	}
	var parts []string
	for _, s := range a.Shares {
		parts = append(parts, fmt.Sprintf("%s %d%%", s.Contributor, int(math.Round(s.Share*100))))
	}
	return strings.Join(parts, " · ")
}
