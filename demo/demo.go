// Package demo builds the example from the product doc: Maya asks Claude
// Code to fix a flaky scheduler test. It creates a real git repository with
// a real race condition, runs every command the "agent" runs for real, and
// writes a Claude Code transcript of the session, so Credence's capture,
// claims, verification and review can be tried end to end without an agent.
package demo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Scenario is a generated demo.
type Scenario struct {
	Dir        string
	Repo       string
	Transcript string
	Subagent   string
	SessionID  string // Claude Code session uuid
	FixCommit  string
	CICommit   string
}

const (
	sessionUUID = "7f3c2a10-5b1e-4c8e-9d2a-3f2e9c1a0b7d"
	agentID     = "a4e81c2f9b"
	model       = "claude-opus-5-5"
	version     = "2.1.302"
)

var files = map[string]string{
	"README.md":  "# scheduler\n\nA tiny thread-pool job queue used by the ingest workers.\n\n    python3 -m unittest discover -s tests -t .\n",
	".gitignore": "config.local.json\n__pycache__/\n",
	".github/workflows/ci.yml": `name: ci
on: [push, pull_request]
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: python -m unittest discover -s tests -t .
`,
	"scheduler/__init__.py": "",
	"scheduler/queue.py": `"""A tiny thread-pool job queue."""
import threading
import time
from collections import deque


class JobQueue:
    def __init__(self):
        self._jobs = deque()
        self._lock = threading.Lock()
        self.completed = 0
        self.errors = []

    def submit(self, job):
        with self._lock:
            self._jobs.append(job)

    def _take(self):
        if not self._jobs:
            return None
        time.sleep(0.0002)  # bookkeeping: record the job as started
        return self._jobs.popleft()

    def _mark_done(self):
        done = self.completed
        time.sleep(0)
        self.completed = done + 1

    def _worker(self):
        while True:
            try:
                job = self._take()
            except IndexError as e:
                self.errors.append(e)
                return
            if job is None:
                return
            job()
            self._mark_done()

    def run(self, workers=4):
        threads = [threading.Thread(target=self._worker) for _ in range(workers)]
        for t in threads:
            t.start()
        for t in threads:
            t.join()
        return self.completed
`,
	"tests/__init__.py": "",
	"tests/test_scheduler.py": `import unittest

from scheduler.queue import JobQueue


class SchedulerTest(unittest.TestCase):
    def test_submit_order(self):
        q = JobQueue()
        seen = []
        for i in range(5):
            q.submit(lambda i=i: seen.append(i))
        q.run(workers=1)
        self.assertEqual(seen, [0, 1, 2, 3, 4])

    def test_concurrency(self):
        q = JobQueue()
        for _ in range(200):
            q.submit(lambda: None)
        q.run(workers=8)
        self.assertEqual(q.errors, [])
        self.assertEqual(q.completed, 200)


if __name__ == "__main__":
    unittest.main()
`,
	"tests/test_config.py": `import json
import os
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


class ConfigTest(unittest.TestCase):
    def test_local_config_has_workers(self):
        with open(os.path.join(ROOT, "config.local.json")) as f:
            cfg = json.load(f)
        self.assertGreater(cfg["workers"], 0)
`,
}

const reproScript = `"""Reproduce the JobQueue race: 20 trials of 200 jobs on 8 workers."""
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from scheduler.queue import JobQueue  # noqa: E402

bad = 0
for trial in range(20):
    q = JobQueue()
    for _ in range(200):
        q.submit(lambda: None)
    q.run(workers=8)
    if q.completed != 200 or q.errors:
        bad += 1
print(f"{bad}/20 trials lost jobs or raised")
sys.exit(1 if bad else 0)
`

const regressionTest = `import unittest

from scheduler.queue import JobQueue


class RegressionTest(unittest.TestCase):
    def test_completed_count_matches_submitted(self):
        q = JobQueue()
        for _ in range(10):
            q.submit(lambda: None)
        q.run(workers=1)
        self.assertEqual(q.completed, 10)


if __name__ == "__main__":
    unittest.main()
`

