package httpapi

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/zhigu34/one-nvr/internal/archive"
)

// A request the browser abandoned is not a server fault.
//
// Pages cancel in-flight fetches on navigation and on query invalidation. The
// blanket 503 turned each of those into a red "服务暂时不可用" banner for
// something that had already stopped mattering, and it made a real outage
// indistinguishable from a normal page change.
func TestCanceledRequestIsNotReportedAsAnOutage(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/api/v1/archive-targets", nil)
	ctx, cancel := context.WithCancel(request.Context())
	cancel()
	request = request.WithContext(ctx)

	fail(recorder, request, context.Canceled)

	if recorder.Code != 200 || recorder.Body.Len() != 0 {
		t.Fatalf("a canceled request must write nothing, got %d %q", recorder.Code, recorder.Body.String())
	}
}

// The three probe verdicts must reach the page as three different messages.
// Every 4xx used to collapse into "unauthorized", which is what sent an operator
// to re-type a password when the root path was wrong.
func TestArchiveProbeFailuresAreDistinguished(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		code   string
		status int
	}{
		{"credentials", archive.ErrTargetDenied, "archive_target_unauthorized", 422},
		{"root path", archive.ErrRemotePathMissing, "archive_remote_path_missing", 422},
		{"address", archive.ErrTargetUnavailable, "archive_target_unreachable", 422},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest("POST", "/api/v1/archive-targets/test", nil)
			fail(recorder, request, c.err)
			if recorder.Code != c.status {
				t.Fatalf("expected %d, got %d", c.status, recorder.Code)
			}
			if body := recorder.Body.String(); !containsCode(body, c.code) {
				t.Fatalf("expected code %q in %s", c.code, body)
			}
			// The blanket 503 wording must never appear for a probe failure: it
			// says nothing about what to change.
			if containsCode(recorder.Body.String(), "dependency_unavailable") {
				t.Fatal("a probe failure must not read as a service outage")
			}
		})
	}
}

func containsCode(body, code string) bool {
	return len(body) > 0 && (func() bool {
		for i := 0; i+len(code) <= len(body); i++ {
			if body[i:i+len(code)] == code {
				return true
			}
		}
		return false
	})()
}
