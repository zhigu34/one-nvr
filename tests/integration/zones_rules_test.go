package integration

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/zhigu34/one-nvr/internal/detection"
	"github.com/zhigu34/one-nvr/internal/events"
	"github.com/zhigu34/one-nvr/internal/fault"
	"github.com/zhigu34/one-nvr/internal/id"
)

func TestZoneDefinitionAndRendering(t *testing.T) {
	fixture, service := newEventsFixture(t)
	ctx := context.Background()
	zones := detection.New(fixture.DB, fixture.Auth, "", "/dev/null")

	zone, err := zones.UpsertZone(ctx, fixture.Admin, detection.Zone{
		ChannelID: fixture.Channel, Name: "门口",
		Coordinates: []detection.Point{{X: 0.1, Y: 0.1}, {X: 0.4, Y: 0.1}, {X: 0.4, Y: 0.4}, {X: 0.1, Y: 0.4}},
		Objects:     []string{"person"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if zone.ID == "" || zone.NeedsCalibration {
		t.Fatal("a freshly drawn zone needs no calibration", zone)
	}
	// The reference geometry is read from the channel's own source test, never
	// taken from the request: the drawing claims to describe what the detector
	// analyzes, and the server is the side that knows what that is.
	var storedWidth, storedHeight int
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT reference_width,reference_height FROM channel_zones WHERE id=$1`, zone.ID).Scan(&storedWidth, &storedHeight); err != nil {
		t.Fatal(err)
	}
	if storedWidth != 0 || storedHeight != 0 {
		t.Fatalf("a channel without a successful test stores an unknown geometry, got %dx%d", storedWidth, storedHeight)
	}
	// A self-intersecting drawing is refused: it would make the detector's zone
	// semantics meaningless.
	_, err = zones.UpsertZone(ctx, fixture.Admin, detection.Zone{
		ChannelID: fixture.Channel, Name: "蝴蝶结",
		Coordinates: []detection.Point{{X: 0.1, Y: 0.1}, {X: 0.4, Y: 0.4}, {X: 0.4, Y: 0.1}, {X: 0.1, Y: 0.4}},
	})
	if err == nil {
		t.Fatal("a crossing polygon must be refused")
	}
	// A rule bound to the zone must exist before evaluation can match it.
	if _, err := service.UpsertRule(ctx, fixture.Admin, events.Rule{
		ChannelID: fixture.Channel, Name: "门口进入", Kind: "enter", ZoneID: &zone.ID,
		Categories: []string{"person"}, Severity: "high", Cooldown: 60, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := zones.DeleteZone(ctx, fixture.Admin, fixture.Channel, zone.ID); err != nil {
		t.Fatal(err)
	}
	// Deleting the zone cascades to the rule that depended on it: a rule
	// without its zone can never match again.
	var rules int
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM detection_rules`).Scan(&rules); err != nil || rules != 0 {
		t.Fatal("deleting a zone must remove the rules bound to it", rules, err)
	}
}

// A drawing survives any resolution change — Frigate stretches the source into
// its own fixed frame — but not a change of the source's aspect ratio, which
// moves the view under the same normalized coordinates.
func TestZoneCalibrationFollowsAspectRatio(t *testing.T) {
	fixture, _ := newEventsFixture(t)
	ctx := context.Background()
	zones := detection.New(fixture.DB, fixture.Auth, "", "/dev/null")
	seedSourceTest(t, fixture, ctx, 1920, 1080)

	zone, err := zones.UpsertZone(ctx, fixture.Admin, detection.Zone{
		ChannelID: fixture.Channel, Name: "门口",
		Coordinates: []detection.Point{{X: 0.1, Y: 0.1}, {X: 0.4, Y: 0.1}, {X: 0.4, Y: 0.4}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if zone.ReferenceWidth != 1920 || zone.ReferenceHeight != 1080 {
		t.Fatalf("the reference must come from the channel's source test, got %dx%d", zone.ReferenceWidth, zone.ReferenceHeight)
	}
	if zone.NeedsCalibration {
		t.Fatal("a drawing that matches the analyzed stream is not stale")
	}
	// Halving the resolution keeps the view: the detector scales it back.
	seedSourceTest(t, fixture, ctx, 960, 540)
	listed, err := zones.ListZones(ctx, fixture.Admin, fixture.Channel)
	if err != nil || len(listed) != 1 {
		t.Fatal(listed, err)
	}
	if listed[0].NeedsCalibration {
		t.Fatal("a pure resolution change is not drift")
	}
	// Changing the shape moves the view under the drawing.
	seedSourceTest(t, fixture, ctx, 704, 480)
	listed, err = zones.ListZones(ctx, fixture.Admin, fixture.Channel)
	if err != nil || len(listed) != 1 {
		t.Fatal(listed, err)
	}
	if !listed[0].NeedsCalibration {
		t.Fatal("a changed aspect ratio must flag the drawing for recalibration")
	}
}

// seedSourceTest records one successful source test for the fixture channel
// with the given reported geometry, which is what the calibration check reads.
func seedSourceTest(t *testing.T, fixture publishFixture, ctx context.Context, width, height int) {
	t.Helper()
	job, _ := id.New()
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO jobs(id,kind,object_id,idempotency_key,parameter_digest,state) VALUES($1,'source.test',$2,$3,decode(repeat('11',32),'hex'),'succeeded')`, job, fixture.Channel, "seed-"+string(job)); err != nil {
		t.Fatal(err)
	}
	stream := `{"state":"healthy","first_frame":true,"codec":"h264","width":` + strconv.Itoa(width) + `,"height":` + strconv.Itoa(height) + `}`
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO source_tests(id,channel_id,revision_id,job_id,state,configuration_digest,result,observed_at,expires_at,created_by)
		VALUES($1,$2,$3,$4,'succeeded',decode(repeat('11',32),'hex'),('{"main":`+stream+`,"sub":`+stream+`}')::jsonb,clock_timestamp(),clock_timestamp()+interval '5 minutes',$5)`,
		job, fixture.Channel, fixture.Revision, job, fixture.Admin.UserID); err != nil {
		t.Fatal(err)
	}
}

// The zone canvas draws on a still from the detector itself, so the polygon the
// operator draws is relative to the pixels the detector classifies. A channel
// the detector does not watch has no such frame.
func TestDetectionFrameReflectsDetectorState(t *testing.T) {
	fixture, _ := newEventsFixture(t)
	ctx := context.Background()
	var requested string
	detector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = r.URL.Path + "?" + r.URL.RawQuery
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("frame-bytes"))
	}))
	defer detector.Close()
	service := detection.New(fixture.DB, fixture.Auth, detector.URL, "/dev/null")

	// Detection is not enabled for this channel, so the detector has no camera
	// for it and there is nothing honest to show.
	_, _, err := service.Frame(ctx, fixture.Admin, fixture.Channel)
	var refused *fault.Error
	if !errors.As(err, &refused) || refused.Code != "detection_frame_not_ready" {
		t.Fatal("a channel without enabled detection must not produce a frame", err)
	}
	if _, err := service.Upsert(ctx, fixture.Admin, detection.Config{
		ChannelID: fixture.Channel, Enabled: true, Stream: "sub", FPS: 5, Labels: []string{"person"},
	}); err != nil {
		t.Fatal(err)
	}
	data, contentType, err := service.Frame(ctx, fixture.Admin, fixture.Channel)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "frame-bytes" || contentType != "image/jpeg" {
		t.Fatal("the frame bytes must pass through unchanged", string(data), contentType)
	}
	// The still is requested at the detector's own frame height, because that
	// frame is the coordinate space the zone coordinates are normalized in.
	if requested != "/api/ch01/latest.jpg?height=360" {
		t.Fatalf("the still must be the detector's own frame, requested %q", requested)
	}
}

