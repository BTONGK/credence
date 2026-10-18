// Package ingest is the daemon's capture path: it consumes hook records from
// the spool, tails harness transcripts, normalizes native records into
// events, redacts them and appends them to each session's hash chain.
package ingest

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/BTONGK/credence/adapters"
	"github.com/BTONGK/credence/capture"
	"github.com/BTONGK/credence/daemon/redact"
	"github.com/BTONGK/credence/daemon/store"
	"github.com/BTONGK/credence/internal/config"
	"github.com/BTONGK/credence/internal/gitx"
	"github.com/BTONGK/credence/internal/home"
	"github.com/BTONGK/credence/internal/pricing"
	"github.com/BTONGK/credence/policy"
	"github.com/BTONGK/credence/spec"
)

// Engine ingests transcripts and hooks.
type Engine struct {
	Store *store.Store
	Cfg   *config.Config
	Log   *log.Logger
	// Key salts redaction placeholders.
	Key []byte
	// Since is the earliest transcript mtime captured automatically.
	Since time.Time
	// Notify is called with a session id whenever it gets new events.
	Notify func(sessionID string)

	mu        sync.Mutex
	files     map[string]*fileState
	redactors map[string]*redact.Redactor
	sessions  map[string]*sessCtx
	active    map[string]time.Time // transcript path -> last change
	closing   map[string]time.Time // session id -> close after
}

type fileState struct {
	adapter adapters.Adapter
	parser  adapters.Parser
	lines   int
	offset  int64
	session string
}

type sessCtx struct {
	row      *store.SessionRow
	internal bool
	lastTS   time.Time
	model    string
	actors   map[string]string // model -> contributor id
	isNew    bool
}

// New creates an engine.
func New(st *store.Store, cfg *config.Config, logger *log.Logger, key []byte, since time.Time) *Engine {
	return &Engine{
		Store: st, Cfg: cfg, Log: logger, Key: key, Since: since,
		files: map[string]*fileState{}, redactors: map[string]*redact.Redactor{}, sessions: map[string]*sessCtx{},
		active: map[string]time.Time{}, closing: map[string]time.Time{},
	}
}

// SessionID derives Credence's session id from the harness id.
func SessionID(harness, hid string) string {
	sum := sha256.Sum256([]byte(harness + ":" + hid))
	return "ses_" + hex.EncodeToString(sum[:])[:10]
}

func lineHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

func (e *Engine) logf(format string, args ...any) {
	if e.Log != nil {
		e.Log.Printf(format, args...)
	}
}

// ---- transcripts ----

// ProcessFile reads new lines of a transcript. force ignores the Since cutoff.
func (e *Engine) ProcessFile(ctx context.Context, path string, a adapters.Adapter, force bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.processFile(ctx, path, a, force)
}

