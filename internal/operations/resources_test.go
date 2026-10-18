package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// A sampler with a missing /proc must report unknown, not zero. Zero CPU is
// indistinguishable from an idle healthy host, so a failed read that produced
// zero would be a lie in the direction that hides a broken measurement.
func TestSampleReportsUnknownWhenProcMissing(t *testing.T) {
	sampler := &hostSampler{proc: filepath.Join(t.TempDir(), "absent")}
	got := sampler.Sample(context.Background())
	if got.CPUKnown {
		t.Fatal("CPU reported known from a missing /proc/stat")
	}
	if got.MemoryKnown {
		t.Fatal("memory reported known from a missing /proc/meminfo")
	}
	if got.LoadKnown {
		t.Fatal("load reported known from a missing /proc/loadavg")
	}
}

// The first sample has no previous counters to subtract, so CPU is unknown; the
// second one has an interval and must report a percentage.
func TestSampleComputesCPUFromTwoReadings(t *testing.T) {
	dir := t.TempDir()
	// idle, then total both advance: 100 of 400 ticks went to work.
	writeFile(t, dir, "stat", "cpu  100 0 100 200 0 0 0 0 0 0\n")
	writeFile(t, dir, "meminfo", "MemTotal: 1000 kB\nMemAvailable: 250 kB\n")
	writeFile(t, dir, "loadavg", "1.50 1.20 1.00 1/100 1234\n")

	sampler := &hostSampler{proc: dir}
	first := sampler.Sample(context.Background())
	if first.CPUKnown {
		t.Fatal("the first sample has no interval and must not report CPU")
	}

	// +300 busy, +100 idle: 75% busy.
	writeFile(t, dir, "stat", "cpu  400 0 100 300 0 0 0 0 0 0\n")
	second := sampler.Sample(context.Background())
	if !second.CPUKnown {
		t.Fatal("the second sample has an interval and must report CPU")
	}
	if second.CPUPercent != 75 {
		t.Fatalf("cpu = %v%%, want 75%%", second.CPUPercent)
	}
	if !second.MemoryKnown || second.MemoryPercent != 75 {
		t.Fatalf("memory = %v (known %v), want 75%%", second.MemoryPercent, second.MemoryKnown)
	}
	if second.MemoryUsed != 750*1024 {
		t.Fatalf("memory used = %d, want 750 KiB", second.MemoryUsed)
	}
	if !second.LoadKnown || second.LoadAverage != 1.5 {
		t.Fatalf("load = %v, want 1.5", second.LoadAverage)
	}
}

// iowait is time the CPU was waiting, not working: counting it as busy reports a
// disk-bound host as CPU-bound.
func TestSampleTreatsIowaitAsIdle(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "stat", "cpu  0 0 0 100 0 0 0 0 0 0\n")
	writeFile(t, dir, "meminfo", "MemTotal: 100 kB\nMemAvailable: 100 kB\n")
	writeFile(t, dir, "loadavg", "0.00 0.00 0.00 1/1 1\n")
	sampler := &hostSampler{proc: dir}
	sampler.Sample(context.Background())

	// 100 ticks all spent in iowait.
	writeFile(t, dir, "stat", "cpu  0 0 0 100 100 0 0 0 0 0\n")
	got := sampler.Sample(context.Background())
	if !got.CPUKnown || got.CPUPercent != 0 {
		t.Fatalf("cpu = %v (known %v), want 0%%: iowait is not busy time", got.CPUPercent, got.CPUKnown)
	}
}

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
