package httpapi

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/zhigu34/one-nvr/internal/archive"
	"github.com/zhigu34/one-nvr/internal/fault"
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

// The probe verdicts must reach the page as different messages.
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
		{"read-only mount", archive.ErrTargetReadOnly, "archive_target_read_only", 422},
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
			// A 403 must not be worded as a credential problem: the credentials
			// were accepted, and telling an operator otherwise wastes the one
			// clue that would have pointed at the mount.
			if c.code == "archive_target_read_only" {
				if containsCode(recorder.Body.String(), "密码被拒绝") {
					t.Fatal("a read-only target must not be described as rejected credentials")
				}
				if !containsCode(recorder.Body.String(), "403") {
					t.Fatal("the read-only message must name the 403 it came from")
				}
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

// A published segment whose local copy is gone must be offered to the cloud, not
// reported as "not published yet".
//
// OpenSegment used to answer ErrContentNotReady for every state that was not
// ready. That message means "wait", so the HTTP layer never consulted the
// archive, and a recording with a healthy remote replica became unplayable the
// moment its local file disappeared — which is exactly the case ARC-04 exists to
// cover.
func TestLostLocalCopyFallsBackToCloud(t *testing.T) {
	for _, code := range []string{"recording_missing", "recording_damaged", "recording_deleted", "pool_unavailable"} {
		t.Run(code, func(t *testing.T) {
			err := fault.New(409, code, "x")
			if !localFailureSupportsCloud(err) {
				t.Fatalf("%s must fall back to the verified cloud copy", code)
			}
		})
	}
	// And "not published yet" must NOT: there is no copy to serve, and asking the
	// archive would just delay the honest answer.
	if localFailureSupportsCloud(fault.New(409, "recording_not_ready", "x")) {
		t.Fatal("a segment that is not published must not consult the archive")
	}
}
