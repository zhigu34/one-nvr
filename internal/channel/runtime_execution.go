package channel

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/database"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/jobs"
)

// Runtime sampling shares explicit source jobs' channel lock, but uses one
// replaceable configuration-fenced lease instead of accumulating polling jobs.
func NewRuntimeExecution(ctx context.Context, db *database.DB, ch id.ID) (*Execution, error) {
	bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	conn, err := db.Pool.Acquire(bounded)
	if err != nil {
		return nil, err
	}
	var locked bool
	if err := conn.QueryRow(bounded, "SELECT pg_try_advisory_lock(hashtextextended($1,170019))", string(ch)).Scan(&locked); err != nil || !locked {
		conn.Release()
		if err != nil {
			return nil, err
		}
		return nil, ErrExecutionBusy
	}
	token, err := id.New()
	if err != nil {
		conn.Conn().Close(bounded)
		conn.Release()
		return nil, err
	}
	work, stop := context.WithCancel(ctx)
	e := &Execution{Conn: conn, ChannelID: ch, Runtime: true, Lease: jobs.Lease{Kind: "recording.monitor", FencingToken: token}, ctx: work, cancel: stop, done: make(chan struct{})}
	err = conn.QueryRow(bounded, `INSERT INTO channel_runtime_leases(channel_id,fencing_token,configuration_version,expires_at) SELECT id,$2,version,clock_timestamp()+interval '30 seconds' FROM channels WHERE id=$1 AND NOT EXISTS(SELECT 1 FROM source_switches WHERE channel_id=$1 AND state IN ('queued','running')) ON CONFLICT(channel_id) DO UPDATE SET fencing_token=EXCLUDED.fencing_token,configuration_version=EXCLUDED.configuration_version,expires_at=EXCLUDED.expires_at RETURNING configuration_version`, ch, token).Scan(&e.RuntimeVersion)
	if err == nil {
		err = e.Check(bounded)
	}
	if err != nil {
		stop()
		e.unlock()
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrExecutionBusy
		}
		return nil, err
	}
	go func() {
		defer close(e.done)
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-work.Done():
				return
			case <-ticker.C:
				if err := e.Check(work); err != nil {
					stop()
					return
				}
			}
		}
	}()
	return e, nil
}
