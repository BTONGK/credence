package verifiers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/BTONGK/credence/internal/gitx"
	"github.com/BTONGK/credence/spec"
)

// Git is tier 1: commit and file assertions checked against the repository.
type Git struct{}

func (Git) Name() string { return "git" }
func (Git) Tier() int    { return 1 }

func (Git) Verify(ctx context.Context, vc *Context, c *spec.Claim) []spec.Verdict {
	d := vc.Data
	repo := d.Session.Repo
	var out []spec.Verdict
	if repo == "" {
		return nil
	}
	// Commits cited as produced by this session must have been made in it.
	for _, ev := range c.Evidence {
		t := spec.ParseTarget(ev.Target)
		if t.Scheme != "git" || t.Commit == "" || ev.Relation == spec.RelInformed {
			continue
		}
		start := time.Now()
		if d.CommitBySHA(t.Commit) == nil {
			out = append(out, verdict(vc, c, "git", 1, "commit_in_session", spec.ResultFail, "not_in_session",
				fmt.Sprintf("commit %s was not made during this session", short(t.Commit)), nil, start))
			continue
		}
		if len(gitx.BranchesContaining(repo, t.Commit)) == 0 {
			out = append(out, verdict(vc, c, "git", 1, "commit_reachable", spec.ResultInconclusive, "unreachable",
				fmt.Sprintf("commit %s is not on any branch (rebased or amended?)", short(t.Commit)), nil, start))
		}
	}
	for _, a := range c.Assertions {
		start := time.Now()
		switch a.Check() {
		case "commit_exists":
			sha := a.Str("commit")
			if full, err := gitx.ResolveCommit(repo, sha); err == nil {
				out = append(out, verdict(vc, c, "git", 1, "commit_exists", spec.ResultPass, "exists", "commit "+short(full)+" exists", a, start))
			} else {
				out = append(out, verdict(vc, c, "git", 1, "commit_exists", spec.ResultFail, "missing", "commit "+short(sha)+" does not exist", a, start))
			}
		case "commit_touches":
			sha := a.Str("commit")
			cm, err := gitx.ShowCommit(repo, sha)
			if err != nil {
				out = append(out, verdict(vc, c, "git", 1, "commit_touches", spec.ResultFail, "missing", "commit "+short(sha)+" does not exist", a, start))
				continue
			}
			touched := map[string]bool{}
			for _, f := range cm.Files {
				touched[f] = true
			}
			var missing []string
			paths := a.Strs("paths")
			for _, p := range paths {
				p = d.RelPath(p)
				if !touched[p] && !touchedDir(cm.Files, p) {
					missing = append(missing, p)
				}
			}
			switch {
			case len(paths) == 0:
				out = append(out, verdict(vc, c, "git", 1, "commit_touches", spec.ResultPass, "touches",
					fmt.Sprintf("commit %s changes %d files (+%d −%d)", short(cm.SHA), len(cm.Files), cm.Added, cm.Deleted), a, start))
			case len(missing) == 0:
				out = append(out, verdict(vc, c, "git", 1, "commit_touches", spec.ResultPass, "touches",
					fmt.Sprintf("commit %s changes %s", short(cm.SHA), strings.Join(paths, ", ")), a, start))
			default:
				out = append(out, verdict(vc, c, "git", 1, "commit_touches", spec.ResultFail, "untouched_paths",
					fmt.Sprintf("commit %s does not change %s", short(cm.SHA), strings.Join(missing, ", ")), a, start))
			}
		case "file_exists":
			commit := a.Str("commit")
			if commit == "" {
				commit = "HEAD"
			}
			var missing []string
			for _, p := range a.Strs("paths") {
				if !gitx.FileExistsAt(repo, commit, d.RelPath(p)) {
					missing = append(missing, p)
				}
			}
			if len(missing) == 0 {
				out = append(out, verdict(vc, c, "git", 1, "file_exists", spec.ResultPass, "exists", "files exist at "+short(commit), a, start))
			} else {
				out = append(out, verdict(vc, c, "git", 1, "file_exists", spec.ResultFail, "missing",
					fmt.Sprintf("%s missing at %s", strings.Join(missing, ", "), short(commit)), a, start))
			}
		}
	}
	return out
}

func touchedDir(files []string, dir string) bool {
	dir = strings.TrimSuffix(dir, "/") + "/"
	for _, f := range files {
		if strings.HasPrefix(f, dir) {
			return true
		}
	}
	return false
}
