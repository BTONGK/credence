// Package conformance checks the reference implementation against the
// fixtures in this directory. Other implementations can use the same
// fixtures. Regenerate them with CREDENCE_UPDATE_FIXTURES=1 go test.
package conformance

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/BTONGK/credence/daemon/ledger"
	"github.com/BTONGK/credence/internal/keys"
	"github.com/BTONGK/credence/spec"
)

var update = os.Getenv("CREDENCE_UPDATE_FIXTURES") == "1"

func load(t *testing.T, name string, v any) {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

func save(t *testing.T, name string, v any) {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

type canonCase struct {
	Name      string `json:"name"`
	Value     any    `json:"value"`
	Canonical string `json:"canonical"`
	Hash      string `json:"hash"`
}

func TestCanonical(t *testing.T) {
	if update {
		var cases []canonCase
		for _, c := range []struct {
			name, value string
		}{
			{"keys are sorted by byte value", `{"b":1,"a":2,"B":3,"aa":4}`},
			{"nested objects and arrays keep array order", `{"z":[3,1,2],"y":{"d":true,"c":null}}`},
			{"no HTML escaping", `{"cmd":"a < b && c > d"}`},
			{"unicode is written as UTF-8", `{"name":"Zoë","emoji":"✓"}`},
			{"numbers in shortest form", `{"p":0.85,"n":50,"small":1e-7,"neg":-2.5}`},
			{"empty containers", `{"a":[],"o":{}}`},
		} {
			var v any
			dec := json.NewDecoder(bytes.NewReader([]byte(c.value)))
			dec.UseNumber()
			if err := dec.Decode(&v); err != nil {
				t.Fatal(err)
			}
			cb, _ := spec.Canonical(v)
			cases = append(cases, canonCase{Name: c.name, Value: v, Canonical: string(cb), Hash: spec.HashBytes(cb)})
		}
		save(t, "canonical.json", cases)
	}
	var cases []canonCase
	load(t, "canonical.json", &cases)
	for _, c := range cases {
		cb, err := spec.Canonical(c.Value)
		if err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		if string(cb) != c.Canonical {
			t.Errorf("%s: canonical\n got %s\nwant %s", c.Name, cb, c.Canonical)
		}
		if h := spec.HashBytes(cb); h != c.Hash {
			t.Errorf("%s: hash %s, want %s", c.Name, h, c.Hash)
		}
	}
}

type claimCase struct {
	Name        string          `json:"name"`
	Claim       json.RawMessage `json:"claim"`
	ContentHash string          `json:"content_hash"`
	ID          string          `json:"id"`
}

func fixtureClaims() []spec.Claim {
	conf := 0.85
	at := time.Date(2026, 10, 18, 5, 37, 51, 15449000, time.UTC)
	return []spec.Claim{
		{
			Spec: spec.ClaimSpec, SessionID: "ses_3b2028956d", Claimant: "agent:claude-code/claude-opus-5-5@cfg_82c2",
			Origin: spec.OriginSelf, Type: spec.TypeFix, Title: "Fixed race condition in job scheduler",
			Statement: "test_concurrency fails at the parent of f7539d5 and passes at f7539d5 in 50 of 50 runs",
			Assertions: []spec.Assertion{
				{"check": "test_fails_before_passes_after", "test": "tests/test_scheduler.py::SchedulerTest::test_concurrency", "commit": "f7539d5", "runs": 50},
				{"check": "commit_touches", "commit": "f7539d5", "paths": []any{"scheduler/queue.py"}},
			},
			Evidence: []spec.Evidence{
				{Target: "git:commit/f7539d5edba1d12677c72372c3d7388d15ca2cfe", Relation: spec.RelProduced},
				{Target: "event:ses_3b2028956d#36-38", Relation: spec.RelProduced, Note: "scheduler/queue.py"},
			},
			CoContributors: []spec.CoContributor{{ID: "human:maya", Basis: "identified the failing test in the first prompt"}},
			SelfConfidence: &conf, SourceEvent: 57, CreatedAt: at,
		},
		{
			Spec: spec.ClaimSpec, SessionID: "ses_3b2028956d", Claimant: "auditor:rules@0.9", Origin: spec.OriginAuditor,
			Type: spec.TypeOps, Title: "CI: stress the concurrency test 20 times per run",
			Statement:  "Commit 7ba509d changes .github/workflows/ci.yml (+2 −0)",
			Assertions: []spec.Assertion{{"check": "commit_touches", "commit": "7ba509d", "paths": []any{".github/workflows/ci.yml"}}},
			Evidence:   []spec.Evidence{{Target: "git:commit/7ba509d", Relation: spec.RelProduced}, {Target: "event:ses_3b2028956d#51-55", Relation: spec.RelProduced}},
			CreatedAt:  at,
		},
		{
			Spec: spec.ClaimSpec, SessionID: "ses_3b2028956d", Claimant: "human:maya", Origin: spec.OriginHuman,
			Type: spec.TypeNegativeResult, Title: "Retries in _take hide the race", Statement: "Retrying popleft does not fix lost completions <&>",
			Assertions: []spec.Assertion{}, Evidence: []spec.Evidence{}, CreatedAt: at,
		},
	}
}

func TestClaims(t *testing.T) {
	if update {
		var cases []claimCase
		for i, c := range fixtureClaims() {
			c.AssignID()
			body := c
			body.ID = ""
			raw, _ := json.Marshal(body)
			cases = append(cases, claimCase{Name: []string{"self-claim with assertions", "auditor claim", "human negative result with HTML characters"}[i],
				Claim: raw, ContentHash: c.ContentHash(), ID: c.ID})
		}
		save(t, "claims.json", cases)
	}
	var cases []claimCase
	load(t, "claims.json", &cases)
	for _, cc := range cases {
		var c spec.Claim
		if err := json.Unmarshal(cc.Claim, &c); err != nil {
			t.Fatalf("%s: %v", cc.Name, err)
		}
		if h := c.ContentHash(); h != cc.ContentHash {
			t.Errorf("%s: content hash %s, want %s", cc.Name, h, cc.ContentHash)
		}
		c.AssignID()
		if c.ID != cc.ID {
			t.Errorf("%s: id %s, want %s", cc.Name, c.ID, cc.ID)
		}
		// The id must not depend on an id or signature already present.
		c.Signature = "ed25519:000000000000:AAAA"
		if c.ContentHash() != cc.ContentHash {
			t.Errorf("%s: signature changed the content hash", cc.Name)
		}
	}
}

type targetCase struct {
	Target  string `json:"target"`
	Scheme  string `json:"scheme"`
	Session string `json:"session,omitempty"`
	From    int    `json:"from,omitempty"`
	To      int    `json:"to,omitempty"`
	Commit  string `json:"commit,omitempty"`
	Path    string `json:"path,omitempty"`
	ID      string `json:"id,omitempty"`
}

func TestTargets(t *testing.T) {
	if update {
		var cases []targetCase
		for _, tg := range []string{
			"event:ses_3b2028956d#36-38", "event:ses_3b2028956d#31", "git:commit/f7539d5", "file:scheduler/queue.py@f7539d5#L16-L30",
			"file:README.md", "test_run:run_8e4fd01e23", "ci:github/acme/scheduler/f7539d5", "pr:github/acme/scheduler#12",
			"issue:github/acme/scheduler#7", "unresolved:the flaky test", "no scheme",
		} {
			p := spec.ParseTarget(tg)
			cases = append(cases, targetCase{Target: tg, Scheme: p.Scheme, Session: p.Session, From: p.From, To: p.To, Commit: p.Commit, Path: p.Path, ID: p.ID})
		}
		save(t, "targets.json", cases)
	}
	var cases []targetCase
	load(t, "targets.json", &cases)
	for _, c := range cases {
		p := spec.ParseTarget(c.Target)
		got := targetCase{Target: c.Target, Scheme: p.Scheme, Session: p.Session, From: p.From, To: p.To, Commit: p.Commit, Path: p.Path, ID: p.ID}
		if got != c {
			t.Errorf("%s: parsed %+v, want %+v", c.Target, got, c)
		}
	}
}

type ledgerFixture struct {
	Description string           `json:"description"`
	PublicKey   string           `json:"public_key"`
	Valid       *ledger.Export   `json:"valid"`
	Tampered    []tamperedLedger `json:"tampered"`
}

type tamperedLedger struct {
	Name    string         `json:"name"`
	Export  *ledger.Export `json:"export"`
	Problem string         `json:"problem"`
}

func buildLedger(t *testing.T) ledgerFixture {
	seed := bytes.Repeat([]byte{7}, ed25519.SeedSize)
	signer := keys.NewSigner(ed25519.NewKeyFromSeed(seed))
	at := time.Date(2026, 10, 18, 6, 0, 0, 0, time.UTC)
	var entries []spec.LedgerEntry
	prev := ""
	for i, c := range fixtureClaims()[:2] {
		c.AssignID()
		raw, _ := json.Marshal(c)
		decision := []string{spec.ReviewAccepted, spec.ReviewDisputed}[i]
		hd := &spec.HumanDecision{Decision: decision, By: "human:maya", At: at.Add(time.Duration(i) * time.Minute)}
		if decision == spec.ReviewDisputed {
			hd.Reason = "not_valuable"
		}
		e := spec.LedgerEntry{
			Spec: spec.LedgerSpec, Seq: int64(i + 1), Kind: "decision", ClaimID: c.ID, ClaimHash: c.ContentHash(), Claim: raw,
			Status: spec.StatusVerified, VerdictHashes: []string{}, HumanDecision: hd,
			Attribution: &spec.Attribution{ClaimID: c.ID, Method: "line_provenance", Shares: []spec.Share{{Contributor: "agent:claude-code/claude-opus-5-5@cfg_82c2", Share: 1, Basis: "wrote lines"}}},
			Commits:     []string{"f7539d5edba1d12677c72372c3d7388d15ca2cfe"}, AgentConfig: "agent:claude-code/claude-opus-5-5@cfg_82c2",
			PrevEntryHash: prev, CreatedAt: hd.At,
		}
		e.EntryHash = e.ComputeHash()
		e.Signature, _ = signer.Sign(e.EntryHash)
		prev = e.EntryHash
		entries = append(entries, e)
	}
	export := func(es []spec.LedgerEntry) *ledger.Export {
		var buf bytes.Buffer
		if err := ledger.WriteJSON(&buf, es, signer.Pub); err != nil {
			t.Fatal(err)
		}
		ex, err := ledger.ReadExport(&buf)
		if err != nil {
			t.Fatal(err)
		}
		ex.Exported = at
		return ex
	}
	clone := func() []spec.LedgerEntry {
		b, _ := json.Marshal(entries)
		var out []spec.LedgerEntry
		_ = json.Unmarshal(b, &out)
		return out
	}
	f := ledgerFixture{
		Description: "A two-entry ledger signed with the Ed25519 key derived from a seed of 32 bytes of 0x07. The valid export MUST verify; each tampered export MUST fail with the named problem.",
		Valid:       export(entries),
		PublicKey:   keys.PublicKeyString(signer.Pub),
	}
	es := clone()
	es[0].HumanDecision.Decision = spec.ReviewRejected
	f.Tampered = append(f.Tampered, tamperedLedger{Name: "decision changed after signing", Export: export(es), Problem: "entry_hash does not match its content"})
	es = clone()
	es = es[1:]
	f.Tampered = append(f.Tampered, tamperedLedger{Name: "first entry removed", Export: export(es), Problem: "prev_entry_hash does not match the previous entry"})
	es = clone()
	var c spec.Claim
	_ = json.Unmarshal(es[1].Claim, &c)
	c.Title = "Something grander"
	es[1].Claim, _ = json.Marshal(c)
	es[1].EntryHash = es[1].ComputeHash()
	es[1].Signature, _ = signer.Sign(es[1].EntryHash)
	f.Tampered = append(f.Tampered, tamperedLedger{Name: "claim snapshot edited and re-signed", Export: export(es), Problem: "claim snapshot does not match claim_hash"})
	es = clone()
	other := keys.NewSigner(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{9}, ed25519.SeedSize)))
	es[1].Signature, _ = other.Sign(es[1].EntryHash)
	f.Tampered = append(f.Tampered, tamperedLedger{Name: "entry signed by another key", Export: export(es), Problem: "signed by a different key"})
	return f
}

func TestLedger(t *testing.T) {
	if update {
		save(t, "ledger.json", buildLedger(t))
	}
	var f ledgerFixture
	b, err := os.ReadFile("ledger.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	pub, err := keys.ParsePublicKey(f.Valid.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if rep := ledger.VerifyChain(f.Valid.Entries, pub); !rep.OK || rep.Head != f.Valid.Head {
		t.Fatalf("valid ledger does not verify: %+v", rep)
	}
	for _, tc := range f.Tampered {
		rep := ledger.VerifyChain(tc.Export.Entries, pub)
		if rep.OK {
			t.Errorf("%s: tampered ledger verified", tc.Name)
			continue
		}
		found := false
		for _, p := range rep.Problems {
			if p.Reason == tc.Problem {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: problems %+v, want %q", tc.Name, rep.Problems, tc.Problem)
		}
	}
}
