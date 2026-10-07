package channel

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zhigu34/one-nvr/internal/database"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/jobs"
)

var ErrExecutionBusy = errors.New("channel_execution_busy")

// Execution owns one dedicated connection's channel advisory lock. Every
// mutation checks its job lease in the same transaction, and every external
// call checks the connection and lease immediately beforehand.
type Execution struct {
	Conn           *pgxpool.Conn
	Lease          jobs.Lease
	ChannelID      id.ID
	Runtime        bool
	RuntimeVersion int64
	ctx            context.Context
	cancel         context.CancelFunc
	done           chan struct{}
	mu             sync.Mutex
}

func NewExecution(ctx context.Context, db *database.DB, lease jobs.Lease, ch id.ID) (*Execution, error) {
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
	work, stop := context.WithCancel(ctx)
	e := &Execution{Conn: conn, Lease: lease, ChannelID: ch, ctx: work, cancel: stop, done: make(chan struct{})}
	// Validate before handing ownership to the caller.
	if err := e.Check(bounded); err != nil {
		stop()
		e.unlock()
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
func (e *Execution) Context() context.Context { return e.ctx }
func (e *Execution) Check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.ctx.Err(); err != nil {
		return err
	}
	// A media-operation deadline does not revoke a live channel owner. Check
	// ownership under its own bounded connection context so rollback can proceed.
	bounded, cancel := context.WithTimeout(e.ctx, 3*time.Second)
	defer cancel()
	e.mu.Lock()
	defer e.mu.Unlock()
	var object id.ID
	var err error
	if e.Runtime {
		err = e.Conn.QueryRow(bounded, `UPDATE channel_runtime_leases l SET expires_at=CASE WHEN expires_at<clock_timestamp()+interval '20 seconds' THEN clock_timestamp()+interval '30 seconds' ELSE expires_at END FROM channels c WHERE c.id=l.channel_id AND l.channel_id=$1 AND l.fencing_token=$2 AND l.configuration_version=$3 AND c.version=$3 AND l.expires_at>clock_timestamp() RETURNING l.channel_id`, e.ChannelID, e.Lease.FencingToken, e.RuntimeVersion).Scan(&object)
	} else {
		err = e.Conn.QueryRow(bounded, `SELECT object_id FROM jobs WHERE id=$1 AND kind=$2 AND state='running' AND fencing_token=$3 AND attempt=$4 AND lease_expires_at>clock_timestamp()`, e.Lease.ID, e.Lease.Kind, e.Lease.FencingToken, e.Lease.Attempt).Scan(&object)
	}
	if errors.Is(err, pgx.ErrNoRows) || err == nil && object != e.ChannelID {
		err = jobs.ErrLeaseLost
	}
	if err != nil {
		e.cancel()
	}
	return err
}
func (e *Execution) WithinTx(ctx context.Context, fn func(context.Context, pgx.Tx) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.ctx.Err(); err != nil {
		return err
	}
	bounded, cancel := context.WithTimeout(e.ctx, 5*time.Second)
	defer cancel()
	e.mu.Lock()
	defer e.mu.Unlock()
	tx, err := e.Conn.Begin(bounded)
	if err != nil {
		e.cancel()
		return err
	}
	defer tx.Rollback(context.WithoutCancel(bounded))
	fence := func() error {
		var object id.ID
		var err error
		if e.Runtime {
			err = tx.QueryRow(bounded, `SELECT c.id FROM channel_runtime_leases l JOIN channels c ON c.id=l.channel_id WHERE c.id=$1 AND l.fencing_token=$2 AND l.configuration_version=$3 AND c.version=$3 AND l.expires_at>clock_timestamp() FOR UPDATE OF c,l`, e.ChannelID, e.Lease.FencingToken, e.RuntimeVersion).Scan(&object)
		} else {
			err = tx.QueryRow(bounded, `SELECT object_id FROM jobs WHERE id=$1 AND kind=$2 AND state='running' AND fencing_token=$3 AND attempt=$4 AND lease_expires_at>clock_timestamp() FOR UPDATE`, e.Lease.ID, e.Lease.Kind, e.Lease.FencingToken, e.Lease.Attempt).Scan(&object)
		}
		if errors.Is(err, pgx.ErrNoRows) || err == nil && object != e.ChannelID {
			return jobs.ErrLeaseLost
		}
		return err
	}
	if err := fence(); err != nil {
		e.cancel()
		return err
	}
	if err := fn(bounded, tx); err != nil {
		return err
	}
	if err := fence(); err != nil {
		e.cancel()
		return err
	}
	return tx.Commit(bounded)
}
func (e *Execution) unlock() {
	bounded, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var unlocked bool
	if err := e.Conn.QueryRow(bounded, "SELECT pg_advisory_unlock(hashtextextended($1,170019))", string(e.ChannelID)).Scan(&unlocked); err != nil || !unlocked {
		e.Conn.Conn().Close(bounded)
	}
	e.Conn.Release()
}
func (e *Execution) Close() { e.cancel(); <-e.done; e.mu.Lock(); defer e.mu.Unlock(); e.unlock() }
