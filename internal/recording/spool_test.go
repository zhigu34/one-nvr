package recording

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhigu34/one-nvr/internal/media/zlm"
)

func testCompletion() Completion {
	return Completion{Key: zlm.StreamKey{VHost: "__defaultVhost__", App: "one_nvr", Stream: "bb6f7f61-e7dd-4eee-a03c-a53167d6d688"}, MediaServerID: "one-nvr-test", FilePath: "/storage/pool/.work/zlm/bb6f7f61-e7dd-4eee-a03c-a53167d6d688/closed.mp4", Size: 1024, StartTime: time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC), Duration: time.Second}
}
func TestSpoolDurableBoundedAndNoEviction(t *testing.T) {
	dir := t.TempDir()
	spool := &Spool{Dir: filepath.Join(dir, "recording-spool")}
	c := testCompletion()
	if err := spool.Put(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if err := spool.Put(context.Background(), c); err != nil {
		t.Fatal("duplicate rejected", err)
	}
	entries, err := os.ReadDir(spool.Dir)
	if err != nil {
		t.Fatal(err)
	}
	files := 0
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".json" {
			files++
			info, _ := entry.Info()
			if info.Mode().Perm() != 0600 {
				t.Fatal("spool payload readable by others")
			}
		}
	}
	if files != 1 {
		t.Fatal("duplicate spool payload", files)
	}
	info, err := os.Stat(spool.Dir)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("spool directory not private", err)
	}
	var got []Completion
	if err := spool.Drain(context.Background(), func(_ context.Context, item Completion) error { got = append(got, item); return nil }); err != nil || len(got) != 1 || got[0].FilePath != c.FilePath {
		t.Fatal("durable payload not replayed", err)
	}
	quota, err := os.Create(filepath.Join(spool.Dir, "pending-fixture.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if err := quota.Truncate(256 << 20); err != nil {
		t.Fatal(err)
	}
	quota.Close()
	if err := spool.Put(context.Background(), c); err == nil {
		t.Fatal("full spool accepted callback")
	}
	if _, err := os.Stat(filepath.Join(spool.Dir, "pending-fixture.tmp")); err != nil {
		t.Fatal("unprocessed file evicted")
	}
	outside := t.TempDir()
	redirect := filepath.Join(t.TempDir(), "spool")
	if err := os.Symlink(outside, redirect); err != nil {
		t.Fatal(err)
	}
	if err := (&Spool{Dir: redirect}).Put(context.Background(), c); err == nil {
		t.Fatal("symlink spool root accepted")
	}
}

func TestSpoolInterruptedFileDoesNotStrandAcknowledgedCallbacks(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "recording-spool")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(dir, ".pending-interrupted.tmp")
	if err := os.WriteFile(orphan, []byte(`{"Key":`), 0600); err != nil {
		t.Fatal(err)
	}
	spool := Spool{Dir: dir}
	if err := spool.Put(context.Background(), testCompletion()); err != nil {
		t.Fatal(err)
	}
	replayed := 0
	err := spool.Drain(context.Background(), func(context.Context, Completion) error { replayed++; return nil })
	if replayed != 1 {
		t.Fatal("partial unacknowledged write stranded accepted callback", replayed, err)
	}
	if err == nil {
		t.Fatal("corrupt orphan not reported")
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Fatal("orphan evidence deleted")
	}
}
