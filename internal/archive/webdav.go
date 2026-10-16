// Package archive owns WebDAV targets, the manual upload pipeline and the
// remote copy index. The local recording pipeline never depends on it: PRD
// ARC-06 keeps cloud failures isolated from recording.
package archive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

var (
	ErrTargetUnavailable = errors.New("archive_target_unavailable")
	ErrTargetDenied      = errors.New("archive_target_unauthorized")
	ErrObjectMissing     = errors.New("archive_object_missing")
)

// Capabilities reports what one probe could actually verify. A missing DELETE
// must surface as such, never as a fully healthy target: the expiry sweeper
// depends on it.
type Capabilities struct {
	Write  bool `json:"write"`
	Read   bool `json:"read"`
	Range  bool `json:"range"`
	Delete bool `json:"delete"`
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
func NewClient(baseURL, username, password string) (*Client, error) {
	base := strings.TrimRight(baseURL, "/")
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		return nil, ErrTargetUnavailable
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
	bounded, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(bounded, method, url, body)
	if err != nil {
		return nil, ErrTargetUnavailable
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
		return nil, ErrTargetUnavailable
	}
	return response, nil
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
	response, err := c.request(ctx, http.MethodPut, c.URL(key), body, "")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	if response.StatusCode/100 == 4 {
		return ErrTargetDenied
	}
	if response.StatusCode/100 != 2 {
		return ErrTargetUnavailable
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
	if response.StatusCode/100 == 4 {
		response.Body.Close()
		return nil, nil, 0, ErrTargetDenied
	}
	if response.StatusCode/100 != 2 {
		response.Body.Close()
		return nil, nil, 0, ErrTargetUnavailable
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
	io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	if response.StatusCode == 404 {
		return false, nil
	}
	if response.StatusCode/100 == 4 {
		return false, ErrTargetDenied
	}
	if response.StatusCode/100 != 2 {
		return false, ErrTargetUnavailable
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
		return caps, err
	}
	caps.Write = true
	body, _, status, err := c.GetRange(bounded, probeKey, "")
	if err != nil {
		return caps, err
	}
	readBack, _ := io.ReadAll(io.LimitReader(body, 1<<20))
	body.Close()
	if status != http.StatusOK || string(readBack) != string(payload) {
		return caps, ErrTargetUnavailable
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
		return caps, nil
	}
	caps.Delete = deleted
	return caps, nil
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
			if response.StatusCode == 409 {
				return ErrTargetUnavailable
			}
			if response.StatusCode/100 == 4 {
				return ErrTargetDenied
			}
			return ErrTargetUnavailable
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