func (e *Engine) processFile(ctx context.Context, path string, a adapters.Adapter, force bool) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	ts, err := e.Store.GetTranscript(path)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if errors.Is(err, sql.ErrNoRows) {
		ts = nil
	}
	if ts == nil && !force && !e.Since.IsZero() && st.ModTime().Before(e.Since) {
		return nil
	}
	fsx := e.files[path]
	if fsx != nil && ts != nil && st.Size() == ts.Size && st.ModTime().Equal(ts.MTime) {
		return nil
	}
	if fsx == nil {
		fsx = &fileState{adapter: a, parser: a.NewParser(path)}
		if ts != nil {
			fsx.session = ts.SessionID
			if ts.Offset > 0 {
				bad, err := e.replay(fsx, path, ts.Offset)
				if err != nil {
					return err
				}
				if len(bad) > 0 && fsx.session != "" {
					e.logf("transcript %s: %d previously seen lines changed", path, len(bad))
					_ = e.Store.AddSessionFlag(fsx.session, "transcript_rewritten")
				}
			}
		}
		e.files[path] = fsx
	}
	if st.Size() < fsx.offset {
		// Truncated or replaced: flag it and resume from the new end.
		if fsx.session != "" {
			_ = e.Store.AddSessionFlag(fsx.session, "transcript_truncated")
		}
		fsx.parser = a.NewParser(path)
		fsx.lines, fsx.offset = 0, 0
		if _, err := e.replay(fsx, path, st.Size()); err != nil {
			return err
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Seek(fsx.offset, io.SeekStart); err != nil {
		return err
	}
	br := bufio.NewReaderSize(f, 256<<10)
	type batch struct {
		hid     string
		parsed  []adapters.Parsed
		firstLn int
	}
	var order []string
	batches := map[string]*batch{}
	var hashes []string
	startLine := fsx.lines + 1
	for {
		line, err := br.ReadBytes('\n')
		if err != nil {
			break // partial trailing line stays for the next pass
		}
		fsx.offset += int64(len(line))
		fsx.lines++
		body := bytes.TrimRight(line, "\r\n")
		hashes = append(hashes, lineHash(body))
		if len(bytes.TrimSpace(body)) == 0 {
			continue
		}
		p, perr := fsx.parser.Parse(body, fsx.lines)
		if perr != nil {
			continue
		}
		hid := p.Meta.HarnessSessionID
		if hid == "" {
			hid = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		}
		b := batches[hid]
		if b == nil {
			b = &batch{hid: hid, firstLn: fsx.lines}
			batches[hid] = b
			order = append(order, hid)
		}
		b.parsed = append(b.parsed, p)
	}
	for _, hid := range order {
		sid, err := e.applyBatch(ctx, a, path, hid, batches[hid].parsed)
		if err != nil {
			return err
		}
		if fsx.session == "" {
			fsx.session = sid
		}
	}
	if err := e.Store.PutLineHashes(ctx, path, startLine, hashes); err != nil {
		return err
	}
	if len(hashes) > 0 {
		e.active[path] = time.Now()
	}
	return e.Store.PutTranscript(&store.TranscriptState{
		Path: path, Harness: a.Name(), SessionID: fsx.session, Offset: fsx.offset, Lines: fsx.lines,
		Size: st.Size(), MTime: st.ModTime(),
	})
}

// replay re-feeds lines [0, upto) to rebuild parser state after a restart,
// returning line numbers whose hash changed since they were first seen.
func (e *Engine) replay(fsx *fileState, path string, upto int64) ([]int, error) {
	stored, err := e.Store.LineHashes(path)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	br := bufio.NewReaderSize(io.LimitReader(f, upto), 256<<10)
	var bad []int
	for {
		line, err := br.ReadBytes('\n')
		if err != nil {
			break
		}
		fsx.offset += int64(len(line))
		fsx.lines++
		body := bytes.TrimRight(line, "\r\n")
		if h, ok := stored[fsx.lines]; ok && h != lineHash(body) {
			bad = append(bad, fsx.lines)
		}
		if len(bytes.TrimSpace(body)) > 0 {
			_, _ = fsx.parser.Parse(body, fsx.lines)
		}
	}
	return bad, nil
}

// VerifyTranscript re-hashes a transcript and reports lines that changed
// after Credence first saw them (an agent with shell access editing its log).
func (e *Engine) VerifyTranscript(path string) ([]int, error) {
	stored, err := e.Store.LineHashes(path)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	br := bufio.NewReaderSize(f, 256<<10)
	n := 0
	var bad []int
	for {
		line, err := br.ReadBytes('\n')
		if err != nil {
			break
		}
		n++
		if h, ok := stored[n]; ok && h != lineHash(bytes.TrimRight(line, "\r\n")) {
			bad = append(bad, n)
		}
	}
	if n < len(stored) {
		for i := n + 1; i <= len(stored); i++ {
			bad = append(bad, i)
		}
	}
	return bad, nil
}

func (e *Engine) loadSession(harness, hid string) (*sessCtx, error) {
	id := SessionID(harness, hid)
	if sc := e.sessions[id]; sc != nil {
		return sc, nil
	}
	row, err := e.Store.GetSession(id)
	sc := &sessCtx{actors: map[string]string{}}
	switch {
	case errors.Is(err, sql.ErrNoRows):
		row = &store.SessionRow{Session: spec.Session{ID: id, Harness: harness, HarnessSessionID: hid, State: "open", Models: []string{}}}
		sc.isNew = true
	case err != nil:
		return nil, err
	default:
		sc.lastTS = row.LastActivity
		if row.SkipReason == "internal" {
			sc.internal = true
		}
		if len(row.Models) > 0 {
			sc.model = row.Models[len(row.Models)-1]
		}
	}
	sc.row = row
	e.sessions[id] = sc
	return sc, nil
}

var internalRe = regexp.MustCompile(`(?i)\[credence-(auditor|judge)\]`)

func (e *Engine) applyBatch(ctx context.Context, a adapters.Adapter, path, hid string, parsed []adapters.Parsed) (string, error) {
	harness := a.Name()
	for _, p := range parsed {
		if p.Meta.Harness != "" {
			harness = Slug(p.Meta.Harness)
			break
		}
	}
	sc, err := e.loadSession(harness, hid)
	if err != nil {
		return "", err
	}
	s := &sc.row.Session
	var drafts []adapters.Draft
	for _, p := range parsed {
		m := p.Meta
		if m.Cwd != "" && s.Cwd == "" {
			s.Cwd = m.Cwd
		}
		if m.Branch != "" {
			s.Branch = m.Branch
		}
		if m.Model != "" {
			sc.model = m.Model
			if !contains(s.Models, m.Model) {
				s.Models = append(s.Models, m.Model)
			}
		}
		if m.Version != "" {
			s.HarnessVersion = m.Version
		}
		if m.Title != "" {
			s.Title = m.Title
		}
		if !m.StartedAt.IsZero() && (s.StartedAt.IsZero() || m.StartedAt.Before(s.StartedAt)) {
			s.StartedAt = m.StartedAt
		}
		if m.AbsoluteTokens {
			s.Tokens = spec.TokenUsage{Input: m.TokensIn, Output: m.TokensOut, CacheRead: m.CacheRead, CacheWrite: m.CacheWrite}
		} else {
			s.Tokens.Input += m.TokensIn
			s.Tokens.Output += m.TokensOut
			s.Tokens.CacheRead += m.CacheRead
			s.Tokens.CacheWrite += m.CacheWrite
		}
		if m.ParentID != "" {
			s.ParentSessionID = SessionID(harness, m.ParentID)
		}
		if m.AgentType != "" {
			s.AgentType = m.AgentType
		}
		if m.Internal {
			sc.internal = true
		}
		for i := range p.Drafts {
			// Model per draft for actor attribution.
			if p.Drafts[i].Payload == nil {
				p.Drafts[i].Payload = map[string]any{}
			}
			if sc.model != "" {
				if _, ok := p.Drafts[i].Payload["model"]; !ok && (p.Drafts[i].Role == "agent" || p.Drafts[i].Role == "subagent") {
					p.Drafts[i].Payload["_model"] = sc.model
				}
			}
		}
		drafts = append(drafts, p.Drafts...)
	}
	if s.Cwd != "" && adapters.UnderDir(s.Cwd, home.Dir()) || s.Cwd == home.Dir() {
		sc.internal = true
	}
	for _, d := range drafts {
		if d.Kind == spec.KindPrompt {
			if t, _ := d.Payload["text"].(string); internalRe.MatchString(t) {
				sc.internal = true
			}
		}
	}
	if s.Transcript.Path == "" || s.ParentSessionID == "" {
		s.Transcript.Path = path
	}
	if sc.internal {
		s.Skipped, s.SkipReason = true, "internal"
		s.LastActivity = maxTime(s.LastActivity, time.Now().UTC())
		if s.StartedAt.IsZero() {
			s.StartedAt = time.Now().UTC()
		}
		return s.ID, e.Store.UpsertSession(s)
	}
	e.resolveRepo(sc, drafts)
	if s.Title == "" {
		for _, d := range drafts {
			if d.Kind == spec.KindPrompt && d.Role == "human" {
				if t, _ := d.Payload["text"].(string); t != "" {
					s.Title = titleFrom(t)
					break
				}
			}
		}
	}
	if s.Human == "" {
		s.Human = e.human(s.Repo)
	}
	red := e.redactor(s.Repo)
	var events []spec.Event
	for _, d := range drafts {
		ts := d.TS
		if ts.IsZero() {
			ts = sc.lastTS
		}
		if ts.IsZero() {
			ts = time.Now().UTC()
		}
		if ts.After(sc.lastTS) {
			sc.lastTS = ts
		}
		model, _ := d.Payload["model"].(string)
		if model == "" {
			model, _ = d.Payload["_model"].(string)
		}
		delete(d.Payload, "_model")
		raw, err := json.Marshal(d.Payload)
		if err != nil {
			continue
		}
		events = append(events, spec.Event{
			TS: ts.UTC(), Kind: d.Kind, Actor: e.actor(sc, d.Role, model), Payload: red.JSON(raw),
			SrcPath: path, SrcLine: d.SrcLine,
		})
	}
	if s.StartedAt.IsZero() {
		if len(events) > 0 {
			s.StartedAt = events[0].TS
		} else {
			s.StartedAt = time.Now().UTC()
		}
	}
	s.LastActivity = maxTime(s.LastActivity, sc.lastTS)
	if s.LastActivity.IsZero() {
		s.LastActivity = s.StartedAt
	}
	if sc.model != "" {
		s.AgentConfig = e.actor(sc, roleOf(s), sc.model)
	}
	if len(s.Models) > 0 {
		s.CostUSD = pricing.Cost(s.Models[0], s.Tokens.Input, s.Tokens.Output, s.Tokens.CacheRead, s.Tokens.CacheWrite)
	}
	if s.State == "closed" && len(events) > 0 {
		s.State = "open" // resumed session
		s.EndedAt = nil
	}
	if err := e.Store.UpsertSession(s); err != nil {
		return "", err
	}
	sc.isNew = false
	appended, err := e.Store.AppendEvents(ctx, s.ID, events)
	if err != nil {
		return "", err
	}
	for _, ev := range appended {
		if ev.Kind == spec.KindCommit {
			e.linkCommit(s, ev)
		}
	}
	if len(appended) > 0 {
		_ = e.Store.Touch(s.ID, s.LastActivity)
		if e.Notify != nil {
			e.Notify(s.ID)
		}
	}
	return s.ID, nil
}

func roleOf(s *spec.Session) string {
	if s.ParentSessionID != "" {
		return "subagent"
	}
	return "agent"
}

func (e *Engine) resolveRepo(sc *sessCtx, drafts []adapters.Draft) {
	s := &sc.row.Session
	if s.Repo == "" {
		root := gitx.Root(s.Cwd)
		if root == "" {
			counts := map[string]int{}
			for _, d := range drafts {
				if d.Kind != spec.KindFileEdit {
					continue
				}
				p, _ := d.Payload["path"].(string)
				if p == "" {
					continue
				}
				if !filepath.IsAbs(p) && s.Cwd != "" {
					p = filepath.Join(s.Cwd, p)
				}
				if r := gitx.Root(filepath.Dir(p)); r != "" {
					counts[r]++
				}
			}
			best := 0
			for r, n := range counts {
				if n > best {
					root, best = r, n
				}
			}
		}
		if root != "" {
			s.Repo = root
			s.RepoName = filepath.Base(root)
			if s.Branch == "" {
				s.Branch = gitx.Branch(root)
			}
		}
	}
	for _, ex := range e.Cfg.Capture.ExcludeRepos {
		if s.Repo != "" && (s.Repo == ex || adapters.UnderDir(s.Repo, ex)) {
			s.Skipped, s.SkipReason = true, "excluded_repo"
			return
		}
	}
	if s.Repo == "" {
		s.Skipped, s.SkipReason = true, "not_in_git_repo"
	} else if s.SkipReason == "not_in_git_repo" {
		s.Skipped, s.SkipReason = false, ""
	}
}

func (e *Engine) redactor(repo string) *redact.Redactor {
	if r, ok := e.redactors[repo]; ok {
		return r
	}
	var extra []string
	if p, err := policy.Load(repo); err == nil {
		extra = p.Redaction.ExtraPatterns
	}
	r := redact.New(e.Key, extra)
	e.redactors[repo] = r
	return r
}

// InvalidatePolicy drops cached per-repo state after credence.yaml changes.
func (e *Engine) InvalidatePolicy(repo string) {
	e.mu.Lock()
	delete(e.redactors, repo)
	e.mu.Unlock()
}

var slugRe = regexp.MustCompile(`[^a-z0-9._-]+`)

// Slug normalizes a handle.
func Slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugRe.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

// Human is the contributor id of the person working in repo.
func (e *Engine) Human(repo string) string { return e.human(repo) }

func (e *Engine) human(repo string) string {
	if e.Cfg.Human != "" {
		return "human:" + Slug(e.Cfg.Human)
	}
	if repo != "" {
		if n := gitx.UserName(repo); n != "" {
			return "human:" + Slug(strings.Fields(n)[0])
		}
	}
	if u := os.Getenv("USER"); u != "" {
		return "human:" + Slug(u)
	}
	return "human:unknown"
}

func (e *Engine) actor(sc *sessCtx, role, model string) string {
	s := &sc.row.Session
	switch role {
	case "human":
		if s.Human == "" {
			s.Human = e.human(s.Repo)
		}
		return s.Human
	case "tool":
		return "tool:" + s.Harness
	case "system":
		return "system:" + s.Harness
	case "subagent":
		parent := s.ParentSessionID
		if parent == "" {
			parent = s.ID
		}
		t := s.AgentType
		if t == "" {
			t = "general"
		}
		return "subagent:" + parent + "/" + Slug(t)
	}
	if model == "" {
		model = sc.model
	}
	if model == "" {
		model = "unknown"
	}
	if id, ok := sc.actors[model]; ok {
		return id
	}
	cfg := e.agentConfig(s, model)
	id := "agent:" + s.Harness + "/" + model + "@" + cfg
	sc.actors[model] = id
	return id
}

// agentConfig hashes what a builder tunes: model, project and user
// instructions, skills and MCP servers.
func (e *Engine) agentConfig(s *spec.Session, model string) string {
	comp := map[string]any{"harness": s.Harness, "model": model}
	instr := map[string]string{}
	var files []string
	if s.Repo != "" {
		for _, f := range []string{"CLAUDE.md", "CLAUDE.local.md", ".claude/CLAUDE.md", "AGENTS.md", ".cursorrules", "GEMINI.md"} {
			files = append(files, filepath.Join(s.Repo, f))
		}
		if m, _ := filepath.Glob(filepath.Join(s.Repo, ".cursor", "rules", "*")); len(m) > 0 {
			files = append(files, m...)
		}
	}
	uh := home.UserHome()
	files = append(files, filepath.Join(uh, ".claude", "CLAUDE.md"), filepath.Join(uh, ".codex", "AGENTS.md"))
	for _, f := range files {
		if b, err := os.ReadFile(f); err == nil {
			rel := f
			if s.Repo != "" {
				if r, err := filepath.Rel(s.Repo, f); err == nil && !strings.HasPrefix(r, "..") {
					rel = r
				}
			}
			rel = strings.Replace(rel, uh, "~", 1)
			instr[rel] = spec.ShortHex(spec.HashBytes(b), 12)
		}
	}
	comp["instructions"] = instr
	var skills []string
	for _, dir := range []string{filepath.Join(uh, ".claude", "skills"), filepath.Join(s.Repo, ".claude", "skills")} {
		if s.Repo == "" && strings.HasPrefix(dir, "/.claude") {
			continue
		}
		entries, _ := os.ReadDir(dir)
		for _, en := range entries {
			if en.IsDir() {
				h := ""
				if b, err := os.ReadFile(filepath.Join(dir, en.Name(), "SKILL.md")); err == nil {
					h = spec.ShortHex(spec.HashBytes(b), 8)
				}
				skills = append(skills, en.Name()+"@"+h)
			}
		}
	}
	sort.Strings(skills)
	comp["skills"] = skills
	var mcp []string
	if s.Repo != "" {
		if m, err := adapters.ReadJSONFile(filepath.Join(s.Repo, ".mcp.json")); err == nil {
			if servers, ok := m["mcpServers"].(map[string]any); ok {
				for k := range servers {
					mcp = append(mcp, k)
				}
			}
		}
	}
	sort.Strings(mcp)
	comp["mcp_servers"] = mcp
	h := spec.MustHash(comp)
	id := "cfg_" + spec.ShortHex(h, 4)
	raw, _ := json.Marshal(comp)
	_ = e.Store.UpsertAgentConfig(&store.AgentConfig{ID: "agent:" + s.Harness + "/" + model + "@" + id, Harness: s.Harness, Model: model, Components: raw})
	return id
}

func (e *Engine) linkCommit(s *spec.Session, ev spec.Event) {
	if s.Repo == "" {
		return
	}
	var p struct {
		SHA string `json:"sha"`
	}
	if json.Unmarshal(ev.Payload, &p) != nil || p.SHA == "" {
		return
	}
	c, err := gitx.ShowCommit(s.Repo, p.SHA)
	if err != nil {
		return
	}
	LinkCommitArtifact(e.Store, s.Repo, s.ID, c, spec.RelProduced)
}

// LinkCommitArtifact records a commit artifact and links it to a session.
func LinkCommitArtifact(st *store.Store, repo, sessionID string, c *gitx.Commit, rel string) *spec.Artifact {
	meta, _ := json.Marshal(c)
	a := &spec.Artifact{Kind: "commit", Locator: spec.CommitTarget(c.SHA), ContentHash: "git:" + c.SHA, Repo: repo, SessionID: sessionID, Meta: meta}
	if err := st.UpsertArtifact(a); err != nil {
		return nil
	}
	if existing, err := st.ArtifactByLocator(a.Locator); err == nil {
		a = existing
	}
	_ = st.LinkArtifact(sessionID, a.ID, rel)
	return a
}

func titleFrom(t string) string {
	t = strings.TrimSpace(t)
	if i := strings.IndexByte(t, '\n'); i > 0 {
		t = t[:i]
	}
	if len(t) > 80 {
		t = t[:77] + "…"
	}
	return t
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// ---- scanning ----

var skipDirs = map[string]bool{"memory": true, "tool-results": true, "node_modules": true, ".git": true, "todos": true, "shell-snapshots": true}

// Scan walks transcript roots (full) or polls recently active files.
func (e *Engine) Scan(ctx context.Context, full bool) {
	if full {
		for _, a := range e.enabledAdapters() {
			for _, root := range append(a.TranscriptRoots(), e.extraRoots(a)...) {
				e.walk(ctx, root, a)
			}
		}
		return
	}
	e.mu.Lock()
	var hot []string
	cutoff := time.Now().Add(-15 * time.Minute)
	for p, t := range e.active {
		if t.After(cutoff) {
			hot = append(hot, p)
		} else {
			delete(e.active, p)
		}
	}
	e.mu.Unlock()
	for _, p := range hot {
		if ctx.Err() != nil {
			return
		}
		a, ok := e.adapterFor(p)
		if !ok {
			continue
		}
		if err := e.ProcessFile(ctx, p, a, true); err != nil && !os.IsNotExist(err) {
			e.logf("ingest %s: %v", p, err)
		}
	}
}

func (e *Engine) extraRoots(a adapters.Adapter) []string {
	if a.Name() != "generic" {
		return nil
	}
	return e.Cfg.Capture.ExtraRoots
}

func (e *Engine) adapterFor(path string) (adapters.Adapter, bool) {
	e.mu.Lock()
	fsx := e.files[path]
	e.mu.Unlock()
	if fsx != nil {
		return fsx.adapter, true
	}
	return adapters.ForPath(path)
}

func (e *Engine) enabledAdapters() []adapters.Adapter {
	all := adapters.All()
	if len(e.Cfg.Capture.Harnesses) == 0 {
		return all
	}
	var res []adapters.Adapter
	for _, a := range all {
		if contains(e.Cfg.Capture.Harnesses, a.Name()) {
			res = append(res, a)
		}
	}
	return res
}

func (e *Engine) walk(ctx context.Context, root string, a adapters.Adapter) {
	base := strings.Count(filepath.Clean(root), string(os.PathSeparator))
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || ctx.Err() != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if skipDirs[d.Name()] || strings.Count(p, string(os.PathSeparator))-base > 5 {
				return fs.SkipDir
			}
			return nil
		}
		if !a.IsTranscript(p) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if !e.Since.IsZero() && info.ModTime().Before(e.Since) {
			// Only files already being tracked are rechecked.
			if _, err := e.Store.GetTranscript(p); err != nil {
				return nil
			}
		}
		if err := e.ProcessFile(ctx, p, a, false); err != nil && !os.IsNotExist(err) {
			e.logf("ingest %s: %v", p, err)
		}
		return nil
	})
}

