package recording

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestPublishCrashAndNoOverwrite(t *testing.T) {
	path := t.TempDir()
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := root.MkdirAll(".work/run", 0700); err != nil {
		t.Fatal(err)
	}
	original := ".work/run/closed.mp4"
	target := "recordings/CH01/2026-10-05/closed.mp4"
	if err := root.WriteFile(original, []byte("original media"), 0600); err != nil {
		t.Fatal(err)
	}
	info, _ := root.Stat(original)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := atomicPublish(canceled, root, original, target, info); err == nil {
		t.Fatal("canceled publication moved media")
	}
	if _, err := root.Stat(original); err != nil {
		t.Fatal("canceled operation lost original")
	}
	if err := atomicPublish(context.Background(), root, original, target, info); err != nil {
		t.Fatal(err)
	}
	moved, _ := root.Stat(target)
	if !os.SameFile(info, moved) {
		t.Fatal("publication copied/replaced inode")
	}
	if _, err := root.Stat(original); !os.IsNotExist(err) {
		t.Fatal("original still exists after atomic rename")
	}
	if err := root.WriteFile(original, []byte("second media"), 0600); err != nil {
		t.Fatal(err)
	}
	second, _ := root.Stat(original)
	if err := atomicPublish(context.Background(), root, original, target, second); err == nil {
		t.Fatal("existing target overwritten")
	}
	raw, _ := root.ReadFile(target)
	if string(raw) != "original media" {
		t.Fatal("existing target changed")
	}
	if _, err := root.Stat(original); err != nil {
		t.Fatal("conflict deleted original")
	}
	if err := root.Remove(original); err != nil {
		t.Fatal(err)
	}
	if err := root.Symlink(target, original); err != nil {
		t.Fatal(err)
	}
	if err := atomicPublish(context.Background(), root, original, "recordings/other.mp4", moved); err == nil {
		t.Fatal("source symlink accepted")
	}
	outside := t.TempDir()
	if err := root.Symlink(outside, "escape"); err != nil {
		t.Fatal(err)
	}
	if err := atomicPublish(context.Background(), root, target, "escape/new.mp4", moved); err == nil {
		t.Fatal("parent symlink accepted")
	}
	files, _ := os.ReadDir(outside)
	if len(files) != 0 {
		t.Fatal("wrote outside pool", filepath.Base(path))
	}
}

func TestPublicationFailureKeepsMediaAndEvidence(t *testing.T) {
	for _, mode := range []string{"cross-device", "directory-sync"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			source, target := "closed.mp4", "recordings/CH01/closed.mp4"
			if err := root.WriteFile(source, []byte("fixture bytes"), 0600); err != nil {
				t.Fatal(err)
			}
			info, _ := root.Stat(source)
			move := renameNoReplace
			syncDirectory := func(dir *os.File) error { return dir.Sync() }
			if mode == "cross-device" {
				move = func(int, string, int, string) error { return syscall.EXDEV }
			}
			if mode == "directory-sync" {
				syncDirectory = func(*os.File) error { return syscall.EIO }
			}
			if err := atomicPublishWith(context.Background(), root, source, target, info, move, syncDirectory); err == nil {
				t.Fatal("publication failure reported ready")
			}
			retained := source
			if mode == "directory-sync" {
				retained = target
			}
			actual, err := root.Stat(retained)
			if err != nil || !os.SameFile(info, actual) {
				t.Fatal("failure lost media identity", err)
			}
			if mode == "cross-device" {
				if _, err := root.Stat(target); !os.IsNotExist(err) {
					t.Fatal("cross-device failure copied media")
				}
			}
		})
	}
}
