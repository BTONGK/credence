package rollup

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BTONGK/credence/daemon/ledger"
	"github.com/BTONGK/credence/internal/keys"
	"github.com/BTONGK/credence/spec"
)

type dec struct {
	title, typ, claimant, decision string
	conf                           float64
	survival                       string // 30d result, optional
}

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

// chain builds a signed, hash-linked ledger like the daemon writes.
func chain(t *testing.T, s *keys.Signer, by string, ds []dec) []spec.LedgerEntry {
	t.Helper()
	var es []spec.LedgerEntry
	prev := ""
	add := func(e spec.LedgerEntry) {
		e.Spec, e.Seq, e.PrevEntryHash, e.VerdictHashes = spec.LedgerSpec, int64(len(es)+1), prev, []string{}
		e.CreatedAt = t0.Add(time.Duration(len(es)) * time.Minute)
		e.EntryHash = e.ComputeHash()
		e.Signature, _ = s.Sign(e.EntryHash)
		prev = e.EntryHash
		es = append(es, e)
	}
	for _, d := range ds {
		conf := d.conf
		c := &spec.Claim{SessionID: "ses_0000000001", Claimant: d.claimant, Origin: spec.OriginSelf, Type: d.typ, Title: d.title,
			Statement: "s", SelfConfidence: &conf, CreatedAt: t0}
		c.AssignID()
		raw, _ := json.Marshal(c)
		base := spec.LedgerEntry{ClaimID: c.ID, ClaimHash: c.ContentHash(), Claim: raw, Status: spec.StatusVerified, Repo: "scheduler"}
		e := base
		e.Kind = "decision"
		e.HumanDecision = &spec.HumanDecision{Decision: d.decision, By: by}
		add(e)
		if d.survival != "" {
			sv := base
			sv.Kind = "survival"
			sv.Survival = &spec.SurvivalCheck{Window: "30d", Result: d.survival}
			add(sv)
		}
	}
	return es
}

func signer(b byte) *keys.Signer {
	return keys.NewSigner(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{b}, ed25519.SeedSize)))
}

func write(t *testing.T, dir, name string, ex TeamExport) {
	t.Helper()
	var buf bytes.Buffer
	if err := ledger.Encode(&buf, ex); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

const (
	opus  = "agent:claude-code/claude-opus-5-5@cfg_aaaa"
	codex = "agent:codex/gpt-5.5@cfg_bbbb"
)

func TestRollup(t *testing.T) {
	dir := t.TempDir()
	maya, sam := signer(1), signer(2)
	mayaEntries := chain(t, maya, "human:maya", []dec{
		{"Fixed the race", spec.TypeFix, opus, spec.ReviewAccepted, 0.9, spec.SurvivalSurviving},
		{"Added docs", spec.TypeDocs, opus, spec.ReviewEdited, 0.8, ""},
		{"Rewrote queue", spec.TypeRefactor, codex, spec.ReviewAccepted, 0.7, spec.SurvivalReverted},
	})
	mex := TeamExport{Export: ledger.NewExport(mayaEntries, maya.Pub)}
	mex.OptIn, _ = SignOptIn(maya, "human:maya", mex.Head)
	write(t, dir, "maya.json", mex)
	samEntries := chain(t, sam, "human:sam", []dec{
		{"All tests pass", spec.TypeTest, codex, spec.ReviewDisputed, 0.95, ""},
	})
	write(t, dir, "sam.json", TeamExport{Export: ledger.NewExport(samEntries, sam.Pub)})
	// The same export twice must not double count.
	write(t, dir, "sam-copy.json", TeamExport{Export: ledger.NewExport(samEntries, sam.Pub)})

	srcs, err := ReadPaths([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	r := Compute(srcs, Options{By: "agent"})
	if r.Claims != 4 || len(r.Rows) != 2 {
		t.Fatalf("claims %d rows %+v", r.Claims, r.Rows)
	}
	top := r.Rows[0] // opus: fix 3×1 + docs 1×0.5
	if top.Key != "cfg_aaaa" || top.Label != "claude-code/claude-opus-5-5" || top.Value != 3.5 || top.Survived30 != 1 || top.Checked30 != 1 {
		t.Errorf("opus row: %+v", top)
	}
	cx := r.Rows[1] // codex: refactor reverted (0) + disputed test (0)
	if cx.Key != "cfg_bbbb" || cx.Value != 0 || cx.Reverted != 1 || cx.Disputed != 1 || cx.Checked30 != 1 || cx.Survived30 != 0 {
		t.Errorf("codex row: %+v", cx)
	}
	if want := (0.3*0.3 + 0.95*0.95) / 2; cx.BrierN != 2 || abs(cx.Brier-want) > 1e-9 {
		t.Errorf("codex brier %v (%d), want %v", cx.Brier, cx.BrierN, want)
	}

	// Only Maya opted in; Sam is counted but not named.
	p := Compute(srcs, Options{By: "person"})
	if len(p.Rows) != 2 || p.Rows[0].Label != "maya" || p.Rows[0].Claims != 3 || p.Rows[1].Key != notShared || p.Rows[1].Claims != 1 {
		t.Errorf("person rows: %+v %+v", p.Rows[0], p.Rows[1])
	}
	var buf bytes.Buffer
	WriteText(&buf, p, true)
	if strings.Contains(buf.String(), "sam") {
		t.Errorf("a person who did not opt in is named:\n%s", buf.String())
	}

	// Pinning keys excludes everyone else.
	if r := Compute(srcs, Options{Trusted: map[string]bool{maya.KeyID: true}}); r.Claims != 3 || r.Dropped != 2 {
		t.Errorf("trusted: claims %d dropped %d", r.Claims, r.Dropped)
	}
}

func TestRollupRejectsTampering(t *testing.T) {
	dir := t.TempDir()
	maya, eve := signer(1), signer(9)
	es := chain(t, maya, "human:maya", []dec{{"Fixed the race", spec.TypeFix, opus, spec.ReviewDisputed, 0.9, ""}})

	tampered := append([]spec.LedgerEntry(nil), es...)
	hd := *tampered[0].HumanDecision
	hd.Decision = spec.ReviewAccepted
	tampered[0].HumanDecision = &hd
	write(t, dir, "tampered.json", TeamExport{Export: ledger.NewExport(tampered, maya.Pub)})

	// An opt-in signed by someone else's key does not name anyone.
	forged := TeamExport{Export: ledger.NewExport(es, maya.Pub)}
	forged.OptIn, _ = SignOptIn(eve, "human:maya", forged.Head)
	write(t, dir, "forged.json", forged)

	srcs, err := ReadPaths([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*Source{}
	for _, s := range srcs {
		byName[s.Name] = s
	}
	if s := byName["tampered.json"]; s.OK || len(s.Problems) == 0 {
		t.Errorf("tampered export verified: %+v", s)
	}
	if s := byName["forged.json"]; !s.OK || s.Person != "" || !strings.Contains(strings.Join(s.Problems, ";"), "opt-in does not verify") {
		t.Errorf("forged opt-in accepted: %+v", s)
	}
	r := Compute(srcs, Options{By: "person"})
	if r.Claims != 1 || r.Dropped != 1 || r.Rows[0].Key != notShared || r.Rows[0].Disputed != 1 {
		t.Errorf("rollup: claims %d dropped %d rows %+v", r.Claims, r.Dropped, r.Rows)
	}
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