// Backfill ingests transcripts modified since t regardless of the cutoff.
func (e *Engine) Backfill(ctx context.Context, since time.Time, harness string) (int, error) {
	n := 0
	for _, a := range e.enabledAdapters() {
		if harness != "" && a.Name() != harness {
			continue
		}
		for _, root := range a.TranscriptRoots() {
			_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() || !a.IsTranscript(p) {
					if d != nil && d.IsDir() && skipDirs[d.Name()] {
						return fs.SkipDir
					}
					return nil
				}
				info, err := d.Info()
				if err != nil || info.ModTime().Before(since) {
					return nil
				}
				if err := e.ProcessFile(ctx, p, a, true); err == nil {
					n++
				}
				return nil
			})
		}
	}
	return n, nil
}

// ---- hooks ----

// ConsumeSpool reads new hook records from every spool file.
func (e *Engine) ConsumeSpool(ctx context.Context) {
	files, _ := filepath.Glob(filepath.Join(home.Spool(), "*.jsonl"))
	for _, path := range files {
		off := e.Store.SpoolOffset(path)
		recs, newOff, err := capture.ReadSpool(path, off)
		if err != nil {
			e.logf("spool %s: %v", path, err)
			continue
		}
		for _, rec := range recs {
			e.HandleHook(ctx, rec)
		}
		if newOff != off {
			_ = e.Store.SetSpoolOffset(path, newOff)
		}
		e.rotateSpool(path, newOff)
	}
}

