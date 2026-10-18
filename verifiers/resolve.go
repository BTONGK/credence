package verifiers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/BTONGK/credence/internal/gitx"
	"github.com/BTONGK/credence/spec"
)

// Resolve is tier 0: every evidence target must point at something that
// exists, and event spans must sit on an intact hash chain.
type Resolve struct{}

func (Resolve) Name() string { return "resolve" }
func (Resolve) Tier() int    { return 0 }

func (Resolve) Verify(ctx context.Context, vc *Context, c *spec.Claim) []spec.Verdict {
	start := time.Now()
	d := vc.Data
	var resolved, unresolved, broken []string
	strong := 0 // produced/demonstrates evidence that resolved
	chainOK := map[string]bool{}
	for _, ev := range c.Evidence {
		t := spec.ParseTarget(ev.Target)
		ok, why := true, ""
		switch t.Scheme {
		case "event":
			evs := d.Events
			if t.Session != d.Session.ID {
				evs = d.ChildEvents[t.Session]
				if evs == nil {
					ok, why = false, "session "+t.Session+" is not part of this report"
					break
				}
			}
			if len(evs) == 0 || t.From < 1 || t.To > evs[len(evs)-1].Seq {
				ok, why = false, fmt.Sprintf("events #%d-%d out of range", t.From, t.To)
				break
			}
			if _, done := chainOK[t.Session]; !done {
				chainOK[t.Session] = chainIntact(evs)
			}
			if !chainOK[t.Session] {
				broken = append(broken, fmt.Sprintf("event chain of %s does not verify", t.Session))
			}
		case "git":
			if t.Commit == "" || d.Session.Repo == "" || !gitx.Exists(d.Session.Repo, t.Commit) {
				ok, why = false, "commit "+short(t.Commit)+" does not exist"
				if t.Commit != "" && d.Session.Repo != "" {
					broken = append(broken, why)
				}
			}
		case "file":
			commit := t.Commit
			if commit == "" {
				commit = "HEAD"
			}
			content, err := gitx.FileAt(d.Session.Repo, commit, t.Path)
			if err != nil {
				ok, why = false, fmt.Sprintf("%s does not exist at %s", t.Path, short(commit))
			} else if t.From > 0 {
				n := strings.Count(content, "\n") + 1
				if t.To > n || t.From > t.To {
					ok, why = false, fmt.Sprintf("%s has %d lines; L%d-L%d out of range", t.Path, n, t.From, t.To)
				}
			}
		case "test_run":
			if _, err := vc.Store.GetTestRun(t.ID); err != nil {
				ok, why = false, "test run "+t.ID+" not found"
			}
		case "ci", "pr", "issue":
			// Checked against GitHub by tier 1; resolvable if the repo has a GitHub remote.
			if _, _, gh := gitx.GitHubRepo(d.Session.Repo); !gh {
				ok, why = false, "no GitHub remote to resolve "+ev.Target
			}
		default:
			ok, why = false, strings.TrimPrefix(t.Raw, "unresolved:")
		}
		if ok {
			resolved = append(resolved, ev.Target)
			if ev.Relation != spec.RelInformed {
				strong++
			}
		} else {
			unresolved = append(unresolved, why)
		}
	}
	var out []spec.Verdict
	switch {
	case len(broken) > 0:
		out = append(out, verdict(vc, c, "resolve", 0, "evidence_resolves", spec.ResultFail, "integrity",
			strings.Join(dedupe(broken), "; "), nil, start))
	case len(c.Evidence) == 0:
		out = append(out, verdict(vc, c, "resolve", 0, "evidence_resolves", spec.ResultBlocked, "no_evidence",
			"the claim cites no evidence", nil, start))
	case strong == 0:
		details := "no cited evidence resolved to work done in this session"
		if len(unresolved) > 0 {
			details += ": " + strings.Join(dedupe(unresolved), "; ")
		}
		out = append(out, verdict(vc, c, "resolve", 0, "evidence_resolves", spec.ResultBlocked, "unresolved", details, nil, start))
	case len(unresolved) > 0:
		out = append(out, verdict(vc, c, "resolve", 0, "evidence_resolves", spec.ResultPass, "partial",
			fmt.Sprintf("%d of %d targets resolved; unresolved: %s", len(resolved), len(c.Evidence), strings.Join(dedupe(unresolved), "; ")), nil, start))
	default:
		out = append(out, verdict(vc, c, "resolve", 0, "evidence_resolves", spec.ResultPass, "resolved",
			fmt.Sprintf("all %d evidence targets resolved", len(resolved)), nil, start))
	}
	return out
}

// chainIntact recomputes the hash chain of a session's events.
func chainIntact(evs []spec.Event) bool {
	prev := ""
	for i := range evs {
		e := evs[i]
		if e.Prev != prev || spec.HashBytes(e.Payload) != e.PayloadRef || e.ChainHash() != e.Hash {
			return false
		}
		prev = e.Hash
	}
	return true
}

// ChainIntact is exported for `credence doctor` and ledger verification.
func ChainIntact(evs []spec.Event) bool { return chainIntact(evs) }

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func dedupe(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}