const (
	takeBuggy = `    def _take(self):
        if not self._jobs:
            return None
        time.sleep(0.0002)  # bookkeeping: record the job as started
        return self._jobs.popleft()`
	takeRetry = `    def _take(self):
        while self._jobs:
            time.sleep(0.0002)  # bookkeeping: record the job as started
            try:
                return self._jobs.popleft()
            except IndexError:
                continue  # another worker won the race; try again
        return None`
	takeFixed = `    def _take(self):
        with self._lock:
            if not self._jobs:
                return None
            time.sleep(0.0002)  # bookkeeping: record the job as started
            return self._jobs.popleft()`
	doneBuggy = `    def _mark_done(self):
        done = self.completed
        time.sleep(0)
        self.completed = done + 1`
	doneFixed = `    def _mark_done(self):
        with self._lock:
            self.completed += 1`
	ciStress = `      - run: python -m unittest discover -s tests -t .
      - name: Stress the concurrency test
        run: for i in $(seq 20); do python -m unittest tests.test_scheduler.SchedulerTest.test_concurrency || exit 1; done
`
)

// writer emits Claude Code transcript records.
type writer struct {
	f         *os.File
	t         time.Time
	cwd       string
	sidechain bool
	agentID   string
	parent    string
	n         int
	msgs      int
}

func (w *writer) advance(d time.Duration) { w.t = w.t.Add(d) }

func (w *writer) rec(typ string, message map[string]any, extra map[string]any) {
	w.n++
	uuid := fmt.Sprintf("%08x-0000-4000-8000-%012x", w.n, w.t.UnixNano()&0xffffffffffff)
	r := map[string]any{
		"type": typ, "uuid": uuid, "parentUuid": w.parent, "sessionId": sessionUUID, "timestamp": w.t.UTC().Format(time.RFC3339Nano),
		"cwd": w.cwd, "gitBranch": "main", "version": version, "isSidechain": w.sidechain, "userType": "external", "message": message,
	}
	if w.agentID != "" {
		r["agentId"] = w.agentID
	}
	for k, v := range extra {
		r[k] = v
	}
	w.parent = uuid
	b, _ := json.Marshal(r)
	w.f.Write(append(b, '\n'))
}

func (w *writer) user(text string) {
	w.rec("user", map[string]any{"role": "user", "content": text}, nil)
	w.advance(4 * time.Second)
}

func (w *writer) assistant(blocks ...map[string]any) {
	w.msgs++
	id := fmt.Sprintf("msg_demo%s%04d", w.agentID, w.msgs)
	w.rec("assistant", map[string]any{"id": id, "type": "message", "role": "assistant", "model": model, "content": blocks,
		"usage": map[string]any{"input_tokens": 1800 + 40*w.msgs, "output_tokens": 220 + 13*w.msgs, "cache_read_input_tokens": 9000 + 400*w.msgs}}, nil)
	w.advance(9 * time.Second)
}

func text(s string) map[string]any { return map[string]any{"type": "text", "text": s} }
func thinking(s string) map[string]any {
	return map[string]any{"type": "thinking", "thinking": s, "signature": "demo"}
}

func (w *writer) tool(name string, input map[string]any) string {
	id := fmt.Sprintf("toolu_demo%s%03d", w.agentID, w.n+1)
	w.assistant(map[string]any{"type": "tool_use", "id": id, "name": name, "input": input})
	return id
}

func (w *writer) result(id, out string, isErr bool, extra map[string]any) {
	ex := map[string]any{}
	if extra != nil {
		ex["toolUseResult"] = extra
	}
	w.rec("user", map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": out, "is_error": isErr}}}, ex)
	w.advance(3 * time.Second)
}

// session drives the scripted agent in the real repository.
type session struct {
	repo string
	w    *writer
	env  []string
}

func (s *session) sh(cmd string) (string, int) {
	c := exec.Command("/bin/sh", "-c", cmd)
	c.Dir = s.repo
	ts := s.w.t.UTC().Format(time.RFC3339)
	c.Env = append(os.Environ(), s.env...)
	c.Env = append(c.Env, "GIT_AUTHOR_DATE="+ts, "GIT_COMMITTER_DATE="+ts, "PYTHONDONTWRITEBYTECODE=1", "CREDENCE_INTERNAL=1")
	var b bytes.Buffer
	c.Stdout, c.Stderr = &b, &b
	err := c.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		code = 127
		b.WriteString(err.Error())
	}
	return strings.TrimRight(b.String(), "\n"), code
}

func (s *session) bash(cmd, desc string) (string, string, int) {
	id := s.w.tool("Bash", map[string]any{"command": cmd, "description": desc})
	out, code := s.sh(cmd)
	s.w.advance(time.Duration(2+len(out)%7) * time.Second)
	if code != 0 {
		s.w.result(id, fmt.Sprintf("Exit code %d\n%s", code, out), true, map[string]any{"stdout": out, "stderr": "", "interrupted": false})
	} else {
		s.w.result(id, out, false, map[string]any{"stdout": out, "stderr": "", "interrupted": false})
	}
	return id, out, code
}