func (e *Engine) rotateSpool(path string, off int64) {
	st, err := os.Stat(path)
	if err != nil || st.Size() < 4<<20 || off < st.Size() {
		return
	}
	old := path + ".old"
	if os.Rename(path, old) != nil {
		return
	}
	// Drain anything appended between the last read and the rename.
	recs, _, _ := capture.ReadSpool(old, off)
	for _, rec := range recs {
		e.HandleHook(context.Background(), rec)
	}
	_ = os.Remove(old)
	_ = e.Store.SetSpoolOffset(path, 0)
}

// HandleHook applies one hook record: it pulls the transcript forward and
// records turn and session boundaries.
func (e *Engine) HandleHook(ctx context.Context, rec adapters.HookRecord) {
	raw, _ := json.Marshal(rec)
	_ = e.Store.InsertHookEvent(&store.HookEvent{
		Harness: rec.Harness, Event: rec.Event, HarnessSessionID: rec.SessionID, TranscriptPath: rec.TranscriptPath,
		Cwd: rec.Cwd, TS: rec.TS, Raw: raw,
	})
	a, ok := adapters.Get(rec.Harness)
	if !ok {
		return
	}
	if tp := a.LocateTranscript(rec); tp != "" {
		owner := a
		if pa, ok := adapters.ForPath(tp); ok {
			owner = pa
		}
		if err := e.ProcessFile(ctx, tp, owner, true); err != nil && !os.IsNotExist(err) {
			e.logf("hook ingest %s: %v", tp, err)
		}
		e.mu.Lock()
		e.active[tp] = time.Now()
		e.mu.Unlock()
	}
	if rec.SessionID == "" {
		return
	}
	sid := SessionID(rec.Harness, rec.SessionID)
	if _, err := e.Store.GetSession(sid); err != nil {
		e.ensureHookSession(rec, sid)
	}
	b := a.Boundaries()
	switch {
	case contains(b.TurnEnd, rec.Event):
		_ = e.Store.MarkStop(sid, rec.TS)
	case contains(b.End, rec.Event):
		e.mu.Lock()
		// The transcript is written asynchronously and can lag the end hook.
		e.closing[sid] = rec.TS.Add(5 * time.Second)
		e.mu.Unlock()
	}
}

