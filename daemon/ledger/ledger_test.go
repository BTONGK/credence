package ledger

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/BTONGK/credence/daemon/store"
	"github.com/BTONGK/credence/internal/keys"
	"github.com/BTONGK/credence/spec"
)

func setup(t *testing.T) (*Ledger, []*spec.Claim) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Date(2026, 10, 17, 20, 0, 0, 0, time.UTC)
	if err := st.UpsertSession(&spec.Session{ID: "ses_0000000001", Harness: "claude-code", HarnessSessionID: "x", State: "closed",
		StartedAt: now, LastActivity: now, Models: []string{}, Human: "human:maya"}); err != nil {
		t.Fatal(err)
	}
	var cs []*spec.Claim
	for i, typ := range []string{spec.TypeFix, spec.TypeTest} {
		c := &spec.Claim{SessionID: "ses_0000000001", Claimant: "agent:claude-code/claude-opus-5-5@cfg_0001", Origin: spec.OriginSelf,
			Type: typ, Title: []string{"Fixed the race", "All tests pass"}[i], Statement: "s",
			Evidence: []spec.Evidence{{Target: "git:commit/abc1234", Relation: spec.RelProduced}}, CreatedAt: now}
		c.AssignID()
		if _, err := st.InsertClaim(c); err != nil {
			t.Fatal(err)
		}
		status := []string{spec.StatusVerified, spec.StatusContradicted}[i]
		if err := st.SetClaimState(c.ID, store.ClaimState{Status: status, Section: status, Notes: []string{"n"}}); err != nil {
			t.Fatal(err)
		}
		cs = append(cs, c)
	}
	signer := keys.NewSigner(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{1}, ed25519.SeedSize)))
	return &Ledger{Store: st, Signer: signer}, cs
}

func TestDecideAndVerify(t *testing.T) {
	l, cs := setup(t)
	ctx := context.Background()
	fix, allPass := cs[0], cs[1]

	if _, err := l.Decide(ctx, fix.ID, spec.HumanDecision{Decision: spec.ReviewAccepted, By: "human:maya"}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Decide(ctx, allPass.ID, spec.HumanDecision{Decision: spec.ReviewAccepted, By: "human:maya"}); !errors.Is(err, ErrContradicted) {
		t.Fatalf("accepting a contradicted claim: err = %v, want ErrContradicted", err)
	}
	if _, err := l.Decide(ctx, allPass.ID, spec.HumanDecision{Decision: spec.ReviewDisputed, By: "human:maya"}); err == nil {
		t.Fatal("a dispute without a reason was accepted")
	}
	if _, err := l.Decide(ctx, allPass.ID, spec.HumanDecision{Decision: spec.ReviewEdited, By: "human:maya"}); err == nil {
		t.Fatal("an edit without new text was accepted")
	}
	e, err := l.Decide(ctx, allPass.ID, spec.HumanDecision{Decision: spec.ReviewEdited, By: "human:maya", EditedTitle: "Scheduler tests pass; config test needs a local file"})
	if err != nil {
		t.Fatal(err)
	}
	if e.Seq != 2 || len(e.Commits) != 1 || e.Commits[0] != "abc1234" {
		t.Fatalf("entry: seq=%d commits=%v", e.Seq, e.Commits)
	}

	es, err := l.Store.LedgerEntries(store.LedgerFilter{})
	if err != nil {
		t.Fatal(err)
	}
	rep := VerifyChain(es, l.Signer.Pub)
	if !rep.OK || rep.Entries != 2 {
		t.Fatalf("chain does not verify: %+v", rep)
	}

	// The export round-trips and verifies with only its embedded key.
	var buf bytes.Buffer
	if err := WriteJSON(&buf, es, l.Signer.Pub); err != nil {
		t.Fatal(err)
	}
	ex, err := ReadExport(&buf)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := keys.ParsePublicKey(ex.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if rep := VerifyChain(ex.Entries, pub); !rep.OK || ex.Head != es[1].EntryHash {
		t.Fatalf("export does not verify: %+v", rep)
	}

	// Any edit is detected.
	es[0].HumanDecision.Decision = spec.ReviewRejected
	if rep := VerifyChain(es, l.Signer.Pub); rep.OK {
		t.Fatal("tampered entry verified")
	}
}

func TestNotes(t *testing.T) {
	l, cs := setup(t)
	e, err := l.Decide(context.Background(), cs[0].ID, spec.HumanDecision{Decision: spec.ReviewAccepted, By: "human:maya"})
	if err != nil {
		t.Fatal(err)
	}
	line := noteLine(t, l, e)
	got := ParseNotes("garbage\n" + line + "\n")
	if len(got) != 1 || !got[0].Valid || got[0].KeyID != l.Signer.KeyID {
		t.Fatalf("notes: %+v", got)
	}
	// A note whose entry was edited no longer verifies.
	e.Status = spec.StatusVerified + "!"
	if got := ParseNotes(noteLine(t, l, e)); len(got) != 1 || got[0].Valid {
		t.Fatalf("edited note verified: %+v", got)
	}
}

func noteLine(t *testing.T, l *Ledger, e *spec.LedgerEntry) string {
	t.Helper()
	b, err := jsonMarshal(Note{Entry: e, PublicKey: keys.PublicKeyString(l.Signer.Pub)})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }
