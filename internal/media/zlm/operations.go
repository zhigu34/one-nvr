package zlm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/zhigu34/one-nvr/internal/id"
)

var ErrMediaOperation = errors.New("media_operation_unavailable")
var ErrProxyAbsent = errors.New("media_proxy_absent")
var ErrStreamAbsent = errors.New("media_stream_absent")
var ErrInvalidMediaInput = errors.New("invalid_media_input")

type StreamKey struct{ VHost, App, Stream string }
type ProxyInput struct {
	Key            StreamKey
	URL, Transport string
}

func (ProxyInput) String() string { return "<private media input redacted>" }

type ProxyRef struct {
	Key       StreamKey
	OpaqueKey string
}
type Track struct {
	Codec  string  `json:"codec"`
	Ready  bool    `json:"ready"`
	Width  int     `json:"width"`
	Height int     `json:"height"`
	FPS    float64 `json:"fps"`
	Frames int64   `json:"frames"`
}
type StreamSnapshot struct {
	Key            StreamKey
	Tracks         []Track
	Recording      bool
	BytesPerSecond int64
	ObservedAt     time.Time
}

func (k StreamKey) Validate() error {
	if k.VHost != "__defaultVhost__" || k.App != "one_nvr" {
		return ErrInvalidMediaInput
	}
	if _, err := id.Parse(k.Stream); err != nil {
		return ErrInvalidMediaInput
	}
	return nil
}
func (k StreamKey) values() url.Values {
	return url.Values{"schema": {"rtsp"}, "vhost": {k.VHost}, "app": {k.App}, "stream": {k.Stream}}
}
func New(baseURL, secret string, client *http.Client) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || secret == "" {
		return nil, ErrInvalidMediaInput
	}
	if client == nil {
		client = &http.Client{}
	}
	copyClient := *client
	copyClient.Timeout = 10 * time.Second
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	// Ignore proxy environment: media management is always a private fixed peer.
	if copyClient.Transport == nil {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.Proxy = nil
		copyClient.Transport = tr
	}
	return &Client{Client: &copyClient, baseURL: strings.TrimRight(baseURL, "/"), secret: secret}, nil
}
func (c *Client) call(ctx context.Context, endpoint string, args url.Values, out any) error {
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	args.Set("secret", c.secret)
	req, err := http.NewRequestWithContext(bounded, "POST", c.baseURL+"/index/api/"+endpoint, strings.NewReader(args.Encode()))
	if err != nil {
		return ErrInvalidMediaInput
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.Client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrMediaOperation
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return ErrMediaOperation
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return ErrMediaOperation
	}
	var envelope struct {
		Code *int `json:"code"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Code == nil || *envelope.Code != 0 {
		return ErrMediaOperation
	}
	if json.Unmarshal(raw, out) != nil {
		return ErrMediaOperation
	}
	return nil
}
func (c *Client) AddProxy(ctx context.Context, in ProxyInput) (ProxyRef, error) {
	if err := in.Key.Validate(); err != nil {
		return ProxyRef{}, err
	}
	u, err := url.Parse(in.URL)
	if err != nil || u.Scheme != "rtsp" || u.Host == "" || u.Fragment != "" || (in.Transport != "tcp" && in.Transport != "udp") {
		return ProxyRef{}, ErrInvalidMediaInput
	}
	args := in.Key.values()
	args.Set("url", in.URL)
	args.Set("enable_mp4", "0")
	args.Set("enable_hls", "0")
	args.Set("enable_hls_fmp4", "0")
	args.Set("auto_close", "0")
	args.Set("retry_count", "0")
	args.Set("timeout_sec", "8")
	args.Set("enable_rtsp", "1")
	args.Set("enable_rtmp", "0")
	args.Set("enable_ts", "0")
	args.Set("enable_fmp4", "0")
	args.Set("rtp_type", "0")
	if in.Transport == "udp" {
		args.Set("rtp_type", "1")
	}
	var out struct {
		Data struct {
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := c.call(ctx, "addStreamProxy", args, &out); err != nil {
		return ProxyRef{}, err
	}
	if out.Data.Key != in.Key.VHost+"/"+in.Key.App+"/"+in.Key.Stream {
		return ProxyRef{}, ErrMediaOperation
	}
	return ProxyRef{Key: in.Key, OpaqueKey: out.Data.Key}, nil
}
func (c *Client) RemoveProxy(ctx context.Context, ref ProxyRef) error {
	if err := ref.Key.Validate(); err != nil {
		return err
	}
	if ref.OpaqueKey != ref.Key.VHost+"/"+ref.Key.App+"/"+ref.Key.Stream {
		return ErrInvalidMediaInput
	}
	var out struct {
		Data struct {
			Flag *bool `json:"flag"`
		} `json:"data"`
	}
	if err := c.call(ctx, "delStreamProxy", url.Values{"key": {ref.OpaqueKey}}, &out); err != nil {
		return err
	}
	if out.Data.Flag == nil {
		return ErrMediaOperation
	}
	if !*out.Data.Flag {
		return ErrProxyAbsent
	}
	return nil
}
func (c *Client) Inspect(ctx context.Context, key StreamKey) (StreamSnapshot, error) {
	if err := key.Validate(); err != nil {
		return StreamSnapshot{}, err
	}
	// The detailed response runs on the media owner's thread. Establish
	// registry presence first so an absent stream does not consume a bounded
	// reconnect window waiting for details of a previous physical source.
	var presence struct {
		Online *bool `json:"online"`
	}
	if err := c.call(ctx, "isMediaOnline", key.values(), &presence); err != nil {
		return StreamSnapshot{}, err
	}
	if presence.Online == nil {
		return StreamSnapshot{}, ErrMediaOperation
	}
	if !*presence.Online {
		return StreamSnapshot{}, ErrStreamAbsent
	}
	var out struct {
		VHost     string `json:"vhost"`
		App       string `json:"app"`
		Stream    string `json:"stream"`
		Recording *bool  `json:"isRecordingMP4"`
		Bytes     int64  `json:"bytesSpeed"`
		Tracks    []struct {
			Codec  string  `json:"codec_id_name"`
			Ready  bool    `json:"ready"`
			Width  int     `json:"width"`
			Height int     `json:"height"`
			FPS    float64 `json:"fps"`
			Frames int64   `json:"frames"`
		} `json:"tracks"`
	}
	if err := c.call(ctx, "getMediaInfo", key.values(), &out); err != nil {
		if errors.Is(err, ErrMediaOperation) {
			var online struct {
				Online *bool `json:"online"`
			}
			if c.call(ctx, "isMediaOnline", key.values(), &online) == nil && online.Online != nil && !*online.Online {
				return StreamSnapshot{}, ErrStreamAbsent
			}
		}
		return StreamSnapshot{}, err
	}
	if out.VHost != key.VHost || out.App != key.App || out.Stream != key.Stream || out.Recording == nil || out.Bytes < 0 {
		return StreamSnapshot{}, ErrMediaOperation
	}
	snapshot := StreamSnapshot{Key: key, Recording: *out.Recording, BytesPerSecond: out.Bytes, ObservedAt: time.Now().UTC(), Tracks: make([]Track, 0, len(out.Tracks))}
	for _, track := range out.Tracks {
		snapshot.Tracks = append(snapshot.Tracks, Track{Codec: track.Codec, Ready: track.Ready, Width: track.Width, Height: track.Height, FPS: track.FPS, Frames: track.Frames})
	}
	return snapshot, nil
}
func (c *Client) StartRecord(ctx context.Context, key StreamKey, workDir string, maxSeconds int) error {
	if err := key.Validate(); err != nil {
		return err
	}
	if maxSeconds != 60 || !filepath.IsAbs(workDir) || filepath.Clean(workDir) != workDir || !strings.HasPrefix(workDir, "/storage/") || !strings.Contains(workDir, "/.work/") || strings.ContainsAny(workDir, "\x00\r\n") {
		return ErrInvalidMediaInput
	}
	args := key.values()
	args.Set("type", "1")
	args.Set("customized_path", workDir)
	args.Set("max_second", "60")
	var out struct {
		Result *bool `json:"result"`
	}
	if err := c.call(ctx, "startRecord", args, &out); err != nil {
		return err
	}
	if out.Result == nil || !*out.Result {
		return ErrMediaOperation
	}
	return nil
}
func (c *Client) StopRecord(ctx context.Context, key StreamKey) error {
	if err := key.Validate(); err != nil {
		return err
	}
	args := key.values()
	args.Set("type", "1")
	var out struct {
		Result *bool `json:"result"`
	}
	if err := c.call(ctx, "stopRecord", args, &out); err != nil {
		return err
	}
	if out.Result == nil || !*out.Result {
		return ErrMediaOperation
	}
	return nil
}
