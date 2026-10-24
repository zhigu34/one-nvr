package storage

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"syscall"
	"time"

	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/id"
)

// PRD FILE-04: the index must not claim footage that is not there.
//
// The check runs in the worker, on its own cadence, rather than as a side effect
// of an admin opening the storage page. The previous shape had three defects that
// compounded:
//
//   - it only ran when someone listed pools, so a site whose page was never
//     opened never reconciled at all;
//   - it walked with a fixed three-second budget and no cursor, so a pool larger
//     than the budget re-examined the first rows on every request and never
//     reached the rest — the missing file was always in the part it skipped;
//   - one unreadable file (permissions, an I/O error, a directory where a file
//     is expected) aborted the whole pool scan, which froze reconciliation for
//     every other recording on that pool.
//
// The pass below is bounded by a row count instead of wall-clock time, ordered by
// `verified_at` so successive passes walk the whole pool, and treats a file it
// cannot read as "no evidence" rather than as damage.

const (
	// ReconcileInterval is how often the worker verifies the index against disk.
	// The work is a stat per file and the pool is walked in batches, so ten
	// minutes keeps the index honest without competing with recording I/O.
	ReconcileInterval = 10 * time.Minute
	// reconcileBatch bounds one pool's slice per pass. Every check is one stat
	// plus one small update; a batch this size finishes in well under a second on
	// a NAS, and the cursor design means a larger pool simply takes several
	// passes.
	reconcileBatch = 512
)

// RunReconcile drives the verification pass until the context ends.
func (s *Service) RunReconcile(ctx context.Context) {
	ticker := time.NewTicker(ReconcileInterval)
	defer ticker.Stop()
	for {
		if err := s.ReconcilePools(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("storage reconciliation unavailable", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// ReconcilePools runs one bounded pass over every enabled pool.
//
// Pools are independent: one that cannot be opened (its mount is gone, its
// marker is missing) is skipped and the others still run.
func (s *Service) ReconcilePools(ctx context.Context) error {
	rows, err := s.DB.Pool.Query(ctx, "SELECT "+poolColumns+" FROM storage_pools WHERE enabled ORDER BY created_at,id")
	if err != nil {
		return err
	}
	var pools []Pool
	for rows.Next() {
		pool, err := scanPool(rows)
		if err != nil {
			rows.Close()
			return err
		}
		pools = append(pools, pool)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, pool := range pools {
		if ctx.Err() != nil {
			return nil
		}
		if _, err := s.VerifyBatch(ctx, pool, reconcileBatch); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			// A broken database connection is not a per-file problem; report it
			// so the loop logs rather than silently skipping every pool.
			return err
		}
	}
	return nil
}

// VerifyBatch reconciles up to `limit` ready locations of one pool.
//
// Ordering by `verified_at` is the cursor: every successfully verified row gets a
// fresh timestamp, so the next pass picks the files that have waited longest.
// Files the pass cannot examine keep their old timestamp — they are retried, but
// they occupy one slot in each batch rather than blocking the rest.
func (s *Service) VerifyBatch(ctx context.Context, pool Pool, limit int) (int, error) {
	if limit <= 0 || limit > 4096 {
		limit = reconcileBatch
	}
	root, _, err := s.OpenMediaRoot(ctx, pool.ID)
	if err != nil {
		// An unavailable pool is a state the page already reports; there is
		// nothing here to verify and nothing to change. A database error is not
		// that: swallowing it would make the pass look healthy while it did no
		// work at all.
		if errors.Is(err, ErrMediaProof) || errors.Is(err, auth.ErrNotFound) {
			return 0, nil
		}
		return 0, err
	}
	defer root.Close()
	rows, err := s.DB.Pool.Query(ctx, `SELECT l.segment_id,l.relative_path,l.size_bytes,s.file_identity
		FROM recording_locations l JOIN recording_segments s ON s.id=l.segment_id
		WHERE l.pool_id=$1 AND l.state='ready' AND s.state='ready'
		ORDER BY l.verified_at, l.segment_id
		LIMIT $2`, pool.ID, limit)
	if err != nil {
		return 0, err
	}
	type location struct {
		ID       id.ID
		Path     string
		Size     int64
		Identity []byte
	}
	var locations []location
	for rows.Next() {
		var l location
		if err := rows.Scan(&l.ID, &l.Path, &l.Size, &l.Identity); err != nil {
			rows.Close()
			return 0, err
		}
		locations = append(locations, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	checked := 0
	for _, location := range locations {
		if ctx.Err() != nil {
			return checked, nil
		}
		if err := s.verifyLocation(ctx, root, pool, location.ID, location.Path, location.Size, location.Identity); err != nil {
			return checked, err
		}
		checked++
	}
	return checked, nil
}

// verifyLocation applies one file's verdict to the index.
//
// The three outcomes stay distinguishable, which is the whole point:
//
//   - the file is gone: `missing`;
//   - the file is there but is not the published one: `damaged`;
//   - the file cannot be examined at all (permissions, an I/O error, a directory
//     at the path, a symlink): unchanged. We have no evidence of loss, and
//     claiming damage would be a verdict the evidence does not support. The row
//     is retried on a later pass.
func (s *Service) verifyLocation(ctx context.Context, root *os.Root, pool Pool, segmentID id.ID, relative string, locatedSize int64, identity []byte) error {
	file, err := OpenMediaFile(root, relative)
	if err != nil {
		if _, statErr := root.Lstat(relative); !os.IsNotExist(statErr) {
			return nil
		}
		// The state guard matters: the deletion core unlinks the file and only
		// then commits `deleted`. Without `state='ready'` a stat landing in that
		// window — or any later pass — would overwrite a deliberate deletion with
		// 'missing', and the two are required to stay distinguishable (PRD
		// FILE-04).
		if _, err := s.DB.Pool.Exec(ctx, `UPDATE recording_locations SET state='missing',verified_at=clock_timestamp()
			WHERE segment_id=$1 AND pool_id=$2 AND state='ready'`, segmentID, pool.ID); err != nil {
			return err
		}
		_, err := s.DB.Pool.Exec(ctx, `UPDATE recording_segments SET state='missing',error_code='missing'
			WHERE id=$1 AND state='ready'`, segmentID)
		return err
	}
	info, statErr := file.Stat()
	file.Close()
	if statErr != nil {
		return nil
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || fileDamage(identity, locatedSize, uint64(stat.Dev), uint64(stat.Ino), info.Size(), info.ModTime().UnixNano()) {
		if _, err := s.DB.Pool.Exec(ctx, `UPDATE recording_locations SET state='damaged',verified_at=clock_timestamp()
			WHERE segment_id=$1 AND pool_id=$2 AND state='ready'`, segmentID, pool.ID); err != nil {
			return err
		}
		_, err := s.DB.Pool.Exec(ctx, `UPDATE recording_segments SET state='damaged',error_code='damaged'
			WHERE id=$1 AND state='ready'`, segmentID)
		return err
	}
	// A healthy row moves to the back of the queue. This is the only write a
	// successful check performs, and it is also the cursor.
	_, err = s.DB.Pool.Exec(ctx, `UPDATE recording_locations SET verified_at=clock_timestamp()
		WHERE segment_id=$1 AND pool_id=$2 AND state='ready'`, segmentID, pool.ID)
	return err
}
