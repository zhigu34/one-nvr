package recording

import (
	"testing"
	"time"

	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/media/zlm"
)

// The media server names files in the container's local time, which the
// deployment sets to the site's zone.
//
// Parsing them as UTC put every recovered file's start off by the offset, and
// when the offset is positive it pushed the start into the FUTURE — where the
// plausibility guard rejected it and the segment stayed 'provisional', never
// playable. Two parsers of the same naming rule disagreed: ParseBufferFragment
// took a zone from e43d4c9, this one took UTC.
func TestNativeStartUsesTheSiteZone(t *testing.T) {
	const zone = "Asia/Shanghai"
	run := id.ID("11111111-1111-4111-8111-111111111111")
	// A run created just before the file was written, so the guard accepts it.
	created := time.Date(2026, 10, 20, 6, 0, 0, 0, time.UTC) // 14:00 local
	receipt := runReceipt{
		Version:          2,
		NativeTimezone:   zone,
		WorkRelativePath: ".work/zlm/" + string(run),
		Key:              zlm.StreamKey{VHost: "__defaultVhost__", App: "one_nvr", Stream: "s1"},
		CreatedAt:        created,
	}
	prefix := receipt.WorkRelativePath + "/record/one_nvr/s1/"
	// 14:05 LOCAL is 06:05 UTC. Parsing the name as UTC would give 14:05 UTC —
	// eight hours in the future, which the guard then drops.
	start, ok := nativeStart(receipt, prefix+"2026-10-20/2026-10-20-14-05-00-0.mp4")
	if !ok {
		t.Fatal("a local-time file name was rejected; parsing it as UTC puts it eight hours ahead of the run")
	}
	want := time.Date(2026, 10, 20, 6, 5, 0, 0, time.UTC)
	if !start.Equal(want) {
		t.Fatalf("start = %v, want %v (14:05 Asia/Shanghai)", start, want)
	}
}

// A version-1 receipt recorded "UTC" because the field was hardcoded. That value
// was never the truth, so it is refused rather than trusted: recovering nothing
// beats recovering the wrong instant.
func TestNativeStartRefusesALegacyUTCReceipt(t *testing.T) {
	const run = id.ID("22222222-2222-4222-8222-222222222222")
	receipt := runReceipt{
		Version:          1,
		NativeTimezone:   "UTC",
		WorkRelativePath: ".work/zlm/" + string(run),
		Key:              zlm.StreamKey{VHost: "__defaultVhost__", App: "one_nvr", Stream: "s1"},
		CreatedAt:        time.Date(2026, 10, 20, 6, 0, 0, 0, time.UTC),
	}
	prefix := receipt.WorkRelativePath + "/record/one_nvr/s1/"
	if _, ok := nativeStart(receipt, prefix+"2026-10-20/2026-10-20-06-05-00-0.mp4"); ok {
		t.Fatal("a version-1 receipt's hardcoded UTC was trusted")
	}
}

// A version-2 receipt may legitimately say UTC — a site deployed in UTC.
func TestNativeStartAcceptsUTCOnACurrentReceipt(t *testing.T) {
	const run = id.ID("33333333-3333-4333-8333-333333333333")
	receipt := runReceipt{
		Version:          2,
		NativeTimezone:   "UTC",
		WorkRelativePath: ".work/zlm/" + string(run),
		Key:              zlm.StreamKey{VHost: "__defaultVhost__", App: "one_nvr", Stream: "s1"},
		CreatedAt:        time.Date(2026, 10, 20, 6, 0, 0, 0, time.UTC),
	}
	prefix := receipt.WorkRelativePath + "/record/one_nvr/s1/"
	start, ok := nativeStart(receipt, prefix+"2026-10-20/2026-10-20-06-05-00-0.mp4")
	if !ok {
		t.Fatal("a UTC site's own receipt was refused")
	}
	if want := time.Date(2026, 10, 20, 6, 5, 0, 0, time.UTC); !start.Equal(want) {
		t.Fatalf("start = %v, want %v", start, want)
	}
}

// A receipt from before the fix still names the same run, so it must not be
// rejected as a conflict — only its timezone is ignored.
func TestLegacyReceiptMatchesTheSameRun(t *testing.T) {
	run := id.ID("44444444-4444-4444-8444-444444444444")
	base := runReceipt{
		Version:          1,
		NativeTimezone:   "UTC",
		WorkRelativePath: ".work/zlm/" + string(run),
		Key:              zlm.StreamKey{VHost: "__defaultVhost__", App: "one_nvr", Stream: "s1"},
		CreatedAt:        time.Date(2026, 10, 20, 6, 0, 0, 0, time.UTC),
	}
	current := base
	current.Version = 2
	current.NativeTimezone = "Asia/Shanghai"
	if !sameRunExceptZone(base, current) {
		t.Fatal("a legacy receipt was treated as a different run; crash recovery would stay blocked forever")
	}
	// A different stream is a different run.
	other := current
	other.Key.Stream = "s2"
	if sameRunExceptZone(base, other) {
		t.Fatal("two different streams were treated as the same run")
	}
}
