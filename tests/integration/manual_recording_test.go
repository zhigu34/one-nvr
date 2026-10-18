package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zhigu34/one-nvr/internal/httpapi"
	"github.com/zhigu34/one-nvr/internal/recording"
)

// REC-03: a manual recording is a bounded window. Starting one must not change
// the channel's configured mode — it is a request, not a setting — and it must
// survive a policy that says 'none'.
func TestManualWindowDoesNotChangePolicy(t *testing.T) {
	fixture := newPublicationFixture(t)
	ctx := context.Background()
	service := fixture.Service

	window, err := service.StartManual(ctx, fixture.Admin, fixture.Channel, 30)
	if err != nil {
		t.Fatal(err)
	}
	if window.RequestedMinutes != 30 || window.RemainingMS <= 0 {
		t.Fatalf("window did not open as requested: %+v", window)
	}

	var mode string
	if err := fixture.DB.Pool.QueryRow(ctx,
		"SELECT mode FROM recording_policies WHERE channel_id=$1", fixture.Channel).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode == "manual" {
		t.Fatal("the policy mode was rewritten by a manual recording; the window " +
			"is an event and the policy is a setting, and REC-04 requires the " +
			"recording to survive an edit made while it runs")
	}
}

// The scheduler's union (REC-04): a channel whose policy is none must still be
// recording while a manual window is open.
func TestManualWindowMakesRecordingOwed(t *testing.T) {
	fixture := newPublicationFixture(t)
	ctx := context.Background()
	service := fixture.Service
	// The fixture registers a revision but does not apply it; an obligation for
	// an unapplied channel is an exclusion by design, so bind it first.
	if _, err := fixture.DB.Pool.Exec(ctx, "UPDATE channels SET current_revision_id=$2 WHERE id=$1", fixture.Channel, fixture.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, "UPDATE recording_policies SET mode='none' WHERE channel_id=$1", fixture.Channel); err != nil {
		t.Fatal(err)
	}
	// Settle the pre-existing obligation first: the fold closes the open
	// interval and opens a new one only when the verdict changes, so the row
	// this test reads must be the one produced after the manual window opened.
	if err := service.ReconcileObligation(ctx, fixture.Channel); err != nil {
		t.Fatal(err)
	}
	if _, err := service.StartManual(ctx, fixture.Admin, fixture.Channel, 10); err != nil {
		t.Fatal(err)
	}
	if err := service.ReconcileObligation(ctx, fixture.Channel); err != nil {
		t.Fatal(err)
	}
	var source string
	var exclusion *string
	if err := fixture.DB.Pool.QueryRow(ctx,
		`SELECT source,exclusion FROM recording_obligations
		 WHERE channel_id=$1 AND ended_at IS NULL`, fixture.Channel).Scan(&source, &exclusion); err != nil {
		t.Fatal(err)
	}
	if source != "manual" || exclusion != nil {
		t.Fatalf("obligation source=%q exclusion=%v while a manual window is open; "+
			"the interval must be owed, otherwise a failed manual recording hides "+
			"inside an exclusion and the channel reports as fully covered",
			source, exclusion)
	}
}

// A continuous policy wins the union, but the obligation is owed once: two
// reasons to record do not mean two recordings.
func TestContinuousAndManualOweOneInterval(t *testing.T) {
	fixture := newPublicationFixture(t)
	ctx := context.Background()
	service := fixture.Service
	if _, err := fixture.DB.Pool.Exec(ctx, "UPDATE channels SET current_revision_id=$2 WHERE id=$1", fixture.Channel, fixture.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, "UPDATE recording_policies SET mode='continuous' WHERE channel_id=$1", fixture.Channel); err != nil {
		t.Fatal(err)
	}
	if _, err := service.StartManual(ctx, fixture.Admin, fixture.Channel, 5); err != nil {
		t.Fatal(err)
	}
	if err := service.ReconcileObligation(ctx, fixture.Channel); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := fixture.DB.Pool.QueryRow(ctx,
		`SELECT count(*) FROM recording_obligations
		 WHERE channel_id=$1 AND ended_at IS NULL`, fixture.Channel).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("open obligation intervals = %d with both continuous and manual "+
			"active, want 1: the union is a boolean, not a sum", count)
	}
}

