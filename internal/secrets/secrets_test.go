package secrets

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestInitRepairsPermissionsWithoutReplacingKeys(t *testing.T) {
	for _, mode := range []os.FileMode{0400, 0644, 0700} {
		t.Run(mode.String(), func(t *testing.T) {
			dir := t.TempDir()
			first, err := Init(dir)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "secrets", "one-nvr.json")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, mode); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(dir); err == nil {
				t.Fatal("runtime accepted non-private key file")
			}
			second, err := Init(dir)
			if err != nil {
				t.Fatal("retry must restore valid file permissions:", err)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if second != first || !bytes.Equal(before, after) || info.Mode().Perm() != 0600 {
				t.Fatal("retry replaced keys or failed to restore permissions")
			}
		})
	}
}

func TestInitRefusesUnsafeOrCorruptKeyFilesWithoutChangingThem(t *testing.T) {
	for _, kind := range []string{"corrupt", "symlink", "directory", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Mkdir(filepath.Join(dir, "secrets"), 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "secrets", "one-nvr.json")
			var outside string
			var err error
			switch kind {
			case "corrupt":
				err = os.WriteFile(path, []byte("corrupt"), 0644)
			case "oversized":
				err = os.WriteFile(path, bytes.Repeat([]byte("x"), 16385), 0644)
			case "directory":
				err = os.Mkdir(path, 0700)
			case "symlink":
				outside = filepath.Join(t.TempDir(), "outside")
				if err := os.WriteFile(outside, []byte("preserve"), 0644); err != nil {
					t.Fatal(err)
				}
				err = os.Symlink(outside, path)
			}
			if err != nil {
				t.Fatal(err)
			}
			var outsideBefore os.FileInfo
			if outside != "" {
				outsideBefore, err = os.Stat(outside)
				if err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Init(dir); err == nil {
				t.Fatal("unsafe key file accepted")
			}
			after, err := os.Lstat(path)
			if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() || before.Size() != after.Size() {
				t.Fatal("invalid file changed", err)
			}
			if outside != "" {
				outsideAfter, err := os.Stat(outside)
				if err != nil || !os.SameFile(outsideBefore, outsideAfter) || outsideBefore.Mode() != outsideAfter.Mode() {
					t.Fatal("symlink target changed", err)
				}
			}
		})
	}
}

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
