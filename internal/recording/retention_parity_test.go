package recording

import (
	"testing"

	"github.com/zhigu34/one-nvr/internal/reliability"
)

// Two packages sweep the same table, so their windows must agree.
//
// source_observations is the raw sample history: the recording package's
// retention loop deletes from it, and the reliability layer derives intervals from
// it and keeps seven days per PRD REL-06. The windows live in separate packages
// only because of the dependency direction, and a shorter one wins by simply
// running first — silently truncating the history the other was written to keep,
// then leaving it with nothing to delete.
//
// This test is a tripwire on the pair, not on either constant: changing one without
// the other is the mistake it exists to catch.
func TestObservationRetentionMatchesTheReliabilityTier(t *testing.T) {
	if ObservationRetention != reliability.ObservationRetention {
		t.Fatalf("observation retention diverged: recording=%v reliability=%v; the shorter sweep wins",
			ObservationRetention, reliability.ObservationRetention)
	}
}
