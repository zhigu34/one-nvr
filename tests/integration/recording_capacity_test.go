package integration

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/channel"
	"github.com/zhigu34/one-nvr/internal/fault"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/jobs"
	"github.com/zhigu34/one-nvr/internal/media/zlm"
	"github.com/zhigu34/one-nvr/internal/recording"
	"github.com/zhigu34/one-nvr/internal/storage"
)

func recordingExecution(t *testing.T, f publishFixture) *channel.Execution {
	t.Helper()
	ctx := context.Background()
	key, _ := id.New()
	if err := f.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		_, err := jobs.Enqueue(ctx, tx, jobs.Input{Kind: "source.policy_apply", ObjectID: f.Channel, IdempotencyKey: string(key), Payload: []byte(`{"controlled_start":true}`)})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	lease, err := (jobs.Repository{DB: f.DB}).Claim(ctx, "source.policy_apply")
	if err != nil {
		t.Fatal(err)
	}
	e, err := channel.NewExecution(ctx, f.DB, lease, f.Channel)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	return e
}
func seedCurrentBitrate(t *testing.T, f publishFixture, rate int64) id.ID {
	t.Helper()
	ctx := context.Background()
	var session id.ID
	if err := f.DB.Pool.QueryRow(ctx, "SELECT stream_session_id FROM recording_runs WHERE id=$1", f.Run).Scan(&session); err != nil {
		t.Fatal(err)
	}
	for index, age := range []string{"20 seconds", "1 second"} {
		sample, _ := id.New()
		if _, err := f.DB.Pool.Exec(ctx, `INSERT INTO recording_bitrate_samples(id,channel_id,source_revision_id,stream_session_id,bytes_per_second,frames,valid,observed_at) VALUES($1,$2,$3,$4,$5,$6,true,clock_timestamp()-$7::interval)`, sample, f.Channel, f.Revision, session, rate, 100+index, age); err != nil {
			t.Fatal(err)
		}
	}
	return session
}
func TestRecordingCapacityUnknownBlocksNewRecorder(t *testing.T) {
	f, media, _, _ := testedSource(t)
	ctx := context.Background()
	if _, err := f.DB.Pool.Exec(ctx, "DELETE FROM recording_bitrate_samples WHERE channel_id=$1", f.Channel); err != nil {
		t.Fatal(err)
	}
	var session id.ID
	if err := f.DB.Pool.QueryRow(ctx, "SELECT stream_session_id FROM recording_runs WHERE id=$1", f.Run).Scan(&session); err != nil {
		t.Fatal(err)
	}
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE recording_runs SET state='stopped' WHERE id=$1", f.Run); err != nil {
		t.Fatal(err)
	}
	media.recording[string(session)] = false
	e := recordingExecution(t, f)
	_, err := f.Service.Start(ctx, e, recording.StartInput{SessionID: session, PoolID: f.Pool.ID})
	var problem *fault.Error
	if !errors.As(err, &problem) || problem.Code != "bitrate_unknown" {
		t.Fatal("unknown bitrate started a recorder", err)
	}
	var open int
	if err := f.DB.Pool.QueryRow(ctx, "SELECT count(*) FROM recording_runs WHERE channel_id=$1 AND state IN ('starting','recording','stopping')", f.Channel).Scan(&open); err != nil || open != 0 {
		t.Fatal("blocked start created a recorder intent", open, err)
	}
	seedCurrentBitrate(t, f, 1<<20)
	if _, err := f.Service.Start(ctx, e, recording.StartInput{SessionID: session, PoolID: f.Pool.ID}); err != nil {
		t.Fatal("paired fresh bitrate did not permit start", err)
	}
}

func TestRecordingCapacityRecoveryRequiresDoubleLineAndTwoHealthySamples(t *testing.T) {
	f, media, _, _ := testedSource(t)
	ctx := context.Background()
	session := seedCurrentBitrate(t, f, 1<<20)
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE recording_runs SET state='stopped' WHERE id=$1", f.Run); err != nil {
		t.Fatal(err)
	}
	media.recording[string(session)] = false
	e := recordingExecution(t, f)
	free := func(value int64) {
		t.Helper()
		if _, err := f.DB.Pool.Exec(ctx, "UPDATE storage_pool_checks SET free_bytes=$2 WHERE pool_id=$1 AND service IN ('api','worker')", f.Pool.ID, value); err != nil {
			t.Fatal(err)
		}
	}
	start := func(expected string) {
		t.Helper()
		_, err := f.Service.Start(ctx, e, recording.StartInput{SessionID: session, PoolID: f.Pool.ID})
		var problem *fault.Error
		if !errors.As(err, &problem) || problem.Code != expected {
			t.Fatal("capacity outcome incorrect", expected, err)
		}
	}
	free(1 << 30)
	start("low_space")
	free(15 << 30)
	start("capacity_recovery_wait")
	free(25 << 30)
	start("capacity_recovery_wait")
	start("capacity_recovery_wait")
	var healthy int
	if err := f.DB.Pool.QueryRow(ctx, "SELECT healthy_samples FROM recording_capacity_blocks WHERE channel_id=$1", f.Channel).Scan(&healthy); err != nil || healthy != 1 {
		t.Fatal("rapid retry counted as second healthy sample", healthy, err)
	}
	// Controlled clock advance for the persisted cadence, not a production bypass.
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE recording_capacity_blocks SET last_healthy_at=clock_timestamp()-interval '11 seconds' WHERE channel_id=$1", f.Channel); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Service.Start(ctx, e, recording.StartInput{SessionID: session, PoolID: f.Pool.ID}); err != nil {
		t.Fatal("confirmed double-line recovery not allowed", err)
	}
}

