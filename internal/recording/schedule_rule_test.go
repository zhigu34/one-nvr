package recording

import (
	"testing"
	"time"
)

func mustZone(t *testing.T, name string) *time.Location {
	t.Helper()
	zone, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	return zone
}

func at(t *testing.T, zone *time.Location, value string) time.Time {
	t.Helper()
	parsed, err := time.ParseInLocation("2006-01-02 15:04", value, zone)
	if err != nil {
		t.Fatalf("parse %s: %v", value, err)
	}
	return parsed
}

func TestScheduleWindowValidation(t *testing.T) {
	valid := ScheduleWindow{Weekday: 1, StartMinute: 8 * 60, EndMinute: 12 * 60}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid window rejected: %v", err)
	}
	// 1440 means midnight at the end of the day and is legal.
	if err := (ScheduleWindow{Weekday: 7, StartMinute: 0, EndMinute: 1440}).Validate(); err != nil {
		t.Fatalf("full-day window rejected: %v", err)
	}
	for name, window := range map[string]ScheduleWindow{
		"weekday zero":    {Weekday: 0, StartMinute: 0, EndMinute: 60},
		"weekday eight":   {Weekday: 8, StartMinute: 0, EndMinute: 60},
		"empty":           {Weekday: 1, StartMinute: 60, EndMinute: 60},
		"reversed":        {Weekday: 1, StartMinute: 120, EndMinute: 60},
		"negative start":  {Weekday: 1, StartMinute: -1, EndMinute: 60},
		"end past 1440":   {Weekday: 1, StartMinute: 0, EndMinute: 1441},
		"zero-length end": {Weekday: 1, StartMinute: 0, EndMinute: 0},
	} {
		if err := window.Validate(); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

// The boundary is the whole point of half-open intervals: adjacent windows must
// meet without overlapping for an instant, or an obligation derived from them
// would double-count one minute at every boundary.
func TestScheduleActiveAtIsHalfOpen(t *testing.T) {
	zone := mustZone(t, "Asia/Shanghai")
	windows := []ScheduleWindow{
		{Weekday: 1, StartMinute: 8 * 60, EndMinute: 12 * 60},
		{Weekday: 1, StartMinute: 12 * 60, EndMinute: 18 * 60},
	}
	cases := map[string]struct {
		when string
		want bool
	}{
		"before start":    {"2026-10-19 07:59", false},
		"at start":        {"2026-10-19 08:00", true},
		"inside":          {"2026-10-19 10:30", true},
		"one min to end":  {"2026-10-19 11:59", true},
		"at end":          {"2026-10-19 12:00", true}, // the second window starts here
		"inside second":   {"2026-10-19 15:00", true},
		"at final end":    {"2026-10-19 18:00", false},
		"after final end": {"2026-10-19 18:01", false},
	}
	for name, test := range cases {
		got := ScheduleActiveAt(windows, zone, at(t, zone, test.when))
		if got != test.want {
			t.Fatalf("%s: got %v want %v", name, got, test.want)
		}
	}
}

// 2026-10-19 is a Monday. The schedule below deliberately omits weekdays, so a
// Sunday query against a Monday window must be false — the weekday is part of
// the rule, not just the time of day.
func TestScheduleActiveAtRespectsWeekday(t *testing.T) {
	zone := mustZone(t, "Asia/Shanghai")
	windows := []ScheduleWindow{{Weekday: 1, StartMinute: 0, EndMinute: 1440}}
	if !ScheduleActiveAt(windows, zone, at(t, zone, "2026-10-19 00:00")) {
		t.Fatal("Monday 00:00 should be active")
	}
	if !ScheduleActiveAt(windows, zone, at(t, zone, "2026-10-19 23:59")) {
		t.Fatal("Monday 23:59 should be active")
	}
	if ScheduleActiveAt(windows, zone, at(t, zone, "2026-10-20 00:00")) {
		t.Fatal("Tuesday 00:00 must not match a Monday-only window")
	}
	if ScheduleActiveAt(windows, zone, at(t, zone, "2026-10-18 12:00")) {
		t.Fatal("Sunday must not match a Monday-only window")
	}
}

// An overnight window is stored as two rows. The second half belongs to the next
// weekday, which is exactly the case a naive single-row implementation gets
// wrong: it would report the tail as not scheduled.
func TestOvernightWindowCoversBothHalves(t *testing.T) {
	zone := mustZone(t, "Asia/Shanghai")
	// Monday 22:00 -> Tuesday 02:00, as the editor enters it.
	operatorView := []ScheduleWindow{{Weekday: 1, StartMinute: 22 * 60, EndMinute: 2*60 + 1440}}
	rows := SplitOvernight(operatorView[0])
	if len(rows) != 2 {
		t.Fatalf("overnight window did not split: %+v", rows)
	}
	if rows[0].Weekday != 1 || rows[0].StartMinute != 1320 || rows[0].EndMinute != 1440 {
		t.Fatalf("first half wrong: %+v", rows[0])
	}
	if rows[1].Weekday != 2 || rows[1].StartMinute != 0 || rows[1].EndMinute != 120 {
		t.Fatalf("second half wrong: %+v", rows[1])
	}
	for _, test := range []struct {
		when string
		want bool
	}{
		{"2026-10-19 21:59", false},
		{"2026-10-19 22:00", true},
		{"2026-10-19 23:59", true},
		{"2026-10-20 00:00", true},
		{"2026-10-20 01:59", true},
		{"2026-10-20 02:00", false},
		{"2026-10-20 23:00", false},
	} {
		if got := ScheduleActiveAt(rows, zone, at(t, zone, test.when)); got != test.want {
			t.Fatalf("%s: got %v want %v", test.when, got, test.want)
		}
	}
}

// Sunday night wraps into Monday, so the day arithmetic must not treat 7+1 as 8.
func TestOvernightWindowWrapsSundayToMonday(t *testing.T) {
	zone := mustZone(t, "Asia/Shanghai")
	rows := SplitOvernight(ScheduleWindow{Weekday: 7, StartMinute: 23 * 60, EndMinute: 60 + 1440})
	if len(rows) != 2 || rows[1].Weekday != 1 {
		t.Fatalf("Sunday wrap produced %+v", rows)
	}
	// 2026-10-18 is a Sunday, 2026-10-19 the following Monday.
	if !ScheduleActiveAt(rows, zone, at(t, zone, "2026-10-19 00:30")) {
		t.Fatal("Sunday-overnight tail must be active on Monday")
	}
	if ScheduleActiveAt(rows, zone, at(t, zone, "2026-10-18 00:30")) {
		t.Fatal("Sunday-overnight tail must not be active on the preceding Sunday")
	}
}

// Round trip: what the editor entered must come back as what the editor entered,
// otherwise every save silently rewrites the operator's schedule into two rows.
func TestMergeOvernightRestoresTheOperatorView(t *testing.T) {
	rows := []ScheduleWindow{
		{Weekday: 1, StartMinute: 22 * 60, EndMinute: 1440},
		{Weekday: 2, StartMinute: 0, EndMinute: 120},
		{Weekday: 3, StartMinute: 9 * 60, EndMinute: 17 * 60},
	}
	merged := MergeOvernightWindows(rows)
	if len(merged) != 2 {
		t.Fatalf("expected the overnight pair to merge into one: %+v", merged)
	}
	if merged[0].Weekday != 1 || merged[0].StartMinute != 1320 || merged[0].EndMinute != 1560 {
		t.Fatalf("merged overnight wrong: %+v", merged[0])
	}
	if merged[1].Weekday != 3 || merged[1].EndMinute != 1020 {
		t.Fatalf("unrelated window damaged: %+v", merged[1])
	}
	// The merged form must split back into exactly the stored rows.
	back := SplitOvernight(merged[0])
	if len(back) != 2 || back[0] != rows[0] || back[1] != rows[1] {
		t.Fatalf("round trip changed the rows: %+v", back)
	}
}

// Two real full days must stay two windows. If the merge accepted a head that
// also runs to 1440, an every-day 00:00-24:00 schedule would collapse into one
// endless window and the day boundaries in the UI would disappear.
func TestMergeLeavesFullDaysAlone(t *testing.T) {
	rows := []ScheduleWindow{
		{Weekday: 1, StartMinute: 0, EndMinute: 1440},
		{Weekday: 2, StartMinute: 0, EndMinute: 1440},
	}
	if merged := MergeOvernightWindows(rows); len(merged) != 2 {
		t.Fatalf("full days must not merge, got %+v", merged)
	}
}

// The site timezone is the interpretation, not a display detail: 08:00 in
// Shanghai is a different instant from 08:00 UTC, and a schedule must follow the
// site's wall clock.
func TestScheduleUsesSiteWallClock(t *testing.T) {
	shanghai := mustZone(t, "Asia/Shanghai")
	utc := time.UTC
	windows := []ScheduleWindow{{Weekday: 1, StartMinute: 8 * 60, EndMinute: 9 * 60}}
	// 2026-10-19 08:30 Shanghai == 2026-10-19 00:30 UTC.
	instant := at(t, shanghai, "2026-10-19 08:30")
	if !ScheduleActiveAt(windows, shanghai, instant) {
		t.Fatal("08:30 local must be inside an 08:00-09:00 local window")
	}
	if ScheduleActiveAt(windows, utc, instant) {
		t.Fatal("the same instant is 00:30 UTC and must be outside 08:00-09:00")
	}
}

// DST: a spring-forward day has a 23-hour local day. A window expressed in wall
// clock minutes must still mean wall clock — evaluating in UTC would shift every
// window by an hour for half the year.
func TestScheduleSurvivesDSTTransition(t *testing.T) {
	zone := mustZone(t, "Europe/Berlin")
	windows := []ScheduleWindow{{Weekday: 7, StartMinute: 8 * 60, EndMinute: 9 * 60}}
	// 2026-03-29 is the spring-forward Sunday in Europe: 02:00 -> 03:00.
	if !ScheduleActiveAt(windows, zone, at(t, zone, "2026-03-29 08:30")) {
		t.Fatal("08:30 local on the DST transition day must be inside the window")
	}
	// And the length of a window is not what matters: 02:30 does not exist that
	// day, so the window it belongs to is reported through the local clock.
	if ScheduleActiveAt(windows, zone, at(t, zone, "2026-03-29 09:30")) {
		t.Fatal("09:30 local must be outside an 08:00-09:00 window")
	}
}

// unionMode returns a decision, not an echo of the stored mode. A planned
// channel outside its windows must decide "none": returning "planned" sends the
// scheduler down the recorder-start path, and the channel would record around
// the clock while showing a schedule in the UI.
func TestUnionModeFoldsAClosedScheduleToNone(t *testing.T) {
	cases := []struct {
		name              string
		policy            string
		manual, scheduled bool
		want              string
	}{
		{"continuous ignores the schedule", "continuous", false, false, "continuous"},
		{"planned inside its window", "planned", false, true, "planned"},
		{"planned outside its window", "planned", false, false, "none"},
		{"planned outside, manual open", "planned", true, false, "manual"},
		{"planned inside, manual open", "planned", true, true, "planned"},
		{"none with manual", "none", true, false, "manual"},
		{"none with a stale schedule", "none", false, true, "none"},
		{"unknown mode records nothing", "", false, false, "none"},
		{"unknown mode cannot borrow a schedule", "garbage", false, true, "none"},
	}
	for _, test := range cases {
		if got := unionMode(test.policy, test.manual, test.scheduled); got != test.want {
			t.Fatalf("%s: got %q want %q", test.name, got, test.want)
		}
	}
}
