package verifiers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/BTONGK/credence/internal/gitx"
	"github.com/BTONGK/credence/spec"
)

// GitHub is a minimal REST client for CI, PR and issue checks.
type GitHub struct {
	BaseURL string
	Client  *http.Client
	once    sync.Once
	token   string
}

// NewGitHub returns a client that authenticates with GITHUB_TOKEN, GH_TOKEN
// or `gh auth token` when available.
func NewGitHub() *GitHub {
	return &GitHub{BaseURL: "https://api.github.com", Client: &http.Client{Timeout: 20 * time.Second}}
}

func (g *GitHub) tok() string {
	g.once.Do(func() {
		for _, k := range []string{"GITHUB_TOKEN", "GH_TOKEN"} {
			if v := os.Getenv(k); v != "" {
				g.token = v
				return
			}
		}
		if p, err := exec.LookPath("gh"); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if out, err := exec.CommandContext(ctx, p, "auth", "token").Output(); err == nil {
				g.token = strings.TrimSpace(string(out))
			}
		}
	})
	return g.token
}

// Get fetches a GitHub API path into v.
func (g *GitHub) Get(ctx context.Context, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", g.BaseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "credence/"+spec.Version)
	if t := g.tok(); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	resp, err := g.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != 200 {
		return fmt.Errorf("github %s: %s", path, resp.Status)
	}
	return json.Unmarshal(body, v)
}

// Post sends a JSON body to a GitHub API path.
func (g *GitHub) Do(ctx context.Context, method, path string, in, out any) error {
	b, _ := json.Marshal(in)
	req, err := http.NewRequestWithContext(ctx, method, g.BaseURL+path, strings.NewReader(string(b)))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "credence/"+spec.Version)
	if t := g.tok(); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	resp, err := g.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("github %s %s: %s %s", method, path, resp.Status, strings.TrimSpace(string(body[:min(len(body), 300)])))
	}
	if out != nil {
		return json.Unmarshal(body, out)
	}
	return nil
}

// GitHubChecks is tier 1: ci_green, pr_merged and issue_closed.
type GitHubChecks struct{}

func (GitHubChecks) Name() string { return "github" }
func (GitHubChecks) Tier() int    { return 1 }

func (GitHubChecks) Verify(ctx context.Context, vc *Context, c *spec.Claim) []spec.Verdict {
	var out []spec.Verdict
	for _, a := range c.Assertions {
		switch a.Check() {
		case "ci_green", "pr_merged", "issue_closed":
		default:
			continue
		}
		out = append(out, githubCheck(ctx, vc, c, a))
	}
	return out
}

func githubCheck(ctx context.Context, vc *Context, c *spec.Claim, a spec.Assertion) spec.Verdict {
	start := time.Now()
	check := a.Check()
	owner, name, ok := gitx.GitHubRepo(vc.Data.Session.Repo)
	if !ok {
		return verdict(vc, c, "github", 1, check, spec.ResultInconclusive, "no_remote", "repository has no GitHub remote", a, start)
	}
	gh := vc.GitHub
	if gh == nil {
		gh = NewGitHub()
	}
	repoPath := "/repos/" + owner + "/" + name
	fail := func(outcome, d string) spec.Verdict {
		return verdict(vc, c, "github", 1, check, spec.ResultFail, outcome, d, a, start)
	}
	pass := func(outcome, d string) spec.Verdict {
		return verdict(vc, c, "github", 1, check, spec.ResultPass, outcome, d, a, start)
	}
	inc := func(outcome, d string) spec.Verdict {
		return verdict(vc, c, "github", 1, check, spec.ResultInconclusive, outcome, d, a, start)
	}
	switch check {
	case "ci_green":
		sha := claimCommit(vc.Data, c, a)
		if sha == "" {
			return inc("no_commit", "no commit to check")
		}
		var runs struct {
			Total int `json:"total_count"`
			Runs  []struct {
				Name       string `json:"name"`
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
			} `json:"check_runs"`
		}
		if err := gh.Get(ctx, repoPath+"/commits/"+sha+"/check-runs?per_page=100", &runs); err != nil {
			return inc("api_error", err.Error())
		}
		var status struct {
			State    string `json:"state"`
			Statuses []struct {
				Context string `json:"context"`
				State   string `json:"state"`
			} `json:"statuses"`
		}
		_ = gh.Get(ctx, repoPath+"/commits/"+sha+"/status", &status)
		if runs.Total == 0 && len(status.Statuses) == 0 {
			return inc("no_ci", "no CI runs reported for "+short(sha)+" (not pushed yet?)")
		}
		var failed, pending []string
		okN := 0
		for _, r := range runs.Runs {
			switch {
			case r.Status != "completed":
				pending = append(pending, r.Name)
			case r.Conclusion == "success" || r.Conclusion == "neutral" || r.Conclusion == "skipped":
				okN++
			default:
				failed = append(failed, r.Name+" ("+r.Conclusion+")")
			}
		}
		for _, s := range status.Statuses {
			switch s.State {
			case "success":
				okN++
			case "pending":
				pending = append(pending, s.Context)
			default:
				failed = append(failed, s.Context+" ("+s.State+")")
			}
		}
		switch {
		case len(failed) > 0:
			return fail("ci_failed", fmt.Sprintf("CI failed at %s: %s", short(sha), strings.Join(failed, ", ")))
		case len(pending) > 0:
			return inc("ci_pending", fmt.Sprintf("CI still running at %s: %s", short(sha), strings.Join(pending, ", ")))
		}
		return pass("ci_green", fmt.Sprintf("%d CI checks passed at %s", okN, short(sha)))
	case "pr_merged":
		n := num(a, "pr")
		if n == 0 {
			return inc("no_number", "assertion names no PR number")
		}
		var pr struct {
			Merged   bool   `json:"merged"`
			State    string `json:"state"`
			MergeSHA string `json:"merge_commit_sha"`
		}
		if err := gh.Get(ctx, fmt.Sprintf("%s/pulls/%d", repoPath, n), &pr); err != nil {
			return inc("api_error", err.Error())
		}
		if pr.Merged {
			return pass("merged", fmt.Sprintf("PR #%d merged as %s", n, short(pr.MergeSHA)))
		}
		if pr.State == "closed" {
			return fail("closed_unmerged", fmt.Sprintf("PR #%d was closed without merging", n))
		}
		return inc("open", fmt.Sprintf("PR #%d is still open", n))
	default:
		n := num(a, "issue")
		if n == 0 {
			return inc("no_number", "assertion names no issue number")
		}
		var is struct {
			State       string `json:"state"`
			StateReason string `json:"state_reason"`
		}
		if err := gh.Get(ctx, fmt.Sprintf("%s/issues/%d", repoPath, n), &is); err != nil {
			return inc("api_error", err.Error())
		}
		if is.State == "closed" && is.StateReason != "not_planned" {
			return pass("closed", fmt.Sprintf("issue #%d is closed", n))
		}
		if is.State == "closed" {
			return fail("not_planned", fmt.Sprintf("issue #%d was closed as not planned", n))
		}
		return inc("open", fmt.Sprintf("issue #%d is still open", n))
	}
}

func num(a spec.Assertion, k string) int {
	if n := a.Int(k, 0); n > 0 {
		return n
	}
	s := strings.TrimPrefix(a.Str(k), "#")
	n, _ := strconv.Atoi(s)
	return n
}