// ensureHookSession creates a minimal session for harness runs that have
// hooks but no transcript yet (e.g. ephemeral Codex runs).
func (e *Engine) ensureHookSession(rec adapters.HookRecord, sid string) {
	if rec.Cwd != "" && (rec.Cwd == home.Dir() || adapters.UnderDir(rec.Cwd, home.Dir())) {
		return
	}
	repo := gitx.Root(rec.Cwd)
	s := spec.Session{
		ID: sid, Harness: rec.Harness, HarnessSessionID: rec.SessionID, Cwd: rec.Cwd, Repo: repo,
		StartedAt: rec.TS, LastActivity: rec.TS, State: "open", Models: []string{},
	}
	if repo != "" {
		s.RepoName = filepath.Base(repo)
		s.Branch = gitx.Branch(repo)
	} else {
		s.Skipped, s.SkipReason = true, "not_in_git_repo"
	}
	s.Human = e.human(repo)
	_ = e.Store.UpsertSession(&s)
}

// DueClosures returns sessions whose end hook has settled.
func (e *Engine) DueClosures(now time.Time) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var res []string
	for sid, t := range e.closing {
		if now.After(t) {
			res = append(res, sid)
			delete(e.closing, sid)
		}
	}
	return res
}

// Forget drops in-memory state for a closed session.
func (e *Engine) Forget(sessionID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.sessions, sessionID)
}

// RefreshSession re-reads a session's transcript (used before closing).
func (e *Engine) RefreshSession(ctx context.Context, sessionID string) {
	s, err := e.Store.GetSession(sessionID)
	if err != nil || s.Transcript.Path == "" {
		return
	}
	a, ok := adapters.Get(s.Harness)
	if !ok {
		return
	}
	if pa, ok := adapters.ForPath(s.Transcript.Path); ok {
		a = pa
	}
	_ = e.ProcessFile(ctx, s.Transcript.Path, a, true)
}

// TranscriptHash returns the sha256 of a transcript file.
func TranscriptHash(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}
