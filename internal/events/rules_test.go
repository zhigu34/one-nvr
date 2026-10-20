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

// Folding duplicates is only safe when position agrees.
//
// The detector assigns a new id to the same person after a brief occlusion,
// producing two overlapping rows for one walk-past. But two people can also be
// in frame at once, and merging those hides a real detection. Measured on this
// site the two cases separate cleanly: dx ≈ 0.01 for a re-identification,
// dx ≈ 0.11–0.22 for two people.
func TestFoldDuplicateDetectionsNeedsTheSamePlace(t *testing.T) {
	base := time.Date(2026, 10, 20, 10, 25, 0, 0, time.UTC)
	ch := id.ID("11111111-1111-4111-8111-111111111111")
	event := func(idValue string, startSec, endSec float64, x float64) Event {
		end := base.Add(time.Duration(endSec) * time.Second)
		start := base.Add(time.Duration(startSec) * time.Second)
		return Event{
			ID: id.ID(idValue), ChannelID: &ch, Category: "person", Score: 0.9,
			StartedAt: start, EndedAt: &end, Box: []float64{x, 0.5, 0.2, 0.3},
		}
	}

	// Same place, overlapping: one subject re-identified.
	same := foldDuplicateDetections([]Event{
		event("aaaaaaaa-1111-4111-8111-111111111111", 22, 42, 0.59),
		event("bbbbbbbb-1111-4111-8111-111111111111", 12, 34, 0.58),
	})
	if len(same) != 1 {
		t.Fatalf("same-place overlap did not fold: %d rows", len(same))
	}
	if len(same[0].MergedIDs) != 1 {
		t.Fatalf("the folded id was not reported: %v", same[0].MergedIDs)
	}

	// Different place, overlapping: two people, must stay apart.
	apart := foldDuplicateDetections([]Event{
		event("cccccccc-1111-4111-8111-111111111111", 22, 42, 0.59),
		event("dddddddd-1111-4111-8111-111111111111", 12, 34, 0.38),
	})
	if len(apart) != 2 {
		t.Fatalf("two people in frame were merged into %d row(s)", len(apart))
	}

	// No box: cannot tell, so never fold.
	unknown := foldDuplicateDetections([]Event{
		event("eeeeeeee-1111-4111-8111-111111111111", 22, 42, 0.59),
		func() Event {
			e := event("ffffffff-1111-4111-8111-111111111111", 12, 34, 0.58)
			e.Box = nil
			return e
		}(),
	})
	if len(unknown) != 2 {
		t.Fatalf("a row with no box was folded on a guess")
	}

	// Sequential, not overlapping: separate events.
	sequential := foldDuplicateDetections([]Event{
		event("99999999-1111-4111-8111-111111111111", 60, 80, 0.59),
		event("88888888-1111-4111-8111-111111111111", 10, 30, 0.59),
	})
	if len(sequential) != 2 {
		t.Fatalf("sequential events were merged")
	}
}
