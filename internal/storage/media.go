package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/media/probe"
	"github.com/zhigu34/one-nvr/internal/media/zlm"
)

var ErrMediaProof = errors.New("zlm_write_evidence_unavailable")

type MediaInspector interface {
	InspectMP4(context.Context, *os.File) (probe.FileEvidence, error)
}

func (s *Service) OpenMediaRoot(ctx context.Context, poolID id.ID) (*os.Root, Pool, error) {
	if err := ctx.Err(); err != nil {
		return nil, Pool{}, err
	}
	pool, err := scanPool(s.DB.Pool.QueryRow(ctx, "SELECT "+poolColumns+" FROM storage_pools WHERE id=$1", poolID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, Pool{}, auth.ErrNotFound
	}
	if err != nil {
		return nil, Pool{}, err
	}
	root, canonical, err := openPool(s.Roots, pool.Path)
	if err != nil {
		return nil, pool, ErrMediaProof
	}
	if canonical != pool.Path || verifyMarker(root, pool) != nil {
		root.Close()
		return nil, pool, ErrMediaProof
	}
	return root, pool, nil
}

// OpenMediaFile rejects symbolic links at every component, including links
// within the same pool that could impersonate another run's file.
func OpenMediaFile(root *os.Root, relative string) (*os.File, error) {
	if relative == "" || filepath.IsAbs(relative) || filepath.Clean(relative) != relative || relative == ".." || strings.HasPrefix(relative, "../") {
		return nil, ErrMediaProof
	}
	var path string
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		path = filepath.Join(path, part)
		info, err := root.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return nil, ErrMediaProof
		}
	}
	f, err := root.Open(relative)
	if err != nil {
		return nil, ErrMediaProof
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
		f.Close()
		return nil, ErrMediaProof
	}
	named, err := root.Lstat(relative)
	if err != nil || !os.SameFile(info, named) {
		f.Close()
		return nil, ErrMediaProof
	}
	return f, nil
}
func (s *Service) PublishZLMEvidence(ctx context.Context, poolID, runID id.ID, e zlm.WriteEvidence) error {
	now := time.Now().UTC()
	if e.PoolID != poolID || e.RecordingID != runID || e.Size <= 0 || e.Duration <= 0 || !e.VideoVerified || e.ObservedAt.Before(now.Add(-30*time.Second)) || e.ObservedAt.After(now.Add(time.Second)) || s.MediaInspector == nil {
		return ErrMediaProof
	}
	root, pool, err := s.OpenMediaRoot(ctx, poolID)
	if err != nil {
		return err
	}
	defer root.Close()
	relative, err := filepath.Rel(pool.Path, e.FilePath)
	if err != nil {
		return ErrMediaProof
	}
	f, err := OpenMediaFile(root, relative)
	if err != nil {
		return err
	}
	defer f.Close()
	initial, err := f.Stat()
	if err != nil {
		return ErrMediaProof
	}
	media, err := s.MediaInspector.InspectMP4(ctx, f)
	if err != nil || !media.Readable || media.Size != e.Size || media.Duration <= 0 || media.Video.Codec == "" || media.Video.Width <= 0 || media.Video.Height <= 0 {
		return ErrMediaProof
	}
	return s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		current, err := scanPool(tx.QueryRow(ctx, "SELECT "+poolColumns+" FROM storage_pools WHERE id=$1 FOR KEY SHARE", poolID))
		if err != nil {
			return err
		}
		if current.Path != pool.Path || verifyMarker(root, current) != nil {
			return ErrMediaProof
		}
		var work string
		if err := tx.QueryRow(ctx, `SELECT work_relative_path FROM recording_runs WHERE id=$1 AND pool_id=$2 AND site_id=$3`, runID, poolID, pool.SiteID).Scan(&work); err != nil {
			return ErrMediaProof
		}
		if !strings.HasPrefix(relative, work+string(filepath.Separator)) {
			return ErrMediaProof
		}
		var matched bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM hook_inbox WHERE run_id=$1 AND payload->>'FilePath'=$2 AND (payload->>'Size')::bigint=$3 AND state<>'diagnostic')`, runID, e.FilePath, e.Size).Scan(&matched); err != nil {
			return err
		}
		if !matched {
			return ErrMediaProof
		}
		before, err := f.Stat()
		after, statErr := root.Lstat(relative)
		if err != nil || statErr != nil || !os.SameFile(before, after) || !os.SameFile(initial, before) || !initial.ModTime().Equal(before.ModTime()) || before.Size() != e.Size || after.Size() != e.Size {
			return ErrMediaProof
		}

		_, err = tx.Exec(ctx, `INSERT INTO storage_pool_checks(pool_id,service,state,reason_code,observed_at,expires_at,total_bytes,free_bytes) VALUES($1,'zlm','healthy','zlm_media_verified',$2,$3,0,0) ON CONFLICT(pool_id,service) DO UPDATE SET state=EXCLUDED.state,reason_code=EXCLUDED.reason_code,observed_at=EXCLUDED.observed_at,expires_at=EXCLUDED.expires_at WHERE storage_pool_checks.observed_at<=EXCLUDED.observed_at`, poolID, e.ObservedAt, e.ObservedAt.Add(30*time.Second))
		return err
	})
}
