package gatewaycontrol

import (
	"context"
	"errors"
	"github.com/zhigu34/one-nvr/internal/id"
	"os"
)

func (c *Controller) Reconcile(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	root, err := controlRoot(c.Directory)
	if err != nil {
		return err
	}
	defer root.Close()
	unlock, err := lockControl(ctx, root)
	if err != nil {
		return err
	}
	defer unlock()
	if err = root.MkdirAll("results", 0700); err != nil {
		return err
	}
	return c.reconcileLocked(ctx, root)
}
func (c *Controller) reconcileLocked(ctx context.Context, root *os.Root) error {
	var in intent
	err := readJSON(root, "intent.json", &in)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = in.Request.Validate(); err != nil {
		return err
	}
	actual, err := c.Runtime.Probe(ctx)
	if err == nil && actual == in.Expected {
		_, err = c.finishSuccess(ctx, root, in)
		return err
	}
	result, err := c.restore(ctx, root, in, "interrupted_application")
	if err != nil && result.ErrorCode == "interrupted_application" {
		return nil
	}
	return err
}

// BootstrapID selects durable intent first, because a crash may have happened
// after switching the listener but before committing its success receipt.
func (c *Controller) BootstrapID() (id.ID, error) {
	root, err := controlRoot(c.Directory)
	if err != nil {
		return "", err
	}
	defer root.Close()
	var in intent
	err = readJSON(root, "intent.json", &in)
	if err == nil {
		if err = in.Request.Validate(); err != nil {
			return "", err
		}
		return in.Request.CertificateID, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	state, err := loadState(root)
	if err != nil {
		return "", err
	}
	if state.ActiveID != nil {
		return *state.ActiveID, nil
	}
	return "", nil
}