// Pressing record twice replaces the deadline; it must not stack windows.
func TestManualStartReplacesRunningWindow(t *testing.T) {
	fixture := newPublicationFixture(t)
	ctx := context.Background()
	service := fixture.Service
	first, err := service.StartManual(ctx, fixture.Admin, fixture.Channel, 60)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.StartManual(ctx, fixture.Admin, fixture.Channel, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !second.ExpiresAt.Before(first.ExpiresAt) {
		t.Fatalf("the second window did not replace the first: %v then %v",
			first.ExpiresAt, second.ExpiresAt)
	}
	var active int
	if err := fixture.DB.Pool.QueryRow(ctx,
		"SELECT count(*) FROM recording_manual_windows WHERE channel_id=$1 AND ended_at IS NULL",
		fixture.Channel).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 1 {
		t.Fatalf("active windows = %d after pressing record twice, want 1", active)
	}
}

// Stopping is idempotent: the desired end state is "not manually recording".
func TestManualStopIsIdempotent(t *testing.T) {
	fixture := newPublicationFixture(t)
	ctx := context.Background()
	service := fixture.Service
	if _, err := service.StartManual(ctx, fixture.Admin, fixture.Channel, 5); err != nil {
		t.Fatal(err)
	}
	if err := service.StopManual(ctx, fixture.Admin, fixture.Channel); err != nil {
		t.Fatal(err)
	}
	if err := service.StopManual(ctx, fixture.Admin, fixture.Channel); err != nil {
		t.Fatalf("stopping an already stopped window failed: %v", err)
	}
}

// The sweep closes expired windows so the audit trail does not show a window
// running forever. The scheduler already ignores them; this is the record.
func TestManualSweepExpiresWindows(t *testing.T) {
	fixture := newPublicationFixture(t)
	ctx := context.Background()
	service := fixture.Service
	if _, err := service.StartManual(ctx, fixture.Admin, fixture.Channel, 1); err != nil {
		t.Fatal(err)
	}
	// Move the whole window into the past rather than waiting a minute. Both
	// timestamps move so the CHECK (expires_at > started_at) still holds: a
	// window that ended before it began is not a state the schema allows, and
	// weakening the constraint to make a test easier would remove a real
	// invariant.
	if _, err := fixture.DB.Pool.Exec(ctx,
		`UPDATE recording_manual_windows
		 SET started_at=clock_timestamp()-interval '2 minutes',
		     expires_at=clock_timestamp()-interval '1 minute'
		 WHERE channel_id=$1`, fixture.Channel); err != nil {
		t.Fatal(err)
	}
	service.SweepManualWindows(ctx)
	var reason *string
	var ended *time.Time
	if err := fixture.DB.Pool.QueryRow(ctx,
		"SELECT end_reason,ended_at FROM recording_manual_windows WHERE channel_id=$1",
		fixture.Channel).Scan(&reason, &ended); err != nil {
		t.Fatal(err)
	}
	if reason == nil || *reason != "expired" || ended == nil {
		t.Fatalf("expired window kept end_reason=%v ended_at=%v", reason, ended)
	}
}

// The PRD bounds the request at 1..120 minutes; zero means the 30-minute default.
func TestManualMinutesAreBounded(t *testing.T) {
	fixture := newPublicationFixture(t)
	ctx := context.Background()
	service := fixture.Service
	window, err := service.StartManual(ctx, fixture.Admin, fixture.Channel, 0)
	if err != nil {
		t.Fatal(err)
	}
	if window.RequestedMinutes != recording.ManualDefaultMinutes {
		t.Fatalf("omitted minutes gave %d, want the %d-minute default",
			window.RequestedMinutes, recording.ManualDefaultMinutes)
	}
	for _, minutes := range []int{-1, recording.ManualMaxMinutes + 1} {
		if _, err := service.StartManual(ctx, fixture.Admin, fixture.Channel, minutes); err == nil {
			t.Fatalf("%d minutes was accepted", minutes)
		}
	}
}

// The endpoint contract: a channel with no running window answers 404, not
// "200 null". A null body would be indistinguishable from a client bug, and the
// UI reads the code 'manual_recording_absent' to decide "idle" — collapsing the
// two would either paint an error on every idle channel or invite a blind write
// against one whose state could not be read.
func TestManualRecordingHTTPReportsAbsenceAs404(t *testing.T) {
	fixture := newPublicationFixture(t)
	handler := httpapi.NewHandler(httpapi.Dependencies{
		Auth: fixture.Auth, Site: fixture.Site, Recordings: fixture.Service,
		PublicURL: "http://one-nvr.test:8080",
	})
	cookie := &http.Cookie{Name: "one_nvr_session", Value: fixture.Session}
	get := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET",
			"http://one-nvr.test:8080/api/v1/channels/"+string(fixture.Channel)+"/manual-recording", nil)
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	absent := get()
	if absent.Code != 404 {
		t.Fatalf("no window must be 404, got %d %s", absent.Code, absent.Body.String())
	}
	if !strings.Contains(absent.Body.String(), "manual_recording_absent") {
		t.Fatalf("absence must carry its own code, got %s", absent.Body.String())
	}
	// Starting one makes the same GET answer with the window.
	if _, err := fixture.Service.StartManual(context.Background(), fixture.Admin, fixture.Channel, 5); err != nil {
		t.Fatal(err)
	}
	running := get()
	if running.Code != 200 || !strings.Contains(running.Body.String(), "remaining_ms") {
		t.Fatalf("running window not served: %d %s", running.Code, running.Body.String())
	}
}

