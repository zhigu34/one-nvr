package gatewaycontrol

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/zhigu34/one-nvr/internal/id"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Mailbox struct{ Directory string }

func (m Mailbox) Submit(ctx context.Context, request ApplyRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	root, err := controlRoot(m.Directory)
	if err != nil {
		return err
	}
	defer root.Close()
	if err = root.MkdirAll("requests", 0700); err != nil {
		return err
	}
	path := "requests/" + string(request.JobID) + ".json"
	var existing ApplyRequest
	err = readJSON(root, path, &existing)
	if err == nil {
		if sameRequest(request, existing) {
			return nil
		}
		return ErrConflict
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data, err := json.Marshal(request)
	if err != nil {
		return err
	}
	nonce, err := id.New()
	if err != nil {
		return err
	}
	temp := ".request-" + string(nonce)
	if err = writeFile(ctx, root, temp, data); err != nil {
		return err
	}
	defer root.Remove(temp)
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = root.Link(temp, path); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		if err = readJSON(root, path, &existing); err != nil {
			return err
		}
		if !sameRequest(request, existing) {
			return ErrConflict
		}
	}
	dir, err := root.Open("requests")
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func (m Mailbox) Wait(ctx context.Context, request ApplyRequest) (ApplyResult, error) {
	if err := request.Validate(); err != nil {
		return ApplyResult{}, err
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		root, err := controlRoot(m.Directory)
		if err != nil {
			return ApplyResult{}, err
		}
		var completed receipt
		err = readJSON(root, "results/"+string(request.JobID)+".json", &completed)
		root.Close()
		if err == nil {
			if !sameRequest(request, completed.Request) || completed.Result.JobID != request.JobID {
				return ApplyResult{}, ErrConflict
			}
			return completed.Result, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return ApplyResult{}, err
		}
		select {
		case <-ctx.Done():
			return ApplyResult{}, ctx.Err()
		case <-ticker.C:
		}
	}
}
func (c *Controller) ProcessRequests(ctx context.Context, https bool) error {
	root, err := controlRoot(c.Directory)
	if err != nil {
		return err
	}
	defer root.Close()
	if err = root.MkdirAll("requests", 0700); err != nil {
		return err
	}
	if err = root.MkdirAll("results", 0700); err != nil {
		return err
	}
	dir, err := root.Open("requests")
	if err != nil {
		return err
	}
	entries, err := dir.ReadDir(-1)
	dir.Close()
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if err = ctx.Err(); err != nil {
			return err
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") || !entry.Type().IsRegular() {
			continue
		}
		jobID, err := id.Parse(strings.TrimSuffix(name, ".json"))
		if err != nil || name != string(jobID)+".json" {
			continue
		}
		var request ApplyRequest
		if err = readJSON(root, "requests/"+name, &request); err != nil || request.JobID != jobID || request.Validate() != nil {
			continue
		}
		if _, err = root.Lstat("results/" + name); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		result := ApplyResult{JobID: jobID, State: "failed", ErrorCode: "https_disabled"}
		if https {
			result, err = c.Apply(ctx, request)
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		// Reconcile unresolved intent before publishing any terminal failure. A
		// failed rollback is left recoverable and cannot claim an active certificate.
		if _, pending := root.Lstat("intent.json"); pending == nil && result.State != "applied" {
			return ErrFailed
		}
		if _, saved := root.Lstat("results/" + name); errors.Is(saved, os.ErrNotExist) {
			if result.ErrorCode == "request_invalid" {
				result.ErrorCode = "version_conflict"
			}
			if err = writeJSON(ctx, root, "results/"+name, receipt{request, result}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Controller) CancelForHTTP(ctx context.Context) error {
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
	var pending intent
	err = readJSON(root, "intent.json", &pending)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = pending.Request.Validate(); err != nil {
		return err
	}
	if err = root.MkdirAll("results", 0700); err != nil {
		return err
	}
	result := ApplyResult{JobID: pending.Request.JobID, State: "failed", ErrorCode: "https_disabled"}
	if err = writeJSON(ctx, root, filepath.Join("results", string(pending.Request.JobID)+".json"), receipt{pending.Request, result}); err != nil {
		return err
	}
	return root.Remove("intent.json")
}
