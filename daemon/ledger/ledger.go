// Package ledger records final claim decisions and survival checks as
// signed, hash-chained entries, verifies the chain, exports it and mirrors
// it to git notes.
package ledger

import (
	"context"
	"crypto/ed25519"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/BTONGK/credence/daemon/store"
	"github.com/BTONGK/credence/internal/gitx"
	"github.com/BTONGK/credence/internal/keys"
	"github.com/BTONGK/credence/policy"
	"github.com/BTONGK/credence/spec"
	"github.com/BTONGK/credence/verifiers"
)

// Ledger appends decisions.
type Ledger struct {
	Store  *store.Store
	Signer *keys.Signer
}

// ErrContradicted means a human tried to accept a claim that failed a
// factual check. They can edit it (restate what is true) or re-verify.
var ErrContradicted = errors.New("claim failed a factual check")

// Decide records a human (or policy) decision on a claim.
func (l *Ledger) Decide(ctx context.Context, claimID string, hd spec.HumanDecision) (*spec.LedgerEntry, error) {
	row, err := l.Store.GetClaim(claimID)
	if err != nil {
		return nil, fmt.Errorf("claim %s: %w", claimID, err)
	}
	switch hd.Decision {
	case spec.ReviewAccepted, spec.ReviewEdited, spec.ReviewDisputed, spec.ReviewRejected:
	default:
		return nil, fmt.Errorf("unknown decision %q", hd.Decision)
	}
	if hd.Decision == spec.ReviewDisputed {
		ok := false
		for _, r := range spec.DisputeReasons {
			if r == hd.Reason {
				ok = true
			}
		}
		if !ok {
			return nil, fmt.Errorf("dispute reason must be one of %s", strings.Join(spec.DisputeReasons, ", "))
		}
	}
	if hd.Decision == spec.ReviewEdited && strings.TrimSpace(hd.EditedTitle) == "" && strings.TrimSpace(hd.EditedStatement) == "" {
		return nil, errors.New("an edit needs a new title or statement")
	}
	if hd.Decision == spec.ReviewAccepted && row.Status == spec.StatusContradicted {
		return nil, fmt.Errorf("%w (%s); edit it to what is true, dispute it, or re-run `credence verify %s --rerun`",
			ErrContradicted, strings.Join(row.Notes, "; "), short(claimID))
	}
	if hd.At.IsZero() {
		hd.At = time.Now().UTC()
	}
	sess, _ := l.Store.GetSession(row.Claim.SessionID)
	e := &spec.LedgerEntry{Kind: "decision", ClaimID: row.Claim.ID, Status: row.Status, HumanDecision: &hd}
	if err := l.fill(e, row, sess); err != nil {
		return nil, err
	}
	if err := l.Store.AppendLedger(ctx, e, l.Signer.Sign); err != nil {
		return nil, err
	}
	rv := &store.Review{Decision: hd.Decision, By: hd.By, Reason: hd.Reason, Note: hd.Note, EditedTitle: hd.EditedTitle,
		EditedStatement: hd.EditedStatement, At: hd.At, LedgerSeq: e.Seq}
	if err := l.Store.SetReview(row.Claim.ID, rv); err != nil {
		return e, err
	}
	l.revalue(row, hd.Decision, row.SurvivalState)
	if sess != nil {
		l.mirror(sess.Repo, e)
	}
	return e, nil
}

// Survival records a survival re-check of an accepted claim.
func (l *Ledger) Survival(ctx context.Context, claimID string, sc spec.SurvivalCheck) (*spec.LedgerEntry, error) {
	row, err := l.Store.GetClaim(claimID)
	if err != nil {
		return nil, err
	}
	sess, _ := l.Store.GetSession(row.Claim.SessionID)
	e := &spec.LedgerEntry{Kind: "survival", ClaimID: row.Claim.ID, Status: row.Status, Survival: &sc}
	if err := l.fill(e, row, sess); err != nil {
		return nil, err
	}
	if err := l.Store.AppendLedger(ctx, e, l.Signer.Sign); err != nil {
		return nil, err
	}
	_ = l.Store.SetSurvival(row.Claim.ID, sc.Result)
	l.revalue(row, row.ReviewState, sc.Result)
	if sess != nil {
		l.mirror(sess.Repo, e)
	}
	return e, nil
}

