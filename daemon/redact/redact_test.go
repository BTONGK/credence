package redact

import (
	"strings"
	"testing"
)

func TestRedactsSecrets(t *testing.T) {
	r := New([]byte("k"), []string{`ACME_[A-Z0-9]{32}`})
	cases := map[string]string{
		"export ANTHROPIC_API_KEY=sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789": "anthropic_key",
		"token ghp_abcdefghijklmnopqrstuvwxyz0123456789AB":                           "github_token",
		"AKIAIOSFODNN7EXAMPLE is the id":                                             "aws_access_key",
		"postgres://admin:hunter2secret@db.internal:5432/app":                        "url_credentials",
		`password = "Sup3rS3cretValue!"`:                                             "assigned_secret",
		"mail maya.lee@acme.io now":                                                  "email",
		"card 4111 1111 1111 1111 ok":                                                "credit_card",
		"ACME_ABCDEFGHIJKLMNOPQRSTUVWXYZ012345":                                      "custom",
		"Authorization: Bearer abcdefghijklmnopqrstuvwxyz.0123456789":                "bearer",
	}
	for in, kind := range cases {
		out := r.String(in)
		if !strings.Contains(out, "[REDACTED:"+kind+":") {
			t.Errorf("%q -> %q, want %s redaction", in, out, kind)
		}
	}
}

func TestLeavesOrdinaryTextAlone(t *testing.T) {
	r := New([]byte("k"), nil)
	for _, in := range []string{
		"commit 3f2e9c1a7b8d9e0f1a2b3c4d5e6f7a8b9c0d1e2f",
		"tests/test_scheduler.py::test_concurrency passed in 0.12s",
		`password = os.environ["DB_PASSWORD"]`,
		"contact noreply@example.com",
		"timestamp 1760745124000",
	} {
		if out := r.String(in); out != in {
			t.Errorf("%q was changed to %q", in, out)
		}
	}
}

func TestStablePlaceholders(t *testing.T) {
	r := New([]byte("k"), nil)
	a := r.String("sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789")
	b := r.String("again: sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789")
	if !strings.HasSuffix(b, a) {
		t.Fatalf("placeholders differ: %q vs %q", a, b)
	}
}

func TestJSON(t *testing.T) {
	r := New([]byte("k"), nil)
	out := string(r.JSON([]byte(`{"cmd":"curl -H 'Authorization: Bearer abcdefghijklmnopqrstuvwxyz0123'","n":3}`)))
	if strings.Contains(out, "abcdefghijklmnopqrstuvwxyz0123") || !strings.Contains(out, `"n":3`) {
		t.Fatalf("bad json redaction: %s", out)
	}
}