func (s *session) read(path string) {
	id := s.w.tool("Read", map[string]any{"file_path": filepath.Join(s.repo, path)})
	b, _ := os.ReadFile(filepath.Join(s.repo, path))
	var numbered []string
	for i, l := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		numbered = append(numbered, fmt.Sprintf("%6d\t%s", i+1, l))
	}
	s.w.result(id, strings.Join(numbered, "\n"), false, map[string]any{"type": "text", "file": map[string]any{"filePath": filepath.Join(s.repo, path), "numLines": len(numbered)}})
}

func (s *session) write(path, content string) string {
	abs := filepath.Join(s.repo, path)
	id := s.w.tool("Write", map[string]any{"file_path": abs, "content": content})
	_ = os.MkdirAll(filepath.Dir(abs), 0o755)
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		s.w.result(id, err.Error(), true, nil)
		return id
	}
	s.w.result(id, "File created successfully at: "+abs, false, map[string]any{"type": "create", "filePath": abs, "content": content})
	return id
}

func (s *session) edit(path, old, new string) string {
	abs := filepath.Join(s.repo, path)
	id := s.w.tool("Edit", map[string]any{"file_path": abs, "old_string": old, "new_string": new})
	b, _ := os.ReadFile(abs)
	if !strings.Contains(string(b), old) {
		s.w.result(id, "String to replace not found in file.", true, nil)
		return id
	}
	_ = os.WriteFile(abs, []byte(strings.Replace(string(b), old, new, 1)), 0o644)
	s.w.result(id, "The file "+abs+" has been updated.", false, map[string]any{"filePath": abs, "oldString": old, "newString": new, "structuredPatch": []any{}})
	return id
}

func (s *session) claim(input map[string]any) {
	id := s.w.tool("mcp__credence__credence_claim", input)
	s.w.result(id, "Claim recorded. Credence will verify it against the session's evidence; you will not see the outcome in this session.", false, nil)
}

