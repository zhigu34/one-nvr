package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAPISuccessDoesNotImplyZLMWritable(t *testing.T) {
	pool, roots := markedPool(t)
	api, err := (&Probe{Roots: roots, Service: "api"}).Check(context.Background(), pool)
	if err != nil || api.State != "healthy" || api.FilesystemID == "" || api.TotalBytes <= 0 {
		t.Fatalf("real API probe: %+v %v", api, err)
	}
	worker, err := (&Probe{Roots: []string{t.TempDir()}, Service: "worker"}).Check(context.Background(), pool)
	if err != nil || worker.State != "unavailable" {
		t.Fatalf("independent worker failure: %+v %v", worker, err)
	}
	zlm, err := (&Probe{Roots: roots, Service: "zlm"}).Check(context.Background(), pool)
	if err != nil || zlm.State != "pending" || zlm.Reason != "test_source_required" {
		t.Fatalf("ZLM must not impersonate worker: %+v %v", zlm, err)
	}
	pool.Checks = []Check{api, worker, zlm}
	if pool.Readiness(time.Now()) == "ready" {
		t.Fatal("API success implied recording readiness")
	}
	entries, err := os.ReadDir(filepath.Join(pool.Path, ".work", "probes"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("probe left files: %v %v", entries, err)
	}
}

func TestSharedFilesystemCapacityNotDoubled(t *testing.T) {
	a, roots := markedPool(t)
	b, otherRoots := markedPool(t)
	ca, err := (&Probe{Roots: roots, Service: "api"}).Check(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	cb, err := (&Probe{Roots: otherRoots, Service: "api"}).Check(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	if ca.FilesystemID != cb.FilesystemID {
		t.Skip("temporary directories on distinct filesystems")
	}
	a.Checks = []Check{ca}
	b.Checks = []Check{cb}
	capacity := AggregateCapacity([]Pool{a, b}, time.Now())
	if capacity.TotalBytes != ca.TotalBytes || capacity.FilesystemCount != 1 {
		t.Fatalf("shared capacity duplicated: %+v", capacity)
	}
	if capacity.FreeBytes > ca.FreeBytes || capacity.FreeBytes > cb.FreeBytes {
		t.Fatal("available capacity inflated")
	}
	if capacity := AggregateCapacity([]Pool{a, b}, time.Now().Add(time.Minute)); capacity.FilesystemCount != 0 {
		t.Fatal("expired capacity counted")
	}
}

func TestProbeCannotEscapeThroughWorkSymlink(t *testing.T) {
	pool, roots := markedPool(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(pool.Path, ".work")); err != nil {
		t.Fatal(err)
	}
	check, err := (&Probe{Roots: roots, Service: "api"}).Check(context.Background(), pool)
	if err != nil || check.State != "unavailable" {
		t.Fatalf("symlink probe: %+v %v", check, err)
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Fatal("wrote outside registered pool")
	}
}
