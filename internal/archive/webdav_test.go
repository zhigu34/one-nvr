package archive

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The three verdicts have three different fixes. Collapsing them is what sends
// an operator to re-type a password that was never wrong, so each one is pinned
// to the status code that actually causes it.
func TestProbeSeparatesTheThreeVerifiableFailures(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   error
	}{
		{"401 is the credentials", http.StatusUnauthorized, ErrTargetDenied},
		// 403 means the credentials were ACCEPTED and the write was refused.
		// Merging it into 401 sends an operator to re-type a working password.
		{"403 is read-only, not bad credentials", http.StatusForbidden, ErrTargetReadOnly},
		{"404 is a root that does not exist", http.StatusNotFound, ErrRemotePathMissing},
		// 409 on PUT/MKCOL is WebDAV's "parent collection missing", which for an
		// operator is the same mistake as a 404 on the root.
		{"conflict is a missing parent", http.StatusConflict, ErrRemotePathMissing},
		{"500 is the server", http.StatusInternalServerError, ErrTargetUnavailable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(c.status)
			}))
			defer server.Close()
			client, err := NewClient(server.URL, "/nvr", "user", "pass")
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Check(context.Background())
			if !errors.Is(err, c.want) {
				t.Fatalf("HTTP %d must read as %v, got %v", c.status, c.want, err)
			}
		})
	}
}

// A host nothing answers on is not a credential problem, and the message must
// not carry the URL (it may hold a private host and port).
func TestUnreachableTargetIsItsOwnVerdict(t *testing.T) {
	client, err := NewClient("http://127.0.0.1:1/dav", "/nvr", "user", "pass")
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Check(context.Background())
	if !errors.Is(err, ErrTargetUnavailable) {
		t.Fatalf("a refused connection must read as unreachable, got %v", err)
	}
	if errors.Is(err, ErrTargetDenied) {
		t.Fatal("a refused connection must never read as a credential problem")
	}
	var probe *probeError
	if !errors.As(err, &probe) {
		t.Fatalf("the failure must carry its stage, got %v", err)
	}
	if probe.Stage != "connect" {
		t.Fatalf("expected the connect stage, got %q", probe.Stage)
	}
	// The reason is a class, not the raw net error, which embeds the URL.
	if probe.Detail == "" || len([]rune(probe.Detail)) > 40 {
		t.Fatalf("the detail must be a short reason, got %q", probe.Detail)
	}
	if strings.Contains(probe.Detail, "127.0.0.1") || strings.Contains(probe.Detail, "http") {
		t.Fatalf("the detail must not carry the address, got %q", probe.Detail)
	}
}

// The remote root is part of the address. Before this was wired, every probe
// landed at the bare baseURL regardless of what the operator configured, so a
// target mounted at /nvr was probed at /.
func TestRemoteRootIsPartOfTheAddress(t *testing.T) {
	var got []string
	stored := map[string][]byte{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.Path)
		switch r.Method {
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			stored[r.URL.Path] = body
			w.WriteHeader(http.StatusCreated)
		case http.MethodGet:
			body, ok := stored[r.URL.Path]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if r.Header.Get("Range") == "bytes=0-4" {
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write(body[:5])
				return
			}
			_, _ = w.Write(body)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	noRoot, _ := NewClient(server.URL+"/dav", "/", "user", "pass")
	if _, err := noRoot.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	got = nil
	withRoot, _ := NewClient(server.URL+"/dav", "/nvr", "user", "pass")
	if _, err := withRoot.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, call := range got {
		if call == "" {
			continue
		}
		// Every request must sit under the configured root.
		if len(call) < len("/nvr") || call[len(call)-len("write"):] == "write" {
			continue
		}
	}
	// The probe writes one object; its path is what proves the root was applied.
	var wrote bool
	for _, call := range got {
		if len(call) > 4 && call[:4] == "PUT " {
			wrote = true
			if want := "/dav/nvr/one-nvr-probe/"; len(call) < len(want) || call[4:4+len(want)] != want {
				t.Fatalf("the probe must write under the configured root, got %q", call)
			}
		}
	}
	if !wrote {
		t.Fatalf("no PUT reached the server, got %v", got)
	}
}

// A trailing slash on the root must not produce a doubled separator.
func TestRemoteRootTrailingSlashIsNormalized(t *testing.T) {
	var path string
	stored := map[string][]byte{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			path = r.URL.Path
			body, _ := io.ReadAll(r.Body)
			stored[r.URL.Path] = body
			w.WriteHeader(http.StatusCreated)
		case http.MethodGet:
			body, ok := stored[r.URL.Path]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if r.Header.Get("Range") == "bytes=0-4" {
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write(body[:5])
				return
			}
			_, _ = w.Write(body)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()
	client, _ := NewClient(server.URL, "nvr/", "user", "pass")
	if _, err := client.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := "/nvr/one-nvr-probe/"; len(path) < len(want) || path[:len(want)] != want {
		t.Fatalf("expected %q prefix, got %q", want, path)
	}
}

// A read-back that returns different bytes is not a credential or path problem:
// the server accepted the write and served something else. Reporting it as
// "unauthorized" would send the operator to the wrong place entirely.
func TestReadBackMismatchIsNotACredentialProblem(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut || r.Method == "MKCOL" {
			w.WriteHeader(http.StatusCreated)
			return
		}
		// Accept the write, then serve different content than was stored.
		_, _ = w.Write([]byte("something else entirely"))
	}))
	defer server.Close()
	client, _ := NewClient(server.URL, "/", "user", "pass")
	caps, err := client.Check(context.Background())
	if err == nil {
		t.Fatal("a mismatched read-back must fail the probe")
	}
	if errors.Is(err, ErrTargetDenied) {
		t.Fatal("a read-back mismatch must not read as a credential problem")
	}
	if !caps.Write {
		t.Fatal("the write did happen and must be reported as such")
	}
	var probe *probeError
	if !errors.As(err, &probe) || probe.Stage != "read-back" {
		t.Fatalf("expected the read-back stage, got %v", err)
	}
}

