// Package importers brings in data from other agent-provenance tools.
// Agent Trace records and git-ai authorship notes become line provenance
// (which contributor wrote each line of a commit); Entire checkpoints, OTLP
// telemetry and arbitrary chat transcripts become captured sessions.
//
// External formats are parsed leniently: unknown fields are ignored and
// records that cannot be tied to a local commit are skipped and counted.
package importers

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BTONGK/credence/adapters"
	"github.com/BTONGK/credence/daemon/attribution"
	"github.com/BTONGK/credence/daemon/ingest"
	"github.com/BTONGK/credence/daemon/store"
	"github.com/BTONGK/credence/internal/gitx"
	"github.com/BTONGK/credence/spec"
)

// KindLineProvenance is the artifact kind holding imported line provenance.
const KindLineProvenance = "line_provenance"

// Options configures an import.
type Options struct {
	Kind    string // agent-trace, git-ai, entire, otlp, transcript
	Path    string // file or directory; meaning depends on Kind
	Repo    string // repository the data belongs to
	Harness string // transcript: adapter to parse with (default generic)
	Store   *store.Store
	Ingest  *ingest.Engine
}

// Result summarizes an import.
type Result struct {
	Kind     string
	Records  int
	Commits  int
	Lines    int
	Skipped  int
	Sessions []string
	Notes    []string
}

func (r *Result) String() string {
	var parts []string
	if r.Records > 0 {
		parts = append(parts, fmt.Sprintf("%d records", r.Records))
	}
	if r.Commits > 0 || r.Lines > 0 {
		parts = append(parts, fmt.Sprintf("line provenance for %d lines in %d commits", r.Lines, r.Commits))
	}
	if len(r.Sessions) > 0 {
		parts = append(parts, fmt.Sprintf("%d sessions", len(r.Sessions)))
	}
	if r.Skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d skipped", r.Skipped))
	}
	if len(parts) == 0 {
		parts = append(parts, "nothing to import")
	}
	s := "imported " + r.Kind + ": " + strings.Join(parts, ", ")
	for _, n := range r.Notes {
		s += "\n  " + n
	}
	return s
}

func (r *Result) note(format string, args ...any) {
	if len(r.Notes) < 20 {
		r.Notes = append(r.Notes, fmt.Sprintf(format, args...))
	}
}

// Kinds lists the supported import kinds.
var Kinds = []string{"agent-trace", "git-ai", "entire", "otlp", "transcript"}

// Run performs an import.
func Run(ctx context.Context, o Options) (*Result, error) {
	if o.Repo == "" {
		o.Repo = "."
	}
	abs, err := filepath.Abs(o.Repo)
	if err != nil {
		return nil, err
	}
	if root := gitx.Root(abs); root != "" {
		o.Repo = root
	} else if o.Kind != "otlp" && o.Kind != "transcript" {
		return nil, fmt.Errorf("%s is not a git repository (use --repo)", o.Repo)
	} else {
		o.Repo = ""
	}
	switch o.Kind {
	case "agent-trace", "agenttrace":
		return AgentTrace(ctx, o)
	case "git-ai", "gitai":
		return GitAI(ctx, o)
	case "entire":
		return Entire(ctx, o)
	case "otlp":
		return OTLP(ctx, o)
	case "transcript":
		return Transcript(ctx, o)
	}
	return nil, fmt.Errorf("unknown import kind %q (one of %s)", o.Kind, strings.Join(Kinds, ", "))
}

// provenance accumulates line hashes per commit.
type provenance map[string]map[string]string // commit -> line hash -> contributor

func (p provenance) add(commit, hash, who string) bool {
	if hash == "" || who == "" {
		return false
	}
	m := p[commit]
	if m == nil {
		m = map[string]string{}
		p[commit] = m
	}
	if _, ok := m[hash]; ok {
		return false
	}
	m[hash] = who
	return true
}

// addLines hashes lines [from, to] (1-based, inclusive) of path at commit.
func (p provenance) addLines(file []string, commit string, from, to int, who string) int {
	if from < 1 {
		from = 1
	}
	if to > len(file) {
		to = len(file)
	}
	n := 0
	for i := from; i <= to; i++ {
		if p.add(commit, adapters.LineHash(file[i-1]), who) {
			n++
		}
	}
	return n
}

// save stores one line_provenance artifact per commit.
func (p provenance) save(st *store.Store, repo, source string, r *Result) error {
	commits := make([]string, 0, len(p))
	for c := range p {
		commits = append(commits, c)
	}
	sort.Strings(commits)
	for _, c := range commits {
		meta, err := json.Marshal(attribution.ImportedProvenance{Source: source, Commit: c, Lines: p[c]})
		if err != nil {
			return err
		}
		a := &spec.Artifact{
			Kind:        KindLineProvenance,
			Locator:     "provenance:" + source + ":" + repo + "@" + c,
			ContentHash: spec.HashBytes(meta),
			Repo:        repo,
			Meta:        meta,
			CreatedAt:   time.Now().UTC(),
		}
		if err := st.UpsertArtifact(a); err != nil {
			return err
		}
		r.Commits++
		r.Lines += len(p[c])
	}
	return nil
}

// fileCache reads files at commits once.
type fileCache struct {
	repo  string
	files map[string][]string
}

func newFileCache(repo string) *fileCache {
	return &fileCache{repo: repo, files: map[string][]string{}}
}

func (f *fileCache) lines(commit, path string) ([]string, bool) {
	k := commit + ":" + path
	if l, ok := f.files[k]; ok {
		return l, l != nil
	}
	text, err := gitx.FileAt(f.repo, commit, path)
	if err != nil {
		f.files[k] = nil
		return nil, false
	}
	l := strings.Split(text, "\n")
	f.files[k] = l
	return l, true
}

// agentID builds a contributor id for an imported agent.
func agentID(tool, model string) string {
	tool = ingest.Slug(tool)
	if tool == "" {
		tool = "unknown"
	}
	if i := strings.LastIndex(model, "/"); i >= 0 {
		model = model[i+1:]
	}
	model = strings.TrimSpace(model)
	if model == "" {
		model = "unknown"
	}
	return "agent:" + tool + "/" + model
}

// parseRanges parses "1-10,15,20-22" into inclusive pairs.
func parseRanges(s string) [][2]int {
	var out [][2]int
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		var a, b int
		if strings.Contains(part, "-") {
			if _, err := fmt.Sscanf(part, "%d-%d", &a, &b); err != nil {
				continue
			}
		} else {
			if _, err := fmt.Sscanf(part, "%d", &a); err != nil {
				continue
			}
			b = a
		}
		if a > 0 && b >= a {
			out = append(out, [2]int{a, b})
		}
	}
	return out
}
