package archive

import (
	"fmt"
	"strconv"
	"strings"
)

// ArchiveWindow is one HH:MM-HH:MM interval in the site's wall clock.
//
// Windows replaced the old "fire at a fixed time" schedule. A single daily
// trigger could only enqueue one bounded batch per day, so a channel that
// accumulated a thousand segments overnight needed ten days to drain. A window
// says "while it is open, keep feeding the queue" — which is what an operator
// means by "upload at night".
type ArchiveWindow struct {
	StartMinute int
	EndMinute   int
}

// String renders the window back in the stored form.
func (w ArchiveWindow) String() string {
	return fmt.Sprintf("%02d:%02d-%02d:%02d", w.StartMinute/60, w.StartMinute%60, w.EndMinute/60, w.EndMinute%60)
}

// Overnight reports whether the window wraps past midnight, such as 22:00-02:00.
func (w ArchiveWindow) Overnight() bool { return w.StartMinute > w.EndMinute }

// Contains reports whether a wall-clock minute falls inside the window.
//
// Half-open [start, end): the minute the window closes is already outside, so
// adjacent windows like 00:00-02:00 and 02:00-04:00 neither overlap nor leave a
// one-minute hole.
func (w ArchiveWindow) Contains(minute int) bool {
	if w.Overnight() {
		return minute >= w.StartMinute || minute < w.EndMinute
	}
	return minute >= w.StartMinute && minute < w.EndMinute
}

// ParseWindows reads the stored spec: one or more HH:MM-HH:MM windows separated
// by commas, each evaluated in the site's timezone.
func ParseWindows(spec string) ([]ArchiveWindow, error) {
	trimmed := strings.TrimSpace(spec)
	if trimmed == "" {
		return nil, ErrScheduleRequired
	}
	out := make([]ArchiveWindow, 0, 2)
	for _, part := range strings.Split(trimmed, ",") {
		part = strings.TrimSpace(part)
		start, end, ok := strings.Cut(part, "-")
		if !ok {
			// A bare HH:MM is the pre-window format. It is refused rather than
			// guessed at: "02:00" could mean a moment or the start of a window,
			// and the two behave differently enough to matter.
			return nil, ErrScheduleFormat
		}
		startMinute, err := parseClock(start)
		if err != nil {
			return nil, ErrScheduleFormat
		}
		endMinute, err := parseClock(end)
		if err != nil {
			return nil, ErrScheduleFormat
		}
		if startMinute == endMinute {
			// Equal ends read as either a zero-length window or a full day, and
			// silently choosing one of those would be a guess about intent.
			return nil, ErrScheduleFormat
		}
		out = append(out, ArchiveWindow{StartMinute: startMinute, EndMinute: endMinute})
	}
	return out, nil
}

// parseClock reads HH:MM into a minute of the day, rejecting values a clock
// cannot show.
func parseClock(s string) (int, error) {
	if len(s) != 5 || s[2] != ':' {
		return 0, fmt.Errorf("not HH:MM")
	}
	hour, err := strconv.Atoi(s[:2])
	if err != nil {
		return 0, err
	}
	minute, err := strconv.Atoi(s[3:])
	if err != nil {
		return 0, err
	}
	// 24:00 is not a time on a clock; a window ending at midnight is written as
	// 00:00 with the day boundary handled by Contains.
	if hour > 23 || minute > 59 {
		return 0, fmt.Errorf("out of range")
	}
	return hour*60 + minute, nil
}

// AnyWindowContains reports whether any window is open at the given minute.
func AnyWindowContains(windows []ArchiveWindow, minute int) bool {
	for _, window := range windows {
		if window.Contains(minute) {
			return true
		}
	}
	return false
}
