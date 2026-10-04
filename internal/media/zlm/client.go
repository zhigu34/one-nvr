// Package zlm defines the private media-server boundary. Write probes require
// a real temporary media source and cannot be replaced by application file I/O.
package zlm

import (
	"context"
	"errors"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/operations"
	"net/http"
)

var ErrTestSourceRequired = errors.New("ZLM write probe requires a real test source")

type Client struct {
	Client    *http.Client
	HealthURL string
}
type ProbeInput struct {
	PoolID            id.ID
	TemporaryStreamID id.ID
}
type WriteEvidence struct {
	PoolID      id.ID
	RecordingID id.ID
}

func (c *Client) Health(ctx context.Context) error {
	return operations.ProbeHTTP(ctx, operations.ProbeTarget{URL: c.HealthURL, Kind: "zlm"}, c.Client)
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
