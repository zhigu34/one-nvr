package storage

import (
	"context"
	"encoding/json"
)

// indexedUsage is the size of the pool's ready index entries.
//
// It deliberately does not touch the filesystem. Verifying that every file is
// still on disk is the background reconciler's job (see reconcile.go), because a
// page load cannot do it honestly: the old implementation walked the pool with a
// three-second budget, ordered by segment id, and restarted from the first row on
// every request. A pool too large for the budget re-checked the same first rows
// forever and never reached the rest, so the file that had gone missing was
// exactly the one it never looked at. Worse, the number it reported was a
// side effect of how far it got, and the same page could answer differently
// depending on how busy the disk was.
//
// The index's own total is cheap, stable, and is what the page actually shows:
// how much recorded material the system believes it has. The reconciler is what
// corrects the index when the files disagree.
func (s *Service) indexedUsage(ctx context.Context, pool Pool) (*int64, error) {
	var total int64
	err := s.DB.Pool.QueryRow(ctx, `SELECT COALESCE(SUM(l.size_bytes),0)
		FROM recording_locations l JOIN recording_segments s ON s.id=l.segment_id
		WHERE l.pool_id=$1 AND l.state='ready' AND s.state='ready'`, pool.ID).Scan(&total)
	if err != nil {
		return nil, err
	}
	return &total, nil
}

// fileDamage reports whether the file on disk is no longer the one that was
// published.
//
// Two things prove that:
//
//   - the same inode on the same device. A different inode means something
//     replaced the file;
//   - the size matches both the recorded identity and the location. A truncated
//     file fails here.
//
// mtime is deliberately NOT part of the verdict. It moves when a file is copied,
// restored from a backup, or touched by anything that rewrites metadata, none of
// which alter the footage. Measured on the live site: 45 segments were quarantined
// as damaged while all 45 probed clean and played normally, purely because their
// mtime had moved.
//
// An empty identity means "never captured", not "changed": the column defaults to
// {} and some writers never filled it. Treating that as a mismatch marked intact
// files damaged on the next pass — the size comparison still catches truncation.
func fileDamage(identity []byte, locatedSize int64, device, inode uint64, size, modified int64) bool {
	var want struct {
		Device, Inode  uint64
		Size, Modified int64
	}
	if json.Unmarshal(identity, &want) != nil {
		return true
	}
	captured := len(identity) > 2
	if captured && (want.Device != device || want.Inode != inode) {
		return true
	}
	if captured && want.Size != size {
		return true
	}
	return size != locatedSize
}
