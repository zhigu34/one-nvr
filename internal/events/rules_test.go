package events

import (
	"testing"
	"time"

	"github.com/zhigu34/one-nvr/internal/id"
)

func siteClock(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse("2006-01-02 15:04", value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestArmingWindow(t *testing.T) {
	cases := []struct {
		name      string
		from, to  string
		at        string
		wantArmed bool
	}{
		{"no window is always armed", "", "", "2026-10-13 03:00", true},
		{"inside a daytime window", "09:00", "17:00", "2026-10-13 12:00", true},
		{"before a daytime window", "09:00", "17:00", "2026-10-13 08:59", false},
		{"the end is exclusive", "09:00", "17:00", "2026-10-13 17:00", false},
		// A night rule spans midnight: 22:00–06:00 means late evening AND
		// small hours, which is the shape an operator actually writes.
		{"late evening in a night window", "22:00", "06:00", "2026-10-13 23:30", true},
		{"small hours in the same window", "22:00", "06:00", "2026-10-13 02:00", true},
		{"midday is outside the night window", "22:00", "06:00", "2026-10-13 12:00", false},
		{"the night window ends exclusively", "22:00", "06:00", "2026-10-13 06:00", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			rule := Rule{ArmFrom: testCase.from, ArmTo: testCase.to}
			if got := withinArming(rule, siteClock(t, testCase.at)); got != testCase.wantArmed {
				t.Fatalf("withinArming(%q..%q at %s) = %v, want %v", testCase.from, testCase.to, testCase.at, got, testCase.wantArmed)
			}
		})
	}
}

func TestClockValidation(t *testing.T) {
	for _, value := range []string{"00:00", "09:30", "23:59"} {
		if !validClock(value) {
			t.Fatalf("%q must be a valid clock", value)
		}
	}
	for _, value := range []string{"", "9:30", "24:00", "09:60", "09-30", "aa:bb"} {
		if validClock(value) {
			t.Fatalf("%q must be refused", value)
		}
	}
}

// Folding is time-based, not identity-based.
//
// The detector cannot tell one person from another: it re-identifies a subject
// after a brief occlusion and emits a second event, and three people walking past
// is three events. The list answers "when was someone there", so a quiet stretch
// shorter than ActivityGap belongs to the same occurrence whoever it was.
func TestFoldMergesOneStretchOfActivity(t *testing.T) {
	base := time.Date(2026, 10, 20, 10, 25, 0, 0, time.UTC)
	ch := id.ID("11111111-1111-4111-8111-111111111111")
	other := id.ID("22222222-2222-4222-8222-222222222222")
	at := func(offset time.Duration) time.Time { return base.Add(offset) }
	event := func(idValue string, start, end time.Duration, category string, channel id.ID) Event {
		e := at(end)
		s := at(start)
		return Event{
			ID: id.ID(idValue), ChannelID: &channel, Category: category, Score: 0.9,
			StartedAt: s, EndedAt: &e,
		}
	}

	// Overlapping: one occurrence.
	if got := foldDuplicateDetections([]Event{
		event("aaaaaaaa-1111-4111-8111-111111111111", 12*time.Second, 34*time.Second, "person", ch),
		event("bbbbbbbb-1111-4111-8111-111111111111", 22*time.Second, 42*time.Second, "person", ch),
	}); len(got) != 1 {
		t.Fatalf("overlapping detections did not fold: %d rows", len(got))
	}

	// A second person right after the first: still one occurrence.
	got := foldDuplicateDetections([]Event{
		event("cccccccc-1111-4111-8111-111111111111", 40*time.Second, 55*time.Second, "person", ch),
		event("dddddddd-1111-4111-8111-111111111111", 10*time.Second, 30*time.Second, "person", ch),
	})
	if len(got) != 1 {
		t.Fatalf("a 10s gap between two people was not folded: %d rows", len(got))
	}
	if len(got[0].MergedIDs) != 1 {
		t.Fatalf("the folded id was not reported: %v", got[0].MergedIDs)
	}
	// The merged row covers the whole stretch, not just the first detection.
	if !got[0].StartedAt.Equal(at(10*time.Second)) || !got[0].EndedAt.Equal(at(55*time.Second)) {
		t.Fatalf("merged span is %v..%v, want 10s..55s", got[0].StartedAt, got[0].EndedAt)
	}

	// Longer than the gap: a separate visit.
	if got := foldDuplicateDetections([]Event{
		event("eeeeeeee-1111-4111-8111-111111111111", 90*time.Second, 100*time.Second, "person", ch),
		event("ffffffff-1111-4111-8111-111111111111", 10*time.Second, 30*time.Second, "person", ch),
	}); len(got) != 2 {
		t.Fatalf("visits %v apart were merged", ActivityGap)
	}

	// Different channels are never one occurrence.
	if got := foldDuplicateDetections([]Event{
		event("99999999-1111-4111-8111-111111111111", 10*time.Second, 30*time.Second, "person", ch),
		event("88888888-1111-4111-8111-111111111111", 12*time.Second, 32*time.Second, "person", other),
	}); len(got) != 2 {
		t.Fatal("detections on two channels were merged")
	}

	// Different categories are different things.
	if got := foldDuplicateDetections([]Event{
		event("77777777-1111-4111-8111-111111111111", 10*time.Second, 30*time.Second, "person", ch),
		event("66666666-1111-4111-8111-111111111111", 12*time.Second, 32*time.Second, "car", ch),
	}); len(got) != 2 {
		t.Fatal("a person and a car were merged")
	}
}
