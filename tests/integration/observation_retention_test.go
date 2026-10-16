package integration

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zhigu34/one-nvr/internal/channel"
	"github.com/zhigu34/one-nvr/internal/httpapi"

	"github.com/zhigu34/one-nvr/internal/id"
)

// Observations are current state, not history: the writer runs every ten
// seconds per channel and nothing ever removed the old rows, so the table grew
// without bound. The sweep must remove what is past its usefulness.
func TestObservationSweepRemovesExpiredRows(t *testing.T) {
	fixture := newPublicationFixture(t)
	ctx := context.Background()

	// One fresh observation and one well past the retention window, both for the
	// same channel and kind.
	fresh, _ := id.New()
	stale, _ := id.New()
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO source_observations(id,channel_id,source_revision_id,kind,state,reason_code,observed_at,expires_at)
		VALUES($1,$2,$3,'main','healthy','ok',clock_timestamp(),clock_timestamp()+interval '30 seconds')`,
		fresh, fixture.Channel, fixture.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO source_observations(id,channel_id,source_revision_id,kind,state,reason_code,observed_at,expires_at)
		VALUES($1,$2,$3,'sub','healthy','ok',clock_timestamp()-interval '3 days',clock_timestamp()-interval '3 days'+interval '30 seconds')`,
		stale, fixture.Channel, fixture.Revision); err != nil {
		t.Fatal(err)
	}

	fixture.Service.SweepObservations(ctx)

	var survivors []id.ID
	rows, err := fixture.DB.Pool.Query(ctx, `SELECT id FROM source_observations WHERE channel_id=$1`, fixture.Channel)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var item id.ID
		if err := rows.Scan(&item); err != nil {
			t.Fatal(err)
		}
		survivors = append(survivors, item)
	}
	if len(survivors) != 1 || survivors[0] != fresh {
		t.Fatalf("the sweep must keep the fresh row and drop the expired one, got %v", survivors)
	}
}

// The sweep must not change what the UI reports: a kind whose rows are all gone
// reads as unobserved, never as healthy. That is what makes deleting them safe.
func TestObservationSweepDoesNotReportHealthFromNothing(t *testing.T) {
	fixture := newPublicationFixture(t)
	ctx := context.Background()

	// Only a stale row exists for main.
	stale, _ := id.New()
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO source_observations(id,channel_id,source_revision_id,kind,state,reason_code,observed_at,expires_at)
		VALUES($1,$2,$3,'main','healthy','ok',clock_timestamp()-interval '3 days',clock_timestamp()-interval '3 days'+interval '30 seconds')`,
		stale, fixture.Channel, fixture.Revision); err != nil {
		t.Fatal(err)
	}

	fixture.Service.SweepObservations(ctx)

	// The publish fixture only wires what publishing needs, so the status
	// reader is attached here; the sweep under test does not depend on it.
	network, _ := channel.ParseNetworkPolicy("192.168.33.0/24", nil)
	fixture.Service.Sources = channel.NewSources(fixture.DB, fixture.Auth, fixture.Site.Secrets, network)
	status, err := fixture.Service.Sources.GetStatus(ctx, fixture.Admin, fixture.Channel)
	if err != nil {
		t.Fatal(err)
	}
	// The point is that the sweep cannot manufacture health: with no rows left
	// the state is derived from what the channel actually is. This fixture has
	// no applied revision, so "not configured" is the honest answer — and it is
	// certainly not "healthy".
	if status.Main.State == "healthy" {
		t.Fatal("with no observations left the state must not read as healthy")
	}
	if status.Main.State != "not_configured" {
		t.Fatalf("an unconfigured channel reads as not_configured, got %q", status.Main.State)
	}
}

// A sweep that cannot keep up must still be bounded: one pass deletes at most
// the batch size, so the first run after upgrade does not hold locks on months
// of backlog. This crosses the batch boundary deliberately.
func TestObservationSweepDrainsInBatches(t *testing.T) {
	fixture := newPublicationFixture(t)
	ctx := context.Background()

	const total = 220 // just past two batches of 200
	for i := 0; i < total; i++ {
		row, _ := id.New()
		if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO source_observations(id,channel_id,source_revision_id,kind,state,reason_code,observed_at,expires_at)
			VALUES($1,$2,$3,'main','healthy','ok',clock_timestamp()-interval '3 days',clock_timestamp()-interval '3 days'+interval '30 seconds')`,
			row, fixture.Channel, fixture.Revision); err != nil {
			t.Fatal(err)
		}
	}

	fixture.Service.SweepObservations(ctx)

	var remaining int
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM source_observations WHERE channel_id=$1`, fixture.Channel).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	// The sweep loops until a partial batch, so a backlog inside one call is
	// drained completely rather than one batch per tick.
	if remaining != 0 {
		t.Fatalf("the sweep must drain its backlog, %d rows left", remaining)
	}
}

// PRD §14: with ONE_NVR_OPENLIST_ENABLE=no the cloud module is stopped and every
// cloud action is paused. Hiding the page is not enough — the API must refuse
// too, or a deployment with the module off can still register targets and
// upload.
func TestArchiveEndpointsAreClosedWhenTheModuleIsOff(t *testing.T) {
	_, accounts, sites, admin := authFixture(t)
	// No archive service is wired, exactly as the assembly does when the switch
	// is off.
	h := httpapi.NewHandler(httpapi.Dependencies{Auth: accounts, Site: sites, PublicURL: "http://nvr.example.com", OpenListEnabled: false})

	paths := []struct{ method, path string }{
		{"GET", "/api/v1/archive-targets"},
		{"POST", "/api/v1/archive-targets"},
		{"PATCH", "/api/v1/archive-targets/00000000-0000-4000-8000-000000000001"},
		{"DELETE", "/api/v1/archive-targets/00000000-0000-4000-8000-000000000001"},
		{"POST", "/api/v1/archive-targets/test"},
		{"POST", "/api/v1/recordings/archive"},
		{"GET", "/api/v1/archive-policies"},
		{"PUT", "/api/v1/archive-policies"},
	}
	for _, entry := range paths {
		body := bytes.NewBufferString(`{}`)
		r := httptest.NewRequest(entry.method, "http://nvr.example.com"+entry.path, body)
		r.AddCookie(&http.Cookie{Name: "one_nvr_session", Value: admin.RawSession})
		r.Header.Set("Origin", "http://nvr.example.com")
		r.Header.Set("X-CSRF-Token", accounts.CSRF(admin.RawSession))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		// Closed means "there is no such thing here", the same answer the
		// detection endpoints give when their module is off.
		if w.Code != 404 {
			t.Fatalf("%s %s: a stopped module must not accept archive work, got %d %s",
				entry.method, entry.path, w.Code, w.Body.String())
		}
	}
}
