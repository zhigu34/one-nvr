package events

import (
	"testing"
	"time"
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
