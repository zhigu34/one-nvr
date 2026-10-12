package detection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/zhigu34/one-nvr/internal/media/probe"
	"github.com/zhigu34/one-nvr/internal/media/zlm"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/fault"
	"github.com/zhigu34/one-nvr/internal/id"
)

// Publish renders every enabled channel's detection settings into Frigate's
// config.yml and triggers the upstream reload. It is the only writer of that
// file, so the on-disk config is always a function of the database rows.
func (s *Service) Publish(ctx context.Context, p auth.Principal) (Revision, error) {
	if err := s.Auth.RequireAdmin(ctx, p); err != nil {
		return Revision{}, err
	}
	cameras, base, err := s.cameraConfigs(ctx)
	if err != nil {
		return Revision{}, err
	}
	rendered, err := Render(base, cameras)
	if err != nil {
		return Revision{}, err
	}
	enabled := 0
	for _, camera := range cameras {
		if camera.Enabled {
			enabled++
		}
	}
	// The rendered camera keys are the detection identity the event stream
	// will report; without a matching mapping row every event is dropped as
	// "unmapped camera". Publishing is therefore also where the mapping is
	// established, so the feature is usable without a hidden manual step.
	if err := s.syncCameraMappings(ctx, cameras); err != nil {
		return Revision{}, err
	}
	hash, err := s.PublishRendered(ctx, rendered, p.UserID, enabled)
	if err != nil {
		return Revision{}, err
	}
	// The visible result of the publication for the page.
	var revision Revision
	err = s.DB.Pool.QueryRow(ctx, `SELECT id,config_hash,state,camera_count,error_code,to_char(created_at,'YYYY-MM-DD"T"HH24:MI:SSOF') FROM detection_config_revisions WHERE config_hash=$1 ORDER BY created_at DESC LIMIT 1`, hash).
		Scan(&revision.ID, &revision.ConfigHash, &revision.State, &revision.CameraCount, &revision.ErrorCode, &revision.CreatedAt)
	if err != nil {
		return Revision{}, err
	}
	return revision, nil
}

