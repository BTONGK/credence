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
)

// ClaimRow is a claim plus its computed state.
type ClaimRow struct {
	Claim         spec.Claim `json:"claim"`
	ClusterID     string     `json:"cluster_id"`
	Status        string     `json:"status"`
	Section       string     `json:"section"`
	Flags         []string   `json:"flags"`
	Notes         []string   `json:"notes"`
	ReviewState   string     `json:"review_state"`
	Review        *Review    `json:"review,omitempty"`
	SurvivalState string     `json:"survival_state"`
	Value         float64    `json:"value"`
	VerifyRun     int        `json:"verify_run"`
	Active        bool       `json:"active"`
	Primary       bool       `json:"primary"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// Review is the latest human decision projected onto a claim.
type Review struct {
	Decision        string    `json:"decision"`
	By              string    `json:"by"`
	Reason          string    `json:"reason,omitempty"`
	Note            string    `json:"note,omitempty"`
	EditedTitle     string    `json:"edited_title,omitempty"`
	EditedStatement string    `json:"edited_statement,omitempty"`
	At              time.Time `json:"at"`
	LedgerSeq       int64     `json:"ledger_seq"`
}

const claimCols = `id,session_id,cluster_id,body,status,section,flags,notes,review_state,review_body,survival_state,value,verify_run,active,primary_claim,updated_at`

func scanClaim(r scanner) (*ClaimRow, error) {
	var c ClaimRow
	var id, sess, body, flags, notes, reviewBody, updated string
	var active, primary int
	if err := r.Scan(&id, &sess, &c.ClusterID, &body, &c.Status, &c.Section, &flags, &notes, &c.ReviewState, &reviewBody,
		&c.SurvivalState, &c.Value, &c.VerifyRun, &active, &primary, &updated); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(body), &c.Claim); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(flags), &c.Flags)
	_ = json.Unmarshal([]byte(notes), &c.Notes)
	if reviewBody != "" {
		var rv Review
		if json.Unmarshal([]byte(reviewBody), &rv) == nil {
			c.Review = &rv
		}
	}
	if c.Flags == nil {
		c.Flags = []string{}
	}
	if c.Notes == nil {
		c.Notes = []string{}
	}
	c.Active = active != 0
	c.Primary = primary != 0
	c.UpdatedAt = ParseTS(updated)
	return &c, nil
}

// InsertClaim stores a claim; returns false if it already existed.
func (s *Store) InsertClaim(c *spec.Claim) (bool, error) {
	body, err := json.Marshal(c)
	if err != nil {
		return false, err
	}
	var conf any
	if c.SelfConfidence != nil {
		conf = *c.SelfConfidence
	}
	now := TS(time.Now())
	res, err := s.DB.Exec(`INSERT OR IGNORE INTO claims(id,session_id,origin,claimant,type,title,statement,body,claim_hash,self_confidence,created_at,updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, c.ID, c.SessionID, c.Origin, c.Claimant, c.Type, c.Title, c.Statement, string(body), c.ContentHash(), conf, now, now)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		// Re-activate a claim that a later pipeline run produced again.
		_, err = s.DB.Exec(`UPDATE claims SET active=1 WHERE id=?`, c.ID)
	}
	return n > 0, err
}

// UpdateClaimBody replaces a claim's stored body (used when evidence hints resolve).
func (s *Store) UpdateClaimBody(c *spec.Claim) error {
	body, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(`UPDATE claims SET body=?, updated_at=? WHERE id=?`, string(body), TS(time.Now()), c.ID)
	return err
}

