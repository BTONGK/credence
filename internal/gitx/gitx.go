// Package gitx wraps the git commands Credence needs: repo discovery,
// commit inspection, clean worktrees and notes.
package gitx

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Run executes git in dir and returns trimmed stdout.
func Run(dir string, args ...string) (string, error) {
	return RunCtx(context.Background(), dir, args...)
}

// RunCtx executes git with a context.
func RunCtx(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return strings.TrimSpace(out.String()), fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return strings.TrimRight(out.String(), "\n"), nil
}

var rootCache sync.Map // dir -> root ("" if none)

// Root returns the top-level directory of the git repo containing dir, or "".
func Root(dir string) string {
	if dir == "" {
		return ""
	}
	if v, ok := rootCache.Load(dir); ok {
		return v.(string)
	}
	root := ""
	if st, err := os.Stat(dir); err == nil && st.IsDir() {
		if mayBeRepo(dir) {
			if out, err := Run(dir, "rev-parse", "--show-toplevel"); err == nil {
				root = out
			}
		}
	} else if err == nil {
		root = Root(filepath.Dir(dir))
	} else {
		// Path may not exist anymore; walk up to an existing parent.
		parent := filepath.Dir(dir)
		if parent != dir {
			root = Root(parent)
		}
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil && root != "" {
		root = resolved
	}
	rootCache.Store(dir, root)
	return root
}

// mayBeRepo reports a .git entry in dir or a parent below any
// GIT_CEILING_DIRECTORIES entry. Folders outside every repo skip running
// git, which on a Mac without developer tools opens an install prompt.
func mayBeRepo(dir string) bool {
	if os.Getenv("GIT_DIR") != "" {
		return true
	}
	ceilings := map[string]bool{}
	for _, c := range filepath.SplitList(os.Getenv("GIT_CEILING_DIRECTORIES")) {
		if c != "" {
			ceilings[filepath.Clean(c)] = true
		}
	}
	for d := filepath.Clean(dir); ; {
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			return true
		}
		parent := filepath.Dir(d)
		if parent == d || ceilings[parent] {
			return false
		}
		d = parent
	}
}

// Branch returns the current branch name of repo.
func Branch(repo string) string {
	out, err := Run(repo, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return ""
	}
	return out
}

// Head returns the full sha of HEAD.
func Head(repo string) string {
	out, _ := Run(repo, "rev-parse", "HEAD")
	return out
}

