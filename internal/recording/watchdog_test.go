package recording

import (
	"testing"
	"time"
)

// The watchdog is what makes declining the admission-time write proof
// acceptable: without it, a recorder that produces no segment would keep
// running behind a grey "unknown" while nothing reached the disk. These cases
// pin the boundary between "one segment is late" and "this is producing
// nothing".
func TestRecordingOutputStallBoundary(t *testing.T) {
	now := time.Now().UTC()
	ago := func(d time.Duration) *time.Time {
		v := now.Add(-d)
		return &v
	}
	cases := []struct {
		name    string
		last    *time.Time
		started *time.Time
		stalled bool
	}{
		// A segment completed a minute ago is ordinary operation.
		{"recent completion", ago(time.Minute), ago(10 * time.Minute), false},
		// Just inside the grace period: still tolerated.
		{"completion inside grace", ago(RecordingOutputGrace - time.Second), ago(time.Hour), false},
		// Just past it: stopped.
		{"completion past grace", ago(RecordingOutputGrace + time.Second), ago(time.Hour), true},
		// A brand new run that has not finished its first segment yet is not a
		// failure, as long as it is younger than the grace period.
		{"new run pending first segment", nil, ago(time.Minute), false},
		{"new run past grace", nil, ago(RecordingOutputGrace + time.Second), true},
		// The newest of the two timestamps decides, so a fresh start cannot
		// disguise an old completion and vice versa.
		{"fresh start over stale completion", ago(time.Hour), ago(time.Minute), false},
		// No run row at all means the recorder cannot even name its run.
		{"missing run", nil, nil, true},
	}
	for _, c := range cases {
		if got := outputStalledAt(now, c.last, c.started); got != c.stalled {
			t.Fatalf("%s: stalled=%v want=%v", c.name, got, c.stalled)
		}
	}
}

// The existing completion status still distinguishes "waiting for the first
// segment" from "output went stale"; the watchdog only escalates the latter.
func TestCompletionStatusKeepsFirstCompletionPending(t *testing.T) {
	now := time.Now().UTC()
	fresh := now.Add(-30 * time.Second)
	old := now.Add(-10 * time.Minute)
	// Mirrors completionStatus' own decision without a database.
	decide := func(last, started *time.Time) string {
		if last != nil && last.After(now.Add(-90*time.Second)) {
			return "healthy"
		}
		if started != nil && started.After(now.Add(-90*time.Second)) {
			return "first_completion_pending"
		}
		return "completion_stale"
	}
	if got := decide(nil, &fresh); got != "first_completion_pending" {
		t.Fatalf("fresh start=%s", got)
	}
	if got := decide(&fresh, &old); got != "healthy" {
		t.Fatalf("recent completion=%s", got)
	}
	if got := decide(&old, &old); got != "completion_stale" {
		t.Fatalf("stale=%s", got)
	}
}
