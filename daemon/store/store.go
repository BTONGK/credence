// Package store is Credence's SQLite persistence layer. The daemon is the
// only writer of the ledger; other processes may read.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/BTONGK/credence/spec"
	_ "modernc.org/sqlite"
)

// Store wraps the database.
type Store struct {
	DB   *sql.DB
	path string
}

// Open opens (and migrates) the database at path.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &Store{DB: db, path: path}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.DB.Close() }

// Path returns the database file path.
func (s *Store) Path() string { return s.path }

// TS formats a time for storage.
func TS(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// ParseTS parses a stored time.
func ParseTS(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func jsonStr(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}

// Tx runs fn in a transaction.
func (s *Store) Tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// ---- meta ----

// GetMeta returns a meta value.
func (s *Store) GetMeta(key string) string {
	var v string
	_ = s.DB.QueryRow(`SELECT value FROM meta WHERE key=?`, key).Scan(&v)
	return v
}

// SetMeta sets a meta value.
func (s *Store) SetMeta(key, value string) error {
	_, err := s.DB.Exec(`INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// ---- sessions ----

// SessionRow is a session plus pipeline bookkeeping.
type SessionRow struct {
	spec.Session
	StopSeenAt  time.Time `json:"stop_seen_at,omitempty"`
	ReportState string    `json:"report_state"`
	ReportHash  string    `json:"report_hash"`
	ReportAt    time.Time `json:"report_at,omitempty"`
}

const sessionCols = `id,harness,harness_session_id,parent_session_id,agent_type,repo,repo_name,branch,cwd,title,
started_at,ended_at,last_activity,state,models,human,agent_config,harness_version,tokens,cost_usd,
transcript_path,transcript_sha256,transcript_lines,event_count,head_hash,start_commit,skipped,skip_reason,flags,
stop_seen_at,report_state,report_hash,report_at`

type scanner interface{ Scan(...any) error }

func scanSession(r scanner) (*SessionRow, error) {
	var s SessionRow
	var started, ended, last, models, tokens, flags, stop, reportAt string
	var skipped int
	err := r.Scan(&s.ID, &s.Harness, &s.HarnessSessionID, &s.ParentSessionID, &s.AgentType, &s.Repo, &s.RepoName,
		&s.Branch, &s.Cwd, &s.Title, &started, &ended, &last, &s.State, &models, &s.Human, &s.AgentConfig,
		&s.HarnessVersion, &tokens, &s.CostUSD, &s.Transcript.Path, &s.Transcript.SHA256, &s.Transcript.Lines,
		&s.EventCount, &s.HeadHash, &s.StartCommit, &skipped, &s.SkipReason, &flags, &stop, &s.ReportState,
		&s.ReportHash, &reportAt)
	if err != nil {
		return nil, err
	}
	s.StartedAt = ParseTS(started)
	if ended != "" {
		t := ParseTS(ended)
		s.EndedAt = &t
	}
	s.LastActivity = ParseTS(last)
	s.StopSeenAt = ParseTS(stop)
	s.ReportAt = ParseTS(reportAt)
	s.Skipped = skipped != 0
	_ = json.Unmarshal([]byte(models), &s.Models)
	_ = json.Unmarshal([]byte(tokens), &s.Tokens)
	_ = json.Unmarshal([]byte(flags), &s.Flags)
	if s.Models == nil {
		s.Models = []string{}
	}
	return &s, nil
}

// UpsertSession inserts or fully updates a session's descriptive fields.
// Pipeline bookkeeping columns are left untouched on update.
func (s *Store) UpsertSession(x *spec.Session) error {
	ended := ""
	if x.EndedAt != nil {
		ended = TS(*x.EndedAt)
	}
	skipped := 0
	if x.Skipped {
		skipped = 1
	}
	if x.Models == nil {
		x.Models = []string{}
	}
	_, err := s.DB.Exec(`INSERT INTO sessions(`+sessionCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'','none','','')
ON CONFLICT(id) DO UPDATE SET parent_session_id=excluded.parent_session_id, agent_type=excluded.agent_type,
 repo=excluded.repo, repo_name=excluded.repo_name, branch=excluded.branch, cwd=excluded.cwd, title=excluded.title,
 started_at=excluded.started_at, ended_at=excluded.ended_at, last_activity=excluded.last_activity, state=excluded.state,
 models=excluded.models, human=excluded.human, agent_config=excluded.agent_config, harness_version=excluded.harness_version,
 tokens=excluded.tokens, cost_usd=excluded.cost_usd, transcript_path=excluded.transcript_path,
 transcript_sha256=excluded.transcript_sha256, transcript_lines=excluded.transcript_lines,
 start_commit=excluded.start_commit, skipped=excluded.skipped, skip_reason=excluded.skip_reason, flags=excluded.flags`,
		x.ID, x.Harness, x.HarnessSessionID, x.ParentSessionID, x.AgentType, x.Repo, x.RepoName, x.Branch, x.Cwd, x.Title,
		TS(x.StartedAt), ended, TS(x.LastActivity), x.State, jsonStr(x.Models), x.Human, x.AgentConfig, x.HarnessVersion,
		jsonStr(x.Tokens), x.CostUSD, x.Transcript.Path, x.Transcript.SHA256, x.Transcript.Lines, x.EventCount, x.HeadHash,
		x.StartCommit, skipped, x.SkipReason, jsonStr(nonNil(x.Flags)))
	return err
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// GetSession returns a session by id.
func (s *Store) GetSession(id string) (*SessionRow, error) {
	return scanSession(s.DB.QueryRow(`SELECT `+sessionCols+` FROM sessions WHERE id=?`, id))
}

// FindSession resolves an id, unique id prefix, or harness session id.
func (s *Store) FindSession(ref string) (*SessionRow, error) {
	if r, err := s.GetSession(ref); err == nil {
		return r, nil
	}
	rows, err := s.DB.Query(`SELECT `+sessionCols+` FROM sessions WHERE id LIKE ? OR harness_session_id LIKE ? LIMIT 2`, ref+"%", ref+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []*SessionRow
	for rows.Next() {
		r, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		res = append(res, r)
	}
	switch len(res) {
	case 0:
		return nil, sql.ErrNoRows
	case 1:
		return res[0], nil
	}
	return nil, fmt.Errorf("session reference %q is ambiguous", ref)
}

// SessionByHarness finds a session by harness and harness session id.
func (s *Store) SessionByHarness(harness, hid string) (*SessionRow, error) {
	return scanSession(s.DB.QueryRow(`SELECT `+sessionCols+` FROM sessions WHERE harness=? AND harness_session_id=?`, harness, hid))
}

// SessionFilter narrows ListSessions.
type SessionFilter struct {
	NeedsReview bool
	Repo        string
	Harness     string
	AgentConfig string
	Since       time.Time
	Until       time.Time
	Query       string
	IncludeKids bool
	Limit       int
}

// ListSessions lists sessions newest first.
func (s *Store) ListSessions(f SessionFilter) ([]*SessionRow, error) {
	var where []string
	var args []any
	if !f.IncludeKids {
		where = append(where, "parent_session_id=''")
	}
	if f.Repo != "" {
		where = append(where, "(repo=? OR repo_name=?)")
		args = append(args, f.Repo, f.Repo)
	}
	if f.Harness != "" {
		where = append(where, "harness=?")
		args = append(args, f.Harness)
	}
	if f.AgentConfig != "" {
		where = append(where, "agent_config=?")
		args = append(args, f.AgentConfig)
	}
	if !f.Since.IsZero() {
		where = append(where, "last_activity>=?")
		args = append(args, TS(f.Since))
	}
	if !f.Until.IsZero() {
		where = append(where, "started_at<=?")
		args = append(args, TS(f.Until))
	}
	if f.Query != "" {
		where = append(where, "(title LIKE ? OR repo_name LIKE ? OR branch LIKE ?)")
		q := "%" + f.Query + "%"
		args = append(args, q, q, q)
	}
	if f.NeedsReview {
		where = append(where, `skipped=0 AND EXISTS (SELECT 1 FROM claims c WHERE c.session_id=sessions.id AND c.active=1 AND c.primary_claim=1 AND c.review_state='pending')`)
	}
	q := `SELECT ` + sessionCols + ` FROM sessions`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY last_activity DESC"
	if f.Limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", f.Limit)
	}
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []*SessionRow
	for rows.Next() {
		r, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		res = append(res, r)
	}
	return res, rows.Err()
}

// ChildSessions lists subagent sessions of parent.
func (s *Store) ChildSessions(parent string) ([]*SessionRow, error) {
	rows, err := s.DB.Query(`SELECT `+sessionCols+` FROM sessions WHERE parent_session_id=? ORDER BY started_at`, parent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []*SessionRow
	for rows.Next() {
		r, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		res = append(res, r)
	}
	return res, rows.Err()
}

// SetSessionState updates state (and ended_at when closing).
func (s *Store) SetSessionState(id, state string, ended *time.Time) error {
	e := ""
	if ended != nil {
		e = TS(*ended)
	}
	_, err := s.DB.Exec(`UPDATE sessions SET state=?, ended_at=CASE WHEN ?<>'' THEN ? ELSE ended_at END WHERE id=?`, state, e, e, id)
	return err
}

// MarkStop records that the harness signalled the end of a turn.
func (s *Store) MarkStop(id string, at time.Time) error {
	_, err := s.DB.Exec(`UPDATE sessions SET stop_seen_at=?, state=CASE WHEN state='closed' THEN state ELSE 'open' END WHERE id=?`, TS(at), id)
	return err
}

// Touch bumps last_activity and reopens a session that went idle.
func (s *Store) Touch(id string, at time.Time) error {
	_, err := s.DB.Exec(`UPDATE sessions SET last_activity=MAX(last_activity, ?), state=CASE WHEN state='idle' THEN 'open' ELSE state END WHERE id=?`, TS(at), id)
	return err
}

// SetReportState records pipeline progress for a session.
func (s *Store) SetReportState(id, state, hash string) error {
	_, err := s.DB.Exec(`UPDATE sessions SET report_state=?, report_hash=?, report_at=? WHERE id=?`, state, hash, TS(time.Now()), id)
	return err
}

// AddSessionFlag adds a flag (idempotent).
func (s *Store) AddSessionFlag(id, flag string) error {
	r, err := s.GetSession(id)
	if err != nil {
		return err
	}
	for _, f := range r.Flags {
		if f == flag {
			return nil
		}
	}
	r.Flags = append(r.Flags, flag)
	_, err = s.DB.Exec(`UPDATE sessions SET flags=? WHERE id=?`, jsonStr(r.Flags), id)
	return err
}

// SetSkipped marks a session skipped (or not).
func (s *Store) SetSkipped(id string, skipped bool, reason string) error {
	v := 0
	if skipped {
		v = 1
	}
	_, err := s.DB.Exec(`UPDATE sessions SET skipped=?, skip_reason=? WHERE id=?`, v, reason, id)
	return err
}

// ---- events ----

// AppendEvents appends events to a session, assigning seq and chaining hashes.
func (s *Store) AppendEvents(ctx context.Context, sessionID string, evs []spec.Event) ([]spec.Event, error) {
	if len(evs) == 0 {
		return nil, nil
	}
	out := make([]spec.Event, 0, len(evs))
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		var seq int
		var prev string
		err := tx.QueryRow(`SELECT seq, hash FROM events WHERE session_id=? ORDER BY seq DESC LIMIT 1`, sessionID).Scan(&seq, &prev)
		if errors.Is(err, sql.ErrNoRows) {
			seq, prev = 0, ""
		} else if err != nil {
			return err
		}
		stmt, err := tx.Prepare(`INSERT INTO events(session_id,seq,ts,kind,actor,payload,payload_ref,src_path,src_line,hash,prev_hash) VALUES(?,?,?,?,?,?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, e := range evs {
			seq++
			e.SessionID = sessionID
			e.Seq = seq
			if len(e.Payload) == 0 {
				e.Payload = json.RawMessage("{}")
			}
			e.PayloadRef = spec.HashBytes(e.Payload)
			e.Prev = prev
			e.Hash = e.ChainHash()
			if _, err := stmt.Exec(e.SessionID, e.Seq, TS(e.TS), e.Kind, e.Actor, string(e.Payload), e.PayloadRef,
				e.SrcPath, e.SrcLine, e.Hash, e.Prev); err != nil {
				return err
			}
			prev = e.Hash
			out = append(out, e)
		}
		_, err = tx.Exec(`UPDATE sessions SET event_count=?, head_hash=? WHERE id=?`, seq, prev, sessionID)
		return err
	})
	return out, err
}

func scanEvent(r scanner) (spec.Event, error) {
	var e spec.Event
	var ts, payload string
	err := r.Scan(&e.SessionID, &e.Seq, &ts, &e.Kind, &e.Actor, &payload, &e.PayloadRef, &e.SrcPath, &e.SrcLine, &e.Hash, &e.Prev)
	e.TS = ParseTS(ts)
	e.Payload = json.RawMessage(payload)
	return e, err
}

const eventCols = `session_id,seq,ts,kind,actor,payload,payload_ref,src_path,src_line,hash,prev_hash`

// Events returns events of a session in [from, to] (to<=0 means all).
func (s *Store) Events(sessionID string, from, to int) ([]spec.Event, error) {
	if to <= 0 {
		to = 1 << 30
	}
	rows, err := s.DB.Query(`SELECT `+eventCols+` FROM events WHERE session_id=? AND seq>=? AND seq<=? ORDER BY seq`, sessionID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []spec.Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		res = append(res, e)
	}
	return res, rows.Err()
}

// Event returns one event.
func (s *Store) Event(sessionID string, seq int) (spec.Event, error) {
	return scanEvent(s.DB.QueryRow(`SELECT `+eventCols+` FROM events WHERE session_id=? AND seq=?`, sessionID, seq))
}

// EventsByKind returns a session's events of the given kinds.
func (s *Store) EventsByKind(sessionID string, kinds ...string) ([]spec.Event, error) {
	if len(kinds) == 0 {
		return s.Events(sessionID, 0, 0)
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(kinds)), ",")
	args := []any{sessionID}
	for _, k := range kinds {
		args = append(args, k)
	}
	rows, err := s.DB.Query(`SELECT `+eventCols+` FROM events WHERE session_id=? AND kind IN (`+ph+`) ORDER BY seq`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []spec.Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		res = append(res, e)
	}
	return res, rows.Err()
}

// ---- transcripts ----

// TranscriptState is the watcher's progress through one transcript file.
type TranscriptState struct {
	Path      string
	Harness   string
	SessionID string
	Offset    int64
	Lines     int
	Size      int64
	MTime     time.Time
}

// GetTranscript returns watcher state for path.
func (s *Store) GetTranscript(path string) (*TranscriptState, error) {
	var t TranscriptState
	var mtime string
	err := s.DB.QueryRow(`SELECT path,harness,session_id,offset,lines,size,mtime FROM transcripts WHERE path=?`, path).
		Scan(&t.Path, &t.Harness, &t.SessionID, &t.Offset, &t.Lines, &t.Size, &mtime)
	if err != nil {
		return nil, err
	}
	t.MTime = ParseTS(mtime)
	return &t, nil
}

// PutTranscript saves watcher state.
func (s *Store) PutTranscript(t *TranscriptState) error {
	_, err := s.DB.Exec(`INSERT INTO transcripts(path,harness,session_id,offset,lines,size,mtime,updated_at) VALUES(?,?,?,?,?,?,?,?)
ON CONFLICT(path) DO UPDATE SET harness=excluded.harness, session_id=excluded.session_id, offset=excluded.offset,
 lines=excluded.lines, size=excluded.size, mtime=excluded.mtime, updated_at=excluded.updated_at`,
		t.Path, t.Harness, t.SessionID, t.Offset, t.Lines, t.Size, TS(t.MTime), TS(time.Now()))
	return err
}

// ListTranscripts returns all known transcripts.
func (s *Store) ListTranscripts() ([]*TranscriptState, error) {
	rows, err := s.DB.Query(`SELECT path,harness,session_id,offset,lines,size,mtime FROM transcripts`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []*TranscriptState
	for rows.Next() {
		var t TranscriptState
		var mtime string
		if err := rows.Scan(&t.Path, &t.Harness, &t.SessionID, &t.Offset, &t.Lines, &t.Size, &mtime); err != nil {
			return nil, err
		}
		t.MTime = ParseTS(mtime)
		res = append(res, &t)
	}
	return res, rows.Err()
}

// LineHashes returns stored line hashes for a transcript (1-based line -> hash).
func (s *Store) LineHashes(path string) (map[int]string, error) {
	rows, err := s.DB.Query(`SELECT line_no, hash FROM transcript_lines WHERE path=?`, path)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[int]string{}
	for rows.Next() {
		var n int
		var h string
		if err := rows.Scan(&n, &h); err != nil {
			return nil, err
		}
		m[n] = h
	}
	return m, rows.Err()
}

// PutLineHashes stores hashes for newly seen lines (never overwrites).
func (s *Store) PutLineHashes(ctx context.Context, path string, start int, hashes []string) error {
	if len(hashes) == 0 {
		return nil
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.Prepare(`INSERT OR IGNORE INTO transcript_lines(path,line_no,hash) VALUES(?,?,?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for i, h := range hashes {
			if _, err := stmt.Exec(path, start+i, h); err != nil {
				return err
			}
		}
		return nil
	})
}

// ---- spool ----

// SpoolOffset returns the consumed byte offset of a spool file.
func (s *Store) SpoolOffset(path string) int64 {
	var o int64
	_ = s.DB.QueryRow(`SELECT offset FROM spool_offsets WHERE path=?`, path).Scan(&o)
	return o
}

// SetSpoolOffset records the consumed byte offset.
func (s *Store) SetSpoolOffset(path string, off int64) error {
	_, err := s.DB.Exec(`INSERT INTO spool_offsets(path,offset) VALUES(?,?) ON CONFLICT(path) DO UPDATE SET offset=excluded.offset`, path, off)
	return err
}

// HookEvent is one record appended by a capture hook.
type HookEvent struct {
	ID               int64           `json:"id"`
	Harness          string          `json:"harness"`
	Event            string          `json:"event"`
	HarnessSessionID string          `json:"session_id"`
	TranscriptPath   string          `json:"transcript_path"`
	Cwd              string          `json:"cwd"`
	TS               time.Time       `json:"ts"`
	Raw              json.RawMessage `json:"raw"`
}

// InsertHookEvent stores a hook record.
func (s *Store) InsertHookEvent(h *HookEvent) error {
	raw := string(h.Raw)
	if raw == "" {
		raw = "{}"
	}
	_, err := s.DB.Exec(`INSERT INTO hook_events(harness,event,harness_session_id,transcript_path,cwd,ts,raw) VALUES(?,?,?,?,?,?,?)`,
		h.Harness, h.Event, h.HarnessSessionID, h.TranscriptPath, h.Cwd, TS(h.TS), raw)
	return err
}

// HookStats summarizes recent hook activity per harness and event.
func (s *Store) HookStats(since time.Time) (map[string]map[string]int, error) {
	rows, err := s.DB.Query(`SELECT harness, event, COUNT(*) FROM hook_events WHERE ts>=? GROUP BY harness, event`, TS(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	res := map[string]map[string]int{}
	for rows.Next() {
		var h, e string
		var n int
		if err := rows.Scan(&h, &e, &n); err != nil {
			return nil, err
		}
		if res[h] == nil {
			res[h] = map[string]int{}
		}
		res[h][e] = n
	}
	return res, rows.Err()
}

// LastHook returns the time of the most recent hook for a harness.
func (s *Store) LastHook(harness string) time.Time {
	var ts string
	_ = s.DB.QueryRow(`SELECT ts FROM hook_events WHERE harness=? ORDER BY id DESC LIMIT 1`, harness).Scan(&ts)
	return ParseTS(ts)
}

// ---- artifacts ----

// UpsertArtifact stores an artifact (by locator) and returns it with its id.
func (s *Store) UpsertArtifact(a *spec.Artifact) error {
	if a.ID == "" {
		a.ID = "art_" + spec.ShortID(spec.HashBytes([]byte(a.Locator)), 10)
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now().UTC()
	}
	meta := string(a.Meta)
	if meta == "" {
		meta = "{}"
	}
	_, err := s.DB.Exec(`INSERT INTO artifacts(id,kind,locator,content_hash,repo,session_id,meta,created_at) VALUES(?,?,?,?,?,?,?,?)
ON CONFLICT(locator) DO UPDATE SET content_hash=excluded.content_hash, meta=excluded.meta`,
		a.ID, a.Kind, a.Locator, a.ContentHash, a.Repo, a.SessionID, meta, TS(a.CreatedAt))
	return err
}

// LinkArtifact associates an artifact with a session.
func (s *Store) LinkArtifact(sessionID, artifactID, relation string) error {
	_, err := s.DB.Exec(`INSERT OR IGNORE INTO session_artifacts(session_id,artifact_id,relation) VALUES(?,?,?)`, sessionID, artifactID, relation)
	return err
}

func scanArtifact(r scanner) (*spec.Artifact, error) {
	var a spec.Artifact
	var meta, created string
	if err := r.Scan(&a.ID, &a.Kind, &a.Locator, &a.ContentHash, &a.Repo, &a.SessionID, &meta, &created); err != nil {
		return nil, err
	}
	a.Meta = json.RawMessage(meta)
	a.CreatedAt = ParseTS(created)
	return &a, nil
}

// ArtifactByLocator finds an artifact.
func (s *Store) ArtifactByLocator(loc string) (*spec.Artifact, error) {
	return scanArtifact(s.DB.QueryRow(`SELECT id,kind,locator,content_hash,repo,session_id,meta,created_at FROM artifacts WHERE locator=?`, loc))
}

// SessionArtifacts lists artifacts linked to a session.
func (s *Store) SessionArtifacts(sessionID string) ([]*spec.Artifact, error) {
	rows, err := s.DB.Query(`SELECT a.id,a.kind,a.locator,a.content_hash,a.repo,a.session_id,a.meta,a.created_at
FROM artifacts a JOIN session_artifacts sa ON sa.artifact_id=a.id WHERE sa.session_id=? ORDER BY a.created_at`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []*spec.Artifact
	for rows.Next() {
		a, err := scanArtifact(rows)
		if err != nil {
			return nil, err
		}
		res = append(res, a)
	}
	return res, rows.Err()
}

// ArtifactsByKind lists artifacts of a kind for a repo.
func (s *Store) ArtifactsByKind(kind, repo string) ([]*spec.Artifact, error) {
	rows, err := s.DB.Query(`SELECT id,kind,locator,content_hash,repo,session_id,meta,created_at FROM artifacts WHERE kind=? AND repo=? ORDER BY created_at`, kind, repo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []*spec.Artifact
	for rows.Next() {
		a, err := scanArtifact(rows)
		if err != nil {
			return nil, err
		}
		res = append(res, a)
	}
	return res, rows.Err()
}

// SessionsWithArtifact lists sessions linked to an artifact locator.
func (s *Store) SessionsWithArtifact(locator string) ([]string, error) {
	rows, err := s.DB.Query(`SELECT sa.session_id FROM session_artifacts sa JOIN artifacts a ON a.id=sa.artifact_id WHERE a.locator=?`, locator)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		res = append(res, id)
	}
	return res, rows.Err()
}

// ---- test runs ----

// TestRun is a test execution performed by a verifier.
type TestRun struct {
	ID        string    `json:"id"`
	ClaimID   string    `json:"claim_id"`
	Repo      string    `json:"repo"`
	Commit    string    `json:"commit"`
	Target    string    `json:"target"`
	Command   string    `json:"command"`
	Runs      int       `json:"runs"`
	Passes    int       `json:"passes"`
	Output    string    `json:"output"`
	CreatedAt time.Time `json:"created_at"`
}

// InsertTestRun stores a run and assigns its id.
func (s *Store) InsertTestRun(t *TestRun) error {
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now().UTC()
	}
	if t.ID == "" {
		t.ID = "run_" + spec.ShortHex(spec.HashBytes([]byte(fmt.Sprint(t.ClaimID, t.Commit, t.Target, t.Command, t.CreatedAt.UnixNano()))), 10)
	}
	_, err := s.DB.Exec(`INSERT OR REPLACE INTO test_runs(id,claim_id,repo,commit_sha,target,command,runs,passes,output,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.ClaimID, t.Repo, t.Commit, t.Target, t.Command, t.Runs, t.Passes, t.Output, TS(t.CreatedAt))
	return err
}

// GetTestRun returns a test run.
func (s *Store) GetTestRun(id string) (*TestRun, error) {
	var t TestRun
	var created string
	err := s.DB.QueryRow(`SELECT id,claim_id,repo,commit_sha,target,command,runs,passes,output,created_at FROM test_runs WHERE id=?`, id).
		Scan(&t.ID, &t.ClaimID, &t.Repo, &t.Commit, &t.Target, &t.Command, &t.Runs, &t.Passes, &t.Output, &created)
	t.CreatedAt = ParseTS(created)
	return &t, err
}

// FindTestRun returns the newest cached run of command at commit for target.
func (s *Store) FindTestRun(repo, commit, target, command string) (*TestRun, error) {
	var t TestRun
	var created string
	err := s.DB.QueryRow(`SELECT id,claim_id,repo,commit_sha,target,command,runs,passes,output,created_at FROM test_runs
		WHERE repo=? AND commit_sha=? AND target=? AND command=? ORDER BY created_at DESC LIMIT 1`, repo, commit, target, command).
		Scan(&t.ID, &t.ClaimID, &t.Repo, &t.Commit, &t.Target, &t.Command, &t.Runs, &t.Passes, &t.Output, &created)
	if err != nil {
		return nil, err
	}
	t.CreatedAt = ParseTS(created)
	return &t, nil
}

// ---- agent configs ----

// AgentConfig is a contributor configuration a builder tunes.
type AgentConfig struct {
	ID         string          `json:"id"`
	Harness    string          `json:"harness"`
	Model      string          `json:"model"`
	Components json.RawMessage `json:"components"`
	FirstSeen  time.Time       `json:"first_seen"`
	LastSeen   time.Time       `json:"last_seen"`
}

// UpsertAgentConfig records an agent configuration.
func (s *Store) UpsertAgentConfig(c *AgentConfig) error {
	now := TS(time.Now())
	comp := string(c.Components)
	if comp == "" {
		comp = "{}"
	}
	_, err := s.DB.Exec(`INSERT INTO agent_configs(id,harness,model,components,first_seen,last_seen) VALUES(?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET last_seen=excluded.last_seen`, c.ID, c.Harness, c.Model, comp, now, now)
	return err
}

// AgentConfigs lists configurations.
func (s *Store) AgentConfigs() ([]*AgentConfig, error) {
	rows, err := s.DB.Query(`SELECT id,harness,model,components,first_seen,last_seen FROM agent_configs ORDER BY last_seen DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []*AgentConfig
	for rows.Next() {
		var c AgentConfig
		var comp, fs, ls string
		if err := rows.Scan(&c.ID, &c.Harness, &c.Model, &comp, &fs, &ls); err != nil {
			return nil, err
		}
		c.Components = json.RawMessage(comp)
		c.FirstSeen, c.LastSeen = ParseTS(fs), ParseTS(ls)
		res = append(res, &c)
	}
	return res, rows.Err()
}

// ---- model usage ----

// RecordUsage logs a model call for budgeting.
func (s *Store) RecordUsage(purpose, provider, model, sessionID string, in, out int64, cost float64) error {
	_, err := s.DB.Exec(`INSERT INTO model_usage(ts,purpose,provider,model,tokens_in,tokens_out,cost_usd,session_id) VALUES(?,?,?,?,?,?,?,?)`,
		TS(time.Now()), purpose, provider, model, in, out, cost, sessionID)
	return err
}

// MonthSpend returns model spend since the start of the current month.
func (s *Store) MonthSpend() float64 {
	now := time.Now().UTC()
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	var v sql.NullFloat64
	_ = s.DB.QueryRow(`SELECT SUM(cost_usd) FROM model_usage WHERE ts>=?`, TS(start)).Scan(&v)
	return v.Float64
}

// ---- review timings ----

// RecordReviewTiming stores how long a human took to review a session.
func (s *Store) RecordReviewTiming(sessionID string, ms int64, claims int, surface string) error {
	_, err := s.DB.Exec(`INSERT INTO review_timings(session_id,ms,claims,surface,at) VALUES(?,?,?,?,?)`, sessionID, ms, claims, surface, TS(time.Now()))
	return err
}
