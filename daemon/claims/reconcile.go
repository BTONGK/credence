package claims

import (
	"sort"
	"strings"

	"github.com/BTONGK/credence/daemon/store"
	"github.com/BTONGK/credence/spec"
)

// features are the evidence footprints used to cluster claims.
type features struct {
	commits map[string]bool
	paths   map[string]bool
	seqs    map[int]bool
	tests   map[string]bool
}

func featuresOf(c *spec.Claim, d *Data) features {
	f := features{commits: map[string]bool{}, paths: map[string]bool{}, seqs: map[int]bool{}, tests: map[string]bool{}}
	for _, ev := range c.Evidence {
		t := spec.ParseTarget(ev.Target)
		switch t.Scheme {
		case "git":
			if t.Commit != "" {
				f.commits[short(t.Commit)] = true
			}
		case "file":
			f.paths[d.RelPath(t.Path)] = true
			if t.Commit != "" && t.Commit != "HEAD" {
				f.commits[short(t.Commit)] = true
			}
		case "event":
			if t.Session != c.SessionID && t.Session != d.Session.ID {
				continue
			}
			for s := t.From; s <= t.To && s-t.From < 5000; s++ {
				f.seqs[s] = true
				if e := d.Event(s); e != nil && e.Kind == spec.KindFileEdit {
					f.paths[d.RelPath(PayloadStr(e, "path"))] = true
				}
				if e := d.Event(s); e != nil && e.Kind == spec.KindCommit {
					if sha := PayloadStr(e, "sha"); sha != "" {
						f.commits[short(sha)] = true
					}
				}
			}
		}
	}
	for _, a := range c.Assertions {
		if sha := a.Str("commit"); sha != "" {
			f.commits[short(sha)] = true
		}
		for _, p := range a.Strs("paths") {
			f.paths[d.RelPath(p)] = true
		}
		if t := a.Str("test"); t != "" {
			f.tests[t] = true
		}
		if cmd := a.Str("command"); cmd != "" {
			f.tests["cmd:"+strings.Join(strings.Fields(cmd), " ")] = true
		}
	}
	return f
}

func short(sha string) string {
	sha = strings.ToLower(sha)
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

var codeFamily = map[string]bool{spec.TypeFix: true, spec.TypeFeature: true, spec.TypeRefactor: true, spec.TypeOps: true}

func typeCompatible(a, b string, sharedCommit bool) bool {
	if a == b {
		return true
	}
	if codeFamily[a] && codeFamily[b] {
		return sharedCommit
	}
	if (a == spec.TypeResearch && b == spec.TypeDecision) || (a == spec.TypeDecision && b == spec.TypeResearch) {
		return true
	}
	return false
}

func overlap[K comparable](a, b map[K]bool) int {
	n := 0
	for k := range a {
		if b[k] {
			n++
		}
	}
	return n
}

func similarity(a, b *spec.Claim, fa, fb features) float64 {
	shared := overlap(fa.commits, fb.commits) > 0
	if !typeCompatible(a.Type, b.Type, shared) {
		return 0
	}
	if a.Type == spec.TypeTest && b.Type == spec.TypeTest && len(fa.tests) > 0 && len(fb.tests) > 0 && overlap(fa.tests, fb.tests) == 0 {
		// Claims about different tests are different claims, even in one commit.
		return 0
	}
	score := 0.0
	if shared {
		score += 0.6
	}
	if n := overlap(fa.paths, fb.paths); n > 0 {
		union := len(fa.paths) + len(fb.paths) - n
		score += 0.3 * float64(n) / float64(union)
	}
	if n := overlap(fa.seqs, fb.seqs); n > 0 {
		m := min(len(fa.seqs), len(fb.seqs))
		score += 0.5 * float64(n) / float64(m)
	}
	if overlap(fa.tests, fb.tests) > 0 {
		score += 0.3
	}
	return score
}

// Reconcile clusters a session's claims by overlapping evidence.
// Disagreement is signal: auditor-only clusters become "found by auditor";
// several self-claims in one cluster indicate claim splitting.
func Reconcile(d *Data, cs []*spec.Claim) []store.Cluster {
	n := len(cs)
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	fs := make([]features, n)
	for i, c := range cs {
		fs[i] = featuresOf(c, d)
	}
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			if similarity(cs[i], cs[j], fs[i], fs[j]) >= 0.5 {
				parent[find(i)] = find(j)
			}
		}
	}
	groups := map[int][]int{}
	for i := range cs {
		r := find(i)
		groups[r] = append(groups[r], i)
	}
	var out []store.Cluster
	for _, idx := range groups {
		var members []string
		hasSelf, hasAud, hasHuman := false, false, false
		selfCount := 0
		for _, i := range idx {
			members = append(members, cs[i].ID)
			switch cs[i].Origin {
			case spec.OriginSelf:
				hasSelf = true
				selfCount++
			case spec.OriginAuditor:
				hasAud = true
			case spec.OriginHuman:
				hasHuman = true
			}
		}
		sort.Strings(members)
		primary := pickPrimary(cs, idx)
		cl := store.Cluster{
			ID: "cls_" + spec.ShortID(spec.HashBytes([]byte(strings.Join(members, ","))), 10), SessionID: d.Session.ID,
			PrimaryClaimID: cs[primary].ID, Members: members, Flags: []string{},
		}
		switch {
		case hasHuman:
			cl.Origin = spec.OriginHuman
		case hasSelf && hasAud:
			cl.Origin = "both"
			cl.Flags = append(cl.Flags, "auditor_agrees")
		case hasSelf:
			cl.Origin = spec.OriginSelf
			cl.Flags = append(cl.Flags, "auditor_did_not_find")
		default:
			cl.Origin = spec.OriginAuditor
			cl.Flags = append(cl.Flags, "found_by_auditor")
		}
		if selfCount > 1 {
			cl.Flags = append(cl.Flags, "merged_split_claims")
		}
		out = append(out, cl)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func pickPrimary(cs []*spec.Claim, idx []int) int {
	rank := func(c *spec.Claim) int {
		switch c.Origin {
		case spec.OriginHuman:
			return 3
		case spec.OriginSelf:
			return 2
		}
		return 1
	}
	best := idx[0]
	for _, i := range idx[1:] {
		a, b := cs[i], cs[best]
		switch {
		case rank(a) > rank(b):
			best = i
		case rank(a) == rank(b) && len(a.Assertions)+len(a.Evidence) > len(b.Assertions)+len(b.Evidence):
			best = i
		case rank(a) == rank(b) && len(a.Assertions)+len(a.Evidence) == len(b.Assertions)+len(b.Evidence) && a.ID < b.ID:
			best = i
		}
	}
	return best
}
