package recording

// PRD REC-02: a weekly schedule is a rule evaluated at an instant, not a list of
// pre-expanded instants.
//
// The whole evaluation lives in this file as pure functions over plain values so
// it can be tested without a database and, more importantly, so there is exactly
// one definition of "is this channel scheduled right now". The scheduler, the
// obligation derivation and the UI preview all have to agree; three
// implementations of overlapping-with-midnight arithmetic would eventually
// disagree at 00:00 and nobody would notice until a completeness figure was off.

import (
	"fmt"
	"time"
)

// ScheduleWindow is one weekly window in site-local wall-clock minutes.
//
// Weekday is ISO-8601: 1=Monday .. 7=Sunday. StartMinute and EndMinute are
// minutes since local midnight; EndMinute is exclusive, and 1440 means midnight
// at the end of that weekday.
type ScheduleWindow struct {
	Weekday     int `json:"weekday"`
	StartMinute int `json:"start_minute"`
	EndMinute   int `json:"end_minute"`
}

// Validate checks one window on its own, without reference to its neighbours.
func (w ScheduleWindow) Validate() error {
	if w.Weekday < 1 || w.Weekday > 7 {
		return fmt.Errorf("weekday must be 1..7")
	}
	if w.StartMinute < 0 || w.StartMinute > 1439 {
		return fmt.Errorf("start_minute must be 0..1439")
	}
	// EndMinute is exclusive, so 1440 (midnight at the end of the day) is legal
	// while 0 is not: an end equal to the start would be an empty window.
	if w.EndMinute < 1 || w.EndMinute > 1440 {
		return fmt.Errorf("end_minute must be 1..1440")
	}
	if w.EndMinute <= w.StartMinute {
		return fmt.Errorf("end_minute must be greater than start_minute")
	}
	return nil
}

// isoWeekday converts a time.Time to the ISO numbering used by the schedule.
func isoWeekday(t time.Time) int {
	// Go: Sunday=0 .. Saturday=6. ISO: Monday=1 .. Sunday=7.
	if t.Weekday() == time.Sunday {
		return 7
	}
	return int(t.Weekday())
}

// minuteOfDay reports minutes since local midnight.
func minuteOfDay(t time.Time) int { return t.Hour()*60 + t.Minute() }

// ScheduleActiveAt reports whether any window covers the given instant when
// interpreted in the given location.
//
// A window is half-open [start, end): 08:00-12:00 does not include 12:00:00,
// which is what makes two adjacent windows (08:00-12:00 and 12:00-18:00) meet
// cleanly instead of overlapping for one instant. This matches the interval
// conventions the rest of the reliability layer already uses.
func ScheduleActiveAt(windows []ScheduleWindow, location *time.Location, at time.Time) bool {
	local := at.In(location)
	weekday := isoWeekday(local)
	minute := minuteOfDay(local)
	for _, w := range windows {
		if w.Weekday == weekday && minute >= w.StartMinute && minute < w.EndMinute {
			return true
		}
	}
	return false
}

// SplitOvernight converts one operator-facing window into the rows that are
// stored, splitting a window that crosses midnight.
//
// Storage keeps every row comparable against a single local date, so "22:00 to
// 02:00 on Monday" becomes Monday 22:00-1440 plus Tuesday 00:00-120. The editor
// shows one window; only storage sees two rows. The alternative — one row whose
// end is on the next day — makes "is this active now" a join across day
// boundaries and gets the weekday wrong for the second half.
//
// Input may carry an end beyond 1440 to mean "wraps past midnight", which is the
// shape MergeOvernightWindows returns and the editor edits. A plain
// end <= 1440 is already single-day and passes through unchanged.
func SplitOvernight(w ScheduleWindow) []ScheduleWindow {
	if w.EndMinute <= 1440 {
		return []ScheduleWindow{w}
	}
	nextDay := w.Weekday%7 + 1
	return []ScheduleWindow{
		{Weekday: w.Weekday, StartMinute: w.StartMinute, EndMinute: 1440},
		{Weekday: nextDay, StartMinute: 0, EndMinute: w.EndMinute - 1440},
	}
}

// MergeOvernightWindows is the inverse of the storage layout: it turns the rows
// back into operator-facing windows so a schedule that was entered as 22:00-02:00
// is displayed as one window rather than two unexplained halves.
//
// It only merges a tail that runs to 1440 with the following weekday's head that
// starts at 0 AND ends before 1440. The second condition is what keeps two real
// full days (two 00:00-1440 rows) from collapsing into one endless window: a
// merged pair must have a bounded end, not run to midnight again.
func MergeOvernightWindows(windows []ScheduleWindow) []ScheduleWindow {
	byKey := map[[2]int]ScheduleWindow{}
	for _, w := range windows {
		byKey[[2]int{w.Weekday, w.StartMinute}] = w
	}
	merged := make([]ScheduleWindow, 0, len(windows))
	used := map[[2]int]bool{}
	for _, w := range windows {
		key := [2]int{w.Weekday, w.StartMinute}
		if used[key] {
			continue
		}
		// A full day is not an overnight tail: merging it with the next day's
		// zero-length-at-start head would erase the boundary between two days.
		if w.EndMinute == 1440 && w.StartMinute > 0 {
			nextDay := w.Weekday%7 + 1
			headKey := [2]int{nextDay, 0}
			if head, ok := byKey[headKey]; ok && !used[headKey] && head.EndMinute < 1440 {
				used[headKey] = true
				merged = append(merged, ScheduleWindow{
					Weekday:     w.Weekday,
					StartMinute: w.StartMinute,
					// Beyond 1440 marks the wrap; SplitOvernight understands it.
					EndMinute: head.EndMinute + 1440,
				})
				continue
			}
		}
		merged = append(merged, w)
	}
	return merged
}

// ScheduleWindowsOverlap reports whether two stored (non-wrapping) rows overlap.
// Both must be on the same weekday; the caller groups by weekday first.
func ScheduleWindowsOverlap(a, b ScheduleWindow) bool {
	if a.Weekday != b.Weekday {
		return false
	}
	return a.StartMinute < b.EndMinute && b.StartMinute < a.EndMinute
}
