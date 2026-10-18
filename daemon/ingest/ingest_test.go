package ingest

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BTONGK/credence/adapters"
	_ "github.com/BTONGK/credence/adapters/claudecode"
	"github.com/BTONGK/credence/daemon/store"
	"github.com/BTONGK/credence/internal/config"
	"github.com/BTONGK/credence/spec"
)

func gitInit(t *testing.T, dir string) string {
	t.Helper()
	run := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Maya", "GIT_AUTHOR_EMAIL=maya@acme.io", "GIT_COMMITTER_NAME=Maya", "GIT_COMMITTER_EMAIL=maya@acme.io")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q", "-b", "main")
	run("config", "user.name", "Maya")
	os.WriteFile(filepath.Join(dir, "a.py"), []byte("x = 1\n"), 0o644)
	run("add", ".")
	run("commit", "-q", "-m", "init")
	os.WriteFile(filepath.Join(dir, "a.py"), []byte("x = 2\n"), 0o644)
	run("commit", "-q", "-am", "fix x")
	return run("rev-parse", "--short", "HEAD")
}

func line(v map[string]any) string {
	b, _ := json.Marshal(v)
	return string(b) + "\n"
}

func TestIngestClaudeTranscript(t *testing.T) {
	t.Setenv("CREDENCE_HOME", t.TempDir())
	cfgDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfgDir)
	repo := t.TempDir()
	repo, _ = filepath.EvalSymlinks(repo)
	sha := gitInit(t, repo)

	st, err := store.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	eng := New(st, config.Default(), nil, []byte("k"), time.Time{})

	sid := "11111111-2222-3333-4444-555555555555"
	tp := filepath.Join(cfgDir, "projects", "-repo", sid+".jsonl")
	os.MkdirAll(filepath.Dir(tp), 0o755)
	base := map[string]any{"sessionId": sid, "cwd": repo, "gitBranch": "main", "version": "2.1.302"}
	rec := func(extra map[string]any) map[string]any {
		m := map[string]any{}
		for k, v := range base {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	content := line(rec(map[string]any{"type": "user", "timestamp": "2026-10-17T20:00:00Z", "message": map[string]any{"role": "user", "content": "fix the flaky test; token sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789"}})) +
		line(rec(map[string]any{"type": "assistant", "timestamp": "2026-10-17T20:00:05Z", "message": map[string]any{"id": "msg_1", "model": "claude-opus-5-5", "usage": map[string]any{"input_tokens": 100, "output_tokens": 50},
			"content": []any{map[string]any{"type": "tool_use", "id": "toolu_1", "name": "Bash", "input": map[string]any{"command": "python -m pytest tests/test_a.py"}}}}})) +
		line(rec(map[string]any{"type": "user", "timestamp": "2026-10-17T20:00:09Z", "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "toolu_1", "is_error": true, "content": "Exit code 1\n1 failed, 3 passed in 0.2s"}}}})) +
		line(rec(map[string]any{"type": "assistant", "timestamp": "2026-10-17T20:01:00Z", "message": map[string]any{"id": "msg_2", "model": "claude-opus-5-5", "usage": map[string]any{"input_tokens": 10, "output_tokens": 5},
			"content": []any{map[string]any{"type": "tool_use", "id": "toolu_2", "name": "Bash", "input": map[string]any{"command": "git commit -am 'fix x'"}}}}})) +
		line(rec(map[string]any{"type": "user", "timestamp": "2026-10-17T20:01:02Z", "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "toolu_2", "content": "[main " + sha + "] fix x\n 1 file changed"}}}}))
	os.WriteFile(tp, []byte(content), 0o644)

	a, _ := adapters.Get("claude-code")
	if err := eng.ProcessFile(context.Background(), tp, a, true); err != nil {
		t.Fatal(err)
	}
	s, err := st.GetSession(SessionID("claude-code", sid))
	if err != nil {
		t.Fatal(err)
	}
	if s.Repo != repo || s.Skipped || s.Tokens.Input != 110 || !strings.HasPrefix(s.AgentConfig, "agent:claude-code/claude-opus-5-5@cfg_") {
		t.Fatalf("bad session: repo=%q skipped=%v reason=%s tokens=%+v cfg=%s", s.Repo, s.Skipped, s.SkipReason, s.Tokens, s.AgentConfig)
	}
	evs, _ := st.Events(s.ID, 0, 0)
	kinds := []string{}
	for _, e := range evs {
		kinds = append(kinds, e.Kind)
	}
	want := "session_start,prompt,command,tool_result,command,tool_result,commit"
	if strings.Join(kinds, ",") != want {
		t.Fatalf("kinds = %v", kinds)
	}
	if strings.Contains(string(evs[1].Payload), "sk-ant-api03") {
		t.Fatalf("secret not redacted: %s", evs[1].Payload)
	}
	var tr map[string]any
	json.Unmarshal(evs[3].Payload, &tr)
	if tr["exit_code"].(float64) != 1 || tr["test_outcome"].(map[string]any)["failed"].(float64) != 1 {
		t.Fatalf("bad tool result: %s", evs[3].Payload)
	}
	prev := ""
	for _, e := range evs {
		if e.Prev != prev || e.ChainHash() != e.Hash {
			t.Fatalf("broken chain at %d", e.Seq)
		}
		prev = e.Hash
	}
	arts, _ := st.SessionArtifacts(s.ID)
	if len(arts) != 1 || arts[0].Kind != "commit" {
		t.Fatalf("commit artifact missing: %+v", arts)
	}
	// Re-processing an unchanged file is a no-op; a rewrite is detected.
	if err := eng.ProcessFile(context.Background(), tp, a, true); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.Events(s.ID, 0, 0); len(n) != len(evs) {
		t.Fatalf("duplicate events on reprocess")
	}
	os.WriteFile(tp, []byte(strings.Replace(content, "1 failed, 3 passed", "4 passed!!!!!!!!!", 1)), 0o644)
	bad, _ := eng.VerifyTranscript(tp)
	if len(bad) != 1 || bad[0] != 3 {
		t.Fatalf("tamper detection: %v", bad)
	}
	_ = spec.KindCommit
}

func TestSessionOutsideRepoIsReported(t *testing.T) {
	t.Setenv("CREDENCE_HOME", t.TempDir())
	cfgDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfgDir)
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	st, err := store.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	eng := New(st, config.Default(), nil, []byte("k"), time.Time{})
	sid := "22222222-3333-4444-5555-666666666666"
	tp := filepath.Join(cfgDir, "projects", "-notes", sid+".jsonl")
	os.MkdirAll(filepath.Dir(tp), 0o755)
	base := map[string]any{"sessionId": sid, "cwd": dir, "version": "2.1.302"}
	rec := func(extra map[string]any) string {
		m := map[string]any{}
		for k, v := range base {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return line(m)
	}
	os.WriteFile(tp, []byte(rec(map[string]any{"type": "user", "timestamp": "2026-10-17T20:00:00Z", "message": map[string]any{"role": "user", "content": "tidy my notes"}})+
		rec(map[string]any{"type": "assistant", "timestamp": "2026-10-17T20:00:05Z", "message": map[string]any{"id": "msg_1", "model": "claude-opus-5-5",
			"content": []any{map[string]any{"type": "text", "text": "I updated `notes.md`."}}}})), 0o644)
	a, _ := adapters.Get("claude-code")
	if err := eng.ProcessFile(context.Background(), tp, a, true); err != nil {
		t.Fatal(err)
	}
	s, err := st.GetSession(SessionID("claude-code", sid))
	if err != nil {
		t.Fatal(err)
	}
	if s.Repo != "" || s.Skipped {
		t.Fatalf("session outside a repo: repo=%q skipped=%v reason=%s", s.Repo, s.Skipped, s.SkipReason)
	}
}
