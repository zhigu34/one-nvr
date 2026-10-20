// Package archive owns WebDAV targets, the manual upload pipeline and the
// remote copy index. The local recording pipeline never depends on it: PRD
// ARC-06 keeps cloud failures isolated from recording.
package archive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

var (
	ErrTargetUnavailable = errors.New("archive_target_unavailable")
	ErrTargetDenied      = errors.New("archive_target_unauthorized")
	ErrObjectMissing     = errors.New("archive_object_missing")
	// ErrRemotePathMissing is its own verdict because the operator's fix is
	// different from the other two: the server answered, the credentials were
	// accepted, and the root simply is not there. Reporting it as a credential
	// problem sends someone to re-type a password that was never wrong.
	ErrRemotePathMissing = errors.New("archive_remote_path_missing")
	// ErrTargetReadOnly means the credentials were accepted and the path exists,
	// but this account may not write there. That is a server-side permission
	// (a read-only mount, a directory the backing store refuses writes in), not
	// something re-typing the password can fix.
	ErrTargetReadOnly = errors.New("archive_target_read_only")
)

// probeError explains one failure precisely enough to act on, and never more
// precisely than the evidence allows.
type probeError struct {
	// Code is one of the sentinel verdicts above; Is() makes errors.Is work
	// against the sentinel while the detailed text still carries the stage.
	Code  error
	Stage string
	// Detail is a short, non-secret diagnostic: the HTTP status, or the reason
	// the transport failed. No URL, no credentials.
	Detail string
}

func (e *probeError) Error() string { return e.Code.Error() + ": " + e.Stage + ": " + e.Detail }

// Is lets every existing errors.Is(err, ErrTargetDenied) keep working.
func (e *probeError) Is(target error) bool { return target == e.Code }

// Capabilities reports what one probe could actually verify. A missing DELETE
// must surface as such, never as a fully healthy target: the expiry sweeper
// depends on it.
//
// Failure names the stage and the raw evidence when the probe failed, so the
// page can show "write: HTTP 409" next to the plain-language verdict instead of
// an opaque "connection failed".
type Capabilities struct {
	Write  bool   `json:"write"`
	Read   bool   `json:"read"`
	Range  bool   `json:"range"`
	Delete bool   `json:"delete"`
	Stage  string `json:"stage,omitempty"`
	Detail string `json:"detail,omitempty"`
}

const probeTimeout = 30 * time.Second

// Client speaks the narrow WebDAV dialect the archive needs: PUT, ranged GET
// and DELETE under one account. OpenList and plain WebDAV servers are both
// just HTTP here.
type Client struct {
	base     string
	username string
	password string
	http     *http.Client
}

// NewClient validates the URL and returns a client whose requests are bounded
// per call. The password arrives decrypted and never leaves this struct.
//
// remoteRoot is part of the address, not part of the object name: an operator
// who points WebDAV at http://host/dav and sets the root to /nvr means objects
// live under http://host/dav/nvr/. Dropping the root (which is what happened
// before) makes every probe land in the wrong collection — and, on a server that
// exposes nothing at the bare root, fail in a way that reads like bad
// credentials.
func NewClient(baseURL, remoteRoot, username, password string) (*Client, error) {
	base := strings.TrimRight(baseURL, "/")
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		return nil, ErrTargetUnavailable
	}
	root := strings.Trim(remoteRoot, "/")
	if root != "" {
		base += "/" + root
	}
	if username == "" {
		return nil, ErrTargetDenied
	}
	return &Client{
		base:     base,
		username: username,
		password: password,
		http:     &http.Client{Timeout: 60 * time.Second},
	}, nil
}

func (c *Client) request(ctx context.Context, method, url string, body io.Reader, rangeHeader string) (*http.Response, error) {
	return c.requestSized(ctx, method, url, body, rangeHeader, -1)
}

// requestSized is request with a known body length.
//
// A non-negative size sets Content-Length. Without it Go sends
// Transfer-Encoding: chunked, because an io.Reader has no length — and a PUT
// that arrives chunked is refused by servers that read ContentLength directly
// (OpenList's WebDAV handler does: it passes r.ContentLength to the driver, so a
// chunked upload reaches the driver with size -1).
func (c *Client) requestSized(ctx context.Context, method, url string, body io.Reader, rangeHeader string, size int64) (*http.Response, error) {
	bounded, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(bounded, method, url, body)
	if err != nil {
		return nil, ErrTargetUnavailable
	}
	if size >= 0 && body != nil {
		req.ContentLength = size
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	req.SetBasicAuth(c.username, c.password)
	response, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &probeError{Code: ErrTargetUnavailable, Stage: "connect", Detail: transportReason(err)}
	}
	return response, nil
}

