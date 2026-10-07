// Package zlm defines the private media-server boundary. Write probes require
// a real temporary media source and cannot be replaced by application file I/O.
package zlm

import (
	"context"
	"errors"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/id"
	"net/http"
	"net/url"
	"time"
)

var ErrTestSourceRequired = errors.New("ZLM write probe requires a real test source")

type Client struct {
	Client  *http.Client
	baseURL string
	secret  string
}

func (*Client) String() string { return "<private media client redacted>" }

type ProbeInput struct {
	PoolID            id.ID
	TemporaryStreamID id.ID
}
type WriteEvidence struct {
	PoolID        id.ID
	RecordingID   id.ID
	FilePath      string
	Size          int64
	Duration      time.Duration
	VideoVerified bool
	ObservedAt    time.Time
}

func (c *Client) Health(ctx context.Context) error {
	var out struct {
		Code int `json:"code"`
	}
	return c.call(ctx, "getThreadsLoad", url.Values{}, &out)
}
func (c *Client) ProbeWrite(ctx context.Context, in ProbeInput) (WriteEvidence, error) {
	if err := ctx.Err(); err != nil {
		return WriteEvidence{}, err
	}
	if _, err := id.Parse(string(in.PoolID)); err != nil {
		return WriteEvidence{}, auth.ErrInvalid
	}
	// Source allocation/record callback evidence belongs to M1-B; this contract
	// accepts identifiers only and never accepts request-supplied paths or secrets.
	return WriteEvidence{}, ErrTestSourceRequired
}
