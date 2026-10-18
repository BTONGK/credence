package importers

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/BTONGK/credence/internal/gitx"
)

// git-ai stores an authorship log per commit in refs/notes/ai: an
// attestation section listing, per file, which prompt wrote which lines,
// then "---" and JSON metadata describing each prompt's agent:
//
//	src/main.rs
//	  4f2a9c1e0b7d3a65 1-10,15
//	---
//	{"schema_version":"authorship/3.0.0",
//	 "prompts":{"4f2a9c1e0b7d3a65":{"agent_id":{"tool":"claude","model":"…"}}}}

type gitAIMeta struct {
	Prompts map[string]struct {
		AgentID struct {
			Tool  string `json:"tool"`
			Model string `json:"model"`
		} `json:"agent_id"`
	} `json:"prompts"`
}

// GitAI imports git-ai authorship notes. Path optionally names the notes
// ref (default "ai").
func GitAI(ctx context.Context, o Options) (*Result, error) {
	r := &Result{Kind: "git-ai"}
	ref := o.Path
	if ref == "" {
		ref = "ai"
	}
	out, err := gitx.Run(o.Repo, "notes", "--ref="+ref, "list")
	if err != nil {
		r.note("no git-ai notes (refs/notes/%s); run `git fetch origin refs/notes/%s:refs/notes/%s` if they live on the remote", ref, ref, ref)
		return r, nil
	}
	prov := provenance{}
	cache := newFileCache(o.Repo)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		commit := f[1]
		r.Records++
		files, meta := parseAuthorshipLog(gitx.ReadNoteRef(o.Repo, ref, commit))
		if len(files) == 0 {
			r.Skipped++
			continue
		}
		for path, entries := range files {
			lines, ok := cache.lines(commit, path)
			if !ok {
				continue
			}
			for _, e := range entries {
				who := agentID("git-ai", "")
				if p, ok := meta.Prompts[e.prompt]; ok {
					who = agentID(orStr(p.AgentID.Tool, "git-ai"), p.AgentID.Model)
				}
				for _, rg := range e.ranges {
					prov.addLines(lines, commit, rg[0], rg[1], who)
				}
			}
		}
	}
	if err := prov.save(o.Store, o.Repo, "git-ai", r); err != nil {
		return nil, err
	}
	return r, nil
}

type authorshipEntry struct {
	prompt string
	ranges [][2]int
}

// parseAuthorshipLog splits a git-ai note into per-file entries and its
// JSON metadata.
func parseAuthorshipLog(text string) (map[string][]authorshipEntry, gitAIMeta) {
	files := map[string][]authorshipEntry{}
	var meta gitAIMeta
	attest, js, found := strings.Cut(text, "\n---\n")
	if !found {
		if a, j, ok := strings.Cut(text, "\n---"); ok {
			attest, js = a, j
		}
	}
	_ = json.Unmarshal([]byte(strings.TrimSpace(js)), &meta)
	cur := ""
	for _, line := range strings.Split(attest, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if line[0] != ' ' && line[0] != '\t' {
			cur = strings.TrimSpace(line)
			if u, err := strconv.Unquote(cur); err == nil {
				cur = u
			}
			continue
		}
		f := strings.Fields(line)
		if cur == "" || len(f) < 2 {
			continue
		}
		files[cur] = append(files[cur], authorshipEntry{prompt: f[0], ranges: parseRanges(strings.Join(f[1:], ""))})
	}
	return files, meta
}
