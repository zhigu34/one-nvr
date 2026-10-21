package detection

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	return s.publish(ctx, p.UserID)
}

// publish is the shared publication core. The manual endpoint and the
// session-drift sync both go through it, so an automatic publication writes
// exactly what a manual one would: same render, same mapping sync, same
// revision record.
func (s *Service) publish(ctx context.Context, actor id.ID) (Revision, error) {
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
	hash, err := s.PublishRendered(ctx, rendered, actor, enabled)
	if err != nil {
		return Revision{}, err
	}
	// The visible result of the publication for the page.
	var revision Revision
	err = s.DB.Pool.QueryRow(ctx, `SELECT id,config_hash,state,camera_count,error_code,to_char(created_at,'YYYY-MM-DD"T"HH24:MI:SSOF'),config_versions FROM detection_config_revisions WHERE config_hash=$1 ORDER BY created_at DESC LIMIT 1`, hash).
		Scan(&revision.ID, &revision.ConfigHash, &revision.State, &revision.CameraCount, &revision.ErrorCode, &revision.CreatedAt, &revision.ConfigVersions)
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
	rows, err := s.DB.Pool.Query(ctx, `SELECT c.id,c.channel_no,d.enabled,d.stream,d.fps,d.labels,d.sensitivity,
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
		var sensitivity Sensitivity
		if err := rows.Scan(&channelID, &channelNo, &enabled, &stream, &fps, &labels, &sensitivity, &session); err != nil {
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
		zones, err := s.loadZoneRenders(ctx, nil, channelID)
		if err != nil {
			return nil, nil, err
		}
		cameras[CameraName(channelNo)] = CameraConfig{
			Enabled:     enabled,
			InputURL:    input,
			FPS:         fps,
			Width:       640,
			Height:      360,
			Labels:      labelList,
			Sensitivity: sensitivity,
			Zones:       zones,
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

// Reload asks the detector to reload and then waits for it to come back.
//
// The wait is the point. /api/restart is asynchronous: it returns 200 as soon as
// the request is accepted, and the detector restarts afterwards. A configuration
// the detector cannot parse therefore produced "published" followed by a detector
// that never came back — the operator saw a success message and lost detection,
// and nothing in the product said which of the two had happened.
//
// Waiting turns that into an honest answer: either the detector serves requests
// again within the budget, or the publish is reported as failed and the previous
// config is restored by the caller.
func (r HTTPReloader) Reload(ctx context.Context) error {
	if r.BaseURL == "" {
		return ErrPublishFailed
	}
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	base := strings.TrimRight(r.BaseURL, "/")
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/restart", nil)
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
	return waitDetectorReady(ctx, client, base)
}

// detectorReadyBudget bounds how long a restart may take. Frigate's restart is a
// process restart: a few seconds normally, longer on a large config. Fifteen
// seconds is well past the observed norm and short enough that an operator
// waiting on the publish dialog does not give up first.
const detectorReadyBudget = 15 * time.Second

// waitDetectorReady polls until the detector answers again.
//
// It tolerates connection failures during the window rather than treating the
// first one as fatal: a restarting process refuses connections by definition, and
// failing on that would report every successful publish as broken.
func waitDetectorReady(ctx context.Context, client *http.Client, base string) error {
	deadline := time.Now().Add(detectorReadyBudget)
	for {
		probe, cancel := context.WithTimeout(ctx, 3*time.Second)
		request, err := http.NewRequestWithContext(probe, http.MethodGet, base+"/api/version", nil)
		if err != nil {
			cancel()
			return ErrPublishFailed
		}
		response, err := client.Do(request)
		if err == nil {
			io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
			response.Body.Close()
			cancel()
			if response.StatusCode/100 == 2 {
				return nil
			}
		} else {
			cancel()
		}
		if ctx.Err() != nil {
			return ErrPublishFailed
		}
		if time.Now().After(deadline) {
			return ErrPublishFailed
		}
		select {
		case <-ctx.Done():
			return ErrPublishFailed
		case <-time.After(500 * time.Millisecond):
		}
	}
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
	err := s.DB.Pool.QueryRow(ctx, `SELECT id,config_hash,state,camera_count,error_code,to_char(created_at,'YYYY-MM-DD"T"HH24:MI:SSOF'),config_versions FROM detection_config_revisions ORDER BY created_at DESC LIMIT 1`).
		Scan(&revision.ID, &revision.ConfigHash, &revision.State, &revision.CameraCount, &revision.ErrorCode, &revision.CreatedAt, &revision.ConfigVersions)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// "Pending" means the stored settings have moved on since the publication
	// the detector is running. Compare the per-channel versions that
	// publication captured, not the rendered hash: the render includes the
	// live session identifier, so it differs after any restart even when no
	// setting changed — and it would report "changed" for reasons the operator
	// cannot act on.
	var published map[string]int64
	if err := json.Unmarshal(revision.ConfigVersions, &published); err != nil {
		published = map[string]int64{}
	}
	current, err := s.configVersions(ctx)
	if err != nil {
		return &revision, nil
	}
	var now map[string]int64
	if err := json.Unmarshal(current, &now); err != nil {
		return &revision, nil
	}
	revision.Pending = !sameVersions(published, now)
	return &revision, nil
}

func sameVersions(a, b map[string]int64) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if other, ok := b[key]; !ok || other != value {
			return false
		}
	}
	return true
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

// SyncAfterSessionChange republishes when the running detector points at a
// media session that no longer exists, without waiting for an operator.
//
// The published input URL contains the channel's stream-session id, and a
// channel gets a new session whenever it reconnects, restarts or switches
// source. The detector would otherwise keep pulling a dead path until someone
// noticed and pressed publish again.
//
// This deliberately does NOT publish pending setting edits: an operator's
// unsaved-then-saved change must stay visible as "not yet published" until
// they choose to apply it. The distinction is the configuration version — if
// any channel's settings moved on since the last publication, the drift is the
// operator's to resolve, not the sync's to overwrite.
func (s *Service) SyncAfterSessionChange(ctx context.Context) {
	if s.DB == nil || s.DB.Pool == nil || s.Auth == nil {
		return
	}
	latest, err := s.latestRevisionRow(ctx)
	if err != nil || latest == nil || latest.State != "published" {
		return
	}
	current, err := s.configVersions(ctx)
	if err != nil {
		return
	}
	var published, now map[string]int64
	if json.Unmarshal(latest.ConfigVersions, &published) != nil || json.Unmarshal(current, &now) != nil {
		return
	}
	if !sameVersions(published, now) {
		// Settings changed: an operator decision is pending, not a drift.
		return
	}
	// Only a session drift may be corrected here. If an enabled channel is
	// currently without a session it is offline, and its camera would drop out
	// of the render — publishing that would restart the detector just because
	// a camera blinked, and restart it again when the camera returns. Waiting
	// costs nothing: the sync runs continuously, so the reconnect is picked up
	// on a later pass.
	enabled, err := s.enabledConfigCount(ctx)
	if err != nil {
		return
	}
	cameras, base, err := s.cameraConfigs(ctx)
	if err != nil || len(cameras) == 0 || len(cameras) != enabled {
		return
	}
	rendered, err := Render(base, cameras)
	if err != nil {
		return
	}
	sum := sha256.Sum256(rendered)
	if hex.EncodeToString(sum[:]) == latest.ConfigHash {
		// Nothing drifted — the published configuration still matches.
		return
	}
	revision, err := s.publish(ctx, "")
	if err != nil {
		slog.Warn("detection session resync failed", "error", err)
		return
	}
	slog.Info("detection configuration resynced after a session change", "cameras", revision.CameraCount)
}

// latestRevisionRow returns the newest publication attempt, or nil when none
// exists. It is the unauthenticated counterpart of LatestRevision, used by the
// worker-side sync.
// latestRevisionRow returns the newest *successful* publication.
//
// Not simply the newest row: a failed publish is an event, not the current state.
// Reading the newest row made one reload failure disable automatic session
// syncing forever — the newest row stayed "failed", the sync bailed out on its
// state check, and the detector kept pulling a media session that no longer
// existed. Measured on the live site: detection had been dead since the failure
// and every channel logged "ffmpeg process is not running", while the page showed
// nothing wrong with publishing.
func (s *Service) latestRevisionRow(ctx context.Context) (*Revision, error) {
	var revision Revision
	err := s.DB.Pool.QueryRow(ctx, `SELECT id,config_hash,state,camera_count,error_code,to_char(created_at,'YYYY-MM-DD"T"HH24:MI:SSOF'),config_versions FROM detection_config_revisions WHERE state='published' ORDER BY created_at DESC LIMIT 1`).
		Scan(&revision.ID, &revision.ConfigHash, &revision.State, &revision.CameraCount, &revision.ErrorCode, &revision.CreatedAt, &revision.ConfigVersions)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &revision, nil
}

// enabledConfigCount is how many channels have detection switched on. The
// session sync compares it against the cameras a render produced to tell
// "every channel is online" from "some channel is down right now".
func (s *Service) enabledConfigCount(ctx context.Context) (int, error) {
	var count int
	err := s.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM detection_configs WHERE enabled`).Scan(&count)
	return count, err
}
