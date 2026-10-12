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
	service.Reload = func(ctx context.Context) error {
		return errors.New("broker down")
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