func TestRuleMatchingAndDeduplication(t *testing.T) {
	fixture, service := newEventsFixture(t)
	ctx := context.Background()
	zones := detection.New(fixture.DB, fixture.Auth, "", "/dev/null")
	if err := service.SetMappings(ctx, fixture.Admin, map[id.ID]string{fixture.Channel: "front-door"}); err != nil {
		t.Fatal(err)
	}
	zone, err := zones.UpsertZone(ctx, fixture.Admin, detection.Zone{
		ChannelID: fixture.Channel, Name: "门口",
		Coordinates: []detection.Point{{X: 0.1, Y: 0.1}, {X: 0.4, Y: 0.1}, {X: 0.4, Y: 0.4}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// One rule that fires on any person, one that needs the zone, and one that
	// is disabled — only the first two may produce hits.
	anyPerson, err := service.UpsertRule(ctx, fixture.Admin, events.Rule{
		ChannelID: fixture.Channel, Name: "任何人", Kind: "appear",
		Categories: []string{"person"}, Severity: "medium", Cooldown: 60, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpsertRule(ctx, fixture.Admin, events.Rule{
		ChannelID: fixture.Channel, Name: "门口进入", Kind: "enter", ZoneID: &zone.ID,
		Categories: []string{"person"}, Severity: "high", Cooldown: 60, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpsertRule(ctx, fixture.Admin, events.Rule{
		ChannelID: fixture.Channel, Name: "已停用", Kind: "appear",
		Categories: []string{"person"}, Severity: "low", Cooldown: 60, Enabled: false,
	}); err != nil {
		t.Fatal(err)
	}
	started := float64(time.Now().UTC().Add(-time.Minute).Unix())
	// A person inside the zone matches both enabled rules.
	if err := service.Ingest(ctx, frigatePayload("new", "rule-1", "front-door", "person", started, nil, []string{"门口"}, false)); err != nil {
		t.Fatal(err)
	}
	var eventID id.ID
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT id FROM events WHERE source_id='rule-1'`).Scan(&eventID); err != nil {
		t.Fatal(err)
	}
	hits, err := service.HitsForEvent(ctx, eventID)
	if err != nil || len(hits) != 2 {
		t.Fatal("the appear rule and the zone rule must both match; the disabled one must not", hits, err)
	}
	// A person the detector did not place in the zone matches only the appear
	// rule: zone matching uses what the detector reported, never geometry.
	if err := service.Ingest(ctx, frigatePayload("new", "rule-2", "front-door", "person", started, nil, nil, false)); err != nil {
		t.Fatal(err)
	}
	var otherID id.ID
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT id FROM events WHERE source_id='rule-2'`).Scan(&otherID); err != nil {
		t.Fatal(err)
	}
	otherHits, err := service.HitsForEvent(ctx, otherID)
	if err != nil || len(otherHits) != 1 || otherHits[0].RuleID != anyPerson.ID {
		t.Fatal("without the zone in entered_zones only the appearance rule matches", otherHits, err)
	}
	// AI-05: an update of the same upstream event refreshes the hit instead of
	// adding another.
	ended := started + 5
	if err := service.Ingest(ctx, frigatePayload("update", "rule-1", "front-door", "person", started, &ended, []string{"门口"}, true)); err != nil {
		t.Fatal(err)
	}
	hits, err = service.HitsForEvent(ctx, eventID)
	if err != nil || len(hits) != 2 {
		t.Fatal("an event update must not create a second hit for the same rule", hits, err)
	}
	// Feedback is recorded on the hit and keeps its original reason.
	if err := service.MarkFalsePositive(ctx, fixture.Admin, hits[0].ID, "是路灯", true); err != nil {
		t.Fatal(err)
	}
	hits, err = service.HitsForEvent(ctx, eventID)
	if err != nil {
		t.Fatal(err)
	}
	if !hits[0].FalsePositive || hits[0].Note != "是路灯" || len(hits[0].Reason) == 0 {
		t.Fatal("the false-positive mark must persist alongside the original reason", hits[0])
	}
}

func TestRuleRefusesUnusableConditions(t *testing.T) {
	fixture, service := newEventsFixture(t)
	ctx := context.Background()
	base := events.Rule{ChannelID: fixture.Channel, Name: "r", Categories: []string{"person"}, Severity: "low", Enabled: true}
	// A zone rule without a zone, a dwell rule without a threshold, an
	// impossible arming window and an unknown severity must all be refused:
	// storing them would create rules that silently never fire.
	cases := map[string]events.Rule{
		"enter without zone": func() events.Rule { r := base; r.Kind = "enter"; return r }(),
		"dwell without seconds": func() events.Rule {
			zone, err := detection.New(fixture.DB, fixture.Auth, "", "/dev/null").UpsertZone(ctx, fixture.Admin, detection.Zone{
				ChannelID: fixture.Channel, Name: "z", Coordinates: []detection.Point{{X: 0, Y: 0}, {X: 1, Y: 0}, {X: 0, Y: 1}},
			})
			if err != nil {
				t.Fatal(err)
			}
			r := base
			r.Kind = "dwell"
			r.ZoneID = &zone.ID
			return r
		}(),
		"half an arming window": func() events.Rule { r := base; r.Kind = "appear"; r.ArmFrom = "09:00"; return r }(),
		"bad clock":             func() events.Rule { r := base; r.Kind = "appear"; r.ArmFrom = "9:00"; r.ArmTo = "17:00"; return r }(),
		"unknown severity":      func() events.Rule { r := base; r.Kind = "appear"; r.Severity = "critical"; return r }(),
		"no categories":         func() events.Rule { r := base; r.Kind = "appear"; r.Categories = nil; return r }(),
	}
	for name, rule := range cases {
		if _, err := service.UpsertRule(ctx, fixture.Admin, rule); err == nil {
			t.Fatalf("%s must be refused", name)
		}
	}
}

// The detector holds one dwell threshold per zone and gates both "present" and
// "entered" on it. The rule layer must keep that constraint visible by refusing
// combinations it cannot express, instead of storing a rule that quietly means
// something else.
func TestDwellAndEntryCannotShareAZone(t *testing.T) {
	fixture, service := newEventsFixture(t)
	ctx := context.Background()
	zones := detection.New(fixture.DB, fixture.Auth, "", "/dev/null")
	zone, err := zones.UpsertZone(ctx, fixture.Admin, detection.Zone{
		ChannelID: fixture.Channel, Name: "门口",
		Coordinates: []detection.Point{{X: 0.1, Y: 0.1}, {X: 0.4, Y: 0.1}, {X: 0.4, Y: 0.4}},
	})
	if err != nil {
		t.Fatal(err)
	}
	seconds := 30
	dwell, err := service.UpsertRule(ctx, fixture.Admin, events.Rule{
		ChannelID: fixture.Channel, Name: "门口逗留", Kind: "dwell", ZoneID: &zone.ID,
		Categories: []string{"person"}, DwellSecond: &seconds, Severity: "medium", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// An entry rule on the same zone would be delayed by the dwell threshold,
	// so it is refused rather than silently retimed.
	_, err = service.UpsertRule(ctx, fixture.Admin, events.Rule{
		ChannelID: fixture.Channel, Name: "门口进入", Kind: "enter", ZoneID: &zone.ID,
		Categories: []string{"person"}, Severity: "high", Enabled: true,
	})
	var refused *fault.Error
	if !errors.As(err, &refused) || refused.Code != "rule_enter_on_dwell_zone" {
		t.Fatal("an entry rule on a dwell zone must be refused", err)
	}
	// A different dwell threshold on the same zone cannot be expressed either:
	// the detector keeps one.
	other := 60
	_, err = service.UpsertRule(ctx, fixture.Admin, events.Rule{
		ChannelID: fixture.Channel, Name: "门口久留", Kind: "dwell", ZoneID: &zone.ID,
		Categories: []string{"person"}, DwellSecond: &other, Severity: "medium", Enabled: true,
	})
	if !errors.As(err, &refused) || refused.Code != "rule_dwell_conflict" {
		t.Fatal("a second dwell threshold on one zone must be refused", err)
	}
	// Updating the same rule with the same threshold is not a conflict with
	// itself.
	renamed := dwell
	renamed.Name = "门口长时间逗留"
	if _, err := service.UpsertRule(ctx, fixture.Admin, renamed); err != nil {
		t.Fatal("a rule must be updatable without conflicting with itself", err)
	}
	// A zone with an entry rule cannot later gain a dwell rule either: that
	// would retroactively delay rules that already exist.
	free, err := zones.UpsertZone(ctx, fixture.Admin, detection.Zone{
		ChannelID: fixture.Channel, Name: "院子",
		Coordinates: []detection.Point{{X: 0.5, Y: 0.5}, {X: 0.9, Y: 0.5}, {X: 0.9, Y: 0.9}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpsertRule(ctx, fixture.Admin, events.Rule{
		ChannelID: fixture.Channel, Name: "院子进入", Kind: "enter", ZoneID: &free.ID,
		Categories: []string{"person"}, Severity: "low", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	_, err = service.UpsertRule(ctx, fixture.Admin, events.Rule{
		ChannelID: fixture.Channel, Name: "院子逗留", Kind: "dwell", ZoneID: &free.ID,
		Categories: []string{"person"}, DwellSecond: &seconds, Severity: "low", Enabled: true,
	})
	if !errors.As(err, &refused) || refused.Code != "rule_enter_on_dwell_zone" {
		t.Fatal("adding a dwell rule to a zone with entry rules must be refused", err)
	}
}
