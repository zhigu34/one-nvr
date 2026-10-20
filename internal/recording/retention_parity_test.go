package recording

import (
	"testing"

	"github.com/zhigu34/one-nvr/internal/reliability"
)

// Two packages sweep the same table, so their windows must agree.
//
// source_observations holds CURRENT STATE, not history: the row TTLs in thirty
// seconds, the status page reads only the newest row, and the reliability layer
// folds each sample into an interval as it arrives. No reader goes back.
//
// The windows live in separate packages only because of the dependency direction,
// and a shorter one wins by simply running first — silently truncating whatever the
// other was written to keep, then leaving it with nothing to delete. This test is a
// tripwire on the PAIR: changing one without the other is the mistake it catches.
//
// The value itself is deliberately short. PRD REL-06's seven days applies to the
// interval tier, which is kept for a year; at sixteen channels a week of raw
// samples is 1.2 GB of rows nothing reads.
func TestObservationRetentionMatchesTheReliabilityTier(t *testing.T) {
	if ObservationRetention != reliability.ObservationRetention {
		t.Fatalf("observation retention diverged: recording=%v reliability=%v; the shorter sweep wins",
			ObservationRetention, reliability.ObservationRetention)
	}
	if ObservationRetention > 48*60*60*1e9 {
		t.Fatalf("observation retention grew to %v; raw samples are current state, not history", ObservationRetention)
	}
}
