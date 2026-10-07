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
			var want struct {
				Device, Inode  uint64
				Size, Modified int64
			}
			stat, ok := info.Sys().(*syscall.Stat_t)
			if json.Unmarshal(location.Identity, &want) != nil || !ok || want.Device != uint64(stat.Dev) || want.Inode != uint64(stat.Ino) || want.Size != info.Size() || want.Modified != info.ModTime().UnixNano() || info.Size() != location.Size {
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