// GetClaim returns a claim by id or unique prefix.
func (s *Store) GetClaim(id string) (*ClaimRow, error) {
	c, err := scanClaim(s.DB.QueryRow(`SELECT `+claimCols+` FROM claims WHERE id=?`, id))
	if err == nil {
		return c, nil
	}
	rows, qerr := s.DB.Query(`SELECT `+claimCols+` FROM claims WHERE id LIKE ? LIMIT 2`, id+"%")
	if qerr != nil {
		return nil, qerr
	}
	defer rows.Close()
	var res []*ClaimRow
	for rows.Next() {
		r, err := scanClaim(rows)
		if err != nil {
			return nil, err
		}
		res = append(res, r)
	}
	if len(res) == 1 {
		return res[0], nil
	}
	if len(res) > 1 {
		return nil, fmt.Errorf("claim reference %q is ambiguous", id)
	}
	return nil, err
}

// ClaimFilter narrows ListClaims.
type ClaimFilter struct {
	SessionID   string
	ActiveOnly  bool
	PrimaryOnly bool
	ReviewState string
	Origin      string
	Since       time.Time
}

// ListClaims returns claims matching f.
func (s *Store) ListClaims(f ClaimFilter) ([]*ClaimRow, error) {
	var where []string
	var args []any
	if f.SessionID != "" {
		where = append(where, "session_id=?")
		args = append(args, f.SessionID)
	}
	if f.ActiveOnly {
		where = append(where, "active=1")
	}
	if f.PrimaryOnly {
		where = append(where, "primary_claim=1")
	}
	if f.ReviewState != "" {
		where = append(where, "review_state=?")
		args = append(args, f.ReviewState)
	}
	if f.Origin != "" {
		where = append(where, "origin=?")
		args = append(args, f.Origin)
	}
	if !f.Since.IsZero() {
		where = append(where, "created_at>=?")
		args = append(args, TS(f.Since))
	}
	q := `SELECT ` + claimCols + ` FROM claims`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY created_at, id"
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []*ClaimRow
	for rows.Next() {
		c, err := scanClaim(rows)
		if err != nil {
			return nil, err
		}
		res = append(res, c)
	}
	return res, rows.Err()
}

// SessionClaims returns the active claims of a session.
func (s *Store) SessionClaims(sessionID string) ([]*ClaimRow, error) {
	return s.ListClaims(ClaimFilter{SessionID: sessionID, ActiveOnly: true})
}

// DeactivateClaims marks unreviewed claims of a session from an origin inactive
// except those in keep. Reviewed claims always stay.
func (s *Store) DeactivateClaims(sessionID, origin string, keep map[string]bool) error {
	rows, err := s.DB.Query(`SELECT id FROM claims WHERE session_id=? AND origin=? AND review_state='pending' AND active=1`, sessionID, origin)
	if err != nil {
		return err
	}
	var drop []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		if !keep[id] {
			drop = append(drop, id)
		}
	}
	rows.Close()
	for _, id := range drop {
		if _, err := s.DB.Exec(`UPDATE claims SET active=0 WHERE id=?`, id); err != nil {
			return err
		}
	}
	return nil
}

// ClaimState is the computed verification state written back to a claim.
type ClaimState struct {
	Status  string
	Section string
	Flags   []string
	Notes   []string
	Value   float64
}

// SetClaimState stores verification results.
func (s *Store) SetClaimState(id string, st ClaimState) error {
	_, err := s.DB.Exec(`UPDATE claims SET status=?, section=?, flags=?, notes=?, value=?, updated_at=? WHERE id=?`,
		st.Status, st.Section, jsonStr(nonNil(st.Flags)), jsonStr(nonNil(st.Notes)), st.Value, TS(time.Now()), id)
	return err
}

// SetClaimCluster assigns cluster membership.
func (s *Store) SetClaimCluster(id, cluster string, primary bool) error {
	p := 0
	if primary {
		p = 1
	}
	_, err := s.DB.Exec(`UPDATE claims SET cluster_id=?, primary_claim=? WHERE id=?`, cluster, p, id)
	return err
}

