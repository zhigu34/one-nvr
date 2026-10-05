package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/database"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/jobs"
	"github.com/zhigu34/one-nvr/internal/media/zlm"
	"github.com/zhigu34/one-nvr/internal/recording"
	"github.com/zhigu34/one-nvr/internal/storage"
	"os"
	"path/filepath"
	"time"
)

func recordingRecoveryScenario(ctx context.Context, db *database.DB, s *recording.Service, media *zlm.Client, p auth.Principal, ids poolFixtureIDs, originalRun, originalSession id.ID, execute func(string, jobs.Handler) error) error {
	key := func(session id.ID) zlm.StreamKey {
		return zlm.StreamKey{VHost: "__defaultVhost__", App: "one_nvr", Stream: string(session)}
	}
	currentRun := func() (id.ID, error) {
		var run id.ID
		err := db.Pool.QueryRow(ctx, "SELECT id FROM recording_runs WHERE channel_id=$1 AND state='recording'", ids.Channel).Scan(&run)
		return run, err
	}
	if err := s.Reconcile(ctx, ids.Channel); err != nil {
		return err
	}
	if run, err := currentRun(); err != nil || run != originalRun {
		return fmt.Errorf("Worker restart did not adopt actual existing run")
	}
	if _, err := s.Sources.SetPolicy(ctx, p, ids.Channel, 4, "none", "recovery-off"); err != nil {
		return err
	}
	if err := execute("source.policy_apply", s.ExecuteSourceChange); err != nil {
		return err
	}
	snapshot, err := media.Inspect(ctx, key(originalSession))
	if err != nil || snapshot.Recording {
		return fmt.Errorf("recording off failed to preserve non-recording pull")
	}
	var inbox id.ID
	for {
		err = db.Pool.QueryRow(ctx, "SELECT id FROM hook_inbox WHERE run_id=$1 AND state='pending' ORDER BY received_at LIMIT 1", originalRun).Scan(&inbox)
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	history, err := s.Publish(ctx, inbox)
	if err != nil || history.RunID != originalRun {
		return fmt.Errorf("off lost original tail")
	}
	if _, err := s.Sources.SetPolicy(ctx, p, ids.Channel, 6, "continuous", "recovery-on"); err != nil {
		return err
	}
	if err := execute("source.policy_apply", s.ExecuteSourceChange); err != nil {
		return err
	}
	secondRun, err := currentRun()
	if err != nil || secondRun == originalRun {
		return fmt.Errorf("recording resume reused stopped run")
	}
	// Remove the actual ZLM proxy, then reconcile against the unchanged channel
	// revision. This is an external absence, not a seeded fake health observation.
	if err := media.RemoveProxy(ctx, zlm.ProxyRef{Key: key(originalSession), OpaqueKey: "__defaultVhost__/one_nvr/" + string(originalSession)}); err != nil {
		return err
	}
	if err := waitPhysicalAbsence(ctx, media, key(originalSession)); err != nil {
		return fmt.Errorf("external source removal not observed: %w", err)
	}
	fresh, err := waitRecoveredSession(ctx, originalSession, func(ctx context.Context) error { return s.Reconcile(ctx, ids.Channel) }, func(ctx context.Context) (id.ID, error) {
		var current id.ID
		err := db.Pool.QueryRow(ctx, "SELECT id FROM stream_sessions WHERE channel_id=$1 AND purpose='main' AND state='active'", ids.Channel).Scan(&current)
		return current, err
	})
	if err != nil {
		var reason string
		_ = db.Pool.QueryRow(ctx, "SELECT reason_code FROM source_observations WHERE channel_id=$1 AND kind='main' ORDER BY observed_at DESC LIMIT 1", ids.Channel).Scan(&reason)
		return fmt.Errorf("source recovery did not reach a fresh active generation: %w (observed=%s)", err, reason)
	}
	timer := time.NewTimer(11 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
	}
	if err := s.CheckPool(ctx, ids.Pool); err != nil {
		return fmt.Errorf("recovery actual pool proof: %w", err)
	}
	if err := s.Reconcile(ctx, ids.Channel); err != nil {
		return err
	}
	recoveredRun, err := currentRun()
	if err != nil || recoveredRun == secondRun {
		return fmt.Errorf("source recovery did not create fresh run")
	}
	// A removed identity marker must stop writes without removing historical files.
	marker := filepath.Join("/storage/pool", ".one-nvr.json")
	if err := os.Rename(marker, marker+".recovery-test"); err != nil {
		return err
	}
	defer os.Rename(marker+".recovery-test", marker)
	if err := s.Reconcile(ctx, ids.Channel); err != nil {
		return err
	}
	snapshot, err = media.Inspect(ctx, key(fresh))
	if err != nil || snapshot.Recording {
		return fmt.Errorf("missing marker did not stop only recorder")
	}
	var state string
	if err := db.Pool.QueryRow(ctx, "SELECT state FROM recording_segments WHERE id=$1", history.ID).Scan(&state); err != nil || state != "ready" {
		return fmt.Errorf("pool fault deleted history")
	}
	if err := os.Rename(marker+".recovery-test", marker); err != nil {
		return err
	}
	// Recover requires two real healthy capacity checks, at least ten seconds apart.
	if err := s.Pools.Sample(ctx, "api"); err != nil {
		return err
	}
	if err := s.Pools.Sample(ctx, "worker"); err != nil {
		return err
	}
	if err := s.CheckPool(ctx, ids.Pool); err != nil {
		return err
	}
	_ = s.Reconcile(ctx, ids.Channel)
	if snapshot, err = media.Inspect(ctx, key(fresh)); err != nil || snapshot.Recording {
		return fmt.Errorf("pool recovery skipped first confirmation")
	}
	timer.Reset(11 * time.Second)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
	}
	if err := s.Reconcile(ctx, ids.Channel); err != nil {
		return err
	}
	finalRun, err := currentRun()
	if err != nil || finalRun == recoveredRun {
		return fmt.Errorf("pool recovery did not allocate a new run")
	}
	if snapshot, err = media.Inspect(ctx, key(fresh)); err != nil || !snapshot.Recording {
		return fmt.Errorf("actual recovery recorder unavailable")
	}
	if err := os.MkdirAll("/storage/pool2", 0700); err != nil {
		return err
	}
	pool, err := s.Pools.Register(ctx, p, storage.RegisterInput{Name: "Replacement pool", Path: "/storage/pool2"})
	if err != nil {
		return err
	}
	if err := s.Pools.Sample(ctx, "api"); err != nil {
		return err
	}
	if err := s.Pools.Sample(ctx, "worker"); err != nil {
		return err
	}
	if err := s.CheckPool(ctx, pool.ID); err != nil {
		return err
	}
	if _, err := s.Sources.BindPool(ctx, p, ids.Channel, pool.ID, 8, "recovery-pool-change"); err != nil {
		return err
	}
	if err := execute("source.pool_switch", s.ExecuteSourceChange); err != nil {
		return err
	}
	var target, oldLocation id.ID
	if err := db.Pool.QueryRow(ctx, "SELECT pool_id FROM recording_runs WHERE channel_id=$1 AND state='recording'", ids.Channel).Scan(&target); err != nil || target != pool.ID {
		return fmt.Errorf("actual pool switch did not select target")
	}
	if err := db.Pool.QueryRow(ctx, "SELECT pool_id FROM recording_locations WHERE segment_id=$1", history.ID).Scan(&oldLocation); err != nil || oldLocation != ids.Pool {
		return fmt.Errorf("pool switch moved historical file")
	}
	evidence := map[string]bool{"upstream_adopted_after_worker_restart": true, "recording_off_keeps_pull_and_tail": true, "source_recovery_new_generation_run": true, "missing_marker_stops_writes_keeps_history": true, "two_healthy_capacity_confirmations": true, "pool_switch_keeps_historical_location": true}
	raw, _ := json.MarshalIndent(evidence, "", "  ")
	if err := os.WriteFile("/evidence/recovery.json", raw, 0644); err != nil {
		return err
	}
	fmt.Println("Actual recording policy, source generation recovery, pool marker fault, hysteresis and historical tail PASS")
	return nil
}

// A proxy deletion acknowledgement precedes asynchronous media unregistration.
// Inject the fault only after actual typed absence is observed, never by treating
// an HTTP/management error as disappearance.
func waitPhysicalAbsence(ctx context.Context, media *zlm.Client, key zlm.StreamKey) error {
	wait, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for {
		_, err := media.Inspect(wait, key)
		if errors.Is(err, zlm.ErrStreamAbsent) {
			return nil
		}
		if err != nil && !errors.Is(err, zlm.ErrMediaOperation) {
			return err
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-wait.Done():
			timer.Stop()
			if err != nil {
				return fmt.Errorf("bounded absence check: %w", err)
			}
			return wait.Err()
		case <-timer.C:
		}
	}
}

func waitRecoveredSession(ctx context.Context, previous id.ID, reconcile func(context.Context) error, find func(context.Context) (id.ID, error)) (id.ID, error) {
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		if err := reconcile(bounded); err != nil {
			return "", err
		}
		current, err := find(bounded)
		if err == nil && current != "" && current != previous {
			return current, nil
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return "", err
		}
		timer := time.NewTimer(10 * time.Second)
		select {
		case <-bounded.Done():
			timer.Stop()
			return "", bounded.Err()
		case <-timer.C:
		}
	}
}
