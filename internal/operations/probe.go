package operations

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/zhigu34/one-nvr/internal/database"
)

var ErrProbeFailed = errors.New("probe_failed")
var versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:[-+][a-zA-Z0-9._-]+)?$`)

type ProbeTarget struct{ URL, Kind string }

func NewProbeClient() *http.Client {
	return &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext, MaxIdleConns: 8, IdleConnTimeout: 30 * time.Second}}
}
func ProbeHTTP(ctx context.Context, target ProbeTarget, client *http.Client) error {
	q, err := http.NewRequestWithContext(ctx, http.MethodGet, target.URL, nil)
	if err != nil {
		return ErrProbeFailed
	}
	r, err := client.Do(q)
	if err != nil {
		return ErrProbeFailed
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		return ErrProbeFailed
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 16385))
	if err != nil || len(body) == 0 || len(body) > 16384 {
		return ErrProbeFailed
	}
	switch target.Kind {
	case "zlm", "openlist":
		var dto struct {
			Code *int `json:"code"`
		}
		if json.Unmarshal(body, &dto) != nil || dto.Code == nil {
			return ErrProbeFailed
		}
		wanted := 0
		if target.Kind == "openlist" {
			wanted = 200
		}
		if *dto.Code != wanted {
			return ErrProbeFailed
		}
	case "health":
		var dto struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(body, &dto) != nil || (dto.Status != "ready" && dto.Status != "alive") {
			return ErrProbeFailed
		}
	case "version": // Frigate version endpoint returns a short plain version.
		version := strings.TrimSpace(string(body))
		if strings.HasPrefix(version, `"`) {
			if json.Unmarshal(body, &version) != nil {
				return ErrProbeFailed
			}
		}
		if len(body) > 256 || !versionPattern.MatchString(version) {
			return ErrProbeFailed
		}
	default:
		return ErrProbeFailed
	}
	return nil
}

// ProbeMQTT performs MQTT 3.1.1 CONNECT and checks CONNACK, then disconnects.
// This is a health probe, not the event subscription client.
// https://docs.oasis-open.org/mqtt/mqtt/v3.1.1/os/mqtt-v3.1.1-os.html
func ProbeMQTT(ctx context.Context, address, username, password string) error {
	if username == "" || password == "" || len(username) > 128 || len(password) > 256 {
		return ErrProbeFailed
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if err != nil {
		return ErrProbeFailed
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	field := func(s string) []byte {
		b := make([]byte, len(s)+2)
		binary.BigEndian.PutUint16(b, uint16(len(s)))
		copy(b[2:], s)
		return b
	}
	body := []byte{0, 4, 'M', 'Q', 'T', 'T', 4, 0xc2, 0, 10}
	for _, s := range []string{"one-nvr-health", username, password} {
		body = append(body, field(s)...)
	}
	header := []byte{0x10}
	for n := len(body); ; {
		b := byte(n % 128)
		n /= 128
		if n > 0 {
			b |= 128
		}
		header = append(header, b)
		if n == 0 {
			break
		}
	}
	if _, err := conn.Write(append(header, body...)); err != nil {
		return ErrProbeFailed
	}
	var ack [4]byte
	if _, err := io.ReadFull(conn, ack[:]); err != nil || ack != [4]byte{0x20, 0x02, 0x00, 0x00} {
		return ErrProbeFailed
	}
	_, _ = conn.Write([]byte{0xe0, 0x00})
	return nil
}

type Prober struct {
	Operations                              *Service
	Targets                                 map[string]ProbeTarget
	Checks                                  map[string]func(context.Context) error
	Client                                  *http.Client
	MQTTAddress, MQTTUsername, MQTTPassword string
}

func (p *Prober) Sample(ctx context.Context) {
	var wg sync.WaitGroup
	observe := func(name string, check func(context.Context) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			err := check(probeCtx)
			cancel()
			state, reason := "healthy", "probe_succeeded"
			if err != nil {
				state, reason = "unavailable", "probe_failed"
			}
			writeCtx, finish := context.WithTimeout(ctx, 2*time.Second)
			defer finish()
			_ = p.Operations.Observe(writeCtx, Observation{Name: name, State: state, Reason: reason, ObservedAt: time.Now()})
		}()
	}
	observe("postgres", func(ctx context.Context) error { return database.Ready(ctx, p.Operations.DB) })
	for name, target := range p.Targets {
		if !p.Operations.Enabled(name) {
			continue
		}
		observe(name, func(ctx context.Context) error { return ProbeHTTP(ctx, target, p.Client) })
	}
	for name, check := range p.Checks {
		if p.Operations.Enabled(name) {
			observe(name, check)
		}
	}
	if p.Operations.FrigateEnabled {
		observe("mqtt", func(ctx context.Context) error { return ProbeMQTT(ctx, p.MQTTAddress, p.MQTTUsername, p.MQTTPassword) })
	}
	wg.Wait()
	writeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_ = p.Operations.Observe(writeCtx, Observation{Name: "worker", State: "healthy", Reason: "sampler_running", ObservedAt: time.Now()})
}
func (p *Prober) Run(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		p.Sample(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
