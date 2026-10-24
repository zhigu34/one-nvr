package httpapi

import (
	"net/http"
	"time"
)

// streamWriteWindow is how long a streaming response may go without writing a
// byte before its connection is closed.
//
// The server-wide WriteTimeout exists so a stalled client cannot pin a handler
// on a JSON reply, and fifteen seconds is generous for that. A download is a
// different shape: the time it takes is decided by the file size and the
// client's link, not by our own processing, so the same limit truncated every
// real recording export mid-file. The browser reports a failed fetch with no
// hint that the server cut it, and the file on the operator's desktop is short.
//
// Extending the deadline as bytes actually flow keeps the stall protection —
// two minutes without progress still ends the response — without inventing a
// size limit. A slow client is fine; a client that stopped reading is not.
const streamWriteWindow = 2 * time.Minute

// streamWriter extends the response's write deadline on every write.
//
// The initial call matters as much as the per-write ones: the server applies
// WriteTimeout when the request is read, so a handler that spends longer than
// that opening a large bundle would already be past its deadline before the
// first byte exists.
func streamWriter(w http.ResponseWriter) http.ResponseWriter {
	out := &deadlineWriter{ResponseWriter: w, controller: http.NewResponseController(w)}
	_ = out.controller.SetWriteDeadline(time.Now().Add(streamWriteWindow))
	return out
}

type deadlineWriter struct {
	http.ResponseWriter
	controller *http.ResponseController
}

func (d *deadlineWriter) Write(p []byte) (int, error) {
	// A response writer whose deadlines cannot be reached (a middleware in
	// front, a test recorder) is not a reason to fail a download: the write
	// proceeds under the deadline that already applies.
	_ = d.controller.SetWriteDeadline(time.Now().Add(streamWriteWindow))
	return d.ResponseWriter.Write(p)
}

// SetWriteDeadline lets an already-wrapped writer stay reachable.
//
// A handler may hand its writer to another handler (a local recording that
// falls back to the cloud copy), which wraps it a second time. Without this,
// the outer controller could not reach the connection and its deadline changes
// would be silently dropped.
func (d *deadlineWriter) SetWriteDeadline(t time.Time) error {
	return d.controller.SetWriteDeadline(t)
}