func TestRecordingCapacityUnknownDoesNotEraseRecoveryLatch(t *testing.T) {
	f, _, _, _ := testedSource(t)
	ctx := context.Background()
	e := recordingExecution(t, f)
	if _, err := f.DB.Pool.Exec(ctx, "INSERT INTO recording_capacity_blocks(channel_id,reason_code) VALUES($1,'low_space')", f.Channel); err != nil {
		t.Fatal(err)
	}
	if _, err := f.DB.Pool.Exec(ctx, "DELETE FROM recording_bitrate_samples WHERE channel_id=$1", f.Channel); err != nil {
		t.Fatal(err)
	}
	var ss id.ID
	if err := f.DB.Pool.QueryRow(ctx, "SELECT stream_session_id FROM recording_runs WHERE id=$1", f.Run).Scan(&ss); err != nil {
		t.Fatal(err)
	}
	if err := f.Service.Stop(ctx, e, recording.Handle{RunID: f.Run, SessionID: ss, PoolID: f.Pool.ID, Key: zlm.StreamKey{VHost: "__defaultVhost__", App: "one_nvr", Stream: string(ss)}}); err != nil {
		t.Fatal(err)
	}
	_, _ = f.Service.Start(ctx, e, recording.StartInput{SessionID: ss, PoolID: f.Pool.ID})
	var reason string
	if err := f.DB.Pool.QueryRow(ctx, "SELECT reason_code FROM recording_capacity_blocks WHERE channel_id=$1", f.Channel).Scan(&reason); err != nil || reason != "low_space" {
		t.Fatal("unknown sample erased recovery threshold", reason, err)
	}
}

func TestRecordingCapacityDisabledSiblingPoolDoesNotBlock(t *testing.T) {
	f, _, _, _ := testedSource(t)
	ctx := context.Background()
	var sibling id.ID
	if err := f.DB.Pool.QueryRow(ctx, "SELECT id FROM channels WHERE channel_no=2").Scan(&sibling); err != nil {
		t.Fatal(err)
	}
	draft, err := f.Service.Sources.CreateDraft(ctx, f.Admin, sibling, 1, channel.DraftInput{Config: channel.SourceConfig{IP: "192.168.33.23", MainPath: "/main"}, IdentityIntent: "replace", Credentials: channel.CredentialInput{PasswordAction: "clear"}})
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	f.Service.Pools.Roots = append(f.Service.Pools.Roots, base)
	f.Service.Pools.Auth = f.Auth
	pool, err := f.Service.Pools.Register(ctx, f.Admin, storage.RegisterInput{Name: "Disabled sibling", Path: base})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Service.Pools.SamplePool(ctx, pool, "worker"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE storage_pools SET enabled=false WHERE id=$1", pool.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE channels SET current_revision_id=$2,storage_pool_id=$3 WHERE id=$1", sibling, draft.ID, pool.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE recording_policies SET mode='continuous' WHERE channel_id=$1", sibling); err != nil {
		t.Fatal(err)
	}
	e := recordingExecution(t, f)
	var ss id.ID
	if err := f.DB.Pool.QueryRow(ctx, "SELECT stream_session_id FROM recording_runs WHERE id=$1", f.Run).Scan(&ss); err != nil {
		t.Fatal(err)
	}
	if err := f.Service.Stop(ctx, e, recording.Handle{RunID: f.Run, SessionID: ss, PoolID: f.Pool.ID, Key: zlm.StreamKey{VHost: "__defaultVhost__", App: "one_nvr", Stream: string(ss)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Service.Start(ctx, e, recording.StartInput{SessionID: ss, PoolID: f.Pool.ID}); err != nil {
		t.Fatal("disabled sibling blocked healthy pool", err)
	}
}
