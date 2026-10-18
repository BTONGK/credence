package verifiers

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BTONGK/credence/daemon/claims"
	"github.com/BTONGK/credence/daemon/store"
	"github.com/BTONGK/credence/internal/gitx"
	"github.com/BTONGK/credence/policy"
	"github.com/BTONGK/credence/spec"
)

const calc = `def is_adult(age):
    return age >= 18


def discount(total, member):
    # members save 10 when total > 100
    if member and total > 100:
        return total - 10
    label = "a >= b"
    return total
`

const strongTest = `import unittest
from calc import is_adult, discount


class T(unittest.TestCase):
    def test_all(self):
        self.assertTrue(is_adult(18))
        self.assertFalse(is_adult(17))
        self.assertEqual(discount(150, True), 140)
        self.assertEqual(discount(150, False), 150)
        self.assertEqual(discount(100, True), 100)
`

const weakTest = `import unittest
from calc import is_adult, discount


class T(unittest.TestCase):
    def test_happy(self):
        self.assertTrue(is_adult(30))
        self.assertEqual(discount(150, True), 140)
`

func gitRepo(t *testing.T, files map[string]string) (repo, base, head string) {
	t.Helper()
	for k, v := range map[string]string{"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.com"} {
		t.Setenv(k, v)
	}
	repo, _ = filepath.EvalSymlinks(t.TempDir())
	git := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("commit", "-q", "--allow-empty", "-m", "base")
	base = git("rev-parse", "HEAD")
	for p, c := range files {
		if err := os.WriteFile(filepath.Join(repo, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("add", ".")
	git("commit", "-q", "-m", "add calc")
	return repo, base, git("rev-parse", "HEAD")
}

func TestMutants(t *testing.T) {
	repo, _, head := gitRepo(t, map[string]string{"calc.py": calc, "test_calc.py": strongTest})
	ms, err := Mutants(repo, head, nil, 20)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range ms {
		got = append(got, m.String())
	}
	want := []string{
		"calc.py:2 `>=` → `>`",
		"calc.py:7 `and` → `or`",
		"calc.py:7 `>` → `>=`",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("mutants:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if ms, _ := Mutants(repo, head, nil, 1); len(ms) != 1 {
		t.Errorf("limit not applied: %d", len(ms))
	}

	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "calc.py"), []byte(calc), 0o644)
	if err := ms[0].Apply(dir); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "calc.py"))
	if !strings.Contains(string(b), "return age > 18\n") || strings.Count(string(b), "\n") != strings.Count(calc, "\n") {
		t.Errorf("applied:\n%s", b)
	}
}

func TestCodeMask(t *testing.T) {
	for _, tc := range []struct {
		line, op string
		code     []bool // per occurrence of op
	}{
		{`x = a == b  # a == b`, "==", []bool{true, false}},
		{`s := "a && b" && ok`, "&&", []bool{false, true}},
		{`// if a < b`, "<", []bool{false}},
		{`if (a || 'b||c') { }`, "||", []bool{true, false}},
		{`msg = "say \"a<b\"" < 3`, "<", []bool{false, true}},
	} {
		m := codeMask(tc.line)
		var got []bool
		for i := 0; ; {
			j := strings.Index(tc.line[i:], tc.op)
			if j < 0 {
				break
			}
			got = append(got, m[i+j])
			i += j + len(tc.op)
		}
		if fmt.Sprint(got) != fmt.Sprint(tc.code) {
			t.Errorf("%q %q: code %v, want %v", tc.line, tc.op, got, tc.code)
		}
	}
}

