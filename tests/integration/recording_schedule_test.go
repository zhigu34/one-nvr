package integration

// PRD REC-02: weekly recording schedules, and REC-04's union for 'planned'.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/recording"
)

// isoToday is the current weekday in the ISO numbering the schedule uses
// (1=Monday .. 7=Sunday). Go's time.Weekday() puts Sunday at 0, so using it
// directly would produce an invalid schedule every Sunday.
func isoToday() int {
	weekday := int(time.Now().UTC().Weekday())
	if weekday == 0 {
		return 7
	}
	return weekday
}

// scheduledFixture binds a channel, sets it to planned and pins the site zone to
// UTC so the test can compute "the current window" without depending on where
// the machine happens to be.
func scheduledFixture(t *testing.T) publishFixture {
	t.Helper()
	fixture := newPublicationFixture(t)
	ctx := context.Background()
	if _, err := fixture.DB.Pool.Exec(ctx, "UPDATE sites SET timezone='UTC' WHERE singleton"); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, "UPDATE channels SET current_revision_id=$2,storage_pool_id=$3 WHERE id=$1", fixture.Channel, fixture.Revision, fixture.Pool.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, "UPDATE recording_policies SET mode='planned' WHERE channel_id=$1", fixture.Channel); err != nil {
		t.Fatal(err)
	}
	return fixture
}

