package main

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/media/zlm"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestRecoveryWaitsForObservedPhysicalAbsence(t *testing.T) {
	var online atomic.Bool
	online.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/index/api/isMediaOnline" {
			fmt.Fprintf(w, `{"code":0,"online":%t}`, online.Load())
			return
		}
		if online.Load() {
			fmt.Fprintf(w, `{"code":0,"vhost":"__defaultVhost__","app":"one_nvr","stream":%q,"isRecordingMP4":false,"bytesSpeed":100}`, r.FormValue("stream"))
		} else {
			fmt.Fprint(w, `{"code":-1}`)
		}
	}))
	defer server.Close()
	client, err := zlm.New(server.URL, "fixture-only", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	session, _ := id.New()
	key := zlm.StreamKey{VHost: "__defaultVhost__", App: "one_nvr", Stream: string(session)}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() {
		timer := time.NewTimer(100 * time.Millisecond)
		defer timer.Stop()
		<-timer.C
		online.Store(false)
	}()
	if err := waitPhysicalAbsence(ctx, client, key); err != nil || online.Load() {
		t.Fatal("recovery fault not yet observed", err)
	}
}

func TestRecoveryWaitsThroughTransientManagementFailure(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/index/api/getMediaInfo" {
			fmt.Fprint(w, `{"code":-1}`)
			return
		}
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, `{"code":0,"online":false}`)
	}))
	defer server.Close()
	client, _ := zlm.New(server.URL, "fixture-only", server.Client())
	session, _ := id.New()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := waitPhysicalAbsence(ctx, client, zlm.StreamKey{VHost: "__defaultVhost__", App: "one_nvr", Stream: string(session)}); err != nil {
		t.Fatal("transient outage mistaken for terminal absence check", err)
	}
	if calls.Load() < 2 {
		t.Fatal("absence accepted before successful management evidence")
	}
}

func TestRecoveryWaitsForNextBoundedReconciliation(t *testing.T) {
	old, _ := id.New()
	fresh, _ := id.New()
	calls := 0
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	got, err := waitRecoveredSession(ctx, old, func(context.Context) error { calls++; return nil }, func(context.Context) (id.ID, error) {
		if calls == 1 {
			return "", pgx.ErrNoRows
		}
		return fresh, nil
	})
	if err != nil || got != fresh || calls != 2 {
		t.Fatal("one unavailable recovery attempt incorrectly terminalized", got, calls, err)
	}
	expired, stop := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer stop()
	if got, err := waitRecoveredSession(expired, old, func(context.Context) error { return nil }, func(context.Context) (id.ID, error) { return old, nil }); err == nil || got != "" {
		t.Fatal("old generation accepted as recovered", got, err)
	}
}

// Each production reconnect may consume its full 15-second window, followed
// by a 10-second reconciliation interval. The fixture must allow two complete
// bounded attempts rather than cancel the second inspect halfway through.
func TestRecoveryBudgetAllowsTwoBoundedAttempts(t *testing.T) {
	old, _ := id.New()
	fresh, _ := id.New()
	calls := 0
	got, err := waitRecoveredSession(context.Background(), old, func(ctx context.Context) error {
		calls++
		timer := time.NewTimer(12 * time.Second)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}, func(context.Context) (id.ID, error) {
		if calls == 1 {
			return "", pgx.ErrNoRows
		}
		return fresh, nil
	})
	if err != nil || got != fresh || calls != 2 {
		t.Fatal("second bounded reconciliation was cut off", got, calls, err)
	}
}
