package recording

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func bufAt(seconds int) time.Time {
	return time.Date(2026, 10, 19, 0, 0, seconds, 0, time.UTC)
}

func fragment(start, end int, size int64) BufferFragment {
	return BufferFragment{
		Path:  fmt.Sprintf("%d.mp4", start),
		Start: bufAt(start),
		End:   bufAt(end),
		Size:  size,
	}
}

// ZLM names fragments after the instant the recorder opened them. The name is
// the only timing evidence these files have — they never get a completion
// callback — so an unparseable name must be skipped rather than guessed at.
func TestParseBufferFragmentReadsTheRecordedStart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "2026-10-19-00-16-31-7.mp4")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := ParseBufferFragment(path, info, time.UTC)
	if !ok {
		t.Fatal("a well-formed ZLM fragment name was rejected")
	}
	want := time.Date(2026, 10, 19, 0, 16, 31, 0, time.UTC)
	if !got.Start.Equal(want) {
		t.Fatalf("start = %v, want %v", got.Start, want)
	}
}

// A file ZLM is still writing carries a leading dot and has no moov yet.
// Handing it to the assembler produces a clip that reads as damaged.
func TestParseBufferFragmentSkipsIncompleteFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		".2026-10-19-00-16-34-8.mp4", // still being written
		"2026-10-19-00-16-31.mp4",    // missing the sequence suffix
		"notes.txt",
	} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		info, _ := os.Stat(path)
		if _, ok := ParseBufferFragment(path, info, time.UTC); ok {
			t.Fatalf("%s was accepted as a fragment", name)
		}
	}
}

// The keep window is measured against the fragment's end, not its start: a
// fragment that is still being written has an end in the future and must never
// be evicted out from under the recorder.
func TestBufferEvictionsKeepsThePromisedWindow(t *testing.T) {
	now := bufAt(100)
	fragments := []BufferFragment{
		fragment(60, 64, 10),
		fragment(64, 68, 10),
		fragment(90, 94, 10),
		fragment(94, 98, 10),
	}
	// keep 30s → cutoff at 70; fragments ending at 64 and 68 are outside it.
	evicted := BufferEvictions(fragments, now, 30, 1<<30)
	if len(evicted) != 2 {
		t.Fatalf("expected the two oldest evicted, got %d", len(evicted))
	}
	for _, item := range evicted {
		if item.Start.After(bufAt(70)) {
			t.Fatalf("evicted a fragment inside the window: %+v", item)
		}
	}
}

// A fragment that still overlaps the cutoff must survive even if it starts
// before it: the promised window is about coverage, not about start times.
func TestBufferEvictionsKeepsFragmentsStraddlingTheCutoff(t *testing.T) {
	now := bufAt(100)
	// Ends at 72, cutoff at 70 — still covers the oldest promised instant.
	fragments := []BufferFragment{fragment(68, 72, 10), fragment(96, 100, 10)}
	if evicted := BufferEvictions(fragments, now, 30, 1<<30); len(evicted) != 0 {
		t.Fatalf("evicted a fragment that still covers the window: %+v", evicted)
	}
}

// The byte budget is the second limit: a high-bitrate site can exceed the cache
// long before the keep window expires, and filling the tmpfs stops the media
// server for every channel.
func TestBufferEvictionsRespectsTheByteBudget(t *testing.T) {
	now := bufAt(100)
	fragments := []BufferFragment{
		fragment(90, 94, 40),
		fragment(94, 98, 40),
		fragment(98, 100, 40),
	}
	// 120 bytes total, budget 80 → one must go, oldest first.
	evicted := BufferEvictions(fragments, now, 600, 80)
	if len(evicted) != 1 {
		t.Fatalf("expected exactly one eviction to fit the budget, got %d", len(evicted))
	}
	if !evicted[0].Start.Equal(bufAt(90)) {
		t.Fatalf("evicted the wrong end: %+v", evicted[0])
	}
}

// The window's real span is the fragments', not the requested one: fragments are
// cut at keyframes, so the first starts before `from` and the last ends after
// `to`. The assembled clip's index must describe its bytes (REC-11).
func TestBufferWindowReportsTheRealSpan(t *testing.T) {
	fragments := []BufferFragment{
		fragment(50, 54, 10),
		fragment(54, 58, 10),
		fragment(58, 62, 10),
	}
	// Ask for 55..60; only the middle two overlap.
	picked, start, end := BufferWindow(fragments, bufAt(55), bufAt(60))
	if len(picked) != 2 {
		t.Fatalf("expected the two overlapping fragments, got %d", len(picked))
	}
	if !start.Equal(bufAt(54)) || !end.Equal(bufAt(62)) {
		t.Fatalf("reported span %v..%v, want 54..62", start, end)
	}
}

// Adjacent fragments meet exactly: one ending at `from` contributes nothing, and
// one starting at `to` is outside. Otherwise a window would drag in a fragment of
// footage entirely outside it.
func TestBufferWindowUsesHalfOpenBounds(t *testing.T) {
	fragments := []BufferFragment{
		fragment(50, 54, 10),
		fragment(60, 64, 10),
	}
	picked, _, _ := BufferWindow(fragments, bufAt(54), bufAt(60))
	if len(picked) != 0 {
		t.Fatalf("half-open bounds dragged in %d fragments", len(picked))
	}
}

// A window with no footage is a real answer, not an error: the channel may have
// started buffering after the event.
func TestBufferWindowReportsEmptyWhenNothingCovers(t *testing.T) {
	picked, start, end := BufferWindow([]BufferFragment{fragment(90, 94, 10)}, bufAt(10), bufAt(20))
	if len(picked) != 0 || !start.IsZero() || !end.IsZero() {
		t.Fatalf("expected an empty result, got %d fragments %v..%v", len(picked), start, end)
	}
}

// The fragment name is in the container's LOCAL time, not UTC. Measured on the
// live site: the media server runs with TZ=Asia/Shanghai and named a fragment
// 01:08 while UTC was 17:12 the previous day. Parsing that as UTC put every
// fragment eight hours in the future, so nothing ever looked expired and the ring
// grew until it would have filled the tmpfs.
func TestParseBufferFragmentUsesTheContainerZone(t *testing.T) {
	zone, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Skip("zone database unavailable")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "2026-10-19-01-08-49-0.mp4")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := ParseBufferFragment(path, info, zone)
	if !ok {
		t.Fatal("fragment rejected")
	}
	// 01:08 in Shanghai is 17:08 UTC the previous day.
	want := time.Date(2026, 10, 18, 17, 8, 49, 0, time.UTC)
	if !got.Start.Equal(want) {
		t.Fatalf("start = %v, want %v (parsing the name as UTC would put it eight hours ahead)", got.Start, want)
	}
}