func (l *Ledger) revalue(row *store.ClaimRow, review, survival string) {
	sess, _ := l.Store.GetSession(row.Claim.SessionID)
	pol := policy.Default()
	if sess != nil {
		if p, err := policy.Load(sess.Repo); err == nil {
			pol = p
		}
	}
	v := verifiers.Value(pol, row.Claim.Type, row.Status, review, survival)
	if review == spec.ReviewEdited && v == 0 && row.Status != spec.StatusBlocked {
		v = pol.Type(row.Claim.Type).Weight * 0.6 // the human restated it as true
	}
	_ = l.Store.SetClaimState(row.Claim.ID, store.ClaimState{Status: row.Status, Section: row.Section, Flags: row.Flags, Notes: row.Notes, Value: v})
}

func (l *Ledger) fill(e *spec.LedgerEntry, row *store.ClaimRow, sess *store.SessionRow) error {
	body, err := json.Marshal(row.Claim)
	if err != nil {
		return err
	}
	e.Claim = body
	e.ClaimHash = row.Claim.ContentHash()
	vs, _ := l.Store.Verdicts(row.Claim.ID, -1)
	e.VerdictHashes = []string{}
	for i := range vs {
		e.VerdictHashes = append(e.VerdictHashes, vs[i].ContentHash())
	}
	e.Attribution, _ = l.Store.GetAttribution(row.Claim.ID)
	if sess != nil {
		e.Repo = sess.RepoName
		e.AgentConfig = sess.AgentConfig
	}
	seen := map[string]bool{}
	for _, ev := range row.Claim.Evidence {
		if c := spec.ParseTarget(ev.Target).Commit; c != "" && c != "HEAD" && !seen[c] && ev.Relation != spec.RelInformed {
			seen[c] = true
			e.Commits = append(e.Commits, c)
		}
	}
	return nil
}

// mirror appends the entry to git notes on its commits when the repo's
// policy enables it. Pushing refs/notes/credence is left to the user.
func (l *Ledger) mirror(repo string, e *spec.LedgerEntry) {
	if repo == "" || len(e.Commits) == 0 {
		return
	}
	pol, err := policy.Load(repo)
	if err != nil || !pol.Ledger.GitNotes {
		return
	}
	line, _ := json.Marshal(Note{Entry: e, PublicKey: keys.PublicKeyString(l.Signer.Pub)})
	for _, c := range e.Commits {
		_ = gitx.AppendNote(repo, c, string(line))
	}
}

// Note is one line of refs/notes/credence: a full signed entry plus the key
// that signed it, so anyone with the repo can verify it offline.
type Note struct {
	Entry     *spec.LedgerEntry `json:"entry"`
	PublicKey string            `json:"public_key"`
}

// ParseNotes reads the note lines on a commit and verifies each entry.
func ParseNotes(text string) []VerifiedNote {
	var out []VerifiedNote
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var n Note
		if json.Unmarshal([]byte(line), &n) != nil || n.Entry == nil {
			continue
		}
		vn := VerifiedNote{Note: n}
		if pub, err := keys.ParsePublicKey(n.PublicKey); err == nil {
			vn.KeyID = keys.KeyID(pub)
			vn.Valid = n.Entry.ComputeHash() == n.Entry.EntryHash && keys.Verify(pub, n.Entry.EntryHash, n.Entry.Signature)
			var c spec.Claim
			if json.Unmarshal(n.Entry.Claim, &c) == nil && c.ContentHash() != n.Entry.ClaimHash {
				vn.Valid = false
			}
		}
		out = append(out, vn)
	}
	return out
}

// VerifiedNote is a parsed note with its verification result.
type VerifiedNote struct {
	Note
	KeyID string
	Valid bool
}

// NoteLine is the compact form mirrored into git notes.
func NoteLine(e *spec.LedgerEntry) map[string]any {
	m := map[string]any{"spec": e.Spec, "seq": e.Seq, "kind": e.Kind, "claim_id": e.ClaimID, "claim_hash": e.ClaimHash,
		"status": e.Status, "entry_hash": e.EntryHash, "signature": e.Signature, "created_at": e.CreatedAt}
	var c spec.Claim
	if json.Unmarshal(e.Claim, &c) == nil {
		m["type"], m["title"] = c.Type, c.Title
	}
	if e.HumanDecision != nil {
		m["decision"], m["by"] = e.HumanDecision.Decision, e.HumanDecision.By
	}
	if e.Survival != nil {
		m["survival"] = e.Survival.Result
	}
	if e.Attribution != nil {
		m["attribution"] = e.Attribution.Shares
	}
	return m
}

// Problem is one chain verification failure.
type Problem struct {
	Seq    int64  `json:"seq"`
	Reason string `json:"reason"`
}

