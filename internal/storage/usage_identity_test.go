package storage

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDamageVerdictIgnoresMovedMtime(t *testing.T) {
	identity, _ := json.Marshal(map[string]any{
		"Device": uint64(1), "Inode": uint64(42),
		"Size": int64(1000), "Modified": time.Now().Add(-time.Hour).UnixNano(),
	})
	// Everything matches except the mtime, which moved by an hour.
	if fileDamage(identity, 1000, 1, 42, 1000, time.Now().UnixNano()) {
		t.Fatal("a moved mtime was treated as damage; copying or restoring a file moves it without touching the footage")
	}
}

func TestDamageVerdictCatchesReplacedAndTruncatedFiles(t *testing.T) {
	identity, _ := json.Marshal(map[string]any{
		"Device": uint64(1), "Inode": uint64(42),
		"Size": int64(1000), "Modified": int64(5),
	})
	if !fileDamage(identity, 1000, 1, 43, 1000, 5) {
		t.Fatal("a different inode means the file was replaced")
	}
	if !fileDamage(identity, 1000, 1, 42, 900, 5) {
		t.Fatal("a smaller file than recorded was accepted")
	}
	if fileDamage(identity, 1000, 1, 42, 1000, 5) {
		t.Fatal("a matching file was rejected")
	}
}

// An empty identity is "never captured": the writer of that row did not record
// evidence, which is a different fact from evidence that disagrees. The size is
// still compared, which catches a truncated file.
func TestDamageVerdictTreatsEmptyIdentityAsUncaptured(t *testing.T) {
	if fileDamage([]byte("{}"), 1000, 1, 42, 1000, 5) {
		t.Fatal("an empty identity was treated as a mismatch")
	}
	if !fileDamage([]byte("{}"), 1000, 1, 42, 900, 5) {
		t.Fatal("an empty identity skipped the size comparison")
	}
}
