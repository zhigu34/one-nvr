package recording

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/channel"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/media/zlm"
	"github.com/zhigu34/one-nvr/internal/storage"
)

type StartInput struct{ SessionID, PoolID id.ID }
type Handle struct {
	RunID, SessionID, PoolID id.ID
	Key                      zlm.StreamKey
	RelativePath             string
}

// Start persists a uniquely mapped run before calling ZLM. An interrupted
// response is reconciled against that same physical stream and run receipt.
func (s *Service) Start(ctx context.Context, e *channel.Execution, in StartInput) (Handle, error) {
	var out Handle
	if err := e.Check(ctx); err != nil {
		return out, err
	}
	root, pool, err := s.Pools.OpenMediaRoot(ctx, in.PoolID)
	if err != nil {
		return out, err
	}
	defer root.Close()
	if !pool.Enabled || pool.SiteID != s.siteID {
		return out, storage.ErrMediaProof
	}
	var ready bool
	if err := s.DB.Pool.QueryRow(ctx, "SELECT count(*)=3 FROM storage_pool_checks WHERE pool_id=$1 AND state='healthy' AND expires_at>clock_timestamp()", in.PoolID).Scan(&ready); err != nil {
		return out, err
	}
	if !ready {
		return out, storage.ErrMediaProof
	}
	if err := e.Check(ctx); err != nil {
		return out, err
	}
	var ss physicalSession
	err = e.WithinTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		ss, err = scanSession(tx.QueryRow(ctx, "SELECT "+physicalColumns+" FROM stream_sessions WHERE id=$1 AND channel_id=$2 AND purpose='main' AND state='active' FOR UPDATE", in.SessionID, e.ChannelID))
		return err
	})
	if err != nil {
		return out, err
	}
	if err := e.Check(ctx); err != nil {
		return out, err
	}
	snapshot, err := s.Media.Inspect(ctx, ss.Key)
	if err != nil {
		return out, err
	}
	err = e.WithinTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var state string
		err := tx.QueryRow(ctx, `SELECT id,stream_session_id,pool_id,work_relative_path,state FROM recording_runs WHERE stream_session_id=$1 AND state IN ('starting','recording','stopping') FOR UPDATE`, ss.ID).Scan(&out.RunID, &out.SessionID, &out.PoolID, &out.RelativePath, &state)
		if err == nil {
			if out.PoolID != in.PoolID || state == "stopping" {
				return storage.ErrMediaProof
			}
			if snapshot.Recording || state == "starting" {
				return nil
			}
			// The upstream recorder actually stopped. Preserve the old run and assign
			// a fresh path for an actual restart instead of rewriting its identity.
			if _, err := tx.Exec(ctx, "UPDATE recording_runs SET state='stopped',ended_at=clock_timestamp() WHERE id=$1", out.RunID); err != nil {
				return err
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		} else if snapshot.Recording {
			return ErrPublicationConflict
		}
		run, err := id.New()
		if err != nil {
			return err
		}
		out = Handle{RunID: run, SessionID: ss.ID, PoolID: in.PoolID, RelativePath: ".work/zlm/" + string(run)}
		_, err = tx.Exec(ctx, `INSERT INTO recording_runs(id,site_id,channel_id,source_revision_id,stream_session_id,pool_id,work_relative_path,purpose) VALUES($1,$2,$3,$4,$5,$6,$7,'continuous')`, run, s.siteID, e.ChannelID, ss.RevisionID, ss.ID, in.PoolID, out.RelativePath)
		return err
	})
	if err != nil {
		return Handle{}, err
	}
	out.Key = ss.Key
	parent, err := openParent(root, out.RelativePath+"/.run-intent", true)
	if err != nil {
		return out, err
	}
	parent.Close()
	if err := e.Check(ctx); err != nil {
		return out, err
	}
	if err := s.DescribeRun(ctx, out.RunID); err != nil {
		return out, err
	}
	if !snapshot.Recording {
		if err := e.Check(ctx); err != nil {
			return out, err
		}
		if err := s.Media.StartRecord(ctx, out.Key, filepath.Join(pool.Path, out.RelativePath), 60); err != nil {
			return out, err
		}
	}
	if err := e.Check(ctx); err != nil {
		return out, err
	}
	snapshot, err = s.Media.Inspect(ctx, out.Key)
	if err != nil || !snapshot.Recording {
		if err != nil {
			return out, err
		}
		return out, zlm.ErrMediaOperation
	}
	err = e.WithinTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "UPDATE recording_runs SET state='recording',started_at=coalesce(started_at,clock_timestamp()),error_code=NULL WHERE id=$1", out.RunID)
		return err
	})
	return out, err
}

func (s *Service) Stop(ctx context.Context, e *channel.Execution, h Handle) error {
	if err := e.WithinTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, "UPDATE recording_runs SET state='stopping' WHERE id=$1 AND channel_id=$2 AND stream_session_id=$3 AND state IN ('starting','recording','stopping')", h.RunID, e.ChannelID, h.SessionID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrPublicationConflict
		}
		return nil
	}); err != nil {
		return err
	}
	if err := e.Check(ctx); err != nil {
		return err
	}
	snapshot, err := s.Media.Inspect(ctx, h.Key)
	if err != nil && !errors.Is(err, zlm.ErrStreamAbsent) {
		return err
	}
	if err == nil && snapshot.Recording {
		if err := e.Check(ctx); err != nil {
			return err
		}
		if err := s.Media.StopRecord(ctx, h.Key); err != nil {
			return err
		}
		if err := e.Check(ctx); err != nil {
			return err
		}
		after, err := s.Media.Inspect(ctx, h.Key)
		if err != nil && !errors.Is(err, zlm.ErrStreamAbsent) {
			return err
		}
		if err == nil && after.Recording {
			return zlm.ErrMediaOperation
		}
	}
	return e.WithinTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "UPDATE recording_runs SET state='stopped',ended_at=coalesce(ended_at,clock_timestamp()) WHERE id=$1", h.RunID)
		return err
	})
}