// Report is the result of verifying the ledger.
type Report struct {
	Entries  int       `json:"entries"`
	Head     string    `json:"head"`
	KeyID    string    `json:"key_id"`
	OK       bool      `json:"ok"`
	Problems []Problem `json:"problems"`
}

// VerifyChain checks hashes, links and signatures of every entry.
func VerifyChain(entries []spec.LedgerEntry, pub ed25519.PublicKey) Report {
	r := Report{Entries: len(entries), OK: true, KeyID: keys.KeyID(pub), Problems: []Problem{}}
	prev := ""
	for i := range entries {
		e := entries[i]
		bad := func(why string) {
			r.OK = false
			r.Problems = append(r.Problems, Problem{Seq: e.Seq, Reason: why})
		}
		if e.PrevEntryHash != prev {
			bad("prev_entry_hash does not match the previous entry")
		}
		if h := e.ComputeHash(); h != e.EntryHash {
			bad("entry_hash does not match its content")
		}
		if !strings.HasPrefix(e.Signature, "ed25519:"+keys.KeyID(pub)+":") {
			bad("signed by a different key")
		} else if !keys.Verify(pub, e.EntryHash, e.Signature) {
			bad("signature does not verify")
		}
		var c spec.Claim
		if json.Unmarshal(e.Claim, &c) == nil && c.ContentHash() != e.ClaimHash {
			bad("claim snapshot does not match claim_hash")
		}
		prev = e.EntryHash
		r.Head = e.EntryHash
	}
	return r
}

// Export is the JSON export envelope.
type Export struct {
	Spec      string             `json:"spec"`
	Exported  time.Time          `json:"exported_at"`
	PublicKey string             `json:"public_key"`
	KeyID     string             `json:"key_id"`
	Head      string             `json:"head"`
	Entries   []spec.LedgerEntry `json:"entries"`
}

// NewExport builds the export envelope for entries signed by pub.
func NewExport(entries []spec.LedgerEntry, pub ed25519.PublicKey) Export {
	ex := Export{Spec: spec.LedgerSpec, Exported: time.Now().UTC(), PublicKey: keys.PublicKeyString(pub), KeyID: keys.KeyID(pub), Entries: entries}
	if ex.Entries == nil {
		ex.Entries = []spec.LedgerEntry{}
	}
	if n := len(entries); n > 0 {
		ex.Head = entries[n-1].EntryHash
	}
	return ex
}

// WriteJSON writes the ledger with its public key so others can verify it.
func WriteJSON(w io.Writer, entries []spec.LedgerEntry, pub ed25519.PublicKey) error {
	return Encode(w, NewExport(entries, pub))
}

// Encode writes v as indented JSON without HTML escaping.
func Encode(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// WriteCSV writes one row per entry.
func WriteCSV(w io.Writer, entries []spec.LedgerEntry, value func(claimID string) float64) error {
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"seq", "created_at", "kind", "claim_id", "type", "title", "status", "decision", "by", "reason", "survival", "value", "contributors", "repo", "commits", "agent_config", "entry_hash"})
	for _, e := range entries {
		var c spec.Claim
		_ = json.Unmarshal(e.Claim, &c)
		dec, by, reason, surv := "", "", "", ""
		title := c.Title
		if e.HumanDecision != nil {
			dec, by, reason = e.HumanDecision.Decision, e.HumanDecision.By, e.HumanDecision.Reason
			if e.HumanDecision.EditedTitle != "" {
				title = e.HumanDecision.EditedTitle
			}
		}
		if e.Survival != nil {
			surv = e.Survival.Result
		}
		var contrib []string
		if e.Attribution != nil {
			for _, s := range e.Attribution.Shares {
				contrib = append(contrib, fmt.Sprintf("%s=%.2f", s.Contributor, s.Share))
			}
		}
		v := ""
		if value != nil {
			v = fmt.Sprintf("%.2f", value(e.ClaimID))
		}
		_ = cw.Write([]string{fmt.Sprint(e.Seq), e.CreatedAt.Format(time.RFC3339), e.Kind, e.ClaimID, c.Type, title, e.Status, dec, by, reason, surv, v,
			strings.Join(contrib, ";"), e.Repo, strings.Join(e.Commits, ";"), e.AgentConfig, e.EntryHash})
	}
	cw.Flush()
	return cw.Error()
}

// ReadExport parses a JSON export.
func ReadExport(r io.Reader) (*Export, error) {
	var ex Export
	dec := json.NewDecoder(r)
	if err := dec.Decode(&ex); err != nil {
		return nil, err
	}
	return &ex, nil
}

func short(id string) string {
	if len(id) > 14 {
		return id[:14]
	}
	return id
}