// BumpVerifyRun increments and returns the claim's verification run number.
func (s *Store) BumpVerifyRun(id string) (int, error) {
	var run int
	err := s.DB.QueryRow(`UPDATE claims SET verify_run=verify_run+1 WHERE id=? RETURNING verify_run`, id).Scan(&run)
	return run, err
}

// SetReview projects a human decision onto a claim.
func (s *Store) SetReview(id string, rv *Review) error {
	_, err := s.DB.Exec(`UPDATE claims SET review_state=?, review_body=?, updated_at=? WHERE id=?`, rv.Decision, jsonStr(rv), TS(time.Now()), id)
	return err
}

// SetSurvival projects a survival check onto a claim.
func (s *Store) SetSurvival(id, state string) error {
	_, err := s.DB.Exec(`UPDATE claims SET survival_state=?, updated_at=? WHERE id=?`, state, TS(time.Now()), id)
	return err
}

// ---- clusters ----

// Cluster groups claims that share evidence.
type Cluster struct {
	ID             string   `json:"id"`
	SessionID      string   `json:"session_id"`
	PrimaryClaimID string   `json:"primary_claim_id"`
	Origin         string   `json:"origin"` // both | self | auditor | human
	Members        []string `json:"members"`
	Flags          []string `json:"flags"`
}

// ReplaceClusters replaces a session's clusters.
func (s *Store) ReplaceClusters(ctx context.Context, sessionID string, cs []Cluster) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM clusters WHERE session_id=?`, sessionID); err != nil {
			return err
		}
		for _, c := range cs {
			if _, err := tx.Exec(`INSERT OR REPLACE INTO clusters(id,session_id,primary_claim_id,origin,members,flags) VALUES(?,?,?,?,?,?)`,
				c.ID, sessionID, c.PrimaryClaimID, c.Origin, jsonStr(c.Members), jsonStr(nonNil(c.Flags))); err != nil {
				return err
			}
			for _, m := range c.Members {
				p := 0
				if m == c.PrimaryClaimID {
					p = 1
				}
				if _, err := tx.Exec(`UPDATE claims SET cluster_id=?, primary_claim=? WHERE id=?`, c.ID, p, m); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// Clusters returns a session's clusters.
func (s *Store) Clusters(sessionID string) ([]Cluster, error) {
	rows, err := s.DB.Query(`SELECT id,session_id,primary_claim_id,origin,members,flags FROM clusters WHERE session_id=?`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []Cluster
	for rows.Next() {
		var c Cluster
		var members, flags string
		if err := rows.Scan(&c.ID, &c.SessionID, &c.PrimaryClaimID, &c.Origin, &members, &flags); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(members), &c.Members)
		_ = json.Unmarshal([]byte(flags), &c.Flags)
		res = append(res, c)
	}
	return res, rows.Err()
}

// ---- verdicts ----

// InsertVerdict stores a verdict for a verification run.
func (s *Store) InsertVerdict(v *spec.Verdict, run int) error {
	if v.ID == "" {
		v.AssignID()
	}
	_, err := s.DB.Exec(`INSERT OR REPLACE INTO verdicts(id,claim_id,run,verifier,tier,check_name,result,outcome,details,body,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		v.ID, v.ClaimID, run, v.Verifier, v.Tier, v.Check, v.Result, v.Outcome, v.Details, jsonStr(v), TS(v.CreatedAt))
	return err
}

// Verdicts returns the verdicts of a claim's latest run (or a given run if run>=0).
func (s *Store) Verdicts(claimID string, run int) ([]spec.Verdict, error) {
	if run < 0 {
		_ = s.DB.QueryRow(`SELECT COALESCE(MAX(run),0) FROM verdicts WHERE claim_id=?`, claimID).Scan(&run)
	}
	rows, err := s.DB.Query(`SELECT body FROM verdicts WHERE claim_id=? AND run=? ORDER BY tier, created_at`, claimID, run)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []spec.Verdict
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		var v spec.Verdict
		if err := json.Unmarshal([]byte(body), &v); err == nil {
			res = append(res, v)
		}
	}
	return res, rows.Err()
}

