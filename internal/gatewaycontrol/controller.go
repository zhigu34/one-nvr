package gatewaycontrol

import (
	"context"
	"errors"
	"github.com/zhigu34/one-nvr/internal/id"
	"os"
	"sync"
	"time"
)

type GatewayState struct {
	ActiveID   *id.ID `json:"active_id"`
	PreviousID *id.ID `json:"previous_id"`
	Evidence
}
type intent struct {
	Request  ApplyRequest `json:"request"`
	Previous GatewayState `json:"previous"`
	Expected Evidence     `json:"expected"`
}
type receipt struct {
	Request ApplyRequest `json:"request"`
	Result  ApplyResult  `json:"result"`
}
type Controller struct {
	Directory     string
	Runtime       Runtime
	VerifyTimeout time.Duration
	mu            sync.Mutex
}

func loadState(root *os.Root) (GatewayState, error) {
	var state GatewayState
	err := readJSON(root, "state.json", &state)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	for _, value := range []*id.ID{state.ActiveID, state.PreviousID} {
		if value != nil {
			if _, err := id.Parse(string(*value)); err != nil {
				return GatewayState{}, ErrInvalid
			}
		}
	}
	return state, nil
}
func (c *Controller) State() (GatewayState, error) {
	root, err := controlRoot(c.Directory)
	if err != nil {
		return GatewayState{}, err
	}
	defer root.Close()
	return loadState(root)
}
func (c *Controller) verify(ctx context.Context, want Evidence) bool {
	timeout := c.VerifyTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	probe, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		actual, err := c.Runtime.Probe(probe)
		if err == nil && actual == want {
			return true
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-probe.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
}
func (c *Controller) Apply(ctx context.Context, request ApplyRequest) (ApplyResult, error) {
	result := ApplyResult{JobID: request.JobID, State: "failed", ErrorCode: "request_invalid"}
	if err := request.Validate(); err != nil {
		return result, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	root, err := controlRoot(c.Directory)
	if err != nil {
		return result, err
	}
	defer root.Close()
	unlock, err := lockControl(ctx, root)
	if err != nil {
		return result, err
	}
	defer unlock()
	if err = root.MkdirAll("results", 0700); err != nil {
		return result, err
	}
	var previous receipt
	err = readJSON(root, "results/"+string(request.JobID)+".json", &previous)
	if err == nil {
		if !sameRequest(request, previous.Request) {
			return result, ErrConflict
		}
		if previous.Result.State != "applied" {
			return previous.Result, ErrFailed
		}
		return previous.Result, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	state, err := loadState(root)
	if err != nil {
		return result, err
	}
	if !sameID(request.ExpectedActiveID, state.ActiveID) {
		return result, ErrConflict
	}
	// An unknown listener must first resolve the original durable intent. It
	// cannot be superseded or given a terminal receipt without verified evidence.
	if err = c.reconcileLocked(ctx, root); err != nil {
		return result, err
	}
	state, err = loadState(root)
	if err != nil {
		return result, err
	}
	if !sameID(request.ExpectedActiveID, state.ActiveID) {
		return result, ErrConflict
	}
	expected, err := c.Runtime.Prepare(ctx, request.CertificateID)
	if err != nil {
		result.ErrorCode = "candidate_invalid"
		result.ActiveID = state.ActiveID
		return result, err
	}
	in := intent{Request: request, Previous: state, Expected: expected}
	if err = writeJSON(ctx, root, "intent.json", in); err != nil {
		return result, err
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if err = c.Runtime.Switch(ctx, request.CertificateID); err == nil {
		err = c.Runtime.Reload(ctx)
	}
	if err == nil && c.verify(ctx, expected) {
		return c.finishSuccess(ctx, root, in)
	}
	return c.restore(ctx, root, in, "verification_failed")
}
func (c *Controller) finishSuccess(ctx context.Context, root *os.Root, in intent) (ApplyResult, error) {
	target := in.Request.CertificateID
	state := GatewayState{ActiveID: &target, PreviousID: in.Previous.ActiveID, Evidence: in.Expected}
	if sameID(in.Previous.ActiveID, &target) {
		state.PreviousID = in.Previous.PreviousID
	}
	result := ApplyResult{JobID: in.Request.JobID, State: "applied", ActiveID: &target, Evidence: in.Expected}
	if err := writeJSON(ctx, root, "state.json", state); err != nil {
		return result, err
	}
	if err := writeJSON(ctx, root, "results/"+string(in.Request.JobID)+".json", receipt{in.Request, result}); err != nil {
		return result, err
	}
	if err := root.Remove("intent.json"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	return result, nil
}
func (c *Controller) restore(ctx context.Context, root *os.Root, in intent, code string) (ApplyResult, error) {
	result := ApplyResult{JobID: in.Request.JobID, State: "failed", ErrorCode: code}
	var old id.ID
	if in.Previous.ActiveID != nil {
		old = *in.Previous.ActiveID
	}
	var err error
	if historical, ok := c.Runtime.(interface {
		PrepareHistorical(context.Context, id.ID) (Evidence, error)
	}); ok {
		_, err = historical.PrepareHistorical(ctx, old)
	} else {
		_, err = c.Runtime.Prepare(ctx, old)
	}
	if err == nil {
		err = c.Runtime.Switch(ctx, old)
	}
	if err == nil {
		err = c.Runtime.Reload(ctx)
	}
	if err != nil || !c.verify(ctx, in.Previous.Evidence) {
		result.ErrorCode = "rollback_unverified"
		// Retain intent and renew the worker lease until a fresh handshake proves
		// either the target or restored listener. A failed receipt here could later
		// contradict a successful recovery and strand database state.
		return result, ErrFailed
	}
	result.ActiveID = in.Previous.ActiveID
	result.Evidence = in.Previous.Evidence
	if err = writeJSON(ctx, root, "state.json", in.Previous); err != nil {
		return result, err
	}
	if err = writeJSON(ctx, root, "results/"+string(in.Request.JobID)+".json", receipt{in.Request, result}); err != nil {
		return result, err
	}
	if err = root.Remove("intent.json"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	return result, ErrFailed
}