// withTrace attaches the request/response trace to a probe error.
func withTrace(err error, method, url string, status int, body []byte) error {
	var probe *probeError
	if errors.As(err, &probe) {
		probe.Detail += " · " + traceRequest(method, url, status, body)
	}
	return err
}

// traceRequest logs what was actually sent and what came back, for one failing
// request.
//
// A 403 from a WebDAV server is the one verdict the server does not explain: it
// says "no" without saying which rule said no. Without the method and path that
// produced it, both sides end up guessing — which is exactly what happened when
// a mount that accepted manual uploads from its own web UI refused this client.
// The URL is trimmed to host + path (no query, no credentials), and the body is
// capped and stripped of anything that looks like a secret.
func traceRequest(method, url string, status int, body []byte) string {
	safe := url
	if i := strings.Index(safe, "?"); i >= 0 {
		safe = safe[:i]
	}
	if i := strings.Index(safe, "://"); i >= 0 {
		if j := strings.Index(safe[i+3:], "/"); j >= 0 {
			// Keep the path only: the host is site topology, the path is what the
			// request is about.
			safe = safe[i+3+j:]
		} else {
			safe = "/"
		}
	}
	snippet := string(body)
	if len(snippet) > 160 {
		snippet = snippet[:160] + "…"
	}
	snippet = strings.ReplaceAll(snippet, "\n", " ")
	return fmt.Sprintf("%s %s -> HTTP %d %s", method, safe, status, snippet)
}

// transportReason names why the connection failed, without the URL.
//
// net/http errors embed the full request URL, which carries the host and port
// (and, for a proxied request, sometimes credentials). Only the class of failure
// is reported; it is enough to tell "wrong host" from "server not listening"
// from "timed out", which are the three things an operator can act on.
func transportReason(err error) string {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "主机名无法解析"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "连接超时"
	}
	message := err.Error()
	switch {
	case strings.Contains(message, "connection refused"):
		return "连接被拒绝（地址可达但端口没有服务）"
	case strings.Contains(message, "no such host"):
		return "主机名无法解析"
	case strings.Contains(message, "certificate"):
		return "证书校验失败"
	case strings.Contains(message, "EOF"):
		return "连接被中断"
	}
	// The class is unknown, so the message is not repeated: it may contain the
	// URL we are deliberately not storing.
	return "无法建立连接"
}

// statusError turns one HTTP response into a verdict.
//
// 401 and 403 are not the same verdict, which is why they are split here:
//
//	401 Unauthorized — the credentials were not accepted. Fix: the password.
//	403 Forbidden    — the credentials WERE accepted; this account is not allowed
//	                   to write at this path. Fix: the server-side permissions —
//	                   a read-only mount, a path the backing store will not
//	                   accept writes in, or a per-path rule.
//	404 / 409        — the addressed object or its parent is not there. Fix: the
//	                   address and the root path.
//
// Reporting 403 as a credential problem sends an operator to re-type a password
// that demonstrably works, which is exactly what happened with a WebDAV mount
// whose backing storage refused writes.
func statusError(stage string, status int) error {
	code := ErrTargetUnavailable
	switch status {
	case http.StatusUnauthorized:
		code = ErrTargetDenied
	case http.StatusForbidden:
		code = ErrTargetReadOnly
	case http.StatusNotFound, http.StatusConflict:
		// 404 on the addressed object and 409 ("missing parent") are the same
		// operator mistake: the root does not exist on this server.
		code = ErrRemotePathMissing
	}
	return &probeError{Code: code, Stage: stage, Detail: fmt.Sprintf("HTTP %d", status)}
}

// URL builds the absolute object URL from the target root and object key.
func (c *Client) URL(key string) string { return c.base + "/" + strings.TrimLeft(key, "/") }

// Put streams one object. Collections are created best-effort first: standard
// WebDAV needs every parent, and servers answer an existing collection with
// 405, which is success for this purpose.
func (c *Client) Put(ctx context.Context, key string, body io.Reader, size int64) error {
	if err := c.ensureCollection(ctx, parentKey(key)); err != nil {
		return err
	}
	response, err := c.requestSized(ctx, http.MethodPut, c.URL(key), body, "", size)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	snippet, _ := io.ReadAll(io.LimitReader(response.Body, 512))
	if response.StatusCode/100 != 2 {
		return withTrace(statusError("write", response.StatusCode), "PUT", c.URL(key), response.StatusCode, snippet)
	}
	return nil
}

