package recording

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A moved mtime must not make a published file look damaged.
//
// The recording package and the storage reconciler answer the same question —
// "is this still the file we published" — and they disagreed: storage excluded
// mtime, this package included it. One copied file was therefore healthy to the
// reconciler and damaged to playback, export and deletion, so its bytes could
// never be reclaimed.
func TestIdentityIgnoresAMovedMtime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clip.mp4")
	if err := os.WriteFile(path, []byte("footage"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	captured, err := identity(info)
	if err != nil {
		t.Fatal(err)
	}

	// Rewrite the mtime only: same inode, same bytes.
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	moved, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !captured.matches(moved) {
		t.Fatal("a moved mtime was treated as damage")
	}

	// A truncated file is still damage: size is part of the verdict.
	if err := os.WriteFile(path, []byte("foo"), 0o600); err != nil {
		t.Fatal(err)
	}
	truncated, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if captured.matches(truncated) {
		t.Fatal("a truncated file passed the identity check")
	}
}

// An identity that was never captured must not be read as "changed".
//
// Rows written before identity capture exist with a zero struct; judging them by
// fields they never had marked intact files damaged on every reconciliation.
func TestUncapturedIdentityMatchesAnything(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clip.mp4")
	if err := os.WriteFile(path, []byte("footage"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !(fileIdentity{}).matches(info) {
		t.Fatal("an uncaptured identity was treated as a mismatch")
	}
}