// ResolveCommit expands a (possibly short) sha or ref to a full commit sha.
func ResolveCommit(repo, ref string) (string, error) {
	if ref == "" {
		return "", errors.New("empty commit ref")
	}
	if !safeRef(ref) {
		return "", fmt.Errorf("unsafe ref %q", ref)
	}
	return Run(repo, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
}

var refRe = regexp.MustCompile(`^[A-Za-z0-9._/@^~{}-]+$`)

func safeRef(r string) bool { return refRe.MatchString(r) && !strings.HasPrefix(r, "-") }

// Parent returns the first parent of commit, or "" for a root commit.
func Parent(repo, commit string) string {
	out, err := Run(repo, "rev-parse", "--verify", "--quiet", commit+"^1")
	if err != nil {
		return ""
	}
	return out
}

// Commit describes one commit.
type Commit struct {
	SHA         string    `json:"sha"`
	Parents     []string  `json:"parents"`
	Author      string    `json:"author"`
	AuthorEmail string    `json:"author_email"`
	AuthorTime  time.Time `json:"author_time"`
	CommitTime  time.Time `json:"commit_time"`
	Subject     string    `json:"subject"`
	Body        string    `json:"body"`
	Files       []string  `json:"files"`
	Added       int       `json:"added"`
	Deleted     int       `json:"deleted"`
}

const commitFmt = "%H%x1f%P%x1f%an%x1f%ae%x1f%at%x1f%ct%x1f%s%x1f%b%x1e"

// ShowCommit returns metadata and changed files for one commit.
func ShowCommit(repo, ref string) (*Commit, error) {
	sha, err := ResolveCommit(repo, ref)
	if err != nil {
		return nil, err
	}
	out, err := Run(repo, "show", "-s", "--format="+commitFmt, sha)
	if err != nil {
		return nil, err
	}
	cs := parseCommits(out)
	if len(cs) == 0 {
		return nil, fmt.Errorf("commit %s not found", ref)
	}
	c := cs[0]
	stat, err := Run(repo, "show", "--numstat", "--format=", "--no-renames", sha)
	if err == nil {
		for _, line := range strings.Split(stat, "\n") {
			f := strings.SplitN(line, "\t", 3)
			if len(f) != 3 {
				continue
			}
			a, _ := strconv.Atoi(f[0])
			d, _ := strconv.Atoi(f[1])
			c.Added += a
			c.Deleted += d
			c.Files = append(c.Files, f[2])
		}
	}
	return &c, nil
}

func parseCommits(out string) []Commit {
	var res []Commit
	for _, rec := range strings.Split(out, "\x1e") {
		rec = strings.TrimLeft(rec, "\n")
		if strings.TrimSpace(rec) == "" {
			continue
		}
		f := strings.Split(rec, "\x1f")
		if len(f) < 8 {
			continue
		}
		at, _ := strconv.ParseInt(f[4], 10, 64)
		ct, _ := strconv.ParseInt(f[5], 10, 64)
		c := Commit{
			SHA: f[0], Author: f[2], AuthorEmail: f[3],
			AuthorTime: time.Unix(at, 0).UTC(), CommitTime: time.Unix(ct, 0).UTC(),
			Subject: f[6], Body: strings.TrimSpace(f[7]),
		}
		if f[1] != "" {
			c.Parents = strings.Fields(f[1])
		}
		res = append(res, c)
	}
	return res
}

// CommitsBetween lists commits on any local branch committed within [from, to].
func CommitsBetween(repo string, from, to time.Time) ([]Commit, error) {
	out, err := Run(repo, "log", "--all", "--no-merges", "--format="+commitFmt,
		"--since="+strconv.FormatInt(from.Unix(), 10), "--until="+strconv.FormatInt(to.Unix(), 10))
	if err != nil {
		return nil, err
	}
	cs := parseCommits(out)
	var res []Commit
	for _, c := range cs {
		if c.CommitTime.Before(from) || c.CommitTime.After(to) {
			continue
		}
		full, err := ShowCommit(repo, c.SHA)
		if err == nil {
			res = append(res, *full)
		}
	}
	return res, nil
}

// Exists reports whether commit exists in repo.
func Exists(repo, commit string) bool {
	_, err := ResolveCommit(repo, commit)
	return err == nil
}

// FileAt returns the content of path at commit.
func FileAt(repo, commit, path string) (string, error) {
	if strings.Contains(path, "..") {
		return "", fmt.Errorf("unsafe path %q", path)
	}
	return Run(repo, "show", commit+":"+strings.TrimPrefix(path, "./"))
}

// FileExistsAt reports whether path exists at commit.
func FileExistsAt(repo, commit, path string) bool {
	_, err := Run(repo, "cat-file", "-e", commit+":"+strings.TrimPrefix(path, "./"))
	return err == nil
}

// Diff returns the unified diff for one commit, optionally restricted to paths.
func Diff(repo, commit string, paths ...string) (string, error) {
	args := []string{"show", "--format=", "--no-color", "--unified=3", commit}
	if len(paths) > 0 {
		args = append(args, "--")
		args = append(args, paths...)
	}
	return Run(repo, args...)
}

// AddedLines maps file -> added line contents for a commit.
func AddedLines(repo, commit string) (map[string][]string, error) {
	d, err := Run(repo, "show", "--format=", "--no-color", "--unified=0", "--no-renames", commit)
	if err != nil {
		return nil, err
	}
	res := map[string][]string{}
	cur := ""
	sc := bufio.NewScanner(strings.NewReader(d))
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		l := sc.Text()
		switch {
		case strings.HasPrefix(l, "+++ "):
			cur = strings.TrimPrefix(strings.TrimPrefix(l, "+++ "), "b/")
			if cur == "/dev/null" {
				cur = ""
			}
		case strings.HasPrefix(l, "+") && cur != "":
			res[cur] = append(res[cur], l[1:])
		}
	}
	return res, nil
}

