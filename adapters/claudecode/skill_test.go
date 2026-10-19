package claudecode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BTONGK/credence/adapters"
)

func TestSkillInstallAndRemove(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	opts := adapters.InstallOptions{Scope: adapters.ScopeUser, Binary: "/opt/credence/bin/credence", DryRun: true}
	rep, err := Adapter{}.Install(opts)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(rep.Actions, "\n"), "installed the /credence skill") {
		t.Errorf("dry run actions lack the skill: %v", rep.Actions)
	}
	if _, err := os.Stat(SkillFile()); err == nil {
		t.Fatal("a dry run wrote the skill")
	}

	opts.DryRun = false
	if _, err := (Adapter{}).Install(opts); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(SkillFile())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"name: credence\n",
		"allowed-tools: Bash(/opt/credence/bin/credence check *)",
		"!`/opt/credence/bin/credence check --harness claude-code --session=${CLAUDE_SESSION_ID}`",
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("skill lacks %q:\n%s", want, b)
		}
	}

	if _, err := (Adapter{}).Uninstall(adapters.InstallOptions{Scope: adapters.ScopeUser}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(SkillFile())); err == nil {
		t.Error("uninstall left the skill folder")
	}
}

func TestSkillLeavesOtherSkillsAlone(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	if err := os.MkdirAll(filepath.Dir(SkillFile()), 0o755); err != nil {
		t.Fatal(err)
	}
	mine := []byte("---\nname: credence\n---\nSomeone else's skill.\n")
	if err := os.WriteFile(SkillFile(), mine, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallSkill("/bin/credence", false); err == nil {
		t.Error("install replaced a skill Credence did not write")
	}
	if _, err := (Adapter{}).Uninstall(adapters.InstallOptions{Scope: adapters.ScopeUser}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(SkillFile()); string(b) != string(mine) {
		t.Error("uninstall changed a skill Credence did not write")
	}
}

func TestSkillQuotesOddPaths(t *testing.T) {
	s := Skill("/Users/Sam Lee/bin/credence")
	if !strings.Contains(s, "!`'/Users/Sam Lee/bin/credence' check") {
		t.Errorf("a path with a space is quoted:\n%s", s)
	}
}
