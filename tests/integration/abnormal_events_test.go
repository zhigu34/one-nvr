package integration

// PRD EVT-04: abnormal events.
//
// The requirement has two halves that are easy to get wrong in opposite
// directions: a condition that persists must remain ONE row, and a condition that
// recovers must keep its history. Writing a row per evaluation is noisy; deleting
// on recovery erases the fact that anything happened.

import (
	"context"
	"testing"
	"time"

	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/operations"
)

// A condition evaluated repeatedly must update one row, not accumulate.
func TestAbnormalEventStaysOneRowWhileFiring(t *testing.T) {
	fixture := newPublicationFixture(t)
	ctx := context.Background()
	service := operations.New(fixture.DB, fixture.Auth, false, false)
	// The detector is unreachable, as a stopped container would report.
	seedComponent(t, fixture, "frigate", "unavailable", "probe_failed")

	for i := 0; i < 3; i++ {
		service.AbnormalSweep(ctx)
	}
	var rows, occurrences int
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT count(*),coalesce(max(occurrences),0) FROM abnormal_events WHERE kind=$1`, operations.KindDetectorDown).
		Scan(&rows, &occurrences); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("a persistent condition produced %d rows; it must stay one", rows)
	}
	if occurrences < 3 {
		t.Fatalf("occurrences = %d; repeated sweeps must be counted on the one row", occurrences)
	}
}

// Recovery closes the row and keeps it: "it came back" is information an operator
// needs the next morning.
func TestAbnormalEventResolvesAndKeepsHistory(t *testing.T) {
	fixture := newPublicationFixture(t)
	ctx := context.Background()
	service := operations.New(fixture.DB, fixture.Auth, false, false)
	seedComponent(t, fixture, "frigate", "unavailable", "probe_failed")
	service.AbnormalSweep(ctx)

	seedComponent(t, fixture, "frigate", "healthy", "")
	service.AbnormalSweep(ctx)

	var state string
	var ended *time.Time
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT state,ended_at FROM abnormal_events WHERE kind=$1`, operations.KindDetectorDown).
		Scan(&state, &ended); err != nil {
		t.Fatal("the resolved incident was deleted instead of kept")
	}
	if state != "resolved" || ended == nil {
		t.Fatalf("state=%q ended=%v; a recovery must close the row with a time", state, ended)
	}

	// A second outage opens a NEW row: recurrence is a new incident, and the
	// history must show both.
	seedComponent(t, fixture, "frigate", "unavailable", "probe_failed")
	service.AbnormalSweep(ctx)
	var rows int
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM abnormal_events WHERE kind=$1`, operations.KindDetectorDown).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 2 {
		t.Fatalf("a recurrence produced %d rows, want the old resolved one plus a new firing one", rows)
	}
}

// A camera whose stream is gone is reported against that channel.
func TestAbnormalEventReportsOfflineCamera(t *testing.T) {
	fixture := newPublicationFixture(t)
	ctx := context.Background()
	if _, err := fixture.DB.Pool.Exec(ctx, "UPDATE channels SET current_revision_id=$2,enabled=true WHERE id=$1", fixture.Channel, fixture.Revision); err != nil {
		t.Fatal(err)
	}
	seedObservation(t, fixture, "main", "unavailable", "source_unavailable")
	service := operations.New(fixture.DB, fixture.Auth, false, false)
	service.AbnormalSweep(ctx)

	var channelID *id.ID
	var severity string
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT channel_id,severity FROM abnormal_events WHERE kind=$1`, operations.KindCameraOffline).
		Scan(&channelID, &severity); err != nil {
		t.Fatal("an offline camera was not reported", err)
	}
	if channelID == nil || *channelID != fixture.Channel {
		t.Fatalf("the incident is not attached to the channel: %v", channelID)
	}
}