// ---- attributions ----

// PutAttribution stores an attribution. Human overrides are not replaced by computed ones.
func (s *Store) PutAttribution(a *spec.Attribution, override bool) error {
	o := 0
	if override {
		o = 1
	}
	q := `INSERT INTO attributions(claim_id,method,body,overridden,updated_at) VALUES(?,?,?,?,?)
ON CONFLICT(claim_id) DO UPDATE SET method=excluded.method, body=excluded.body, overridden=excluded.overridden, updated_at=excluded.updated_at`
	if !override {
		q += ` WHERE attributions.overridden=0`
	}
	_, err := s.DB.Exec(q, a.ClaimID, a.Method, jsonStr(a), o, TS(time.Now()))
	return err
}

// GetAttribution returns a claim's attribution.
func (s *Store) GetAttribution(claimID string) (*spec.Attribution, error) {
	var body string
	if err := s.DB.QueryRow(`SELECT body FROM attributions WHERE claim_id=?`, claimID).Scan(&body); err != nil {
		return nil, err
	}
	var a spec.Attribution
	err := json.Unmarshal([]byte(body), &a)
	return &a, err
}

// ---- ledger ----

// Signer signs an entry hash.
type Signer func(entryHash string) (string, error)

// AppendLedger appends an entry, chaining it to the previous entry and signing it.
func (s *Store) AppendLedger(ctx context.Context, e *spec.LedgerEntry, sign Signer) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		var prev string
		var seq int64
		err := tx.QueryRow(`SELECT seq, entry_hash FROM ledger ORDER BY seq DESC LIMIT 1`).Scan(&seq, &prev)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		e.Spec = spec.LedgerSpec
		e.Seq = seq + 1
		e.PrevEntryHash = prev
		if e.CreatedAt.IsZero() {
			e.CreatedAt = time.Now().UTC()
		}
		e.EntryHash = e.ComputeHash()
		sig, err := sign(e.EntryHash)
		if err != nil {
			return err
		}
		e.Signature = sig
		_, err = tx.Exec(`INSERT INTO ledger(seq,kind,claim_id,entry_hash,prev_entry_hash,body,created_at) VALUES(?,?,?,?,?,?,?)`,
			e.Seq, e.Kind, e.ClaimID, e.EntryHash, e.PrevEntryHash, jsonStr(e), TS(e.CreatedAt))
		return err
	})
}

// LedgerFilter narrows LedgerEntries.
type LedgerFilter struct {
	ClaimID  string
	Kind     string
	Since    time.Time
	AfterSeq int64
	Limit    int
}

// LedgerEntries returns entries in seq order.
func (s *Store) LedgerEntries(f LedgerFilter) ([]spec.LedgerEntry, error) {
	var where []string
	var args []any
	if f.ClaimID != "" {
		where = append(where, "claim_id=?")
		args = append(args, f.ClaimID)
	}
	if f.Kind != "" {
		where = append(where, "kind=?")
		args = append(args, f.Kind)
	}
	if !f.Since.IsZero() {
		where = append(where, "created_at>=?")
		args = append(args, TS(f.Since))
	}
	if f.AfterSeq > 0 {
		where = append(where, "seq>?")
		args = append(args, f.AfterSeq)
	}
	q := `SELECT body FROM ledger`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY seq"
	if f.Limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", f.Limit)
	}
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []spec.LedgerEntry
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		var e spec.LedgerEntry
		if err := json.Unmarshal([]byte(body), &e); err != nil {
			return nil, err
		}
		res = append(res, e)
	}
	return res, rows.Err()
}

// ---- jobs ----

// Job is a unit of idempotent background work.
type Job struct {
	ID        int64
	Kind      string
	Key       string
	SessionID string
	ClaimID   string
	Attempts  int
}

