package recording

import (
	"context"
	"testing"
)

// The two repair passes run from the retention loop, which the assembly calls
// unconditionally. A partially constructed Service (tests, tooling) must be a
// no-op rather than a nil dereference.
func TestRepairPassesAreNilSafe(t *testing.T) {
	ctx := context.Background()
	(&Service{}).RepairEventClipDamage(ctx)
	(&Service{}).SweepEventClipOrphans(ctx)
}