// A pool that cannot be written is critical, not a per-channel nuisance: every
// channel on it stops recording.
func TestAbnormalEventReportsUnwritablePool(t *testing.T) {
	fixture := newPublicationFixture(t)
	ctx := context.Background()
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO storage_pool_checks(pool_id,service,state,reason_code,observed_at,expires_at)
		VALUES($1,'worker','unavailable','read_write_denied',clock_timestamp(),clock_timestamp()+interval '2 minutes')
		ON CONFLICT(pool_id,service) DO UPDATE SET state='unavailable',reason_code='read_write_denied',expires_at=EXCLUDED.expires_at`,
		fixture.Pool.ID); err != nil {
		t.Fatal(err)
	}
	service := operations.New(fixture.DB, fixture.Auth, false, false)
	service.AbnormalSweep(ctx)
	var severity string
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT severity FROM abnormal_events WHERE kind=$1`, operations.KindStorageUnwritable).Scan(&severity); err != nil {
		t.Fatal("an unwritable pool was not reported", err)
	}
	if severity != operations.SeverityCritical {
		t.Fatalf("severity = %q; a pool nobody can write to stops every channel on it", severity)
	}
}

// A publish failure is recorded when it happens and cleared by the next success —
// it cannot be found by a sweep, because a failure fixed before the sweep ran
// would look like it never happened.
func TestAbnormalEventRecordsPublishFailure(t *testing.T) {
	fixture := newPublicationFixture(t)
	ctx := context.Background()
	service := operations.New(fixture.DB, fixture.Auth, false, false)
	service.RecordPublishFailure(ctx, "reload_failed")
	var state string
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT state FROM abnormal_events WHERE kind=$1`, operations.KindConfigPublishFail).Scan(&state); err != nil {
		t.Fatal("a publish failure was not recorded", err)
	}
	if state != "firing" {
		t.Fatalf("state = %q, want firing", state)
	}
	service.ClearPublishFailure(ctx)
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT state FROM abnormal_events WHERE kind=$1`, operations.KindConfigPublishFail).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "resolved" {
		t.Fatalf("a successful publish left the incident %q", state)
	}
}

// A component that has never been probed is unknown, not broken: the first sweep
// after a worker start must not open an incident that closes a second later.
func TestAbnormalEventIgnoresUnprobedComponents(t *testing.T) {
	fixture := newPublicationFixture(t)
	ctx := context.Background()
	service := operations.New(fixture.DB, fixture.Auth, false, false)
	service.AbnormalSweep(ctx)
	var rows int
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM abnormal_events WHERE kind IN ($1,$2)`,
		operations.KindDetectorDown, operations.KindBrokerDown).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("an unprobed component produced %d incidents", rows)
	}
}

// A stale probe is not evidence of health either: a component whose observation
// expired must not be reported as recovered.
func TestAbnormalEventIgnoresStaleProbes(t *testing.T) {
	fixture := newPublicationFixture(t)
	ctx := context.Background()
	service := operations.New(fixture.DB, fixture.Auth, false, false)
	seedComponent(t, fixture, "frigate", "unavailable", "probe_failed")
	service.AbnormalSweep(ctx)
	// The probe stops reporting: expired, but not healthy.
	if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE component_observations SET expires_at=clock_timestamp()-interval '1 second' WHERE name='frigate'`); err != nil {
		t.Fatal(err)
	}
	service.AbnormalSweep(ctx)
	var state string
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT state FROM abnormal_events WHERE kind=$1`, operations.KindDetectorDown).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "firing" {
		t.Fatal("a stale probe was read as a recovery; unknown health is not health")
	}
}

func seedComponent(t *testing.T, fixture publishFixture, name, state, reason string) {
	t.Helper()
	if _, err := fixture.DB.Pool.Exec(context.Background(), `INSERT INTO component_observations(name,state,reason_code,observed_at,expires_at)
		VALUES($1,$2,$3,clock_timestamp(),clock_timestamp()+interval '2 minutes')
		ON CONFLICT(name) DO UPDATE SET state=EXCLUDED.state,reason_code=EXCLUDED.reason_code,observed_at=EXCLUDED.observed_at,expires_at=EXCLUDED.expires_at`,
		name, state, reason); err != nil {
		t.Fatal(err)
	}
}

func seedObservation(t *testing.T, fixture publishFixture, kind, state, reason string) {
	t.Helper()
	if _, err := fixture.DB.Pool.Exec(context.Background(), `INSERT INTO source_observations(id,channel_id,source_revision_id,kind,state,reason_code,observed_at,expires_at)
		VALUES(gen_random_uuid(),$1,$2,$3,$4,$5,clock_timestamp(),clock_timestamp()+interval '2 minutes')`,
		fixture.Channel, fixture.Revision, kind, state, reason); err != nil {
		t.Fatal(err)
	}
}