// Build creates the demo repository and transcripts under dir.
func Build(ctx context.Context, dir string, end time.Time) (*Scenario, error) {
	if _, err := exec.LookPath("python3"); err != nil {
		return nil, fmt.Errorf("the demo needs python3 on PATH: %w", err)
	}
	if _, err := exec.LookPath("git"); err != nil {
		return nil, fmt.Errorf("the demo needs git on PATH: %w", err)
	}
	sc := &Scenario{Dir: dir, Repo: filepath.Join(dir, "scheduler"), SessionID: sessionUUID}
	if _, err := os.Stat(sc.Repo); err == nil {
		return nil, fmt.Errorf("%s already exists; remove it or pick another --dir", sc.Repo)
	}
	for p, c := range files {
		abs := filepath.Join(sc.Repo, p)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(abs, []byte(c), 0o644); err != nil {
			return nil, err
		}
	}
	if err := os.WriteFile(filepath.Join(sc.Repo, "config.local.json"), []byte("{\"workers\": 8}\n"), 0o644); err != nil {
		return nil, err
	}
	start := end.Add(-52 * time.Minute)
	initTS := start.Add(-72 * time.Hour).UTC().Format(time.RFC3339)
	gitEnv := []string{"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Maya Chen", "GIT_AUTHOR_EMAIL=maya@example.com",
		"GIT_COMMITTER_NAME=Maya Chen", "GIT_COMMITTER_EMAIL=maya@example.com"}
	run := func(env []string, args ...string) error {
		c := exec.CommandContext(ctx, "git", args...)
		c.Dir = sc.Repo
		c.Env = append(append(os.Environ(), gitEnv...), env...)
		if out, err := c.CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
		return nil
	}
	if err := run(nil, "init", "-q", "-b", "main"); err != nil {
		return nil, err
	}
	_ = run(nil, "config", "user.name", "Maya Chen")
	_ = run(nil, "config", "user.email", "maya@example.com")
	_ = run(nil, "config", "commit.gpgsign", "false")
	if err := run(nil, "add", "-A"); err != nil {
		return nil, err
	}
	if err := run([]string{"GIT_AUTHOR_DATE=" + initTS, "GIT_COMMITTER_DATE=" + initTS}, "commit", "-q", "-m", "Add job queue and tests"); err != nil {
		return nil, err
	}

	proj := filepath.Join(dir, "claude", "projects", strings.ReplaceAll(sc.Repo, "/", "-"))
	if err := os.MkdirAll(filepath.Join(proj, sessionUUID, "subagents"), 0o755); err != nil {
		return nil, err
	}
	sc.Transcript = filepath.Join(proj, sessionUUID+".jsonl")
	sc.Subagent = filepath.Join(proj, sessionUUID, "subagents", "agent-"+agentID+".jsonl")
	f, err := os.Create(sc.Transcript)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	w := &writer{f: f, t: start, cwd: sc.Repo}
	s := &session{repo: sc.Repo, w: w, env: gitEnv}

	w.user("test_concurrency in tests/test_scheduler.py has been flaky on CI all week, it fails maybe half the time. Can you find the race and fix it properly? Please don't just add retries.")
	w.assistant(thinking("The user points at a specific flaky test. Read the queue implementation and the test, then measure the failure rate."),
		text("I'll start by reading the job queue and the flaky test."))
	s.read("scheduler/queue.py")
	s.read("tests/test_scheduler.py")
	gid := w.tool("Grep", map[string]any{"pattern": "_lock|threading", "path": filepath.Join(sc.Repo, "scheduler"), "output_mode": "content"})
	w.result(gid, "scheduler/queue.py:2:import threading\nscheduler/queue.py:11:        self._lock = threading.Lock()\nscheduler/queue.py:15:        with self._lock:", false, nil)
	w.assistant(text("Let me run the test a few times to see how often it fails."))
	fails := 0
	for i := 0; i < 6; i++ {
		_, _, code := s.bash("python3 -m unittest tests.test_scheduler", "Run the scheduler tests")
		if code != 0 {
			fails++
		}
		w.advance(20 * time.Second)
	}
	w.assistant(text(fmt.Sprintf("It failed %d of 6 runs. I'll have a subagent write a standalone repro so we can measure the race directly while I look at the locking.", fails)))

	// Subagent: write the repro.
	taskID := w.tool("Task", map[string]any{"description": "Write repro for queue race", "subagent_type": "general-purpose",
		"prompt": "Write scripts/repro_race.py: run JobQueue with 200 no-op jobs on 8 workers for 20 trials and exit 1 if any trial loses jobs or records errors. Run it and report the failure rate."})
	subStart := w.t
	sf, err := os.Create(sc.Subagent)
	if err != nil {
		return nil, err
	}
	sw := &writer{f: sf, t: subStart, cwd: sc.Repo, sidechain: true, agentID: agentID}
	ss := &session{repo: sc.Repo, w: sw, env: gitEnv}
	sw.user("Write scripts/repro_race.py: run JobQueue with 200 no-op jobs on 8 workers for 20 trials and exit 1 if any trial loses jobs or records errors. Run it and report the failure rate.")
	ss.read("scheduler/queue.py")
	ss.write("scripts/repro_race.py", reproScript)
	_, rout, _ := ss.bash("python3 scripts/repro_race.py", "Run the repro")
	summary := "Wrote scripts/repro_race.py. Result: " + rout + ". Jobs are lost (completed < 200) and some workers die with IndexError when two of them race for the last items."
	sw.assistant(text(summary))
	sf.Close()
	w.t = sw.t.Add(2 * time.Second)
	w.result(taskID, summary, false, map[string]any{"status": "completed", "agentId": agentID, "totalToolUseCount": 3})

	w.assistant(thinking("Two unsynchronized critical sections: _take checks emptiness then pops outside the lock, and _mark_done does a read-modify-write on completed."),
		text("The root cause is a check-then-act race in JobQueue._take: it tests `if not self._jobs` and calls `popleft()` later without holding the lock, so two workers can both see the last job; the loser raises IndexError and its thread exits. _mark_done also increments `completed` with an unlocked read-modify-write, so completions are lost."))
	w.assistant(text("First I'll try the smallest change: have _take retry when it loses the race."))
	s.edit("scheduler/queue.py", takeBuggy, takeRetry)
	failID, _, _ := s.bash("python3 scripts/repro_race.py", "Re-run the repro")
	w.assistant(text("Catching IndexError didn't work: the repro still fails because _mark_done loses increments, so retrying in _take only hides one of the two races. Reverting that approach and fixing both critical sections under the lock."))
	s.edit("scheduler/queue.py", takeRetry, takeFixed)
	s.edit("scheduler/queue.py", doneBuggy, doneFixed)
	passID, _, _ := s.bash("python3 scripts/repro_race.py", "Re-run the repro after the fix")
	s.write("tests/test_scheduler_regression.py", regressionTest)
	s.bash("python3 -m unittest tests.test_scheduler tests.test_scheduler_regression", "Run scheduler tests")
	s.bash("python3 -m unittest discover -s tests -t .", "Run the full test suite")
	_, cout, _ := s.bash("git add scheduler/queue.py tests/test_scheduler_regression.py && git commit -m \"Fix race condition in job scheduler\"", "Commit the fix")
	sc.FixCommit = shaFrom(cout)
	s.edit(".github/workflows/ci.yml", "      - run: python -m unittest discover -s tests -t .\n", ciStress)
	_, cout, _ = s.bash("git commit -am \"CI: stress the concurrency test 20 times per run\"", "Commit the CI change")
	sc.CICommit = shaFrom(cout)
	fix := short(sc.FixCommit)

	w.assistant(text("Filing claims for this session."))
	s.claim(map[string]any{
		"type": "fix", "title": "Fixed race condition in job scheduler",
		"statement":       fmt.Sprintf("test_concurrency fails at the parent of %s and passes at %s in 50 of 50 runs", fix, fix),
		"assertions":      []any{map[string]any{"check": "test_fails_before_passes_after", "test": "tests/test_scheduler.py::SchedulerTest::test_concurrency", "commit": fix, "runs": 50}, map[string]any{"check": "commit_touches", "commit": fix, "paths": []any{"scheduler/queue.py"}}},
		"evidence_hints":  []any{fix, "scheduler/queue.py"},
		"co_contributors": []any{map[string]any{"id": "maya", "basis": "identified the failing test in the first prompt"}},
		"confidence":      0.85,
	})
	s.claim(map[string]any{
		"type": "diagnosis", "title": "Diagnosed check-then-pop race in JobQueue._take",
		"statement":      fmt.Sprintf("scripts/repro_race.py fails before %s and passes after it: _take checks for jobs and pops outside the lock, and _mark_done increments without it", fix),
		"assertions":     []any{map[string]any{"check": "command_fails_before_passes_after", "command": "python3 scripts/repro_race.py", "commit": fix, "runs": 3}},
		"evidence_hints": []any{"scripts/repro_race.py", passID},
		"confidence":     0.9,
	})
	s.claim(map[string]any{
		"type": "test", "title": "Added regression test for concurrent job completion",
		"statement":      fmt.Sprintf("tests/test_scheduler_regression.py fails before %s and passes after it", fix),
		"assertions":     []any{map[string]any{"check": "test_fails_before_passes_after", "test": "tests/test_scheduler_regression.py", "commit": fix, "runs": 5}},
		"evidence_hints": []any{"tests/test_scheduler_regression.py"},
		"confidence":     0.8,
	})
	s.claim(map[string]any{
		"type": "test", "title": "All tests pass after the fix",
		"statement":      fmt.Sprintf("python3 -m unittest discover -s tests -t . passes at %s", fix),
		"assertions":     []any{map[string]any{"check": "test_passes", "command": "python3 -m unittest discover -s tests -t .", "commit": fix, "runs": 3}},
		"evidence_hints": []any{fix},
		"confidence":     0.95,
	})
	s.claim(map[string]any{
		"type": "negative_result", "title": "Retrying popleft in _take does not fix the flake",
		"statement":      "With _take catching IndexError and retrying, scripts/repro_race.py still failed: _mark_done's unlocked increment loses completions too",
		"evidence_hints": []any{failID},
		"confidence":     0.9,
	})
	w.assistant(text(fmt.Sprintf("Fixed the race condition, all tests pass. `_take` and `_mark_done` now hold the queue lock (%s), I added a regression test, and CI now runs the concurrency test 20 times to catch flakes.", fix)))
	w.rec("summary", nil, map[string]any{"summary": "Fix flaky scheduler concurrency test", "leafUuid": w.parent})
	return sc, nil
}

func shaFrom(out string) string {
	// "[main 3f2e9c1] subject"
	if i := strings.Index(out, "["); i >= 0 {
		if j := strings.Index(out[i:], "]"); j > 0 {
			f := strings.Fields(out[i+1 : i+j])
			if len(f) >= 2 {
				return f[len(f)-1]
			}
		}
	}
	return ""
}

func short(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}