// The idle-pool probe is what keeps the zlm write proof fresh, and starting any
// recording requires that proof to be fresh. Its candidate query used to look
// only for `mode='continuous'`, so on a site where nothing records continuously
// a manual window could never start: the recorder refused with
// zlm_write_evidence_unavailable and no loop would ever produce the evidence
// that refusal demanded. This pins the union into the probe's candidates.
func TestManualWindowKeepsPoolEvidenceEligibleForProbe(t *testing.T) {
	f := newPublicationFixture(t)
	ctx := context.Background()
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE sites SET require_storage_write_proof=true WHERE singleton"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE channels SET current_revision_id=$2,storage_pool_id=$3 WHERE id=$1", f.Channel, f.Revision, f.Pool.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE recording_policies SET mode='none' WHERE channel_id=$1", f.Channel); err != nil {
		t.Fatal(err)
	}
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE recording_runs SET state='stopped' WHERE id=$1", f.Run); err != nil {
		t.Fatal(err)
	}
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE storage_pool_checks SET expires_at=clock_timestamp()-interval '1 second' WHERE pool_id=$1 AND service='zlm'", f.Pool.ID); err != nil {
		t.Fatal(err)
	}
	// The candidate scan is the decision point: a pool it does not name never
	// gets probed, so the zlm write proof stays stale and every start is refused.
	candidates, err := f.Service.IdlePoolCandidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, pool := range candidates {
		if pool == f.Pool.ID {
			t.Fatal("an idle pool with no recording demand must not be probed: that " +
				"would write a probe recording to it every ten seconds forever")
		}
	}
	if _, err := f.Service.StartManual(ctx, f.Admin, f.Channel, 5); err != nil {
		t.Fatal(err)
	}
	candidates, err = f.Service.IdlePoolCandidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, pool := range candidates {
		if pool == f.Pool.ID {
			return
		}
	}
	t.Fatal("a manual window is recording demand: without a probe here the zlm " +
		"write proof never refreshes and the recording can never start")
}
