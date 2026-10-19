package integration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/detection"
	"github.com/zhigu34/one-nvr/internal/fault"
	"github.com/zhigu34/one-nvr/internal/id"
)

func TestDetectionConfigLifecycle(t *testing.T) {
	fixture := newPublicationFixture(t)
	ctx := context.Background()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yml")
	service := detection.New(fixture.DB, fixture.Auth, "", configPath)
	service.MQTTKey = "mqtt-secret"
	service.ProbeToken = "probe-secret"
	reloads := 0
	service.Reload = func(ctx context.Context) error {
		reloads++
		return nil
	}
	// A viewer must not configure detection; an operator with configure may.
	viewer, err := fixture.Auth.CreateUser(ctx, fixture.Admin, auth.CreateUserInput{Username: "detection_viewer", Password: testPassword, Role: "viewer"})
	if err != nil {
		t.Fatal(err)
	}
	login, err := fixture.Auth.Login(ctx, "detection_viewer", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO channel_grants(user_id,channel_id,configure) VALUES($1,$2,true) ON CONFLICT (user_id,channel_id) DO UPDATE SET configure=true`, viewer.ID, fixture.Channel); err != nil {
		t.Fatal(err)
	}
	// An operator (configure allowed, not admin) may save; a viewer's role
	// still forbids configure regardless of the grant.
	operatorUser, err := fixture.Auth.CreateUser(ctx, fixture.Admin, auth.CreateUserInput{Username: "detection_operator", Password: testPassword, Role: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	operatorLogin, err := fixture.Auth.Login(ctx, "detection_operator", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO channel_grants(user_id,channel_id,configure) VALUES($1,$2,true) ON CONFLICT (user_id,channel_id) DO UPDATE SET configure=true`, operatorUser.ID, fixture.Channel); err != nil {
		t.Fatal(err)
	}
	config, err := service.Upsert(ctx, operatorLogin.Principal, detection.Config{
		ChannelID: fixture.Channel, Enabled: true, Stream: "sub", FPS: 5, Labels: []string{"person", "car"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.Version != 1 || !config.Enabled || len(config.Labels) != 2 {
		t.Fatal("the saved configuration must round-trip", config)
	}
	// The generation moves when the detection source changes (CH-08).
	var generation int
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT detection_generation FROM channels WHERE id=$1`, fixture.Channel).Scan(&generation); err != nil || generation != 1 {
		t.Fatal("enabling detection bumps the generation", generation, err)
	}
	// An unsupported label is refused before anything is stored.
	var rejected *fault.Error
	_, err = service.Upsert(ctx, operatorLogin.Principal, detection.Config{
		ChannelID: fixture.Channel, Enabled: true, Stream: "sub", FPS: 5, Labels: []string{"dinosaur"},
	})
	if !errors.As(err, &rejected) || rejected.Code != "detection_label_unsupported" {
		t.Fatal("an unsupported label must be refused", err)
	}
	// A stale version is refused (concurrent editor).
	_, err = service.Upsert(ctx, operatorLogin.Principal, detection.Config{
		ChannelID: fixture.Channel, Enabled: true, Stream: "sub", FPS: 5, Labels: []string{"person"}, Version: 99,
	})
	if !errors.As(err, &rejected) || rejected.Code != "detection_version_conflict" {
		t.Fatal("a stale version must be refused", err)
	}
	// A viewer cannot write even with a configure grant: role still applies.
	if _, err := service.Upsert(ctx, login.Principal, detection.Config{
		ChannelID: fixture.Channel, Enabled: true, Stream: "sub", FPS: 5, Labels: []string{"person"},
	}); err == nil {
		t.Fatal("a viewer must not save detection settings")
	}
	// Publish (admin): renders, writes the file, reloads, records the revision.
	revision, err := service.Publish(ctx, fixture.Admin)
	if err != nil {
		t.Fatal(err)
	}
	if revision.State != "published" || reloads != 1 {
		t.Fatal("a successful publish records one revision and reloads once", revision, reloads)
	}
	rendered, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal("the rendered config must exist", err)
	}
	if len(rendered) == 0 || string(rendered) == "" {
		t.Fatal("rendered config is empty")
	}
	// The page must be able to say whether the running detector matches the
	// stored settings: a publication is a snapshot, so any later edit has to
	// read as pending rather than as "published".
	if revision, err := service.LatestRevision(ctx, fixture.Admin); err != nil {
		t.Fatal(err)
	} else if revision == nil || revision.Pending {
		t.Fatal("immediately after a publication nothing may be pending", revision)
	}
	if _, err := service.Upsert(ctx, fixture.Admin, detection.Config{
		ChannelID: fixture.Channel, Enabled: true, Stream: "sub", FPS: 10, Labels: []string{"person", "car"},
	}); err != nil {
		t.Fatal(err)
	}
	if revision, err := service.LatestRevision(ctx, fixture.Admin); err != nil {
		t.Fatal(err)
	} else if revision == nil || !revision.Pending {
		t.Fatal("an edit after the last publication must read as pending", revision)
	}

	// The session sync must not overwrite an operator's pending edit: settings
	// that moved on since the last publication stay the operator's decision.
	reloadsBefore := reloads
	service.SyncAfterSessionChange(ctx)
	if reloads != reloadsBefore {
		t.Fatal("the session sync republished while a setting change was pending")
	}
	if config, err := service.Upsert(ctx, fixture.Admin, detection.Config{
		ChannelID: fixture.Channel, Enabled: true, Stream: "sub", FPS: 5, Labels: []string{"person", "car"}, Version: 2,
	}); err != nil || config.FPS != 5 {
		t.Fatal("reverting the setting must succeed", config, err)
	}
	if _, err := service.Publish(ctx, fixture.Admin); err != nil {
		t.Fatal(err)
	}
	// With nothing pending, a sync against an unchanged render writes nothing.
	reloadsBefore = reloads
	service.SyncAfterSessionChange(ctx)
	if reloads != reloadsBefore {
		t.Fatal("the session sync published although nothing drifted")
	}

	// Publishing also establishes the event-routing table: without a mapping
	// row the event stream would drop this channel's events as unmapped.
	// The fixture has no live stream session, so no camera was rendered.
	var mapped int
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM frigate_cameras`).Scan(&mapped); err != nil {
		t.Fatal(err)
	}
	if mapped != 0 {
		t.Fatal("no session means no rendered camera, so no mapping", mapped)
	}
}

func TestDetectionPublishFailureKeepsPrevious(t *testing.T) {
	fixture := newPublicationFixture(t)
	ctx := context.Background()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yml")
	previous := []byte(`{"previous":true}` + "\n")
	if err := os.WriteFile(configPath, previous, 0o600); err != nil {
		t.Fatal(err)
	}
	service := detection.New(fixture.DB, fixture.Auth, "", configPath)
	service.MQTTKey = "mqtt-secret"
	service.ProbeToken = "probe-secret"
	service.Reload = func(ctx context.Context) error {
		return errors.New("restart refused")
	}
	if _, err := service.Upsert(ctx, fixture.Admin, detection.Config{
		ChannelID: fixture.Channel, Enabled: true, Stream: "sub", FPS: 5, Labels: []string{"person"},
	}); err != nil {
		t.Fatal(err)
	}
	// A publication without an active stream session renders an empty camera
	// set; the reload then fails and the previous file must survive.
	if _, publishErr := service.Publish(ctx, fixture.Admin); publishErr == nil {
		t.Fatal("a failed reload must surface")
	} else {
		t.Logf("publish error: %v", publishErr)
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) == "" {
		t.Fatal("the config file must still exist")
	}
	// The failed attempt is recorded so the failure is visible, not silent.
	var state string
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT state FROM detection_config_revisions ORDER BY created_at DESC LIMIT 1`).Scan(&state); err != nil || state != "failed" {
		t.Fatal("a failed publication is recorded", state, err)
	}
	_ = id.ID("")
}

// One reload failure must not disable automatic session syncing.
//
// The sync reads the newest publication to decide whether settings have moved
// since it. Reading the newest *row* meant a failed publish — a transient detector
// outage — left "failed" as the newest row forever, the sync bailed out on the
// state check, and the detector kept pulling a media session that no longer
// existed. Measured on the live site: detection had been dead since the failure,
// every channel logged "ffmpeg process is not running", and nothing in the
// product connected the two.
func TestSessionSyncSurvivesAFailedPublication(t *testing.T) {
	fixture := newPublicationFixture(t)
	ctx := context.Background()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yml")
	service := detection.New(fixture.DB, fixture.Auth, "", configPath)
	service.MQTTKey = "mqtt-secret"
	service.ProbeToken = "probe-secret"
	reloadCount := 0
	failReload := false
	service.Reload = func(context.Context) error {
		reloadCount++
		if failReload {
			return detection.ErrPublishFailed
		}
		return nil
	}
	// The sync only republishes when a live session exists for the camera.
	if _, err := fixture.DB.Pool.Exec(ctx, "UPDATE channels SET current_revision_id=$2 WHERE id=$1", fixture.Channel, fixture.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO stream_sessions(id,channel_id,source_revision_id,generation,app,stream,purpose,state)
		VALUES(gen_random_uuid(),$1,$2,1,'one_nvr',gen_random_uuid()::text,'main','active')`, fixture.Channel, fixture.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Upsert(ctx, fixture.Admin, detection.Config{
		ChannelID: fixture.Channel, Enabled: true, Stream: "sub", FPS: 5, Labels: []string{"person"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Publish(ctx, fixture.Admin); err != nil {
		t.Fatal(err)
	}
	published, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}

	// A publish that fails at the reload step, as a detector outage would. The
	// settings do not change: what fails is the detector, not the operator.
	failReload = true
	if _, err := service.Publish(ctx, fixture.Admin); err == nil {
		t.Fatal("a reload failure must surface as a failed publish")
	}
	restored, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != string(published) {
		t.Fatal("a failed publish did not restore the previous config")
	}

	// The detector recovers and a new media session appears, so the published
	// config now points at a dead stream. The newest row is still 'failed'; the
	// newest *published* row is the truth about what the detector is running.
	failReload = false
	before := reloadCount
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO stream_sessions(id,channel_id,source_revision_id,generation,app,stream,purpose,state)
		VALUES(gen_random_uuid(),$1,$2,2,'one_nvr',gen_random_uuid()::text,'main','active')`, fixture.Channel, fixture.Revision); err != nil {
		t.Fatal(err)
	}
	service.SyncAfterSessionChange(ctx)
	if reloadCount == before {
		t.Fatal("a failed publication disabled session syncing; the detector would pull a dead session forever")
	}
}
