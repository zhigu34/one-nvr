package recording

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/media/zlm"
	"time"
)

var ErrPoolProbeBusy = errors.New("pool_probe_busy")

// Separate pool lock: source switches can need a pool check while holding their
// channel lock. Loss of the pinned connection cancels private probe work.
func (s *Service) ownPoolProbe(ctx context.Context, pool id.ID) (context.Context, func(), error) {
	bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	conn, err := s.DB.Pool.Acquire(bounded)
	if err != nil {
		return nil, nil, err
	}
	var owned bool
	if err := conn.QueryRow(bounded, "SELECT pg_try_advisory_lock(hashtextextended($1,170020))", string(pool)).Scan(&owned); err != nil || !owned {
		conn.Release()
		if err != nil {
			return nil, nil, err
		}
		return nil, nil, ErrPoolProbeBusy
	}
	work, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		timer := time.NewTicker(2 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-work.Done():
				return
			case <-timer.C:
				check, finish := context.WithTimeout(work, time.Second)
				var alive int
				err := conn.QueryRow(check, "SELECT 1").Scan(&alive)
				finish()
				if err != nil {
					stop()
					return
				}
			}
		}
	}()
	release := func() {
		stop()
		<-done
		cleanup, end := context.WithTimeout(context.Background(), time.Second)
		defer end()
		var unlocked bool
		if err := conn.QueryRow(cleanup, "SELECT pg_advisory_unlock(hashtextextended($1,170020))", string(pool)).Scan(&unlocked); err != nil || !unlocked {
			conn.Conn().Close(cleanup)
		}
		conn.Release()
	}
	return work, release, nil
}
func (s *Service) recoverPoolProbe(ctx context.Context, pool id.ID) error {
	rows, err := s.DB.Pool.Query(ctx, `SELECT r.id,ss.id,ss.vhost,ss.app,ss.stream,coalesce(ss.proxy_key,ss.vhost||'/'||ss.app||'/'||ss.stream) FROM stream_sessions ss LEFT JOIN recording_runs r ON r.stream_session_id=ss.id LEFT JOIN pool_probe_intents pi ON pi.session_id=ss.id WHERE (r.pool_id=$1 OR pi.pool_id=$1) AND (r.purpose='probe' OR r.id IS NULL) AND ss.purpose='test' AND ss.source_test_id IS NULL AND ss.switch_id IS NULL AND ((pi.session_id IS NOT NULL AND ss.state<>'closed') OR r.state IN ('starting','recording','stopping')) ORDER BY ss.created_at`, pool)
	if err != nil {
		return err
	}
	type orphan struct {
		run     *id.ID
		session id.ID
		key     zlm.StreamKey
		proxy   string
	}
	var all []orphan
	for rows.Next() {
		var o orphan
		if err := rows.Scan(&o.run, &o.session, &o.key.VHost, &o.key.App, &o.key.Stream, &o.proxy); err != nil {
			rows.Close()
			return err
		}
		all = append(all, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, o := range all {
		snapshot, err := s.Media.Inspect(ctx, o.key)
		if err != nil && !errors.Is(err, zlm.ErrStreamAbsent) {
			return err
		}
		if err == nil && snapshot.Recording {
			if err := s.Media.StopRecord(ctx, o.key); err != nil {
				return err
			}
			snapshot, err = s.Media.Inspect(ctx, o.key)
			if err != nil && !errors.Is(err, zlm.ErrStreamAbsent) {
				return err
			}
			if err == nil && snapshot.Recording {
				return zlm.ErrMediaOperation
			}
		}
		if err := s.Media.RemoveProxy(ctx, zlm.ProxyRef{Key: o.key, OpaqueKey: o.proxy}); err != nil && !errors.Is(err, zlm.ErrProxyAbsent) {
			return err
		}
		if err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, "UPDATE recording_runs SET state=CASE WHEN state='failed' THEN state ELSE 'stopped' END,ended_at=coalesce(ended_at,clock_timestamp()),error_code=coalesce(error_code,'pool_probe_interrupted') WHERE id=$1 AND purpose='probe'", o.run); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, "UPDATE stream_sessions SET state='closed',closed_at=coalesce(closed_at,clock_timestamp()) WHERE id=$1 AND purpose='test'", o.session)
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}