func TestMutationScore(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	t.Setenv("CREDENCE_HOME", t.TempDir())
	for _, tc := range []struct {
		name, test, result, outcome string
		explicit                    bool
	}{
		{"strong", strongTest, spec.ResultPass, "mutation_strong", true}, // with min 1.0
		{"weak explicit", weakTest, spec.ResultFail, "mutation_weak", true},
		{"weak implicit", weakTest, spec.ResultInconclusive, "mutation_weak", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, _, head := gitRepo(t, map[string]string{"calc.py": calc, "test_calc.py": tc.test})
			cmd := "python3 -m unittest -q test_calc"
			pol := policy.Default()
			pol.Tests.AllowCommands = []string{cmd}
			cm, err := gitx.ShowCommit(repo, head)
			if err != nil {
				t.Fatal(err)
			}
			d := &claims.Data{Session: &store.SessionRow{Session: spec.Session{ID: "ses_0000000001", Repo: repo, Cwd: repo}},
				Commits: []*gitx.Commit{cm}, Policy: pol}
			c := &spec.Claim{ID: "clm_1", Type: spec.TypeFix}
			vc := &Context{Data: d}
			a := spec.Assertion{"check": "mutation_score", "command": cmd, "commit": head}
			var vs []spec.Verdict
			if tc.explicit {
				if tc.name == "strong" {
					a["min"] = 1.0
				}
				c.Assertions = []spec.Assertion{a}
				vs = (&Mutation{}).Verify(context.Background(), vc, c)
			} else {
				// After a passing test the session itself wrote.
				vc.prior = []spec.Verdict{{Verifier: "tests", Tier: 1, Result: spec.ResultPass, Outcome: "passes_born_in_session",
					Assertion: spec.Assertion{"check": "test_passes", "command": cmd, "commit": head}}}
				vs = (&Mutation{}).Verify(context.Background(), vc, c)
			}
			if len(vs) != 1 {
				t.Fatalf("verdicts: %+v", vs)
			}
			v := vs[0]
			if v.Result != tc.result || v.Outcome != tc.outcome {
				t.Fatalf("%s/%s: %s", v.Result, v.Outcome, v.Details)
			}
			t.Log(v.Details)
			if tc.name == "strong" && !strings.Contains(v.Details, "caught 3/3 mutants of the lines "+head[:7]+" added (score 1.00, needs 1.00)") {
				t.Errorf("details: %s", v.Details)
			}
			if tc.name != "strong" && !strings.Contains(v.Details, "survived: calc.py:2 `>=` → `>`; calc.py:7 `and` → `or`") {
				t.Errorf("details: %s", v.Details)
			}
		})
	}
}

// Through the real pipeline order: tests pass with a test the session wrote,
// then the mutation verifier decides whether the fix is verified.
func TestPipelineLiftsCapWithMutation(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	t.Setenv("CREDENCE_HOME", t.TempDir())
	for _, tc := range []struct {
		test, status, flag string
	}{
		{strongTest, spec.StatusVerified, ""},
		{weakTest, spec.StatusCorroborated, "weak_test"},
	} {
		repo, _, head := gitRepo(t, map[string]string{"calc.py": calc, "test_calc.py": tc.test})
		cmd := "python3 -m unittest -q test_calc"
		pol := policy.Default()
		pol.Tests.AllowCommands = []string{cmd}
		cm, _ := gitx.ShowCommit(repo, head)
		d := &claims.Data{Session: &store.SessionRow{Session: spec.Session{ID: "ses_0000000001", Repo: repo, Cwd: repo}},
			Commits: []*gitx.Commit{cm}, Policy: pol}
		c := &spec.Claim{ID: "clm_1", Type: spec.TypeFix, Title: "Fixed discounts",
			Assertions: []spec.Assertion{{"check": "test_passes", "command": cmd, "commit": head, "runs": 1}},
			Evidence:   []spec.Evidence{{Target: "git:commit/" + head, Relation: spec.RelProduced}}}
		vs := VerifyClaim(context.Background(), &Context{Data: d}, c, "", 1)
		dec := Decide(c, vs, pol, spec.OriginSelf, false)
		var got []string
		for _, v := range vs {
			got = append(got, v.Verifier+"/"+v.Result+"/"+v.Outcome)
		}
		t.Logf("%s %v: %v", dec.Status, dec.Flags, got)
		if dec.Status != tc.status || (tc.flag != "" && !contains(dec.Flags, tc.flag)) {
			t.Errorf("status %s flags %v, want %s %q; verdicts %v", dec.Status, dec.Flags, tc.status, tc.flag, got)
		}
	}
}
