package httpapi

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadinessDoesNotClaimUnavailableDatabaseHealthy(t *testing.T) {
	h := Health(func(context.Context) error { return errors.New("postgres://secret:password@unreachable") })
	for _, path := range []string{"/health/live", "/health/ready"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		want := 200
		if path == "/health/ready" {
			want = 503
		}
		if w.Code != want {
			t.Fatalf("%s got %d", path, w.Code)
		}
		if strings.Contains(w.Body.String(), "password") {
			t.Fatal("readiness leaked database error")
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/channels", nil))
	if w.Code != 404 {
		t.Fatal("unimplemented business route reported success")
	}
}
