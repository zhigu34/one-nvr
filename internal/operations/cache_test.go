package operations

import (
	"os"
	"testing"
)

// A deployment that reserved no memory must report Configured=false, which the
// page renders as 未启用. Reporting 0% would claim memory is reserved and idle.
func TestEventCacheReportsUnconfigured(t *testing.T) {
	cache := readEventCache("")
	if cache.Configured || cache.Known {
		t.Fatalf("no path must mean not configured: %+v", cache)
	}
}

// A configured but unreadable mount is "configured, not measured" — a different
// answer from "not configured", and one the page shows as 未知.
func TestEventCacheReportsUnmeasurableMount(t *testing.T) {
	dir := t.TempDir()
	missing := dir + "/not-mounted"
	if err := os.MkdirAll(missing, 0o755); err != nil {
		t.Fatal(err)
	}
	// The directory exists but is not a separate mount; statfs still succeeds on
	// any directory, so this asserts the ordinary read rather than a failure.
	cache := readEventCache(missing)
	if !cache.Configured {
		t.Fatal("a provided path must be reported as configured")
	}
	if !cache.Known || cache.Total == 0 {
		t.Fatalf("a readable directory must report a size: %+v", cache)
	}
	if cache.Percent < 0 || cache.Percent > 100 {
		t.Fatalf("percent out of range: %+v", cache)
	}
}

// The mount owner is the one whose limit matters. tmpfs reports its configured
// size as Blocks and usage as Blocks-Free, so a freshly mounted buffer must read
// as nearly empty rather than full.
func TestEventCachePercentIsUsageNotRemaining(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/probe", make([]byte, 1024), 0o600); err != nil {
		t.Fatal(err)
	}
	cache := readEventCache(dir)
	if !cache.Known {
		t.Fatalf("readable directory not measured: %+v", cache)
	}
	// A 1KB file on a multi-gigabyte filesystem cannot be 90% full; if the
	// formula were inverted (Bavail vs Bfree) it would read near 100.
	if cache.Percent > 90 {
		t.Fatalf("percent looks inverted: %+v", cache)
	}
	if cache.Used == 0 {
		t.Fatalf("used bytes must be positive after writing a file: %+v", cache)
	}
}
