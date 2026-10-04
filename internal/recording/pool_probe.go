package recording

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/media/zlm"
	"github.com/zhigu34/one-nvr/internal/storage"
)

// ProbeSession is called only for an explicitly requested pool check and a
// persisted temporary session. A plain first-frame test never calls it.
func (s *Service) ProbeSession(ctx context.Context, poolID, sessionID id.ID) error {
	if s.Media == nil || s.Probe == nil || s.Pools == nil {
		return zlm.ErrTestSourceRequired
	}
	root, pool, err := s.Pools.OpenMediaRoot(ctx, poolID)
	if err != nil {
		return err
	}
	defer root.Close()
	if !pool.Enabled {
		return storage.ErrMediaProof
	}
	if err := s.Pools.SamplePool(ctx, pool, "worker"); err != nil {
		return err
	}
	var baseReady bool
	if err := s.DB.Pool.QueryRow(ctx, `SELECT count(*)=2 FROM storage_pool_checks WHERE pool_id=$1 AND service IN ('api','worker') AND state='healthy' AND expires_at>clock_timestamp()`, poolID).Scan(&baseReady); err != nil {
		return err
	}
	if !baseReady {
		return storage.ErrMediaProof
	}
	run, err := id.New()
	if err != nil {
		return err
	}
	key := zlm.StreamKey{VHost: "__defaultVhost__", App: "one_nvr", Stream: string(sessionID)}
	relative := ".work/probes/zlm/" + string(run)
	err = s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		var channelID, revisionID id.ID
		if err := tx.QueryRow(ctx, `SELECT ss.channel_id,ss.source_revision_id FROM stream_sessions ss JOIN channels c ON c.id=ss.channel_id WHERE ss.id=$1 AND ss.vhost=$2 AND ss.app=$3 AND ss.stream=$4 AND ss.state='active' AND ss.purpose='test' AND c.site_id=$5 FOR UPDATE OF ss`, sessionID, key.VHost, key.App, key.Stream, s.siteID).Scan(&channelID, &revisionID); err != nil {
			return zlm.ErrTestSourceRequired
		}
		_, err := tx.Exec(ctx, `INSERT INTO recording_runs(id,site_id,channel_id,source_revision_id,stream_session_id,pool_id,work_relative_path,purpose) VALUES($1,$2,$3,$4,$5,$6,$7,'probe')`, run, s.siteID, channelID, revisionID, sessionID, poolID, relative)
		return err
	})
	if err != nil {
		return err
	}
	success := false
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if !success {
			s.Media.StopRecord(cleanup, key)
		}
		state, reason := "stopped", ""
		if !success {
			state, reason = "failed", "pool_probe_failed"
		}
		s.DB.Pool.Exec(cleanup, `UPDATE recording_runs SET state=$2,ended_at=clock_timestamp(),error_code=NULLIF($3,'') WHERE id=$1`, run, state, reason)
	}()
	if err := root.MkdirAll(relative, 0700); err != nil {
		return storage.ErrMediaProof
	}
	// Reject a work-directory link before handing the canonical private path to ZLM.
	for _, path := range []string{".work", ".work/probes", ".work/probes/zlm", relative} {
		info, err := root.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return storage.ErrMediaProof
		}
	}
	if err := s.Media.StartRecord(ctx, key, filepath.Join(pool.Path, relative), 60); err != nil {
		return err
	}
	if _, err := s.DB.Pool.Exec(ctx, `UPDATE recording_runs SET state='recording',started_at=clock_timestamp() WHERE id=$1`, run); err != nil {
		return err
	}
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
	}
	if err := s.Media.StopRecord(ctx, key); err != nil {
		return err
	}
	if _, err := s.DB.Pool.Exec(ctx, `UPDATE recording_runs SET state='stopping' WHERE id=$1`, run); err != nil {
		return err
	}
	wait, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		var inboxID id.ID
		var raw []byte
		err := s.DB.Pool.QueryRow(wait, `SELECT id,payload FROM hook_inbox WHERE run_id=$1 AND state='pending' ORDER BY received_at LIMIT 1`, run).Scan(&inboxID, &raw)
		if err == nil {
			var c Completion
			if json.Unmarshal(raw, &c) != nil || s.validate(c) != nil {
				return storage.ErrMediaProof
			}
			fileRelative, err := filepath.Rel(pool.Path, c.FilePath)
			if err != nil {
				return storage.ErrMediaProof
			}
			f, err := storage.OpenMediaFile(root, fileRelative)
			if err != nil {
				return err
			}
			proof, err := s.Probe.InspectMP4(wait, f)
			f.Close()
			if err != nil || !proof.Readable || !proof.Video.FirstFrame || proof.Size != c.Size {
				return storage.ErrMediaProof
			}
			e := zlm.WriteEvidence{PoolID: poolID, RecordingID: run, FilePath: c.FilePath, Size: proof.Size, Duration: proof.Duration, VideoVerified: true, ObservedAt: time.Now().UTC()}
			if err := s.Pools.PublishZLMEvidence(wait, poolID, run, e); err != nil {
				return err
			}
			if _, err := s.DB.Pool.Exec(wait, `UPDATE hook_inbox SET state='processed',processed_at=clock_timestamp() WHERE id=$1 AND state='pending'`, inboxID); err != nil {
				return err
			}
			// Probe media is never a formal recording segment. Remove only this verified
			// file through the registered descriptor; crash leftovers remain recoverable.
			if err := root.Remove(fileRelative); err != nil {
				return storage.ErrMediaProof
			}
			success = true
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		select {
		case <-wait.Done():
			return wait.Err()
		case <-ticker.C:
		}
	}
}
