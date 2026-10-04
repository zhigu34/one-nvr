package secrets

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInitializationPreservesExistingSecretAndRejectsSymlinks(t *testing.T) {
	dir := t.TempDir()
	first, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("redeploy replaced secret")
	}
	filename := filepath.Join(dir, "secrets", "one-nvr.json")
	if err := os.WriteFile(filename, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Init(dir); err == nil {
		t.Fatal("replaced corrupt secret")
	}
	other := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(other, "secrets")); err != nil {
		t.Fatal(err)
	}
	if _, err := Init(other); err == nil {
		t.Fatal("followed secret directory symlink")
	}
}
