package archive

import (
	"errors"
	"testing"
	"time"
)

// A window decides whether the scheduler is uploading right now, so its edges
// are the behaviour: half-open [start, end), no overlap, no gap.
func TestWindowContainsIsHalfOpen(t *testing.T) {
	window := ArchiveWindow{StartMinute: 2 * 60, EndMinute: 6 * 60}
	cases := []struct {
		minute int
		want   bool
		why    string
	}{
		{1*60 + 59, false, "one minute before the window"},
		{2 * 60, true, "the opening minute is inside"},
		{4 * 60, true, "the middle"},
		{5*60 + 59, true, "one minute before closing"},
		// Closing minute excluded: two adjacent windows (00:00-02:00 and
		// 02:00-04:00) must not both claim 02:00.
		{6 * 60, false, "the closing minute is outside"},
		{6*60 + 1, false, "after the window"},
	}
	for _, c := range cases {
		if got := window.Contains(c.minute); got != c.want {
			t.Fatalf("%02d:%02d: want %v (%s), got %v", c.minute/60, c.minute%60, c.want, c.why, got)
		}
	}
}

// 22:00-02:00 is open at 23:00 and at 01:00, and closed at 03:00.
func TestWindowCrossingMidnight(t *testing.T) {
	window := ArchiveWindow{StartMinute: 22 * 60, EndMinute: 2 * 60}
	if !window.Overnight() {
		t.Fatal("22:00-02:00 must be recognised as crossing midnight")
	}
	for _, minute := range []int{22 * 60, 23*60 + 30, 0, 1*60 + 59} {
		if !window.Contains(minute) {
			t.Fatalf("%02d:%02d must be inside 22:00-02:00", minute/60, minute%60)
		}
	}
	for _, minute := range []int{2 * 60, 12 * 60, 21*60 + 59} {
		if window.Contains(minute) {
			t.Fatalf("%02d:%02d must be outside 22:00-02:00", minute/60, minute%60)
		}
	}
}

// Adjacent windows neither overlap nor leave a hole, which is what makes the
// half-open rule worth stating.
func TestAdjacentWindowsDoNotOverlap(t *testing.T) {
	windows, err := ParseWindows("00:00-02:00,02:00-04:00")
	if err != nil {
		t.Fatal(err)
	}
	// 02:00 belongs to exactly one of them.
	inside := 0
	for _, window := range windows {
		if window.Contains(2 * 60) {
			inside++
		}
	}
	if inside != 1 {
		t.Fatalf("02:00 must be inside exactly one window, found %d", inside)
	}
	// And 01:59 / 02:01 are both covered.
	if !AnyWindowContains(windows, 1*60+59) || !AnyWindowContains(windows, 2*60+1) {
		t.Fatal("adjacent windows must cover the minutes on either side of the seam")
	}
}

func TestParseWindowsAcceptsTheStoredShape(t *testing.T) {
	for _, spec := range []string{"00:00-23:59", "22:00-02:00", "02:00-06:00,18:00-20:00", " 02:00-06:00 "} {
		if _, err := ParseWindows(spec); err != nil {
			t.Fatalf("%q must parse: %v", spec, err)
		}
	}
	// Round-trip, so a saved value reads back the same way.
	windows, err := ParseWindows("22:00-02:00,06:00-08:00")
	if err != nil {
		t.Fatal(err)
	}
	if got := windows[0].String(); got != "22:00-02:00" {
		t.Fatalf("round-trip changed the window: %q", got)
	}
}

func TestParseWindowsRefusesAmbiguousInput(t *testing.T) {
	cases := []struct {
		spec string
		want error
		why  string
	}{
		{"", ErrScheduleRequired, "empty says nothing"},
		{"   ", ErrScheduleRequired, "whitespace says nothing"},
		// A bare time is the old format; accepting it would mean guessing
		// whether the operator meant a moment or the start of a window.
		{"02:00", ErrScheduleFormat, "a single time is not a window"},
		{"02:00,14:30", ErrScheduleFormat, "the old list format"},
		{"02:00-02:00", ErrScheduleFormat, "equal ends: zero length or all day?"},
		{"24:00-06:00", ErrScheduleFormat, "24:00 is not on a clock"},
		{"02:00-24:00", ErrScheduleFormat, "a window ending at midnight is 00:00"},
		{"02:60-06:00", ErrScheduleFormat, "no sixty minutes in an hour"},
		{"2:00-06:00", ErrScheduleFormat, "HH must be two digits"},
		{"02:00-6:00", ErrScheduleFormat, "MM must be two digits"},
		{"ab:cd-06:00", ErrScheduleFormat, "not a time"},
		{"0200-0600", ErrScheduleFormat, "missing colon"},
		{"02:00-06:00,", ErrScheduleFormat, "trailing comma is not a window"},
		{"02:00-06:00-08:00", ErrScheduleFormat, "two dashes"},
	}
	for _, c := range cases {
		_, err := ParseWindows(c.spec)
		if !errors.Is(err, c.want) {
			t.Fatalf("%q: want %v (%s), got %v", c.spec, c.want, c.why, err)
		}
	}
}

// WindowOpenAt resolves the window in the site's zone, not the server's.
func TestWindowOpenAtUsesTheSiteZone(t *testing.T) {
	// 2026-10-21T18:00:00Z is 02:00 next day in Asia/Shanghai.
	at := mustParse(t, "2026-10-21T18:00:00Z")
	open, err := WindowOpenAt("02:00-06:00", "Asia/Shanghai", at)
	if err != nil {
		t.Fatal(err)
	}
	if !open {
		t.Fatal("02:00-06:00 must be open at 02:00 Shanghai time")
	}
	// The same instant against a UTC site is 18:00, outside the window.
	open, err = WindowOpenAt("02:00-06:00", "UTC", at)
	if err != nil {
		t.Fatal(err)
	}
	if open {
		t.Fatal("the window must be evaluated in the site's zone, not the caller's")
	}
	// A malformed spec reads as closed: uploading outside the operator's stated
	// hours is worse than not uploading.
	open, err = WindowOpenAt("garbage", "UTC", at)
	if err == nil || open {
		t.Fatal("a malformed spec must read as closed")
	}
	// An unknown zone is closed too, for the same reason.
	open, err = WindowOpenAt("02:00-06:00", "Mars/Olympus", at)
	if err == nil || open {
		t.Fatal("an unknown zone must read as closed")
	}
}

// mustParse is a test helper for a fixed instant.
func mustParse(t *testing.T, s string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return at
}
