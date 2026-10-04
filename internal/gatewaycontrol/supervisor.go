package gatewaycontrol

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"syscall"
	"time"
)

// Supervise owns the Nginx child and the mailbox loop in the same container.
// Exiting either side terminates the container; no Docker control API is used.
func Supervise(ctx context.Context, n *Nginx) error {
	controller := &Controller{Directory: n.Directory, Runtime: n}
	parsed, err := url.Parse(n.PublicURL)
	if err != nil {
		return err
	}
	https := parsed.Scheme == "https"
	target, err := controller.BootstrapID()
	if err != nil {
		return err
	}
	if !https {
		if err = controller.CancelForHTTP(ctx); err != nil {
			return err
		}
		target = ""
	}
	if https {
		// A pending intent is validated at current time; an already committed
		// version may still be served after expiry so its status remains observable.
		state, err := controller.State()
		if err != nil {
			return err
		}
		if state.ActiveID != nil && *state.ActiveID == target {
			_, err = n.PrepareHistorical(ctx, target)
		} else {
			_, err = n.Prepare(ctx, target)
		}
		if err != nil {
			if state.ActiveID == nil {
				target = ""
				_, err = n.Prepare(ctx, target)
			} else {
				target = *state.ActiveID
				_, err = n.PrepareHistorical(ctx, target)
			}
		}
	} else {
		_, err = n.Prepare(ctx, target)
	}
	if err != nil {
		return ErrFailed
	}
	if err = n.Switch(ctx, target); err != nil {
		return err
	}
	process, err := n.Start(ctx)
	if err != nil {
		return err
	}
	ended := make(chan error, 1)
	go func() { ended <- process.Wait() }()
	defer func() {
		_ = process.Process.Signal(syscall.SIGQUIT)
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		select {
		case <-ended:
		case <-timer.C:
			_ = process.Process.Kill()
			<-ended
		}
	}()
	if https && target != "" {
		metadata, err := n.PrepareHistorical(ctx, target)
		if err != nil {
			return err
		}
		if !controller.verify(ctx, metadata) {
			return ErrFailed
		}
	}
	if https {
		if err = controller.Reconcile(ctx); err != nil {
			slog.Warn("gateway interrupted application is not yet reconciled")
		}
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	nextReconcile := time.Now().Add(10 * time.Second)
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-ended:
			// Put completion back for the deferred supervisor to reap once.
			ended <- err
			if err == nil {
				return errors.New("nginx exited unexpectedly")
			}
			return ErrFailed
		case <-ticker.C:
			if https && !time.Now().Before(nextReconcile) {
				_ = controller.Reconcile(ctx)
				nextReconcile = time.Now().Add(10 * time.Second)
			}
			if err = controller.ProcessRequests(ctx, https); err != nil && ctx.Err() == nil {
				slog.Warn("gateway application remains pending")
			}
		}
	}
}
