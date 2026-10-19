package detection

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// A restart that is accepted but never comes back must be reported as a failure.
//
// This is the live-site defect: /api/restart answers 200 as soon as the request
// is accepted, so a config the detector cannot parse produced "published"
// followed by a detector that never returned. The operator saw a success message
// and lost detection.
func TestReloadFailsWhenTheDetectorNeverReturns(t *testing.T) {
	var accepted atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/restart" {
			accepted.Store(true)
			w.WriteHeader(http.StatusOK)
			return
		}
		// Everything else refuses, as a dead process would.
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := HTTPReloader{BaseURL: server.URL}.Reload(ctx)
	if err == nil {
		t.Fatal("a detector that never returned was reported as a successful reload")
	}
	if !accepted.Load() {
		t.Fatal("the restart request was never sent")
	}
}

// A restart that does come back is a success — and the wait must not treat the
// connection refusals during the restart as fatal.
func TestReloadSucceedsWhenTheDetectorReturns(t *testing.T) {
	var restarted atomic.Bool
	var probes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/restart":
			restarted.Store(true)
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/api/version":
			probes.Add(1)
			if !restarted.Load() {
				w.WriteHeader(http.StatusOK)
				return
			}
			// The first probes after the restart fail, as a starting process does.
			if probes.Load() < 4 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := (HTTPReloader{BaseURL: server.URL}).Reload(ctx); err != nil {
		t.Fatalf("a detector that came back was reported as failed: %v", err)
	}
}

// A refused restart request is still a failure, and must not wait for readiness.
func TestReloadFailsWhenTheRestartIsRefused(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := time.Now()
	if err := (HTTPReloader{BaseURL: server.URL}).Reload(ctx); err == nil {
		t.Fatal("a refused restart was reported as successful")
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("a refused restart waited %v before failing", elapsed)
	}
}
