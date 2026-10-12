package detection

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/zhigu34/one-nvr/internal/events"

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
		cameras[CameraName(channelNo)] = CameraConfig{
			Enabled:  enabled,
			InputURL: "rtsp://zlm:554/one_nvr/" + session,
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
	return map[string]any{
		"mqtt": map[string]any{
			"host":         "mqtt",
			"user":         "one_nvr",
			"password":     s.MQTTKey,
			"topic_prefix": "frigate",
		},
		"detectors": map[string]any{"cpu": map[string]any{"type": "cpu", "num_threads": 2}},
		"model":     map[string]any{"path": "/cpu_model.tflite"},
		"version":   version,
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

// MQTTReloader publishes the Frigate reload command through one short-lived
// broker connection per publication. A reload is rare; keeping a persistent
// second connection would add failure modes without buying anything.
type MQTTReloader struct {
	Address, Username, Password string
}

func (r MQTTReloader) Reload(ctx context.Context) error {
	client, err := mqttDial(ctx, r.Address, r.Username, r.Password)
	if err != nil {
		return err
	}
	defer client.Close()
	return client.Publish(ctx, "frigate/reload", []byte("{}"))
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

// mqttDial is the broker connector, kept behind a variable so the publication
// test can substitute a fake without a real broker.
var mqttDial = func(ctx context.Context, address, username, password string) (mqttPublisher, error) {
	client, err := events.Dial(ctx, address, username, password, "one-nvr-detection")
	if err != nil {
		return nil, err
	}
	return client, nil
}

type mqttPublisher interface {
	Publish(ctx context.Context, topic string, payload []byte) error
	Close() error
}
