package storage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPoolRootConfinement(t *testing.T) {
	base := t.TempDir()
	inside := filepath.Join(base, "disk")
	outside := t.TempDir()
	if err := os.Mkdir(inside, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(base, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{outside, filepath.Join(base, "escape"), filepath.Join(base, "missing"), "relative"} {
		root, _, err := openPool([]string{base}, path)
		if err == nil {
			root.Close()
			t.Fatalf("unsafe/absent directory accepted: %s", path)
		}
	}
	root, canonical, err := openPool([]string{base}, inside)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	want, _ := filepath.EvalSymlinks(inside)
	if canonical != want {
		t.Fatalf("canonical=%s want=%s", canonical, want)
	}
}
