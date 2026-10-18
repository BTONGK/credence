package importers

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/BTONGK/credence/adapters"
	"github.com/BTONGK/credence/adapters/claudecode"
	"github.com/BTONGK/credence/adapters/codex"
	"github.com/BTONGK/credence/adapters/generic"
	"github.com/BTONGK/credence/internal/gitx"
	"github.com/BTONGK/credence/internal/home"
)

// EntireBranch is where Entire keeps session checkpoints.
const EntireBranch = "entire/checkpoints/v1"

// Entire imports agent transcripts saved by Entire on its checkpoints
// branch. Each checkpoint directory holds a transcript (*.jsonl) and
// usually a metadata.json naming the agent and session. Checkpoints of the
// same session are cumulative, so only the longest transcript per session
// is imported. Path optionally overrides the branch.
func Entire(ctx context.Context, o Options) (*Result, error) {
	r := &Result{Kind: "entire"}
	branch := o.Path
	if branch == "" {
		branch = EntireBranch
	}
	if _, err := gitx.ResolveCommit(o.Repo, branch); err != nil {
		if _, err2 := gitx.ResolveCommit(o.Repo, "origin/"+branch); err2 != nil {
			r.note("no %s branch; run `git fetch origin %s:%s` if it lives on the remote", branch, branch, branch)
			return r, nil
		}
		branch = "origin/" + branch
	}
	list, err := gitx.Run(o.Repo, "ls-tree", "-r", "--name-only", branch)
	if err != nil {
		return nil, err
	}
	type checkpoint struct {
		dir, transcript, agent, session string
		size                            int
	}
	byDir := map[string]*checkpoint{}
	for _, p := range strings.Split(list, "\n") {
		dir, base := path.Split(p)
		cp := byDir[dir]
		if cp == nil {
			cp = &checkpoint{dir: dir}
			byDir[dir] = cp
		}
		switch {
		case strings.HasSuffix(base, ".jsonl"):
			// Prefer the full transcript over partial ones.
			if cp.transcript == "" || base == "full.jsonl" {
				cp.transcript = p
			}
		case base == "metadata.json":
			raw, err := gitx.Run(o.Repo, "show", branch+":"+p)
			if err != nil {
				continue
			}
			var m map[string]any
			if json.Unmarshal([]byte(raw), &m) == nil {
				cp.agent = adapters.Str(m, "agent", "agent_type", "agent_name", "tool")
				cp.session = adapters.Str(m, "session_id", "sessionId", "agent_session_id")
			}
		}
	}
	best := map[string]*checkpoint{}
	var order []string
	for _, cp := range byDir {
		if cp.transcript == "" {
			continue
		}
		r.Records++
		sz, err := gitx.Run(o.Repo, "cat-file", "-s", branch+":"+cp.transcript)
		if err != nil {
			r.Skipped++
			continue
		}
		cp.size, _ = strconv.Atoi(sz)
		key := cp.session
		if key == "" {
			key = strings.Trim(cp.dir, "/")
		}
		if prev := best[key]; prev == nil {
			order = append(order, key)
			best[key] = cp
		} else if cp.size > prev.size {
			best[key] = cp
		}
	}
	sort.Strings(order)
	dst := home.Sub(filepath.Join("imports", "entire"))
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return nil, err
	}
	for _, key := range order {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		cp := best[key]
		raw, err := gitx.Run(o.Repo, "show", branch+":"+cp.transcript)
		if err != nil {
			r.Skipped++
			continue
		}
		name := adapterForAgent(cp.agent, []byte(raw))
		a, ok := adapters.Get(name)
		if !ok {
			r.Skipped++
			continue
		}
		data := withCwd([]byte(raw+"\n"), o.Repo, name == generic.Name)
		file := filepath.Join(dst, safeName(key)+".jsonl")
		if err := os.WriteFile(file, data, 0o600); err != nil {
			return nil, err
		}
		sids, err := ingestFile(ctx, o, file, a)
		if err != nil {
			r.Skipped++
			r.note("%s: %v", cp.transcript, err)
			continue
		}
		r.Sessions = append(r.Sessions, sids...)
	}
	return r, nil
}

// adapterForAgent picks the transcript parser for an Entire agent name,
// falling back to sniffing the first record.
func adapterForAgent(agent string, raw []byte) string {
	a := strings.ToLower(agent)
	switch {
	case strings.Contains(a, "claude"):
		return claudecode.Name
	case strings.Contains(a, "codex"):
		return codex.Name
	}
	first, _, _ := bytes.Cut(raw, []byte("\n"))
	var m map[string]any
	if json.Unmarshal(first, &m) == nil {
		if _, ok := m["sessionId"]; ok {
			return claudecode.Name
		}
		if t := adapters.Str(m, "type"); t == "session_meta" || t == "response_item" {
			return codex.Name
		}
	}
	return generic.Name
}

// withCwd fills in the working directory on records whose cwd is missing
// or does not exist on this machine, so sessions attach to repo.
func withCwd(data []byte, repo string, always bool) []byte {
	if repo == "" {
		return data
	}
	var out bytes.Buffer
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var m map[string]any
		if json.Unmarshal(line, &m) != nil {
			out.Write(line)
			out.WriteByte('\n')
			continue
		}
		cwd, _ := m["cwd"].(string)
		_, statErr := os.Stat(cwd)
		if (always && cwd == "") || (cwd != "" && statErr != nil) {
			m["cwd"] = repo
			if b, err := json.Marshal(m); err == nil {
				line = b
			}
		}
		out.Write(line)
		out.WriteByte('\n')
	}
	return out.Bytes()
}

func safeName(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		}
		return '-'
	}, s)
	s = strings.Trim(s, "-.")
	if s == "" {
		s = "session"
	}
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}

// ingestFile captures a transcript and returns the sessions it touched.
func ingestFile(ctx context.Context, o Options, file string, a adapters.Adapter) ([]string, error) {
	if err := o.Ingest.ProcessFile(ctx, file, a, true); err != nil {
		return nil, err
	}
	ts, err := o.Store.GetTranscript(file)
	if err != nil {
		return nil, err
	}
	if ts.SessionID == "" {
		return nil, nil
	}
	return []string{ts.SessionID}, nil
}
