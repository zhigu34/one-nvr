package onvif

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	deviceServicePath = "/onvif/device_service"
	maxBodyBytes      = 512 << 10
	// Profiles are resolved one request each, so the count is bounded to keep a
	// probe inside its time budget.
	maxProfiles = 8
)

// Probe asks one camera for its identity and its RTSP streams. It is a bounded,
// read-only conversation: no credential is stored, returned or logged, and the
// caller is responsible for having authorized both the operator and the target
// address before calling.
func Probe(ctx context.Context, o Options) (Result, error) {
	addr, err := netip.ParseAddr(strings.TrimSpace(o.IP))
	if err != nil || addr.Zone() != "" || addr.Is4In6() {
		return Result{}, ErrNotDetected
	}
	if o.Port < 0 || o.Port > 65535 {
		return Result{}, ErrNotDetected
	}
	port := o.Port
	if port == 0 {
		port = 80
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	p := probe{
		ip:       addr.Unmap().String(),
		username: o.Username,
		password: o.Password,
		port:     port,
		client: &http.Client{
			Timeout:       timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			Transport: &http.Transport{
				Proxy:             nil,
				DialContext:       (&net.Dialer{Timeout: timeout}).DialContext,
				MaxIdleConns:      4,
				IdleConnTimeout:   30 * time.Second,
				DisableKeepAlives: false,
			},
		},
	}
	p.deviceURL = "http://" + net.JoinHostPort(p.ip, strconv.Itoa(port)) + deviceServicePath
	// Devices reject a digest whose Created is outside their tolerance, so the
	// device clock is read first and used for every authenticated stamp.
	// GetSystemDateAndTime is the one operation the specification keeps
	// anonymous, which is exactly why it can bootstrap this.
	if root, err := p.call(ctx, p.deviceURL, bodyGetSystemDateAndTime, false); err == nil {
		if clock, ok := deviceClock(root); ok {
			p.offset = time.Until(clock)
		}
	} else if errors.Is(err, ErrUnreachable) || errors.Is(err, ErrNotDetected) {
		return Result{}, err
	}
	info, err := p.call(ctx, p.deviceURL, bodyGetDeviceInformation, true)
	if err != nil {
		return Result{}, err
	}
	out := Result{
		ONVIFPort: port,
		Device: Device{
			Manufacturer:    info.value("Manufacturer"),
			Model:           info.value("Model"),
			FirmwareVersion: info.value("FirmwareVersion"),
			SerialNumber:    info.value("SerialNumber"),
		},
		Streams: make([]Stream, 0, 2),
	}
	caps, err := p.call(ctx, p.deviceURL, bodyGetCapabilities, true)
	if err != nil {
		return Result{}, err
	}
	if ptz, ok := caps.find("PTZ"); ok {
		out.PTZ = strings.TrimSpace(ptz.value("XAddr")) != ""
	}
	mediaURL := p.mediaService(caps)
	profiles, err := p.call(ctx, mediaURL, bodyGetProfiles, true)
	if err != nil {
		return Result{}, err
	}
	count := 0
	for _, profile := range profiles.all("Profiles") {
		if count >= maxProfiles {
			break
		}
		token := profile.attr("token")
		if token == "" {
			continue
		}
		count++
		stream, err := p.resolve(ctx, mediaURL, token, profile)
		if err != nil {
			return Result{}, err
		}
		out.Streams = append(out.Streams, stream)
	}
	if len(out.Streams) == 0 {
		return Result{}, ErrNoProfile
	}
	return out, nil
}

type probe struct {
	ip        string
	port      int
	username  string
	password  string
	offset    time.Duration
	deviceURL string
	client    *http.Client
}

// call performs one SOAP request. Authenticated calls stamp Created from the
// device clock so the digest survives a device that runs on its own time.
func (p *probe) call(ctx context.Context, target, body string, authenticate bool) (node, error) {
	username, password := "", ""
	if authenticate {
		username, password = p.username, p.password
	}
	created := time.Now().Add(p.offset)
	raw, err := envelope(body, username, password, created)
	if err != nil {
		return node{}, ErrResponseInvalid
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(raw))
	if err != nil {
		return node{}, ErrNotDetected
	}
	request.Header.Set("Content-Type", "application/soap+xml; charset=utf-8")
	response, err := p.client.Do(request)
	if err != nil {
		return node{}, ErrUnreachable
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBodyBytes+1))
	if err != nil || len(data) > maxBodyBytes {
		return node{}, ErrResponseInvalid
	}
	root, err := decode(data)
	if err != nil {
		return node{}, ErrNotDetected
	}
	// A web page, a JSON error or any other non-SOAP answer is not a device
	// service; only an envelope is worth reading further.
	if root.XMLName.Local != "Envelope" {
		return node{}, ErrNotDetected
	}
	if message, ok := faultMessage(root); ok {
		if authorized(message) {
			return node{}, fmt.Errorf("%w: %s", ErrAuthRejected, message)
		}
		return node{}, fmt.Errorf("%w: %s", ErrResponseInvalid, message)
	}
	if response.StatusCode != http.StatusOK {
		return node{}, ErrResponseInvalid
	}
	return root, nil
}

