package recording

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/storage"
)

func (s *Service) List(ctx context.Context, p auth.Principal, q Query) (Page, error) {
	out := Page{Items: []Segment{}}
	if q.Start.IsZero() || q.End.IsZero() || !q.End.After(q.Start) || q.End.Sub(q.Start) > 31*24*time.Hour || q.Limit < 1 || q.Limit > 100 || s.Auth == nil {
		return out, auth.ErrInvalid
	}
	var publications []publication
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := s.Auth.RequireChannelTx(ctx, p, q.ChannelID, auth.Playback, tx); err != nil {
			if errors.Is(err, auth.ErrForbidden) {
				return auth.ErrNotFound
			}
			return err
		}
		cursorTime := time.Time{}
		if q.Cursor != "" {
			if err := tx.QueryRow(ctx, "SELECT start_at FROM recording_segments WHERE id=$1 AND channel_id=$2", q.Cursor, q.ChannelID).Scan(&cursorTime); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return auth.ErrNotFound
				}
				return err
			}
		}
		rows, err := tx.Query(ctx, "SELECT "+publicationColumns+` FROM recording_segments s WHERE s.channel_id=$1 AND s.start_at<$3 AND s.end_at>$2 AND ($4::uuid IS NULL OR (s.start_at,s.id)<($5,$4::uuid)) ORDER BY s.start_at DESC,s.id DESC LIMIT $6`, q.ChannelID, q.Start.UTC(), q.End.UTC(), nullableID(q.Cursor), cursorTime, q.Limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			item, err := scanPublication(rows)
			if err != nil {
				return err
			}
			publications = append(publications, item)
		}
		return rows.Err()
	})
	if err != nil {
		return out, err
	}
	if len(publications) > q.Limit {
		publications = publications[:q.Limit]
		cursor := publications[len(publications)-1].ID
		out.NextCursor = &cursor
	}
	for _, item := range publications {
		if item.State == "ready" {
			root, _, err := s.Pools.OpenMediaRoot(ctx, item.PoolID)
			if err != nil {
				item.CheckState = "pool_unavailable"
			} else {
				file, openErr := storage.OpenMediaFile(root, item.Target)
				if openErr != nil {
					_, missingErr := root.Lstat(item.Target)
					if os.IsNotExist(missingErr) {
						item.State = "missing"
						item.CheckState = "missing"
					} else {
						item.CheckState = "media_unavailable"
					}
				} else {
					info, statErr := file.Stat()
					file.Close()
					if statErr != nil {
						item.CheckState = "media_unavailable"
					} else if !item.Identity.matches(info) {
						item.State = "damaged"
						item.CheckState = "damaged"
					}
				}
				root.Close()
			}
			if item.State == "missing" || item.State == "damaged" {
				_, err := s.DB.Pool.Exec(ctx, `UPDATE recording_segments SET state=$2,error_code=$2 WHERE id=$1 AND state='ready'`, item.ID, item.State)
				if err != nil {
					return Page{}, err
				}
				_, err = s.DB.Pool.Exec(ctx, `UPDATE recording_locations SET state=$2,verified_at=clock_timestamp() WHERE segment_id=$1`, item.ID, item.State)
				if err != nil {
					return Page{}, err
				}
			}
		}
		out.Items = append(out.Items, item.Segment)
	}
	return out, nil
}
func nullableID(value id.ID) any {
	if value == "" {
		return nil
	}
	return value
}

func (s *Service) AuthorizeSegment(ctx context.Context, p auth.Principal, recordingID id.ID) error {
	if s.Auth == nil {
		return auth.ErrNotFound
	}
	return s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		var channelID id.ID
		if err := tx.QueryRow(ctx, "SELECT channel_id FROM recording_segments WHERE id=$1", recordingID).Scan(&channelID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return auth.ErrNotFound
			}
			return err
		}
		if err := s.Auth.RequireChannelTx(ctx, p, channelID, auth.Playback, tx); err != nil {
			if errors.Is(err, auth.ErrForbidden) {
				return auth.ErrNotFound
			}
			return err
		}
		return nil
	})
}