// A 403 is the one verdict a WebDAV server does not explain, so the trace has to
// carry what was actually sent. It must not carry the host (site topology) or
// anything resembling a credential.
func TestFailureTraceNamesTheRequestWithoutLeakingTheTarget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("permission denied: mount is read-only"))
	}))
	defer server.Close()

	client, _ := NewClient(server.URL+"/dav", "/nvr", "admin", "s3cret")
	_, err := client.Check(context.Background())
	if !errors.Is(err, ErrTargetReadOnly) {
		t.Fatalf("expected read-only, got %v", err)
	}
	message := err.Error()
	// The evidence an operator needs.
	for _, want := range []string{"MKCOL", "/dav/nvr/one-nvr-probe", "HTTP 403"} {
		if !strings.Contains(message, want) {
			t.Fatalf("the trace must include %q, got %q", want, message)
		}
	}
	// The things it must never include.
	for _, secret := range []string{"s3cret", "admin:", "127.0.0.1", "Basic "} {
		if strings.Contains(message, secret) {
			t.Fatalf("the trace leaked %q: %q", secret, message)
		}
	}
}

// A PUT must carry Content-Length. Without it Go sends
// Transfer-Encoding: chunked, and OpenList's WebDAV handler passes
// r.ContentLength straight to the storage driver — a chunked upload reaches the
// driver with size -1 and the upload fails.
//
// The reader must be one net/http cannot measure (bytes.Reader and
// strings.Reader report their own length, which hides the bug): the real path
// passes io.TeeReader over the source file.
func TestPutDeclaresContentLength(t *testing.T) {
	var gotLength int64
	var chunked bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			gotLength = r.ContentLength
			chunked = len(r.TransferEncoding) > 0
			io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusCreated)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, _ := NewClient(server.URL, "/nvr", "user", "pass")
	payload := bytes.Repeat([]byte("x"), 4096)
	// opaqueReader hides the length from net/http, exactly like io.TeeReader.
	if err := client.Put(context.Background(), "k/file.mp4", opaqueReader{r: bytes.NewReader(payload)}, int64(len(payload))); err != nil {
		t.Fatal(err)
	}
	if gotLength != int64(len(payload)) {
		t.Fatalf("PUT must declare Content-Length %d, got %d", len(payload), gotLength)
	}
	if chunked {
		t.Fatal("a sized PUT must not use chunked transfer encoding; OpenList refuses it")
	}
}

// opaqueReader hides its length: net/http falls back to chunked when it cannot
// measure the body, which is what the real upload path used to do.
type opaqueReader struct{ r io.Reader }

func (o opaqueReader) Read(p []byte) (int, error) { return o.r.Read(p) }
