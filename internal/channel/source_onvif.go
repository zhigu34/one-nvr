package channel

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/audit"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/fault"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/onvif"
)

// probeBudget bounds the whole conversation with one camera, not each request.
// An operator is waiting for this answer, and a camera that cannot finish a few
// small SOAP calls inside this window is not worth making them wait longer for.
const probeBudget = 5 * time.Second

// ProbeInput is one camera to ask. Username and Password are transient: they are
// used for the probe only and are never stored, returned or audited.
type ProbeInput struct {
	IP        string `json:"ip"`
	ONVIFPort *int   `json:"onvif_port,omitempty"`
	Username  string `json:"username"`
	Password  string `json:"password"`
}

// ProbeONVIF reports what one camera offers so the operator does not have to type
// stream paths by hand (PRD CH-06). It is read-only: no revision is created, no
// media source is switched and no credential is stored. The result is evidence
// the operator still has to save through the normal revision and test flow, which
// is why this never touches current_revision_id.
func (s *SourceService) ProbeONVIF(ctx context.Context, p auth.Principal, channelID id.ID, in ProbeInput) (onvif.Result, error) {
	var out onvif.Result
	if !validCredential(in.Username) || !validCredential(in.Password) {
		return out, auth.ErrInvalid
	}
	if in.ONVIFPort != nil && (*in.ONVIFPort < 1 || *in.ONVIFPort > 65535) {
		return out, auth.ErrInvalid
	}
	// Authorization and the address boundary are checked in their own
	// transaction, which is released before the camera is contacted: a slow
	// device must never hold a database connection.
	var target netip.Addr
	if err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := s.Auth.RequireChannelTx(ctx, p, channelID, auth.Configure, tx); err != nil {
			return err
		}
		addr, err := s.network.validateTarget(in.IP)
		target = addr
		return err
	}); err != nil {
		return out, err
	}
	port := 0
	if in.ONVIFPort != nil {
		port = *in.ONVIFPort
	}
	bounded, cancel := context.WithTimeout(ctx, probeBudget)
	defer cancel()
	result, probeErr := onvif.Probe(bounded, onvif.Options{IP: target.String(), Port: port, Username: in.Username, Password: in.Password})
	result.Streams = usableStreams(result.Streams, target)
	if probeErr == nil && len(result.Streams) == 0 {
		probeErr = onvif.ErrNoProfile
	}
	code := ""
	if probeErr != nil {
		mapped := onvifFault(probeErr)
		code = mapped.Code
		probeErr = mapped
	}
	// The attempt is recorded whether it succeeded or not: an operator asking a
	// camera for its configuration is a configuration event, and the failures are
	// the half that explains a later success. Credentials never enter the entry,
	// and neither does the device's own fault text — only this product's code.
	if err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		details, err := json.Marshal(struct {
			IP        string `json:"ip"`
			ONVIFPort int    `json:"onvif_port"`
			Profiles  int    `json:"profiles"`
			Result    string `json:"result"`
		}{IP: target.String(), ONVIFPort: port, Profiles: len(result.Streams), Result: outcome(code)})
		if err != nil {
			return err
		}
		return audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "onvif.probed", ObjectID: channelID, Details: details})
	}); err != nil {
		return out, err
	}
	if probeErr != nil {
		return out, probeErr
	}
	return result, nil
}

func outcome(code string) string {
	if code == "" {
		return "succeeded"
	}
	return code
}

// usableStreams drops anything the device offered that this product cannot store
// or must not follow: a stream pointing somewhere other than the probed camera is
// outside the configured boundary, and a path that the source configuration would
// reject must not reach the form as if it were usable.
func usableStreams(streams []onvif.Stream, target netip.Addr) []onvif.Stream {
	out := make([]onvif.Stream, 0, len(streams))
	for _, stream := range streams {
		addr, err := netip.ParseAddr(stream.IP)
		if err != nil || addr.Unmap() != target {
			continue
		}
		if stream.RTSPPort < 1 || stream.RTSPPort > 65535 || !validSourcePath(stream.Path, false) {
			continue
		}
		out = append(out, stream)
	}
	return out
}

// onvifFault is the only place an ONVIF failure becomes an API answer, so the
// audit code and the HTTP code can never disagree. Operator-correctable answers
// are 422; anything the operator cannot fix by editing the form is a retryable
// 503, matching how this API already separates the two.
func onvifFault(err error) *fault.Error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return fault.New(503, "onvif_timeout", "ONVIF 探测超时，请检查网络与摄像头负载后重试")
	case errors.Is(err, onvif.ErrUnreachable):
		return fault.New(503, "onvif_unreachable", "摄像头无响应，请检查地址、ONVIF 端口与网络")
	case errors.Is(err, onvif.ErrNotDetected):
		return fault.New(422, "onvif_not_detected", "该地址不是 ONVIF 设备服务，请确认 ONVIF 端口（常见 80 / 8000）")
	case errors.Is(err, onvif.ErrAuthRejected):
		return fault.New(422, "onvif_auth_rejected", "ONVIF 认证被拒绝，请检查用户名与密码")
	case errors.Is(err, onvif.ErrNoProfile):
		return fault.New(422, "onvif_no_profile", "设备未提供可用的 RTSP 码流")
	default:
		return fault.New(503, "onvif_response_invalid", "ONVIF 响应无法解析")
	}
}
