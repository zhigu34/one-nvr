//go:build gateway_runtime

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/zhigu34/one-nvr/internal/database"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/media/probe"
	"github.com/zhigu34/one-nvr/internal/recording"
)

type crashPublisher struct{ phase string }
type publicationCheckpoint struct {
	Phase    string `json:"phase"`
	Original string `json:"original"`
	Target   string `json:"target"`
}

func (p crashPublisher) Move(ctx context.Context, root *os.Root, original, target string, expected os.FileInfo) error {
	if p.phase == "after" {
		if err := (recording.NativePublisher{}).Move(ctx, root, original, target, expected); err != nil {
			return err
		}
	}
	checkpoint, err := json.Marshal(publicationCheckpoint{p.phase, original, target})
	if err != nil {
		return err
	}
	if err := os.WriteFile("/results/publication-checkpoint.new", checkpoint, 0600); err != nil {
		return err
	}
	if err := os.Rename("/results/publication-checkpoint.new", "/results/publication-checkpoint"); err != nil {
		return err
	}
	// The parent must SIGKILL this process. This never returns a simulated error.
	for {
		time.Sleep(time.Hour)
	}
}

// Worker is paused while real ZLM closes the next fragment. A separate actual
// publisher process discovers only registered run receipts and is killed at the
// native Move boundary. Recovery uses normal lease expiry, not seeded DB state.
func TestGatewayMediaRuntimePublishCrash(t *testing.T) {
	if os.Getenv("ONE_NVR_GATEWAY_RUNTIME") != "isolated" {
		t.Fatal("requires isolated publication runtime")
	}
	phase := os.Getenv("ONE_NVR_PUBLISH_PHASE")
	if phase != "before" && phase != "after" {
		t.Fatal("requires explicit publication crash phase")
	}
	db, err := database.Open(context.Background(), os.Getenv("ONE_NVR_DATABASE_URL"))
	if err != nil {
		t.Fatal("publication fixture database unavailable")
	}
	defer db.Pool.Close()
	service := recording.New(db, jointMedia(t), probe.Runner{FFprobe: "/usr/bin/ffprobe"}, []string{"/storage"}, "/data")
	if os.Getenv("ONE_NVR_PUBLICATION_CHILD") == "yes" {
		service.Files = crashPublisher{phase: phase}
		end := time.Now().Add(100 * time.Second)
		for time.Now().Before(end) {
			_ = service.Recover(context.Background())
			time.Sleep(time.Second)
		}
		t.Fatal("actual closed fragment never reached Move")
	}
	_ = os.Remove("/results/publication-checkpoint")
	process := exec.Command(os.Args[0], "-test.run=^TestGatewayMediaRuntimePublishCrash$", "-test.timeout=180s")
	process.Env = append(os.Environ(), "ONE_NVR_PUBLICATION_CHILD=yes")
	// Child stdout is intentionally private; no credential-bearing media responses.
	if process.Start() != nil {
		t.Fatal("publisher subprocess unavailable")
	}
	waited := false
	defer func() {
		if !waited {
			process.Process.Kill()
			process.Wait()
		}
	}()
	var checkpoint publicationCheckpoint
	jointWait(t, 105, "publisher did not reach real crash boundary", func() bool {
		raw, err := os.ReadFile("/results/publication-checkpoint")
		return err == nil && json.Unmarshal(raw, &checkpoint) == nil && checkpoint.Phase == phase && checkpoint.Original != "" && checkpoint.Target != ""
	})
	if process.Process.Kill() != nil {
		t.Fatal("publisher SIGKILL failed")
	}
	err = process.Wait()
	waited = true
	var exited *exec.ExitError
	if !errors.As(err, &exited) {
		t.Fatal("publisher did not terminate by signal")
	}
	status, ok := exited.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatal("publisher was not killed at checkpoint")
	}
	var segment, run, revision, pool id.ID
	var base, original, target, zone string
	if err = db.Pool.QueryRow(context.Background(), runtimeFinalizingFileQuery, checkpoint.Original, checkpoint.Target).Scan(&segment, &run, &revision, &pool, &base, &original, &target, &zone); err != nil {
		t.Fatal("crash lost durable finalizing identity")
	}
	_, originalErr := os.Stat(filepath.Join(base, original))
	_, targetErr := os.Stat(filepath.Join(base, target))
	if phase == "before" && (originalErr != nil || !errors.Is(targetErr, os.ErrNotExist)) {
		t.Fatal("pre-move crash changed original file")
	}
	if phase == "after" && (!errors.Is(originalErr, os.ErrNotExist) || targetErr != nil) {
		t.Fatal("post-move crash lost moved file")
	}
	jointWait(t, 120, "SIGKILL publication did not recover", func() bool {
		_ = service.Recover(context.Background())
		var ready bool
		err := db.Pool.QueryRow(context.Background(), `SELECT state='ready' AND run_id=$2 AND source_revision_id=$3 AND pool_id=$4 AND target_relative_path=$5 AND naming_timezone=$6 FROM recording_segments WHERE id=$1`, segment, run, revision, pool, target, zone).Scan(&ready)
		return err == nil && ready
	})
	file, err := os.Open(filepath.Join(base, target))
	if err != nil {
		t.Fatal("recovered publication missing")
	}
	defer file.Close()
	if _, err = (probe.Runner{FFprobe: "/usr/bin/ffprobe"}).InspectMP4(context.Background(), file); err != nil {
		t.Fatal("recovered actual MP4 invalid")
	}
	jointWrite(t, "/results/joint-publish-"+phase+".json", map[string]any{"phase": phase, "actual_sigkill": true, "actual_zlm_fragment": true, "frozen_identity_preserved": true, "recovered_ready": true})
}