// A planned channel covers its scheduled window as owed recording; outside every
// window the time is an exclusion, not downtime. Both halves matter: reporting
// the unscheduled part as owed would make every planned channel look permanent
// broken, and reporting the scheduled part as an exclusion would hide a real
// recording failure.
func TestPlannedChannelOwesOnlyInsideItsWindow(t *testing.T) {
	fixture := scheduledFixture(t)
	ctx := context.Background()
	// Window today, 00:00-24:00, in UTC (the site zone the fixture set).
	today := isoToday()
	if _, err := fixture.Service.PutSchedule(ctx, fixture.Admin, fixture.Channel, []recording.ScheduleWindow{
		{Weekday: today, StartMinute: 0, EndMinute: 1440},
	}, channelVersion(t, fixture)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.Service.ReconcileObligation(ctx, fixture.Channel); err != nil {
		t.Fatal(err)
	}
	var source string
	var exclusion *string
	if err := fixture.DB.Pool.QueryRow(ctx,
		`SELECT source,exclusion FROM recording_obligations WHERE channel_id=$1 AND ended_at IS NULL ORDER BY started_at DESC LIMIT 1`,
		fixture.Channel).Scan(&source, &exclusion); err != nil {
		t.Fatal(err)
	}
	if source != "planned" || exclusion != nil {
		t.Fatalf("inside a scheduled window recording must be owed: source=%q exclusion=%v", source, exclusion)
	}

	// Now move the schedule one day away: nothing is scheduled right now.
	if _, err := fixture.Service.PutSchedule(ctx, fixture.Admin, fixture.Channel, []recording.ScheduleWindow{
		{Weekday: today%7 + 1, StartMinute: 0, EndMinute: 1440},
	}, channelVersion(t, fixture)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.Service.ReconcileObligation(ctx, fixture.Channel); err != nil {
		t.Fatal(err)
	}
	exclusion = nil
	if err := fixture.DB.Pool.QueryRow(ctx,
		`SELECT source,exclusion FROM recording_obligations WHERE channel_id=$1 AND ended_at IS NULL ORDER BY started_at DESC LIMIT 1`,
		fixture.Channel).Scan(&source, &exclusion); err != nil {
		t.Fatal(err)
	}
	if source != "planned" || exclusion == nil || *exclusion != "disabled" {
		t.Fatalf("outside every window the time must be excluded, not owed: source=%q exclusion=%v", source, exclusion)
	}
}

// REC-04: a manual window keeps recording even when the schedule is closed. The
// union must not let one source of demand cancel another.
func TestManualWindowOverridesAClosedSchedule(t *testing.T) {
	fixture := scheduledFixture(t)
	ctx := context.Background()
	// The schedule is on the *next* day, so this instant is unscheduled.
	today := isoToday()
	if _, err := fixture.Service.PutSchedule(ctx, fixture.Admin, fixture.Channel, []recording.ScheduleWindow{
		{Weekday: today%7 + 1, StartMinute: 0, EndMinute: 1440},
	}, channelVersion(t, fixture)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.Service.StartManual(ctx, fixture.Admin, fixture.Channel, 10); err != nil {
		t.Fatal(err)
	}
	if err := fixture.Service.ReconcileObligation(ctx, fixture.Channel); err != nil {
		t.Fatal(err)
	}
	var source string
	var exclusion *string
	if err := fixture.DB.Pool.QueryRow(ctx,
		`SELECT source,exclusion FROM recording_obligations WHERE channel_id=$1 AND ended_at IS NULL ORDER BY started_at DESC LIMIT 1`,
		fixture.Channel).Scan(&source, &exclusion); err != nil {
		t.Fatal(err)
	}
	if source != "manual" || exclusion != nil {
		t.Fatalf("a manual window must keep recording owed outside the schedule: source=%q exclusion=%v", source, exclusion)
	}
}

// Overlapping windows inside one submitted week are rejected, and the stored
// schedule is left untouched: a rejected week must not half-apply.
//
// The overlap lives inside a single PUT because the endpoint replaces the whole
// week — two separate calls could never overlap, since the second replaces the
// first.
func TestOverlappingWindowsAreRejected(t *testing.T) {
	fixture := scheduledFixture(t)
	ctx := context.Background()
	if _, err := fixture.Service.PutSchedule(ctx, fixture.Admin, fixture.Channel, []recording.ScheduleWindow{
		{Weekday: 1, StartMinute: 8 * 60, EndMinute: 12 * 60},
	}, channelVersion(t, fixture)); err != nil {
		t.Fatal(err)
	}
	_, err := fixture.Service.PutSchedule(ctx, fixture.Admin, fixture.Channel, []recording.ScheduleWindow{
		{Weekday: 1, StartMinute: 8 * 60, EndMinute: 12 * 60},
		{Weekday: 1, StartMinute: 11 * 60, EndMinute: 13 * 60},
	}, channelVersion(t, fixture))
	if err == nil {
		t.Fatal("overlapping windows accepted")
	}
	schedule, err := fixture.Service.GetSchedule(ctx, fixture.Admin, fixture.Channel)
	if err != nil {
		t.Fatal(err)
	}
	if len(schedule.Windows) != 1 || schedule.Windows[0].EndMinute != 12*60 {
		t.Fatalf("a rejected week changed the stored schedule: %+v", schedule.Windows)
	}
}

// An overnight window round-trips as one operator-facing window even though it is
// stored as two rows, and it covers both halves of the night.
func TestOvernightWindowRoundTrips(t *testing.T) {
	fixture := scheduledFixture(t)
	ctx := context.Background()
	entered := []recording.ScheduleWindow{
		{Weekday: 1, StartMinute: 22 * 60, EndMinute: 2*60 + 1440},
	}
	if _, err := fixture.Service.PutSchedule(ctx, fixture.Admin, fixture.Channel, entered, channelVersion(t, fixture)); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := fixture.DB.Pool.QueryRow(ctx, "SELECT count(*) FROM recording_schedules WHERE channel_id=$1", fixture.Channel).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 2 {
		t.Fatalf("overnight window must be stored as two rows, got %d", rows)
	}
	schedule, err := fixture.Service.GetSchedule(ctx, fixture.Admin, fixture.Channel)
	if err != nil {
		t.Fatal(err)
	}
	if len(schedule.Windows) != 1 {
		t.Fatalf("overnight window must come back as one window, got %+v", schedule.Windows)
	}
	if schedule.Windows[0].Weekday != 1 || schedule.Windows[0].StartMinute != 22*60 || schedule.Windows[0].EndMinute != 2*60+1440 {
		t.Fatalf("overnight window changed shape: %+v", schedule.Windows[0])
	}
}

// A schedule on a channel whose mode is not planned must not make it record: the
// schedule decides *when* an already-planned channel records, not whether it
// records at all.
func TestScheduleDoesNotRecordForANonPlannedChannel(t *testing.T) {
	fixture := scheduledFixture(t)
	ctx := context.Background()
	if _, err := fixture.DB.Pool.Exec(ctx, "UPDATE recording_policies SET mode='none' WHERE channel_id=$1", fixture.Channel); err != nil {
		t.Fatal(err)
	}
	today := isoToday()
	if _, err := fixture.Service.PutSchedule(ctx, fixture.Admin, fixture.Channel, []recording.ScheduleWindow{
		{Weekday: today, StartMinute: 0, EndMinute: 1440},
	}, channelVersion(t, fixture)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.Service.ReconcileObligation(ctx, fixture.Channel); err != nil {
		t.Fatal(err)
	}
	var exclusion *string
	if err := fixture.DB.Pool.QueryRow(ctx,
		`SELECT exclusion FROM recording_obligations WHERE channel_id=$1 AND ended_at IS NULL ORDER BY started_at DESC LIMIT 1`,
		fixture.Channel).Scan(&exclusion); err != nil {
		t.Fatal(err)
	}
	if exclusion == nil {
		t.Fatal("a schedule on a 'none' channel made recording owed")
	}
}

// An empty week is a valid answer: the channel is planned but never records. It
// must be distinguishable from "no schedule was ever saved" only by intent, not
// by an error.
func TestEmptyScheduleIsValidAndNeverOwed(t *testing.T) {
	fixture := scheduledFixture(t)
	ctx := context.Background()
	if _, err := fixture.Service.PutSchedule(ctx, fixture.Admin, fixture.Channel, nil, channelVersion(t, fixture)); err != nil {
		t.Fatal(err)
	}
	schedule, err := fixture.Service.GetSchedule(ctx, fixture.Admin, fixture.Channel)
	if err != nil {
		t.Fatal(err)
	}
	if len(schedule.Windows) != 0 || schedule.Active {
		t.Fatalf("empty schedule must be inert: %+v", schedule)
	}
	if err := fixture.Service.ReconcileObligation(ctx, fixture.Channel); err != nil {
		t.Fatal(err)
	}
	var exclusion *string
	if err := fixture.DB.Pool.QueryRow(ctx,
		`SELECT exclusion FROM recording_obligations WHERE channel_id=$1 AND ended_at IS NULL ORDER BY started_at DESC LIMIT 1`,
		fixture.Channel).Scan(&exclusion); err != nil {
		t.Fatal(err)
	}
	if exclusion == nil {
		t.Fatal("an empty schedule must not owe recording")
	}
}

// The API constructs a bare recording.Service for its read endpoints (no site id
// private field), so any schedule read that depended on that field returned an
// unexplained 503. This pins the endpoint through the same construction the API
// uses, which is the only way that regression is visible from a test.
func TestScheduleEndpointWorksWithTheAPIBareService(t *testing.T) {
	fixture := scheduledFixture(t)
	ctx := context.Background()
	if _, err := fixture.Service.PutSchedule(ctx, fixture.Admin, fixture.Channel, []recording.ScheduleWindow{
		{Weekday: 1, StartMinute: 8 * 60, EndMinute: 12 * 60},
	}, channelVersion(t, fixture)); err != nil {
		t.Fatal(err)
	}
	bare := &recording.Service{DB: fixture.DB, Auth: fixture.Auth, Pools: fixture.Service.Pools}
	schedule, err := bare.GetSchedule(ctx, fixture.Admin, fixture.Channel)
	if err != nil {
		t.Fatalf("the API's bare service cannot read a schedule: %v", err)
	}
	if schedule.Timezone == "" {
		t.Fatal("schedule returned without the site timezone")
	}
	if len(schedule.Windows) != 1 {
		t.Fatalf("schedule lost its windows: %+v", schedule.Windows)
	}
}

// Writing a schedule through the bare service needs the same shape to keep
// working; the overlap check and the audit entry both live on that path.
func TestScheduleWriteWorksWithTheAPIBareService(t *testing.T) {
	fixture := scheduledFixture(t)
	ctx := context.Background()
	bare := &recording.Service{DB: fixture.DB, Auth: fixture.Auth, Pools: fixture.Service.Pools}
	if _, err := bare.PutSchedule(ctx, fixture.Admin, fixture.Channel, []recording.ScheduleWindow{
		{Weekday: 2, StartMinute: 22 * 60, EndMinute: 2*60 + 1440},
	}, channelVersion(t, fixture)); err != nil {
		t.Fatalf("the API's bare service cannot write a schedule: %v", err)
	}
	var rows int
	if err := fixture.DB.Pool.QueryRow(ctx, "SELECT count(*) FROM recording_schedules WHERE channel_id=$1", fixture.Channel).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 2 {
		t.Fatalf("overnight window was not split into two rows, got %d", rows)
	}
}

// Applying 'planned' must reach the switch machinery. source_switches.desired_mode
// declared only ('none','continuous') while old_mode already accepted 'planned',
// so a mode that could be recorded as *old* could never be requested as *new* —
// the insert was rejected before any work happened.
func TestPlannedModeCanBeAppliedThroughTheSwitch(t *testing.T) {
	// testedSource wires Sources; the shorter fixture does not, and this test is
	// about the switch path rather than about reading a schedule.
	fixture, _, _, _ := testedSource(t)
	ctx := context.Background()
	// Back to none so the planned apply is a real change.
	if _, err := fixture.DB.Pool.Exec(ctx, "UPDATE recording_policies SET mode='none' WHERE channel_id=$1", fixture.Channel); err != nil {
		t.Fatal(err)
	}
	change, err := fixture.Service.Sources.SetPolicy(ctx, fixture.Admin, fixture.Channel, 3, "planned", "plan-apply")
	if err != nil {
		t.Fatalf("applying 'planned' was rejected before reaching the switch: %v", err)
	}
	var desired string
	if err := fixture.DB.Pool.QueryRow(ctx, "SELECT desired_mode FROM source_switches WHERE job_id=$1", change.JobID).Scan(&desired); err != nil {
		t.Fatal(err)
	}
	if desired != "planned" {
		t.Fatalf("switch recorded %q, want planned", desired)
	}
}

// A planned channel inside its window is a reason to run the idle-pool probe.
//
// The probe is the only way a pool's zlm write evidence is refreshed on a site
// where nothing records continuously, and the recorder refuses to start without
// fresh evidence. Leaving `planned` out of the demand predicate therefore made a
// weekly schedule unable to record at all — the same deadlock fbb10d6 fixed for
// manual windows, reintroduced when scheduled recording was added.
func TestPlannedChannelIsAProbeCandidate(t *testing.T) {
	fixture := scheduledFixture(t)
	ctx := context.Background()
	if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE recording_policies SET mode='planned' WHERE channel_id=$1`, fixture.Channel); err != nil {
		t.Fatal(err)
	}
	// A window that covers the whole of today, in the site zone the fixture set.
	today := isoToday()
	if _, err := fixture.Service.PutSchedule(ctx, fixture.Admin, fixture.Channel, []recording.ScheduleWindow{
		{Weekday: today, StartMinute: 0, EndMinute: 1440},
	}, channelVersion(t, fixture)); err != nil {
		t.Fatal(err)
	}
	// Expire whatever evidence exists so the pool only qualifies through demand.
	if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE storage_pool_checks SET expires_at=clock_timestamp()-interval '1 minute' WHERE pool_id=$1`, fixture.Pool.ID); err != nil {
		t.Fatal(err)
	}

	candidates, err := fixture.Service.IdlePoolCandidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, candidate := range candidates {
		if candidate == fixture.Pool.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("a planned channel inside its window did not make its pool a probe candidate: the zlm evidence can never refresh, so scheduled recording can never start")
	}

	// Outside the window it is NOT demand: an empty schedule means no reason to
	// record, and probing then would be work with no purpose.
	if _, err := fixture.Service.PutSchedule(ctx, fixture.Admin, fixture.Channel, nil, channelVersion(t, fixture)); err != nil {
		t.Fatal(err)
	}
	candidates, err = fixture.Service.IdlePoolCandidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range candidates {
		if candidate == fixture.Pool.ID {
			t.Fatal("a planned channel with no window open was still treated as demand")
		}
	}
}

// channelVersion reads the channel's current version, which is the concurrency
// token every channel write shares.
func channelVersion(t *testing.T, fixture publishFixture) int64 {
	t.Helper()
	var version int64
	if err := fixture.DB.Pool.QueryRow(context.Background(),
		"SELECT version FROM channels WHERE id=$1", fixture.Channel).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

// A stale schedule write must be refused, not silently applied.
//
// The schedule is a FULL REPLACE: the writer deletes every row and inserts its
// own. Without a version guard, two operators editing the schedule and the mode at
// the same time erase each other's whole week — not one field, the entire
// schedule — and the second write leaves no trace that anything was lost.
//
// The token is channels.version, the same one every other channel write uses.
func TestScheduleWriteRequiresTheCurrentVersion(t *testing.T) {
	fixture := scheduledFixture(t)
	ctx := context.Background()
	stale := channelVersion(t, fixture)

	// One operator writes and commits.
	today := isoToday()
	if _, err := fixture.Service.PutSchedule(ctx, fixture.Admin, fixture.Channel,
		[]recording.ScheduleWindow{{Weekday: today, StartMinute: 0, EndMinute: 600}}, stale); err != nil {
		t.Fatal(err)
	}

	// A second operator, holding the version they read before that write, tries to
	// replace the week with something else.
	_, err := fixture.Service.PutSchedule(ctx, fixture.Admin, fixture.Channel,
		[]recording.ScheduleWindow{{Weekday: today, StartMinute: 700, EndMinute: 800}}, stale)
	if !errors.Is(err, auth.ErrConflict) {
		t.Fatalf("a stale schedule write returned %v, want a conflict", err)
	}

	// The first operator's week must be intact.
	schedule, err := fixture.Service.GetSchedule(ctx, fixture.Admin, fixture.Channel)
	if err != nil {
		t.Fatal(err)
	}
	if len(schedule.Windows) != 1 || schedule.Windows[0].StartMinute != 0 {
		t.Fatalf("the refused write still changed the schedule: %+v", schedule.Windows)
	}
}
