package jobs

import (
	"context"
	"errors"
	"time"
)

// Handler must honor cancellation before every external side effect. Filesystem /
// gateway adapters additionally reconcile durable intent after lease takeover.
type Handler func(context.Context, Lease) (Result, error)

// Execute renews the fenced lease every ten seconds. A renewal failure cancels
// the handler and forbids committing its result, even if the handler returned nil.
func Execute(ctx context.Context, repo Repository, lease Lease, handler Handler) error {
	work, cancel := context.WithCancel(ctx)
	defer cancel()
	finished := make(chan struct{})
	renewDone := make(chan struct{})
	lost := make(chan struct{}, 1)
	go func() {
		defer close(renewDone)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-finished:
				return
			case <-work.Done():
				return
			case <-ticker.C:
				renewCtx, stop := context.WithTimeout(work, 5*time.Second)
				err := repo.Renew(renewCtx, lease)
				stop()
				if err != nil {
					lost <- struct{}{}
					cancel()
					return
				}
			}
		}
	}()
	result, err := handler(work, lease)
	close(finished)
	<-renewDone
	select {
	case <-lost:
		return ErrLeaseLost
	default:
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return repo.Fail(ctx, lease, "handler_failed")
	}
	return repo.Complete(ctx, lease, result)
}

// Run only consumes a registered kind, leaving unsupported operations queued.
func Run(ctx context.Context, repo Repository, kind string, handler Handler) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		lease, err := repo.Claim(ctx, kind)
		if errors.Is(err, ErrNoJob) {
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
				continue
			}
		}
		if err != nil {
			return err
		}
		if err = Execute(ctx, repo, lease, handler); err != nil && !errors.Is(err, ErrLeaseLost) {
			return err
		}
	}
}
