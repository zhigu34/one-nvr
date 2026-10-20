package integration

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The retention sweep must run without an Auth service.
//
// The worker builds its recording service without one — `recording.New` does not
// set Auth, and run.go never assigns it — while the integration fixtures do set it.
// That difference meant every test passed and the production sweep returned on its
// first line: a site configured to keep 30 days kept everything forever, with the
// setting visible and audited on the page.
//
// The sweep acts on the site's behalf, not a user's; deleteLocal takes an empty
// actor for exactly that case. This test pins the absence of the guard rather than
// the presence of a dependency.
func TestRetentionSweepNeedsNoAuthService(t *testing.T) {
	fixture := scheduledFixture(t)
	ctx := context.Background()
	// The production shape: no Auth on the service.
	fixture.Service.Auth = nil
	if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE sites SET recording_retention_days=1`); err != nil {
		t.Fatal(err)
	}
	segment := seedSegment(t, fixture, time.Now().UTC().Add(-72*time.Hour), time.Now().UTC().Add(-71*time.Hour))
	path := "recordings/" + string(segment) + ".mp4"
	target := filepath.Join(fixture.Pool.Path, path)
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old-footage"), 0o600); err != nil {
		t.Fatal(err)
	}

	fixture.Service.SweepRetention(ctx)

	var state string
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT state FROM recording_segments WHERE id=$1`, segment).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "deleted" {
		t.Fatalf("a segment 3 days old survived a 1-day retention: state=%q (the sweep never ran)", state)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("the file is still on disk after retention deleted the segment")
	}
}

// A file lost outside one-nvr must leave the candidate queue.
//
// The sweeps select `state='ready' ... ORDER BY end_at LIMIT 50` and stop as soon
// as a round deletes nothing. A missing file used to return "missing" WITHOUT
// changing the row, so it stayed at the head of that ordered queue forever: every
// round found no file, deleted nothing, and returned — while hundreds of deletable
// segments sat behind it. That is starvation, and it reports nothing.
func TestMissingFileLeavesTheRetentionQueue(t *testing.T) {
	fixture := scheduledFixture(t)
	ctx := context.Background()
	fixture.Service.Auth = nil
	if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE sites SET recording_retention_days=1`); err != nil {
		t.Fatal(err)
	}
	// The oldest segment has no file; a newer one still does and is also expired.
	missing := seedSegment(t, fixture, time.Now().UTC().Add(-90*time.Hour), time.Now().UTC().Add(-89*time.Hour))
	present := seedSegment(t, fixture, time.Now().UTC().Add(-80*time.Hour), time.Now().UTC().Add(-79*time.Hour))
	presentPath := "recordings/" + string(present) + ".mp4"
	presentTarget := filepath.Join(fixture.Pool.Path, presentPath)
	if err := os.MkdirAll(filepath.Dir(presentTarget), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(presentTarget, []byte("old-footage"), 0o600); err != nil {
		t.Fatal(err)
	}

	fixture.Service.SweepRetention(ctx)

	var missingState, presentState string
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT state FROM recording_segments WHERE id=$1`, missing).Scan(&missingState); err != nil {
		t.Fatal(err)
	}
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT state FROM recording_segments WHERE id=$1`, present).Scan(&presentState); err != nil {
		t.Fatal(err)
	}
	if missingState != "missing" {
		t.Fatalf("a segment whose file is gone stayed %q; every sweep will select it first and stop", missingState)
	}
	if presentState != "deleted" {
		t.Fatalf("the deletable segment behind the missing one was never reached: state=%q", presentState)
	}
}
