package integration

// Which detections the event list shows.
//
// A two-second detection with no picture and no footage anywhere has nothing on
// its card — the operator sees a blank tile whose only content is that something
// was briefly seen. Measured on this site: 24 hours held 868 events, 407 of them
// three seconds or shorter, and 103 of those had neither evidence nor a
// neighbour.

import (
	"context"
	"testing"
	"time"

	"github.com/zhigu34/one-nvr/internal/events"
	"github.com/zhigu34/one-nvr/internal/id"
)

func listSources(t *testing.T, service *events.Service, fixture publishFixture) map[string]bool {
	t.Helper()
	page, err := service.List(context.Background(), fixture.Admin, events.ListQuery{
		Start: time.Now().UTC().Add(-24 * time.Hour),
		End:   time.Now().UTC().Add(time.Minute),
		Limit: 200,
	})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, item := range page.Items {
		out[item.SourceID] = true
	}
	return out
}

func insertEvent(t *testing.T, fixture publishFixture, sourceID string, start time.Time, span time.Duration, snapshotPath *string) id.ID {
	t.Helper()
	eventID := mustID(t)
	end := start.Add(span)
	if _, err := fixture.DB.Pool.Exec(context.Background(),
		`INSERT INTO events(id,channel_id,source,source_id,category,score,zones,started_at,ended_at,snapshot_state,snapshot_relative_path,media_state,raw)
		 VALUES($1,$2,'frigate',$3,'person',0.6,'[]',$4,$5,'gone',$6,'unavailable','{}')`,
		eventID, fixture.Channel, sourceID, start, end, snapshotPath); err != nil {
		t.Fatal(err)
	}
	return eventID
}

// A short detection with nothing to show is not listed.
func TestEvidenceFreeSliverIsHidden(t *testing.T) {
	fixture := newNotifyFixture(t)
	now := time.Now().UTC()
	// A two-second detection, no picture, no footage, nothing else nearby.
	insertEvent(t, fixture.fixture, "sliver", now.Add(-10*time.Minute), 2*time.Second, nil)
	// A long detection with no picture is still shown: it lasted long enough to
	// be a visit, and the card can say so.
	insertEvent(t, fixture.fixture, "long-no-shot", now.Add(-20*time.Minute), 30*time.Second, nil)

	seen := listSources(t, fixture.service, fixture.fixture)
	if seen["sliver"] {
		t.Fatal("a 2-second event with no picture and no footage was listed")
	}
	if !seen["long-no-shot"] {
		t.Fatal("a 30-second event was hidden; only evidence-free slivers are hidden")
	}
}

// A short detection that HAS a picture is shown. The rule is about having nothing
// to show, not about being brief.
func TestShortEventWithAPictureIsListed(t *testing.T) {
	fixture := newNotifyFixture(t)
	now := time.Now().UTC()
	path := "snapshots/01/2026-10-22/shot.jpg"
	insertEvent(t, fixture.fixture, "short-with-shot", now.Add(-10*time.Minute), 2*time.Second, &path)

	if !listSources(t, fixture.service, fixture.fixture)["short-with-shot"] {
		t.Fatal("a 2-second event WITH a picture was hidden; the operator can see something")
	}
}

// A short detection covered by footage is shown: the operator can jump to it.
func TestShortEventWithFootageIsListed(t *testing.T) {
	fixture := newNotifyFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	start := now.Add(-10 * time.Minute).Truncate(time.Second)
	insertEvent(t, fixture.fixture, "short-with-clip", start, 2*time.Second, nil)

	// Footage covering that moment on the same channel.
	segmentID := mustID(t)
	path := "recordings/" + string(segmentID) + ".mp4"
	if _, err := fixture.fixture.DB.Pool.Exec(ctx, `INSERT INTO recording_segments(id,channel_id,source_revision_id,run_id,pool_id,
		original_relative_path,target_relative_path,original_start,naming_timezone,utc_offset_seconds,start_at,end_at,size_bytes,file_identity,state)
		VALUES($1,$2,$3,$4,$5,$6,$6,$7,'UTC',0,$7,$8,1024,'{}','ready')`,
		segmentID, fixture.fixture.Channel, fixture.fixture.Revision, fixture.fixture.Run, fixture.fixture.Pool.ID, path,
		start.Add(-30*time.Second), start.Add(60*time.Second)); err != nil {
		t.Fatal(err)
	}

	if !listSources(t, fixture.service, fixture.fixture)["short-with-clip"] {
		t.Fatal("a 2-second event covered by footage was hidden; the operator can play it")
	}
}

// The count the page reports and the rows it renders come from the same query, so
// a hidden event cannot inflate the total.
func TestHiddenEventsDoNotInflateThePage(t *testing.T) {
	fixture := newNotifyFixture(t)
	now := time.Now().UTC()
	for i := 0; i < 5; i++ {
		insertEvent(t, fixture.fixture, "sliver-"+string(rune('a'+i)), now.Add(-time.Duration(10+i)*time.Minute), 1*time.Second, nil)
	}
	insertEvent(t, fixture.fixture, "real", now.Add(-30*time.Minute), 20*time.Second, nil)

	page, err := fixture.service.List(context.Background(), fixture.fixture.Admin, events.ListQuery{
		Start: now.Add(-24 * time.Hour), End: now.Add(time.Minute), Limit: 200,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].SourceID != "real" {
		t.Fatalf("the page holds %d events, want only the real one", len(page.Items))
	}
}

// A running event is never hidden, however short its elapsed time looks.
//
// The filter computes a duration; a running event has no end, and
// COALESCE(ended_at, started_at) would make every live event look zero-length. It
// also has no picture yet by definition, so the naive predicate hid exactly the
// events an operator most needs to see.
func TestRunningEventIsNeverHidden(t *testing.T) {
	fixture := newNotifyFixture(t)
	now := time.Now().UTC()
	// Started two seconds ago and still running.
	running := mustID(t)
	if _, err := fixture.fixture.DB.Pool.Exec(context.Background(),
		`INSERT INTO events(id,channel_id,source,source_id,category,score,zones,started_at,ended_at,snapshot_state,media_state,raw)
		 VALUES($1,$2,'frigate','live-now','person',0.6,'[]',$3,NULL,'pending','pending','{}')`,
		running, fixture.fixture.Channel, now.Add(-2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if !listSources(t, fixture.service, fixture.fixture)["live-now"] {
		t.Fatal("a running event was hidden as an evidence-free sliver")
	}
}
