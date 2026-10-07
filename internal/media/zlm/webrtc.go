package zlm

import (
	"context"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// RTCPeer is private server state. Neither deletion tokens nor media URLs are
// returned to the browser. Deletion always uses this client's fixed origin.
type RTCPeer struct{ ID, Token string }

func (RTCPeer) String() string { return "<private RTC peer redacted>" }

func (c *Client) Negotiate(ctx context.Context, key StreamKey, offer, ticket string) (string, RTCPeer, error) {
	if key.Validate() != nil || len(offer) > 65536 || !strings.HasPrefix(offer, "v=0") || len(ticket) != 64 {
		return "", RTCPeer{}, ErrInvalidMediaInput
	}
	if _, err := hex.DecodeString(ticket); err != nil {
		return "", RTCPeer{}, ErrInvalidMediaInput
	}
	args := key.values()
	args.Del("schema")
	args.Set("live_token", ticket)
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/index/api/whep?"+args.Encode(), strings.NewReader(offer))
	if err != nil {
		return "", RTCPeer{}, ErrInvalidMediaInput
	}
	req.Header.Set("Content-Type", "application/sdp")
	resp, err := c.Client.Do(req)
	if err != nil {
		return "", RTCPeer{}, ErrMediaOperation
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		return "", RTCPeer{}, ErrMediaOperation
	}
	location, err := url.Parse(resp.Header.Get("Location"))
	base, _ := url.Parse(c.baseURL)
	if err != nil || location.User != nil || location.Fragment != "" || location.Path != "/index/api/delete_webrtc" || location.RawPath != "" || (location.IsAbs() && (location.Scheme != base.Scheme || location.Host != base.Host)) || (!location.IsAbs() && location.Host != "") {
		return "", RTCPeer{}, ErrMediaOperation
	}
	params, err := url.ParseQuery(location.RawQuery)
	if err != nil || len(params) != 2 || len(params["id"]) != 1 || len(params["token"]) != 1 || params.Get("id") == "" || params.Get("token") == "" || len(params.Get("id")) > 512 || len(params.Get("token")) > 512 {
		return "", RTCPeer{}, ErrMediaOperation
	}
	peer := RTCPeer{params.Get("id"), params.Get("token")}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil || len(raw) > 65536 || !strings.HasPrefix(string(raw), "v=0") {
		return "", peer, ErrMediaOperation
	}
	return string(raw), peer, nil
}

func (c *Client) CloseRTC(ctx context.Context, peer RTCPeer) error {
	if peer.ID == "" || peer.Token == "" {
		return ErrInvalidMediaInput
	}
	params := url.Values{"id": {peer.ID}, "token": {peer.Token}}
	req, err := http.NewRequestWithContext(ctx, "DELETE", c.baseURL+"/index/api/delete_webrtc?"+params.Encode(), nil)
	if err != nil {
		return ErrInvalidMediaInput
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		return ErrMediaOperation
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 404 {
		return ErrMediaOperation
	}
	return nil
}
