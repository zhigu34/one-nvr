package storage

import (
	"context"
	"encoding/json"
	"os"
	"syscall"
	"time"

	"github.com/zhigu34/one-nvr/internal/id"
)

// verifiedUsage measures indexed ready locations only. Native working files and
// unrelated mounted content have unknown usage; no mount-wide recursive scan.
func (s *Service) verifiedUsage(ctx context.Context, pool Pool) (*int64, error) {
	root, _, err := s.OpenMediaRoot(ctx, pool.ID)
	if err != nil {
		return nil, nil
	}
	defer root.Close()
	bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	type location struct {
		ID       id.ID
		Path     string
		Size     int64
		Identity []byte
	}
	var after any
	var total int64
	for {
		if bounded.Err() != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, nil
		}
		rows, err := s.DB.Pool.Query(bounded, `SELECT l.segment_id,l.relative_path,l.size_bytes,s.file_identity FROM recording_locations l JOIN recording_segments s ON s.id=l.segment_id WHERE l.pool_id=$1 AND l.state='ready' AND s.state='ready' AND ($2::uuid IS NULL OR l.segment_id>$2::uuid) ORDER BY l.segment_id LIMIT 256`, pool.ID, after)
		if err != nil {
			if bounded.Err() != nil && ctx.Err() == nil {
				return nil, nil
			}
			return nil, err
		}
		var locations []location
		for rows.Next() {
			var l location
			if err := rows.Scan(&l.ID, &l.Path, &l.Size, &l.Identity); err != nil {
				rows.Close()
				return nil, err
			}
			locations = append(locations, l)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			if bounded.Err() != nil && ctx.Err() == nil {
				return nil, nil
			}
			return nil, err
		}
		for _, location := range locations {
			if bounded.Err() != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				return nil, nil
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			file, err := OpenMediaFile(root, location.Path)
			if err != nil {
				if _, statErr := root.Lstat(location.Path); !os.IsNotExist(statErr) {
					return nil, nil
				}
				if _, err := s.DB.Pool.Exec(ctx, `UPDATE recording_locations SET state='missing',verified_at=clock_timestamp() WHERE segment_id=$1 AND pool_id=$2`, location.ID, pool.ID); err != nil {
					return nil, err
				}
				if _, err := s.DB.Pool.Exec(ctx, `UPDATE recording_segments SET state='missing',error_code='missing' WHERE id=$1 AND state='ready'`, location.ID); err != nil {
					return nil, err
				}
				continue
			}
			info, statErr := file.Stat()
			file.Close()
			if statErr != nil {
				return nil, nil
			}
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok || fileDamage(location.Identity, location.Size, uint64(stat.Dev), uint64(stat.Ino), info.Size(), info.ModTime().UnixNano()) {
				if _, err := s.DB.Pool.Exec(ctx, `UPDATE recording_locations SET state='damaged',verified_at=clock_timestamp() WHERE segment_id=$1 AND pool_id=$2`, location.ID, pool.ID); err != nil {
					return nil, err
				}
				if _, err := s.DB.Pool.Exec(ctx, `UPDATE recording_segments SET state='damaged',error_code='damaged' WHERE id=$1 AND state='ready'`, location.ID); err != nil {
					return nil, err
				}
				continue
			}
			total = saturatingAdd(total, location.Size)
		}
		if len(locations) < 256 {
			break
		}
		after = locations[len(locations)-1].ID
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