// cameraConfigs assembles the render input: the database rows plus the
// internal ZLM RTSP path each camera's detection reads (PRD §10.2: detection
// pulls from the media server, never from the camera directly).
func (s *Service) cameraConfigs(ctx context.Context) (map[string]CameraConfig, map[string]any, error) {
	base, err := s.baseConfig(ctx)
	if err != nil {
		return nil, nil, err
	}
	rows, err := s.DB.Pool.Query(ctx, `SELECT c.id,c.channel_no,d.enabled,d.stream,d.fps,d.labels,
			COALESCE((SELECT ss.id::text FROM stream_sessions ss WHERE ss.channel_id=c.id AND ss.purpose<>'probe' AND ss.state NOT IN ('closed','failed') ORDER BY ss.generation DESC,ss.created_at DESC LIMIT 1),'') AS session
		FROM channels c JOIN detection_configs d ON d.channel_id=c.id WHERE d.enabled`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	cameras := map[string]CameraConfig{}
	for rows.Next() {
		var channelID id.ID
		var channelNo int
		var stream, session string
		var fps int
		var enabled bool
		var labels []byte
		if err := rows.Scan(&channelID, &channelNo, &enabled, &stream, &fps, &labels, &session); err != nil {
			return nil, nil, err
		}
		var labelList []string
		if err := json.Unmarshal(labels, &labelList); err != nil {
			return nil, nil, err
		}
		// Without an active session there is nothing to detect on; the camera
		// is omitted rather than pointed at a dead path.
		if session == "" {
			continue
		}
		// The media server authorizes every play through its on_play hook, and
		// the detector holds no user session. It therefore reads through the
		// same internal token the media probes use — without it the hook
		// refuses the pull and detection silently never sees a frame.
		input, err := probe.InternalURL(zlm.StreamKey{VHost: "__defaultVhost__", App: "one_nvr", Stream: session}, s.ProbeToken)
		if err != nil {
			return nil, nil, err
		}
		cameras[CameraName(channelNo)] = CameraConfig{
			Enabled:  enabled,
			InputURL: input,
			FPS:      fps,
			Width:    640,
			Height:   360,
			Labels:   labelList,
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	// One detection profile maps to the locked model path; width/height follow
	// the standard 640x360 detect profile of the locked config.
	for name, camera := range cameras {
		camera.Width, camera.Height = 640, 360
		cameras[name] = camera
	}
	return cameras, base, nil
}

// baseConfig is the site-level half of config.yml: the MQTT link and the
// detector. mqtt/reload makes Frigate re-read the file when told to.
func (s *Service) baseConfig(ctx context.Context) (map[string]any, error) {
	if s.MQTTKey == "" {
		return nil, ErrPublishFailed
	}
	version := "0.17-0"
	if snapshot, err := s.Runtime(ctx, auth.Principal{}); err == nil && snapshot.Version != "" {
		version = snapshot.Version
	}
	detectors, model := detectorConfig(s.Inference)
	return map[string]any{
		"detectors": detectors,
		"model":     model,
		"mqtt": map[string]any{
			"host":         "mqtt",
			"user":         "one_nvr",
			"password":     s.MQTTKey,
			"topic_prefix": "frigate",
		},
		"version": version,
		// Frigate's TLS listener is deliberately off. It exists only for
		// direct browser access to Frigate itself, which this deployment never
		// uses: the detector is reached over the container network and all
		// operator access goes through one-nvr. Left unset, Frigate defaults
		// it to enabled, generates a self-signed certificate, and its certsync
		// service polls 127.0.0.1:8971 with a TLS handshake every minute
		// forever — the "empty request, 400" line in its nginx log.
		"tls": map[string]any{"enabled": false},
	}, nil
}

// RefreshRuntime probes the upstream version and stats (AI-07). It never
// errors the caller: an unreachable detector is a state to show, not a fault.
func (s *Service) RefreshRuntime(ctx context.Context) {
	if s.Frigate == "" {
		return
	}
	client := &http.Client{Timeout: 5 * time.Second}
	reachable := true
	version := ""
	detector := ""
	var inference *float64
	overloaded := false
	if body, err := getJSON(ctx, client, s.Frigate+"/api/version"); err == nil {
		var payload struct {
			Version string `json:"version"`
		}
		if json.Unmarshal(body, &payload) == nil {
			version = payload.Version
		}
	} else {
		reachable = false
	}
	if reachable {
		if body, err := getJSON(ctx, client, s.Frigate+"/api/stats"); err == nil {
			var payload struct {
				Detectors map[string]struct {
					InferenceSpeed *float64 `json:"inference_speed"`
				} `json:"detectors"`
			}
			if json.Unmarshal(body, &payload) == nil {
				for name, value := range payload.Detectors {
					if detector == "" || name < detector {
						detector = name
					}
					if value.InferenceSpeed != nil && (inference == nil || *value.InferenceSpeed > *inference) {
						inference = value.InferenceSpeed
					}
				}
				// The locked profile keeps inference well under this; crossing
				// it means the detector is saturated (AI-07 overloading hint).
				overloaded = inference != nil && *inference > 200
			}
		}
	}
	if _, err := s.DB.Pool.Exec(ctx, `INSERT INTO detection_runtime(id,version,detector,inference_ms,overloaded,reachable,observed_at)
		VALUES('current',$1,$2,$3,$4,$5,clock_timestamp())
		ON CONFLICT (id) DO UPDATE SET version=$1,detector=$2,inference_ms=$3,overloaded=$4,reachable=$5,observed_at=clock_timestamp()`,
		version, detector, inference, overloaded, reachable); err != nil && ctx.Err() == nil {
		slog.Warn("detection runtime snapshot unavailable", "error", err)
	}
}

func getJSON(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fault.New(503, "detection_unavailable", "检测服务不可用")
	}
	return io.ReadAll(io.LimitReader(response.Body, 1<<20))
}

// HTTPReloader makes Frigate re-read the config file by restarting its
// process. Frigate 0.17.2 has no config-reload MQTT command — the only way a
// changed config.yml takes effect is a restart, and its own API is the
// supported path (POST /api/restart). Requests from inside the container
// network are treated as the admin role, so no credential is involved.
type HTTPReloader struct {
	BaseURL string
	Client  *http.Client
}

func (r HTTPReloader) Reload(ctx context.Context) error {
	if r.BaseURL == "" {
		return ErrPublishFailed
	}
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(r.BaseURL, "/")+"/api/restart", nil)
	if err != nil {
		return ErrPublishFailed
	}
	response, err := client.Do(request)
	if err != nil {
		return ErrPublishFailed
	}
	defer response.Body.Close()
	io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	if response.StatusCode/100 != 2 {
		return ErrPublishFailed
	}
	return nil
}

// RunRuntime polls the detector every minute for the capability card.
func (s *Service) RunRuntime(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		s.RefreshRuntime(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// LatestRevision returns the most recent publication attempt for the page.
func (s *Service) LatestRevision(ctx context.Context, p auth.Principal) (*Revision, error) {
	if _, err := s.requireAdminRead(ctx, p); err != nil {
		return nil, err
	}
	var revision Revision
	err := s.DB.Pool.QueryRow(ctx, `SELECT id,config_hash,state,camera_count,error_code,to_char(created_at,'YYYY-MM-DD"T"HH24:MI:SSOF') FROM detection_config_revisions ORDER BY created_at DESC LIMIT 1`).
		Scan(&revision.ID, &revision.ConfigHash, &revision.State, &revision.CameraCount, &revision.ErrorCode, &revision.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &revision, nil
}

func (s *Service) requireAdminRead(ctx context.Context, p auth.Principal) (struct{}, error) {
	if err := s.Auth.RequireAdmin(ctx, p); err != nil {
		return struct{}{}, err
	}
	return struct{}{}, nil
}

// syncCameraMappings makes the event-routing table match what was just
// rendered: one row per enabled camera, keyed by the stable channel name.
// Rows for cameras that are no longer rendered are dropped, so a disabled
// channel stops matching events instead of silently attributing them.
func (s *Service) syncCameraMappings(ctx context.Context, cameras map[string]CameraConfig) error {
	names := make([]string, 0, len(cameras))
	for name, camera := range cameras {
		if camera.Enabled {
			names = append(names, name)
		}
	}
	return s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if len(names) == 0 {
			_, err := tx.Exec(ctx, `DELETE FROM frigate_cameras`)
			return err
		}
		// Channel number is the last two digits of the stable camera name; the
		// name itself is generated from it, never parsed from user input.
		if _, err := tx.Exec(ctx, `DELETE FROM frigate_cameras WHERE frigate_name <> ALL($1)`, names); err != nil {
			return err
		}
		for _, name := range names {
			var channelNo int
			if _, err := fmt.Sscanf(name, "ch%02d", &channelNo); err != nil {
				continue
			}
			if _, err := tx.Exec(ctx, `INSERT INTO frigate_cameras(channel_id,frigate_name)
				SELECT id,$2 FROM channels WHERE channel_no=$1
				ON CONFLICT (channel_id) DO UPDATE SET frigate_name=EXCLUDED.frigate_name`,
				channelNo, name); err != nil {
				return err
			}
		}
		return nil
	})
}

// detectorConfig expresses the deployment's validated detection hardware the
// way Frigate expects it. The CPU path is the universal fallback; the Intel
// path runs the OpenVINO model the pinned image already bundles
// (/openvino-model/ssdlite_mobilenet_v2.xml) on the GPU. Selection comes from
// the hardware report, so a machine without a validated accelerator keeps the
// CPU detector instead of being pointed at a device that is not there.
func detectorConfig(inference string) (map[string]any, map[string]any) {
	// The criterion matches the deployment-time probe exactly (cmd/admin/
	// hardware.go): anything that is not "cpu" is an accelerated profile. The
	// value is not always a vendor name — a validated Intel proposal carries
	// its PCI address ("0000:00:02.0") — so comparing against "intel" would
	// send every real deployment back to the CPU detector.
	if inference != "" && inference != "cpu" {
		// Identical to the deployment-time hardware probe's profile
		// (cmd/admin/hardware.go): one definition of "Intel accelerated" means
		// a publish can never disagree with what the probe validated.
		return map[string]any{
				"intel": map[string]any{"type": "openvino", "device": "GPU"},
			}, map[string]any{
				"path":               "/openvino-model/ssdlite_mobilenet_v2.xml",
				"labelmap_path":      "/openvino-model/coco_91cl_bkgr.txt",
				"width":              300,
				"height":             300,
				"input_tensor":       "nhwc",
				"input_pixel_format": "bgr",
			}
	}
	return map[string]any{
			"cpu": map[string]any{"type": "cpu", "num_threads": 2},
		}, map[string]any{
			"path": "/cpu_model.tflite",
		}
}
