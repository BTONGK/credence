package verifiers

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/BTONGK/credence/daemon/claims"
	"github.com/BTONGK/credence/daemon/store"
	"github.com/BTONGK/credence/internal/gitx"
	"github.com/BTONGK/credence/policy"
	"github.com/BTONGK/credence/spec"
)

func bundledTodos(t *testing.T) []string {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	p, _ := filepath.Abs("../plugins/no_new_todos.py")
	return []string{"python3", p}
}

func TestBundledPluginConforms(t *testing.T) {
	t.Setenv("CREDENCE_HOME", t.TempDir())
	steps, p := CheckPlugin(context.Background(), bundledTodos(t))
	for _, s := range steps {
		if !s.OK || s.Warn {
			t.Errorf("%s: ok=%v warn=%v %s", s.Name, s.OK, s.Warn, s.Detail)
		}
	}
	if p == nil || p.Name() != "plugin:todos" {
		t.Fatalf("plugin: %+v", p)
	}
	bad, _ := CheckPlugin(context.Background(), []string{"sh", "-c", `read l; echo '{"jsonrpc":"2.0","id":1,"result":{"name":"Bad","tier":3,"checks":[]}}'`})
	if Conforms(bad) || len(bad) != 4 {
		t.Errorf("a bad describe conformed or stopped early: %+v", bad)
	}
}

// A policy requirement plugin:<name> is met by that plugin's verdicts.
func TestPluginRequirement(t *testing.T) {
	t.Setenv("CREDENCE_HOME", t.TempDir())
	cmd := bundledTodos(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pl, err := LoadPlugin(ctx, "todos", cmd)
	if err != nil {
		t.Fatal(err)
	}
	pol := policy.Default()
	ft := pol.ClaimTypes[spec.TypeFeature]
	ft.Require = []string{"resolve", "plugin:todos"}
	pol.ClaimTypes[spec.TypeFeature] = ft
	for _, tc := range []struct{ code, status string }{
		{"def f():\n    return 1\n", spec.StatusVerified},
		{"def f():\n    return 1  # TODO: handle 2\n", spec.StatusContradicted},
	} {
		repo, _, head := gitRepo(t, map[string]string{"f.py": tc.code})
		cm, _ := gitx.ShowCommit(repo, head)
		d := &claims.Data{Session: &store.SessionRow{Session: spec.Session{ID: "ses_0000000001", Repo: repo, Cwd: repo}}, Commits: []*gitx.Commit{cm}, Policy: pol}
		c := &spec.Claim{ID: "clm_1", Type: spec.TypeFeature, Title: "Added f",
			Assertions: []spec.Assertion{{"check": "no_new_todos", "commit": head}},
			Evidence:   []spec.Evidence{{Target: "git:commit/" + head, Relation: spec.RelProduced}}}
		vs := VerifyClaim(ctx, &Context{Data: d, Plugins: []*Plugin{pl}}, c, "", 1)
		dec := Decide(c, vs, pol, spec.OriginSelf, false)
		if dec.Status != tc.status {
			t.Errorf("%q: %s (missing %v), verdicts %+v", tc.code, dec.Status, dec.Missing, vs)
		}
	}
}
