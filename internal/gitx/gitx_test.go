package gitx

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMayBeRepo(t *testing.T) {
	top, _ := filepath.EvalSymlinks(t.TempDir())
	site := filepath.Join(top, "demo", "site")
	if err := os.MkdirAll(site, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_DIR", "")
	t.Setenv("GIT_CEILING_DIRECTORIES", "")
	if mayBeRepo(site) {
		t.Errorf("%s has no .git above it", site)
	}
	if err := os.Mkdir(filepath.Join(top, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !mayBeRepo(site) {
		t.Errorf("%s sits under %s/.git", site, top)
	}
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Join(top, "demo"))
	if mayBeRepo(site) {
		t.Errorf("the ceiling at %s/demo hides %s/.git", top, top)
	}
}
