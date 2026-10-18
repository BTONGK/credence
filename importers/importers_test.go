package importers

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/BTONGK/credence/adapters"
	_ "github.com/BTONGK/credence/adapters/generic"
	"github.com/BTONGK/credence/daemon/attribution"
	"github.com/BTONGK/credence/daemon/ingest"
	"github.com/BTONGK/credence/daemon/store"
	"github.com/BTONGK/credence/internal/config"
)

func setup(t *testing.T) (string, string, Options) {
	t.Helper()
	t.Setenv("CREDENCE_HOME", t.TempDir())
	repo, _ := filepath.EvalSymlinks(t.TempDir())
	git := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Maya", "GIT_AUTHOR_EMAIL=m@x.io", "GIT_COMMITTER_NAME=Maya", "GIT_COMMITTER_EMAIL=m@x.io")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(repo, "app.py"), []byte("def handler(event):\n    return process(event)\n\ndef process(event):\n    return event['body']\n"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "add handler")
	sha := git("rev-parse", "HEAD")
	note := "app.py\n  4f2a9c1e0b7d3a65 1-2\n---\n" + `{"schema_version":"authorship/3.0.0","prompts":{"4f2a9c1e0b7d3a65":{"agent_id":{"tool":"cursor","model":"claude-opus-5-5"}}}}`
	git("notes", "--ref=ai", "add", "-m", note, sha)
	st, err := store.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := config.Default()
	cfg.Human = "maya"
	eng := ingest.New(st, cfg, nil, []byte("k"), time.Time{})
	return repo, sha, Options{Repo: repo, Store: st, Ingest: eng}
}

func provOf(t *testing.T, o Options) attribution.Provenance {
	t.Helper()
	arts, err := o.Store.ArtifactsByKind(KindLineProvenance, o.Repo)
	if err != nil {
		t.Fatal(err)
	}
	p := attribution.Provenance{}
	p.Merge(arts)
	return p
}

func TestGitAI(t *testing.T) {
	_, _, o := setup(t)
	o.Kind = "git-ai"
	r, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if r.Commits != 1 || r.Lines != 2 {
		t.Fatalf("result: %s", r)
	}
	p := provOf(t, o)
	if who := p[adapters.LineHash("def handler(event):")]; who != "agent:cursor/claude-opus-5-5" {
		t.Fatalf("line 1 attributed to %q", who)
	}
	if _, ok := p[adapters.LineHash("return event['body']")]; ok {
		t.Fatal("unattributed line should not be imported")
	}
}

func TestAgentTrace(t *testing.T) {
	repo, sha, o := setup(t)
	rec := map[string]any{"version": "0.1.0", "id": "t1", "vcs": map[string]any{"type": "git", "revision": sha}, "tool": map[string]any{"name": "Claude Code"},
		"files": []any{map[string]any{"path": "app.py", "conversations": []any{
			map[string]any{"contributor": map[string]any{"type": "ai", "model_id": "anthropic/claude-opus-5-5"}, "ranges": []any{map[string]any{"start_line": 4, "end_line": 5}}},
			map[string]any{"contributor": map[string]any{"type": "human"}, "ranges": []any{map[string]any{"start_line": 1, "end_line": 1}}},
		}}}}
	b, _ := json.Marshal(rec)
	dir := filepath.Join(repo, ".agent-trace")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "t1.jsonl"), append(b, '\n'), 0o644)
	o.Kind = "agent-trace"
	r, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if r.Records != 1 || r.Lines != 3 {
		t.Fatalf("result: %s", r)
	}
	p := provOf(t, o)
	if who := p[adapters.LineHash("return event['body']")]; who != "agent:claude-code/claude-opus-5-5" {
		t.Fatalf("ai line attributed to %q", who)
	}
	if who := p[adapters.LineHash("def handler(event):")]; who != "human:maya" {
		t.Fatalf("human line attributed to %q", who)
	}
}

func TestOTLPLogs(t *testing.T) {
	repo, _, o := setup(t)
	kv := func(k string, v any) map[string]any {
		switch x := v.(type) {
		case string:
			return map[string]any{"key": k, "value": map[string]any{"stringValue": x}}
		case int:
			return map[string]any{"key": k, "value": map[string]any{"intValue": strconv.Itoa(x)}}
		}
		return nil
	}
	ns := func(sec int) string { return strconv.Itoa(1792000000+sec) + "000000000" }
	logs := map[string]any{"resourceLogs": []any{map[string]any{
		"resource": map[string]any{"attributes": []any{kv("service.name", "claude-code"), kv("credence.repo", repo)}},
		"scopeLogs": []any{map[string]any{"logRecords": []any{
			map[string]any{"timeUnixNano": ns(0), "body": map[string]any{"stringValue": "claude_code.user_prompt"}, "attributes": []any{kv("event.name", "user_prompt"), kv("session.id", "s-1"), kv("prompt", "fix the flaky test")}},
			map[string]any{"timeUnixNano": ns(5), "body": map[string]any{"stringValue": "claude_code.tool_result"}, "attributes": []any{kv("session.id", "s-1"), kv("tool_name", "Bash"), kv("success", "false"),
				kv("tool_parameters", `{"bash_command":"python3 -m pytest tests"}`)}},
			map[string]any{"timeUnixNano": ns(6), "body": map[string]any{"stringValue": "claude_code.api_request"}, "attributes": []any{kv("session.id", "s-1"), kv("model", "claude-opus-5-5"), kv("input_tokens", 1200), kv("output_tokens", 300)}},
		}}},
	}}}
	b, _ := json.Marshal(logs)
	path := filepath.Join(t.TempDir(), "logs.json")
	os.WriteFile(path, b, 0o644)
	o.Kind, o.Path = "otlp", path
	r, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Sessions) != 1 {
		t.Fatalf("result: %s", r)
	}
	s, err := o.Store.GetSession(r.Sessions[0])
	if err != nil {
		t.Fatal(err)
	}
	if s.Harness != "claude-code" || s.Repo != repo || s.Tokens.Input != 1200 {
		t.Fatalf("session: harness=%s repo=%s tokens=%+v", s.Harness, s.Repo, s.Tokens)
	}
	evs, _ := o.Store.Events(s.ID, 0, 0)
	var kinds []string
	for _, e := range evs {
		kinds = append(kinds, e.Kind)
	}
	if got := strings.Join(kinds, ","); !strings.Contains(got, "prompt,command,tool_result") {
		t.Fatalf("kinds = %s", got)
	}
	// Importing the same file again adds nothing.
	r2, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	evs2, _ := o.Store.Events(s.ID, 0, 0)
	if len(evs2) != len(evs) {
		t.Fatalf("re-import duplicated events: %d -> %d (%s)", len(evs), len(evs2), r2)
	}
}