// Enqueue adds a job unless one with the same kind and key exists.
// A failed job with the same key is reset to pending.
func (s *Store) Enqueue(kind, key, sessionID, claimID string, runAfter time.Time) (bool, error) {
	now := TS(time.Now())
	if runAfter.IsZero() {
		runAfter = time.Now()
	}
	res, err := s.DB.Exec(`INSERT INTO jobs(kind,key,session_id,claim_id,status,run_after,created_at,updated_at) VALUES(?,?,?,?,'pending',?,?,?)
ON CONFLICT(kind,key) DO UPDATE SET status='pending', attempts=0, run_after=excluded.run_after, updated_at=excluded.updated_at
WHERE jobs.status='failed'`, kind, key, sessionID, claimID, TS(runAfter), now, now)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// NextJob claims the next runnable job.
func (s *Store) NextJob(kinds []string) (*Job, error) {
	now := TS(time.Now())
	q := `UPDATE jobs SET status='running', attempts=attempts+1, updated_at=?
WHERE id=(SELECT id FROM jobs WHERE status='pending' AND run_after<=?`
	args := []any{now, now}
	if len(kinds) > 0 {
		q += ` AND kind IN (` + strings.TrimSuffix(strings.Repeat("?,", len(kinds)), ",") + `)`
		for _, k := range kinds {
			args = append(args, k)
		}
	}
	q += ` ORDER BY run_after, id LIMIT 1) RETURNING id,kind,key,session_id,claim_id,attempts`
	var j Job
	err := s.DB.QueryRow(q, args...).Scan(&j.ID, &j.Kind, &j.Key, &j.SessionID, &j.ClaimID, &j.Attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &j, err
}

// FinishJob marks a job done, or schedules a retry / marks it failed.
func (s *Store) FinishJob(j *Job, jobErr error, maxAttempts int) error {
	now := time.Now()
	if jobErr == nil {
		_, err := s.DB.Exec(`UPDATE jobs SET status='done', last_error='', updated_at=? WHERE id=?`, TS(now), j.ID)
		return err
	}
	if j.Attempts >= maxAttempts {
		_, err := s.DB.Exec(`UPDATE jobs SET status='failed', last_error=?, updated_at=? WHERE id=?`, jobErr.Error(), TS(now), j.ID)
		return err
	}
	backoff := time.Duration(j.Attempts*j.Attempts) * 15 * time.Second
	_, err := s.DB.Exec(`UPDATE jobs SET status='pending', last_error=?, run_after=?, updated_at=? WHERE id=?`,
		jobErr.Error(), TS(now.Add(backoff)), TS(now), j.ID)
	return err
}

// ResetRunningJobs returns jobs left running by a crash to pending.
func (s *Store) ResetRunningJobs() error {
	_, err := s.DB.Exec(`UPDATE jobs SET status='pending' WHERE status='running'`)
	return err
}

// JobCounts summarizes the queue.
func (s *Store) JobCounts() map[string]int {
	res := map[string]int{}
	rows, err := s.DB.Query(`SELECT status, COUNT(*) FROM jobs GROUP BY status`)
	if err != nil {
		return res
	}
	defer rows.Close()
	for rows.Next() {
		var st string
		var n int
		if rows.Scan(&st, &n) == nil {
			res[st] = n
		}
	}
	return res
}

// RecentJobErrors returns the latest failed jobs.
func (s *Store) RecentJobErrors(limit int) []map[string]string {
	var res []map[string]string
	rows, err := s.DB.Query(`SELECT kind, key, last_error, updated_at FROM jobs WHERE last_error<>'' ORDER BY updated_at DESC LIMIT ?`, limit)
	if err != nil {
		return res
	}
	defer rows.Close()
	for rows.Next() {
		var k, key, e, at string
		if rows.Scan(&k, &key, &e, &at) == nil {
			res = append(res, map[string]string{"kind": k, "key": key, "error": e, "at": at})
		}
	}
	return res
}
