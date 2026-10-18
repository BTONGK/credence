package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/BTONGK/credence/daemon/ledger"
	"github.com/BTONGK/credence/internal/gitx"
	"github.com/BTONGK/credence/internal/keys"
	"github.com/BTONGK/credence/spec"
)

func TestPRCommentFromNotes(t *testing.T) {
	for k, v := range map[string]string{"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.com"} {
		t.Setenv(k, v)
	}
	repo := t.TempDir()
	git := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("commit", "-q", "--allow-empty", "-m", "base")
	base := git("rev-parse", "HEAD")
	git("commit", "-q", "--allow-empty", "-m", "fix")
	head := git("rev-parse", "HEAD")

	signer := keys.NewSigner(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{3}, ed25519.SeedSize)))
	note := func(title string, tamper bool) {
		c := &spec.Claim{SessionID: "ses_0000000001", Claimant: "agent:claude-code/claude-opus-5-5", Origin: spec.OriginSelf, Type: spec.TypeFix,
			Title: title, Statement: "s", Evidence: []spec.Evidence{{Target: "git:commit/" + head[:7], Relation: spec.RelProduced}}, CreatedAt: time.Unix(0, 0).UTC()}
		c.AssignID()
		raw, _ := json.Marshal(c)
		e := &spec.LedgerEntry{Spec: spec.LedgerSpec, Seq: 1, Kind: "decision", ClaimID: c.ID, ClaimHash: c.ContentHash(), Claim: raw,
			Status: spec.StatusVerified, VerdictHashes: []string{}, HumanDecision: &spec.HumanDecision{Decision: spec.ReviewAccepted, By: "human:maya"},
			Commits: []string{head[:7]}, CreatedAt: time.Unix(1, 0).UTC()}
		e.EntryHash = e.ComputeHash()
		e.Signature, _ = signer.Sign(e.EntryHash)
		if tamper {
			e.Status = spec.StatusVerified + " (edited)"
		}
		line, _ := json.Marshal(ledger.Note{Entry: e, PublicKey: keys.PublicKeyString(signer.Pub)})
		if err := gitx.AppendNote(repo, head, string(line)); err != nil {
			t.Fatal(err)
		}
	}
	note("Fixed the scheduler race", false)

	shas, err := gitx.CommitsInRange(repo, base, head)
	if err != nil || len(shas) != 1 {
		t.Fatalf("range: %v %v", shas, err)
	}
	render := func(trusted string) string {
		return renderPRComment(notePRClaims(repo, shas, trustedKeys(trusted)), len(shas), trusted != "")
	}
	if out := render(""); !strings.Contains(out, "✅ | Fixed the scheduler race") || !strings.Contains(out, "not who signed it") {
		t.Errorf("unpinned:\n%s", out)
	}
	if out := render(keys.PublicKeyString(signer.Pub)); !strings.Contains(out, "signed by a trusted key (`"+signer.KeyID+"`)") {
		t.Errorf("pinned by public key:\n%s", out)
	}
	if out := render("0123456789ab, " + "ffffffffffff"); !strings.Contains(out, "untrusted key `"+signer.KeyID+"`") {
		t.Errorf("untrusted:\n%s", out)
	}

	note("Fixed the scheduler race", true)
	if out := render(""); !strings.Contains(out, "failed signature or hash verification") {
		t.Errorf("tampered:\n%s", out)
	}
}
