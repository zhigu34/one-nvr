package recording

import "testing"

// The three lines are the operator's contract: warning tells, pressure cleans,
// critical keeps cleaning. A classification that puts a pool in the wrong band
// either deletes footage nobody asked to delete or lets a disk fill silently.
func TestWatermarkStateClassification(t *testing.T) {
	base := watermarkPolicy{
		WarningPercent:  80,
		PressurePercent: 85,
		TargetPercent:   80,
		CriticalPercent: 95,
		TotalBytes:      1000,
		Known:           true,
	}
	cases := []struct {
		used int64
		want string
	}{
		{0, WatermarkOK},
		{500, WatermarkOK},
		{799, WatermarkOK},
		{800, WatermarkWarning},
		{849, WatermarkWarning},
		{850, WatermarkPressure},
		{949, WatermarkPressure},
		{950, WatermarkCritical},
		{1000, WatermarkCritical},
	}
	for _, tc := range cases {
		policy := base
		policy.UsedBytes = tc.used
		policy.FreeBytes = policy.TotalBytes - tc.used
		if got := policy.state(); got != tc.want {
			t.Fatalf("%d/%d used = %s, want %s; the bands are what decides whether "+
				"footage is deleted", tc.used, policy.TotalBytes, got, tc.want)
		}
	}
}

// Display rounding must not decide the threshold. 799/1000 displays as 80 but
// has not reached 80%, and classifying it as warning would fire every band up
// to one point early.
func TestWatermarkThresholdDoesNotUseDisplayRounding(t *testing.T) {
	policy := watermarkPolicy{
		Known: true, TotalBytes: 1000, UsedBytes: 799, FreeBytes: 201,
		WarningPercent: 80, PressurePercent: 85, TargetPercent: 80, CriticalPercent: 95,
	}
	if got := policy.state(); got != WatermarkOK {
		t.Fatalf("799/1000 classified as %s, want ok: 79.9%% has not reached 80%%", got)
	}
	if percentUsed(799, 1000) != 80 {
		t.Fatal("display rounding changed; the UI should still read 80")
	}
	exact := policy
	exact.UsedBytes = 800
	if got := exact.state(); got != WatermarkWarning {
		t.Fatalf("800/1000 classified as %s, want warning", got)
	}
}

// An unmeasured pool is not a healthy pool. Reporting it as ok would paint a
// green pool over a capacity probe that has never answered.
func TestWatermarkUnknownIsNotHealthy(t *testing.T) {
	for _, policy := range []watermarkPolicy{
		{Known: false, TotalBytes: 1000, WarningPercent: 80, PressurePercent: 85, CriticalPercent: 95},
		{Known: true, TotalBytes: 0, WarningPercent: 80, PressurePercent: 85, CriticalPercent: 95},
	} {
		if got := policy.state(); got != WatermarkUnknown {
			t.Fatalf("unmeasured pool reported %s, want unknown", got)
		}
		if policy.cleanupNeeded() {
			t.Fatal("an unmeasured pool must not trigger deletion: we do not know " +
				"whether it needs space")
		}
	}
}

// Only pressure and critical delete. Warning is an advisory band: if it deleted
// too, the operator could not tell 80% from 85% by observing behaviour.
func TestWatermarkWarningDoesNotDelete(t *testing.T) {
	policy := watermarkPolicy{
		Known: true, TotalBytes: 1000, UsedBytes: 820, FreeBytes: 180,
		WarningPercent: 80, PressurePercent: 85, TargetPercent: 80, CriticalPercent: 95,
		SafeLine: 1,
	}
	if policy.state() != WatermarkWarning {
		t.Fatalf("state = %s, want warning", policy.state())
	}
	if policy.cleanupNeeded() {
		t.Fatal("warning triggered cleanup; it is an advisory band")
	}
}

// Rounding must favour the threshold, not the pool: 84.2% used is reported as
// 85%, so a pool that has effectively crossed the pressure line is treated as
// having crossed it.
func TestPercentUsedRoundsUp(t *testing.T) {
	cases := []struct {
		used, total int64
		want        int
	}{
		{842, 1000, 85},
		{800, 1000, 80},
		{799, 1000, 80},
		{1, 1000, 1},
		{0, 1000, 0},
		{1000, 1000, 100},
		{1001, 1000, 100},
		{5, 0, 0},
	}
	for _, tc := range cases {
		if got := percentUsed(tc.used, tc.total); got != tc.want {
			t.Fatalf("percentUsed(%d,%d) = %d, want %d", tc.used, tc.total, got, tc.want)
		}
	}
}

// The target is the larger of the configured percentage and the measured safety
// line. Without the floor, cleaning to a target percentage can stop with less
// free space than one segment needs — the disk would refill immediately and the
// channel gate would block recording right after a "successful" cleanup.
func TestWatermarkTargetRespectsSafetyLine(t *testing.T) {
	byPercent := watermarkPolicy{
		Known: true, TotalBytes: 1000, TargetPercent: 80, SafeLine: 50,
	}
	if got := byPercent.targetFreeBytes(); got != 200 {
		t.Fatalf("target = %d, want 200 (20%% of 1000)", got)
	}
	byLine := watermarkPolicy{
		Known: true, TotalBytes: 1000, TargetPercent: 99, SafeLine: 300,
	}
	if got := byLine.targetFreeBytes(); got != 300 {
		t.Fatalf("target = %d, want the 300-byte safety line", got)
	}
	// A full-disk target percentage of 100 has no percentage component at all;
	// the floor is then the only target.
	full := watermarkPolicy{Known: true, TotalBytes: 1000, TargetPercent: 100, SafeLine: 120}
	if got := full.targetFreeBytes(); got != 120 {
		t.Fatalf("target = %d, want the safety line alone", got)
	}
}

// A pool at the target percentage must not be considered clean while it is
// still below the safety line; the two conditions have to agree.
func TestWatermarkCriticalOnSmallVolume(t *testing.T) {
	// 10GB floor on a 20GB volume means the safety line alone is 50% of the
	// disk; a "80% used" policy would try to clean down to 4GB free, which the
	// 10GB line forbids.
	policy := watermarkPolicy{
		Known: true, TotalBytes: 20 << 30, UsedBytes: 11 << 30, FreeBytes: 9 << 30,
		WarningPercent: 80, PressurePercent: 85, TargetPercent: 80, CriticalPercent: 95,
		SafeLine: 10 << 30,
	}
	if got := policy.targetFreeBytes(); got != 10<<30 {
		t.Fatalf("target = %d, want the 10GB safety line rather than 4GB", got)
	}
}
