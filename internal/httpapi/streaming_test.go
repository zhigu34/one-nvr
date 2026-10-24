package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// deadlineRecorder stands in for the connection: the server sets a write
// deadline on it, and every write is allowed or refused accordingly.
type deadlineRecorder struct {
	header   http.Header
	deadline time.Time
	writes   int
}

func (d *deadlineRecorder) Header() http.Header                { return d.header }
func (d *deadlineRecorder) WriteHeader(int)                    {}
func (d *deadlineRecorder) Write(p []byte) (int, error)        { d.writes++; return len(p), nil }
func (d *deadlineRecorder) SetWriteDeadline(t time.Time) error { d.deadline = t; return nil }

// A download must outlive the server-wide write timeout.
//
// WriteTimeout is fifteen seconds, which is generous for a JSON reply and fatal
// for a recording export: the deadline is absolute, so any transfer slower than
// that is cut mid-file. The wrapper extends it as bytes flow; what this test
// pins is the mechanism — an initial extension before the first byte (opening a
// bundle can itself take longer than fifteen seconds) and a fresh deadline on
// every write.
func TestStreamWriterExtendsTheDeadlineOnEveryWrite(t *testing.T) {
	inner := &deadlineRecorder{header: http.Header{}}
	out := streamWriter(inner)
	now := time.Now()
	if inner.deadline.Before(now.Add(streamWriteWindow - time.Minute)) {
		t.Fatalf("streamWriter left the connection on the server-wide deadline (%v); "+
			"a download would be truncated before the first byte", inner.deadline.Sub(now))
	}
	first := inner.deadline
	time.Sleep(2 * time.Millisecond)
	if _, err := out.Write([]byte("chunk")); err != nil {
		t.Fatal(err)
	}
	if !inner.deadline.After(first) {
		t.Fatal("a write did not move the deadline forward; a transfer slower than " +
			"the server-wide timeout still ends mid-file")
	}
	if inner.writes != 1 {
		t.Fatalf("the wrapper dropped a write, got %d", inner.writes)
	}
}

// The wrapper must not require a deadline-capable writer. A test recorder, or a
// middleware that hides the connection, cannot set deadlines; the response still
// has to be written.
func TestStreamWriterWritesThroughWhenDeadlinesAreUnsupported(t *testing.T) {
	recorder := httptest.NewRecorder()
	out := streamWriter(recorder)
	if _, err := out.Write([]byte("body")); err != nil {
		t.Fatalf("a writer without deadline support broke the response: %v", err)
	}
	if recorder.Body.String() != "body" {
		t.Fatalf("body = %q", recorder.Body.String())
	}
}