// mediaService keeps the media service on the address that was probed. A device
// is free to advertise any XAddr, and following it blindly would let a camera
// point this service at an address outside the configured camera boundary.
func (p *probe) mediaService(capabilities node) string {
	media, ok := capabilities.find("Media")
	if !ok {
		return p.deviceURL
	}
	parsed, err := url.Parse(strings.TrimSpace(media.value("XAddr")))
	if err != nil || parsed.Path == "" {
		return p.deviceURL
	}
	host, err := netip.ParseAddr(parsed.Hostname())
	if err != nil || host.Unmap().String() != p.ip {
		return p.deviceURL
	}
	return "http://" + net.JoinHostPort(p.ip, strconv.Itoa(p.port)) + parsed.Path
}

func (p *probe) resolve(ctx context.Context, mediaURL, token string, profile node) (Stream, error) {
	stream := Stream{Token: token, Name: profile.value("Name"), Encoding: profile.value("Encoding"), IP: p.ip, RTSPPort: 554}
	if resolution, ok := profile.find("Resolution"); ok {
		stream.Width, _ = strconv.Atoi(resolution.value("Width"))
		stream.Height, _ = strconv.Atoi(resolution.value("Height"))
	}
	uri, err := p.call(ctx, mediaURL, streamRequest(token), true)
	if err != nil {
		return Stream{}, err
	}
	raw := strings.TrimSpace(uri.value("Uri"))
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "rtsp" && parsed.Scheme != "rtsps") || parsed.Host == "" {
		return Stream{}, ErrNoProfile
	}
	if value, err := strconv.Atoi(parsed.Port()); err == nil && value > 0 && value <= 65535 {
		stream.RTSPPort = value
	}
	// The path is stored escaped so a query string or an escaped character
	// survives being rebuilt into a URL later.
	path := parsed.EscapedPath()
	if parsed.RawQuery != "" {
		path += "?" + parsed.RawQuery
	}
	if !strings.HasPrefix(path, "/") || len(path) > 4096 || strings.ContainsAny(path, "\x00\r\n") {
		return Stream{}, ErrNoProfile
	}
	stream.Path = path
	return stream, nil
}

// deviceClock reads UTCDateTime from a GetSystemDateAndTime response. The
// device's timezone is deliberately ignored: WS-Security stamps are UTC.
func deviceClock(root node) (time.Time, bool) {
	utc, ok := root.find("UTCDateTime")
	if !ok {
		return time.Time{}, false
	}
	date, dateOK := utc.find("Date")
	clock, clockOK := utc.find("Time")
	if !dateOK || !clockOK {
		return time.Time{}, false
	}
	year, e1 := strconv.Atoi(date.value("Year"))
	month, e2 := strconv.Atoi(date.value("Month"))
	day, e3 := strconv.Atoi(date.value("Day"))
	hour, e4 := strconv.Atoi(clock.value("Hour"))
	minute, e5 := strconv.Atoi(clock.value("Minute"))
	second, e6 := strconv.Atoi(clock.value("Second"))
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil || e6 != nil {
		return time.Time{}, false
	}
	if month < 1 || month > 12 || day < 1 || day > 31 || hour > 23 || minute > 60 || second > 60 {
		return time.Time{}, false
	}
	return time.Date(year, time.Month(month), day, hour, minute, second, 0, time.UTC), true
}