// IsAncestor reports whether a is an ancestor of b.
func IsAncestor(repo, a, b string) bool {
	_, err := Run(repo, "merge-base", "--is-ancestor", a, b)
	return err == nil
}

// BranchesContaining lists local branches that contain commit.
func BranchesContaining(repo, commit string) []string {
	out, err := Run(repo, "branch", "--format=%(refname:short)", "--contains", commit)
	if err != nil || out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// DefaultBranch guesses the repo's main branch.
func DefaultBranch(repo string) string {
	if out, err := Run(repo, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		return strings.TrimPrefix(out, "origin/")
	}
	for _, b := range []string{"main", "master", "trunk"} {
		if _, err := Run(repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+b); err == nil {
			return b
		}
	}
	return Branch(repo)
}

// RevertsOf finds commits whose message says they revert sha.
func RevertsOf(repo, sha string) []string {
	out, err := Run(repo, "log", "--all", "--format=%H", "--grep=This reverts commit "+sha)
	if err != nil || out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// RemoteURL returns the origin remote URL.
func RemoteURL(repo string) string {
	out, _ := Run(repo, "remote", "get-url", "origin")
	return out
}

var ghRe = regexp.MustCompile(`github\.com[:/]([^/]+)/([^/]+?)(?:\.git)?/?$`)

// GitHubRepo returns owner, name if origin points at GitHub.
func GitHubRepo(repo string) (string, string, bool) {
	m := ghRe.FindStringSubmatch(RemoteURL(repo))
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// UserName returns the configured git user.name.
func UserName(repo string) string {
	out, _ := Run(repo, "config", "user.name")
	return out
}

// Worktree is a clean, detached checkout of one commit.
type Worktree struct {
	Repo string
	Dir  string
	SHA  string
}

// AddWorktree creates a detached worktree of commit under base.
func AddWorktree(ctx context.Context, repo, commit, base string) (*Worktree, error) {
	sha, err := ResolveCommit(repo, commit)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(base, "wt-"+sha[:min(7, len(sha))]+"-")
	if err != nil {
		return nil, err
	}
	_ = os.Remove(dir) // git wants to create it
	if _, err := RunCtx(ctx, repo, "worktree", "add", "--detach", "--force", dir, sha); err != nil {
		return nil, err
	}
	return &Worktree{Repo: repo, Dir: dir, SHA: sha}, nil
}

// Remove deletes the worktree.
func (w *Worktree) Remove() {
	if w == nil {
		return
	}
	_, err := Run(w.Repo, "worktree", "remove", "--force", w.Dir)
	if err != nil {
		_ = os.RemoveAll(w.Dir)
		_, _ = Run(w.Repo, "worktree", "prune")
	}
}

// NotesRef is where Credence mirrors ledger entries.
const NotesRef = "refs/notes/credence"

// AppendNote appends text to the note on commit under NotesRef.
func AppendNote(repo, commit, text string) error {
	_, err := Run(repo, "notes", "--ref="+NotesRef, "append", "-m", text, commit)
	return err
}

// ReadNote returns the note on commit under NotesRef.
func ReadNote(repo, commit string) string {
	out, _ := Run(repo, "notes", "--ref="+NotesRef, "show", commit)
	return out
}

// ReadNoteRef returns the note on commit under an arbitrary notes ref.
func ReadNoteRef(repo, ref, commit string) string {
	out, _ := Run(repo, "notes", "--ref="+ref, "show", commit)
	return out
}

// CommitsInRange lists shas in base..head.
func CommitsInRange(repo, base, head string) ([]string, error) {
	out, err := Run(repo, "rev-list", base+".."+head)
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// Status returns porcelain status lines.
func Status(repo string) []string {
	out, _ := Run(repo, "status", "--porcelain")
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}