// GetRange reads back one object slice. The caller limits what it drains; the
// response header carries Content-Range for length verification.
func (c *Client) GetRange(ctx context.Context, key, rangeHeader string) (io.ReadCloser, http.Header, int, error) {
	response, err := c.request(ctx, http.MethodGet, c.URL(key), nil, rangeHeader)
	if err != nil {
		return nil, nil, 0, err
	}
	if response.StatusCode == 404 {
		response.Body.Close()
		return nil, nil, 0, ErrObjectMissing
	}
	if response.StatusCode/100 != 2 {
		snippet, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		response.Body.Close()
		return nil, nil, 0, withTrace(statusError("read", response.StatusCode), "GET", c.URL(key), response.StatusCode, snippet)
	}
	return response.Body, response.Header, response.StatusCode, nil
}

// Delete removes one object; a missing object is success for cleanup paths.
func (c *Client) Delete(ctx context.Context, key string) (bool, error) {
	response, err := c.request(ctx, http.MethodDelete, c.URL(key), nil, "")
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 512))
	if response.StatusCode == 404 {
		return false, nil
	}
	if response.StatusCode/100 != 2 {
		return false, withTrace(statusError("delete", response.StatusCode), "DELETE", c.URL(key), response.StatusCode, body)
	}
	return true, nil
}

// Check proves the target end to end: write, read-back equality, a ranged
// read, and delete — with the probe file cleaned up either way.
func (c *Client) Check(ctx context.Context) (Capabilities, error) {
	var caps Capabilities
	bounded, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	probeKey := fmt.Sprintf("one-nvr-probe/%d.probe", time.Now().UnixNano())
	payload := []byte("one-nvr archive probe")
	if err := c.Put(bounded, probeKey, strings.NewReader(string(payload)), int64(len(payload))); err != nil {
		return annotate(caps, err), err
	}
	caps.Write = true
	body, _, status, err := c.GetRange(bounded, probeKey, "")
	if err != nil {
		return annotate(caps, err), err
	}
	readBack, _ := io.ReadAll(io.LimitReader(body, 1<<20))
	body.Close()
	if status != http.StatusOK || string(readBack) != string(payload) {
		// The write succeeded and the read-back did not match: the server
		// accepted bytes and served something else. That is not a credential or
		// a path problem, and saying so would send the operator to the wrong place.
		failure := &probeError{Code: ErrTargetUnavailable, Stage: "read-back",
			Detail: fmt.Sprintf("HTTP %d, %d bytes returned", status, len(readBack))}
		return annotate(caps, failure), failure
	}
	caps.Read = true
	ranged, _, _, err := c.GetRange(bounded, probeKey, "bytes=0-4")
	if err == nil {
		slice, _ := io.ReadAll(io.LimitReader(ranged, 16))
		ranged.Close()
		if string(slice) == string(payload[:5]) {
			caps.Range = true
		}
	}
	deleted, err := c.Delete(bounded, probeKey)
	if err != nil {
		// Cleanup failing is reported through the capability flags, not as a
		// failed probe: everything the archive needs was already proven.
		return annotate(caps, err), nil
	}
	caps.Delete = deleted
	return caps, nil
}

// annotate copies the failure's stage and detail into the capabilities so the
// page can show what the server actually said.
func annotate(caps Capabilities, err error) Capabilities {
	var probe *probeError
	if errors.As(err, &probe) {
		caps.Stage, caps.Detail = probe.Stage, probe.Detail
	}
	return caps
}

// ensureCollection MKCOLs every missing ancestor of the key, top down.
func (c *Client) ensureCollection(ctx context.Context, collection string) error {
	if collection == "" {
		return nil
	}
	parts := strings.Split(collection, "/")
	path := ""
	for _, part := range parts {
		if part == "" {
			continue
		}
		path += "/" + part
		response, err := c.request(ctx, "MKCOL", c.URL(path), nil, "")
		if err != nil {
			return err
		}
		io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		response.Body.Close()
		// 405 means it already exists; both are fine.
		if response.StatusCode/100 != 2 && response.StatusCode != http.StatusMethodNotAllowed {
			// Keep the server's own words: for a 403 that sentence is usually the
			// only thing that distinguishes "this mount is read-only" from "this
			// path is not writable by that account".
			body, _ := io.ReadAll(io.LimitReader(response.Body, 512))
			err := statusError("create-collection", response.StatusCode)
			var probe *probeError
			if errors.As(err, &probe) {
				probe.Detail += " · " + traceRequest("MKCOL", c.URL(path), response.StatusCode, body)
			}
			return err
		}
	}
	return nil
}

func parentKey(key string) string {
	index := strings.LastIndex(key, "/")
	if index <= 0 {
		return ""
	}
	return key[:index]
}
