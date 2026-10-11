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
	if q.Start.IsZero() || q.End.IsZero() || !q.End.After(q.Start) || q.End.Sub(q.Start) > 31*24*time.Hour || q.Limit < 1 || q.Limit > 500 || s.Auth == nil {
		return out, auth.ErrInvalid
	}
	if len(q.States) > 8 {
		return out, auth.ErrInvalid
	}
	for _, state := range q.States {
		if !validState(state) {
			return out, auth.ErrInvalid
		}
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
		// The optional filters are all "no restriction when empty" so the
		// playback caller passes none and keeps its exact previous behaviour.
		rows, err := tx.Query(ctx, "SELECT "+publicationColumns+` FROM recording_segments s WHERE s.channel_id=$1 AND s.start_at<$3 AND s.end_at>$2 AND ($4::uuid IS NULL OR (s.start_at,s.id)<($5,$4::uuid)) AND ($7::uuid IS NULL OR s.source_revision_id=$7) AND ($8::uuid IS NULL OR s.pool_id=$8) AND ($9::text[] IS NULL OR s.state=ANY($9)) AND ($10::boolean IS NULL OR s.protected=$10) ORDER BY s.start_at DESC,s.id DESC LIMIT $6`, q.ChannelID, q.Start.UTC(), q.End.UTC(), nullableID(q.Cursor), cursorTime, q.Limit+1, nullableID(q.SourceRevisionID), nullableID(q.PoolID), nullableStates(q.States), q.Protected)
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
	if err := s.verifyPublished(ctx, publications); err != nil {
		return Page{}, err
	}
	for _, item := range publications {
		out.Items = append(out.Items, item.Segment)
	}
	return out, nil
}

// verifyPublished confirms each published file still matches the identity
// recorded at publication and repairs the index when it does not.
//
// One root is opened per pool rather than per segment: opening a pool costs a
// canonical-path resolution and a marker read, and the management view pages up
// to 500 rows that overwhelmingly share a handful of pools. Concurrent callers
// therefore must not observe a partially updated batch between two pools, but
// each pool's own repair stays consistent.
func (s *Service) verifyPublished(ctx context.Context, items []publication) error {
	roots := map[id.ID]*os.Root{}
	defer func() {
		for _, root := range roots {
			root.Close()
		}
	}()
	for i := range items {
		item := &items[i]
		if item.State != "ready" {
			continue
		}
		root, ok := roots[item.PoolID]
		if !ok {
			opened, _, err := s.Pools.OpenMediaRoot(ctx, item.PoolID)
			if err != nil {
				item.CheckState = "pool_unavailable"
				continue
			}
			root = opened
			roots[item.PoolID] = root
		}
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
		if item.State == "missing" || item.State == "damaged" {
			if err := s.markDegraded(ctx, item.ID, item.State); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) markDegraded(ctx context.Context, segmentID id.ID, state string) error {
	if _, err := s.DB.Pool.Exec(ctx, `UPDATE recording_segments SET state=$2,error_code=$2 WHERE id=$1 AND state='ready'`, segmentID, state); err != nil {
		return err
	}
	_, err := s.DB.Pool.Exec(ctx, `UPDATE recording_locations SET state=$2,verified_at=clock_timestamp() WHERE segment_id=$1 AND state<>'deleted'`, segmentID, state)
	return err
}

func validState(state string) bool {
	switch state {
	case "finalizing", "ready", "missing", "damaged", "conflict", "provisional", "deleted":
		return true
	}
	return false
}

// nullableStates encodes "no restriction" as SQL NULL rather than an empty
// array, so an empty filter means every state instead of none.
func nullableStates(states []string) any {
	if len(states) == 0 {
		return nil
	}
	return states
}
func nullableID(value id.ID) any {
	if value == "" {
		return nil
	}
	return value
}

// Summarize reports what the current filter window covers, so the management
// view can show the scope of a selection before anything is deleted. It shares
// the List filters exactly: the numbers describe the same rows the page shows.
//
// DeletedBytes is the space deliberate deletion already reclaimed and
// LogicalBytes is what the index still describes; the two are reported
// separately rather than summed, because adding them would count reclaimed
// media as if it were still occupying the pool.
func (s *Service) Summarize(ctx context.Context, p auth.Principal, q Query) (Stats, error) {
	var out Stats
	if q.Start.IsZero() || q.End.IsZero() || !q.End.After(q.Start) || q.End.Sub(q.Start) > 31*24*time.Hour || s.Auth == nil {
		return out, auth.ErrInvalid
	}
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := s.Auth.RequireChannelTx(ctx, p, q.ChannelID, auth.Playback, tx); err != nil {
			if errors.Is(err, auth.ErrForbidden) {
				return auth.ErrNotFound
			}
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*),
			count(*) FILTER (WHERE state='ready'),
			count(*) FILTER (WHERE state IN ('missing','damaged','conflict')),
			count(*) FILTER (WHERE protected),
			count(*) FILTER (WHERE state='deleted'),
			coalesce(sum(extract(epoch from (end_at-start_at))*1000)::bigint,0),
			coalesce(sum(size_bytes) FILTER (WHERE state<>'deleted'),0),
			coalesce(sum(size_bytes) FILTER (WHERE state='deleted'),0),
			min(start_at),max(end_at)
			FROM recording_segments s
			WHERE s.channel_id=$1 AND s.start_at<$3 AND s.end_at>$2
			  AND ($4::uuid IS NULL OR s.source_revision_id=$4)
			  AND ($5::uuid IS NULL OR s.pool_id=$5)
			  AND ($6::text[] IS NULL OR s.state=ANY($6))
			  AND ($7::boolean IS NULL OR s.protected=$7)`,
			q.ChannelID, q.Start.UTC(), q.End.UTC(), nullableID(q.SourceRevisionID), nullableID(q.PoolID), nullableStates(q.States), q.Protected).
			Scan(&out.Segments, &out.Ready, &out.Anomalous, &out.Protected, &out.Deleted, &out.DurationMS, &out.LogicalBytes, &out.DeletedBytes, &out.OldestStart, &out.NewestEnd)
	})
	return out, err
}
