package importers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/BTONGK/credence/internal/gitx"
)

// Agent Trace (agent-trace.dev) records attribute line ranges of files at
// a VCS revision to conversations and their contributors:
//
//	{"version":"0.1","vcs":{"type":"git","revision":"<sha>"},
//	 "tool":{"name":"cursor"},
//	 "files":[{"path":"src/x.ts","conversations":[{"url":"…",
//	   "contributor":{"type":"ai","model_id":"anthropic/claude-…"},
//	   "ranges":[{"start_line":1,"end_line":20}]}]}]}

type atContributor struct {
	Type    string `json:"type"` // ai, human, mixed, unknown
	ModelID string `json:"model_id"`
}

type atRecord struct {
	ID  string `json:"id"`
	VCS struct {
		Type     string `json:"type"`
		Revision string `json:"revision"`
	} `json:"vcs"`
	Tool struct {
		Name string `json:"name"`
	} `json:"tool"`
	Files []struct {
		Path          string `json:"path"`
		Conversations []struct {
			Contributor atContributor `json:"contributor"`
			Ranges      []struct {
				StartLine   int            `json:"start_line"`
				EndLine     int            `json:"end_line"`
				Contributor *atContributor `json:"contributor"`
			} `json:"ranges"`
		} `json:"conversations"`
	} `json:"files"`
}

// AgentTrace imports Agent Trace records from a file (JSON, JSON array or
// JSONL) or a directory of them (default <repo>/.agent-trace).
func AgentTrace(ctx context.Context, o Options) (*Result, error) {
	r := &Result{Kind: "agent-trace"}
	path := o.Path
	if path == "" {
		path = filepath.Join(o.Repo, ".agent-trace")
	}
	var files []string
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && (strings.HasSuffix(p, ".json") || strings.HasSuffix(p, ".jsonl")) {
				files = append(files, p)
			}
			return nil
		})
	} else {
		files = []string{path}
	}
	human := o.Ingest.Human(o.Repo)
	prov := provenance{}
	cache := newFileCache(o.Repo)
	for _, f := range files {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		recs, err := readATRecords(f)
		if err != nil {
			r.Skipped++
			r.note("%s: %v", f, err)
			continue
		}
		for _, rec := range recs {
			r.Records++
			if rec.VCS.Type != "" && rec.VCS.Type != "git" {
				r.Skipped++
				continue
			}
			sha, err := gitx.ResolveCommit(o.Repo, rec.VCS.Revision)
			if rec.VCS.Revision == "" || err != nil {
				r.Skipped++
				r.note("record %s: revision %q not found in %s", orStr(rec.ID, "?"), rec.VCS.Revision, o.Repo)
				continue
			}
			for _, file := range rec.Files {
				lines, ok := cache.lines(sha, file.Path)
				if !ok {
					continue
				}
				for _, conv := range file.Conversations {
					for _, rg := range conv.Ranges {
						c := conv.Contributor
						if rg.Contributor != nil {
							c = *rg.Contributor
						}
						who := ""
						switch c.Type {
						case "ai", "mixed":
							who = agentID(rec.Tool.Name, c.ModelID)
						case "human":
							who = human
						}
						if who != "" {
							prov.addLines(lines, sha, rg.StartLine, rg.EndLine, who)
						}
					}
				}
			}
		}
	}
	if err := prov.save(o.Store, o.Repo, "agent-trace", r); err != nil {
		return nil, err
	}
	return r, nil
}

// readATRecords decodes a stream of JSON values, each a record or an array.
func readATRecords(path string) ([]atRecord, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	var out []atRecord
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return out, err
		}
		raw = bytes.TrimSpace(raw)
		if len(raw) > 0 && raw[0] == '[' {
			var arr []atRecord
			if err := json.Unmarshal(raw, &arr); err != nil {
				return out, err
			}
			out = append(out, arr...)
			continue
		}
		var rec atRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			return out, err
		}
		out = append(out, rec)
	}
	return out, nil
}

func orStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
