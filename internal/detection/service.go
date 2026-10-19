// Package detection owns per-channel detection configuration, the versioned
// publication of Frigate's camera config, and the runtime capability snapshot.
package detection

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/database"
	"github.com/zhigu34/one-nvr/internal/fault"
	"github.com/zhigu34/one-nvr/internal/id"
)

// Config is one channel's detection settings (AI-01).
type Config struct {
	ChannelID id.ID    `json:"channel_id"`
	Enabled   bool     `json:"enabled"`
	Stream    string   `json:"stream"`
	FPS       int      `json:"fps"`
	Labels    []string `json:"labels"`
	Version   int64    `json:"version"`
}

// RuntimeSnapshot is AI-07's capability view: what the detector reports about
// itself, refreshed by the worker rather than probed per page view.
type RuntimeSnapshot struct {
	Version     string   `json:"version"`
	Detector    string   `json:"detector"`
	InferenceMS *float64 `json:"inference_ms"`
	Overloaded  bool     `json:"overloaded"`
	Reachable   bool     `json:"reachable"`
	ObservedAt  string   `json:"observed_at"`
}

// Revision records one publication attempt.
type Revision struct {
	ID          id.ID   `json:"id"`
	ConfigHash  string  `json:"config_hash"`
	State       string  `json:"state"`
	CameraCount int     `json:"camera_count"`
	ErrorCode   *string `json:"error_code"`
	CreatedAt   string  `json:"created_at"`
	// Pending reports that the stored settings have changed since this
	// publication. Without it the page shows "published" while the running
	// detector serves earlier settings — and a publication is a snapshot, not
	// a standing subscription, so being pending is the normal state after any
	// edit.
	Pending bool `json:"pending"`
	// ConfigVersions is the per-channel detection version captured at
	// publication; it is what Pending compares against.
	ConfigVersions json.RawMessage `json:"-"`
}

// SupportedLabels is the locked model's label set. Unknown categories are
// refused rather than forwarded to Frigate to fail silently (AI-07).
var SupportedLabels = []string{
	"person", "bicycle", "car", "motorcycle", "bus", "truck",
	"cat", "dog", "bird", "horse", "sheep", "cow", "bear",
}

var (
	ErrUnknownLabel   = fault.New(422, "detection_label_unsupported", "模型不支持所选目标类别")
	ErrConfigConflict = fault.New(409, "detection_version_conflict", "检测配置已被他人修改，请刷新后重试")
	ErrPublishFailed  = fault.New(503, "detection_publish_failed", "检测配置发布失败，已保留原配置")
)

var supported = func() map[string]bool {
	out := map[string]bool{}
	for _, label := range SupportedLabels {
		out[label] = true
	}
	return out
}()

// Service owns the configuration table and its publication.
type Service struct {
	DB      *database.DB
	Auth    *auth.Service
	Frigate string // base URL of the Frigate HTTP API
	// ConfigPath is where the rendered config.yml lands (the Frigate /config
	// bind mount). Empty disables publication, which keeps tests hermetic.
	ConfigPath string
	// Reload publishes the Frigate reload command over MQTT. Injected by the
	// assembly so this package never grows its own broker client.
	Reload func(ctx context.Context) error
	// MQTTKey is the broker credential Frigate must reconnect with; it is the
	// same component secret the bundle generated, injected at assembly.
	MQTTKey string
	// ProbeToken authorizes the detector's internal pull through the media
	// server's on_play hook (the detector holds no user session).
	ProbeToken string
	// Inference names the validated hardware profile the detector runs on
	// ("intel" for the accelerated path, empty for CPU). It comes from the
	// deployment's hardware report; a detector pointed at unaccelerated
	// hardware fails at startup, so this is never guessed.
	Inference string
	// OnPublishFailure and OnPublishSuccess report the outcome to whoever records
	// abnormal events (EVT-04). Injected rather than imported: detection must not
	// depend on the operations package, and a publish failure is only knowable
	// here — a sweep would either miss one that was fixed before it ran or report
	// one that never happened.
	OnPublishFailure func(ctx context.Context, detail string)
	OnPublishSuccess func(ctx context.Context)
}

func New(db *database.DB, accounts *auth.Service, frigateURL, configPath string) *Service {
	return &Service{DB: db, Auth: accounts, Frigate: frigateURL, ConfigPath: configPath}
}

// ServiceConfig carries everything a fully working detection service needs.
// Publication runs on the API and the runtime probe runs on the worker, so a
// constructor that silently omits one of these produces a feature that fails
// at the first click — the defect this type exists to prevent.
type ServiceConfig struct {
	FrigateURL string
	ConfigPath string
	MQTTKey    string
	// ProbeToken authorizes the detector's internal pull through the media
	// server on_play hook; the detector holds no user session.
	ProbeToken string
	// Inference is the hardware profile the detector should run on: "intel"
	// for the accelerated path, empty for CPU.
	Inference string
	// OnPublishFailure and OnPublishSuccess report the outcome to whoever records
	// abnormal events (EVT-04). Injected rather than imported: detection must not
	// depend on the operations package, and a publish failure is only knowable
	// here — a sweep would either miss one that was fixed before it ran or report
	// one that never happened.
	OnPublishFailure func(ctx context.Context, detail string)
	OnPublishSuccess func(ctx context.Context)
}

// Complete reports whether every field the publication path needs is set.
// Callers assert this at startup rather than discovering it per request.
func (c ServiceConfig) Complete() bool {
	return c.ConfigPath != "" && c.MQTTKey != "" && c.ProbeToken != ""
}

// NewComplete builds a service that can both render and reload, or reports why
// it cannot. An incomplete configuration is a wiring error, not a runtime
// condition, so it is returned rather than deferred to the first publish.
func NewComplete(db *database.DB, accounts *auth.Service, in ServiceConfig) (*Service, error) {
	if !in.Complete() {
		return nil, ErrPublishFailed
	}
	service := New(db, accounts, in.FrigateURL, in.ConfigPath)
	service.MQTTKey = in.MQTTKey
	service.ProbeToken = in.ProbeToken
	service.Inference = in.Inference
	service.Reload = HTTPReloader{BaseURL: in.FrigateURL}.Reload
	return service, nil
}

// List returns every channel's configuration with defaults filled in.
func (s *Service) List(ctx context.Context, p auth.Principal) ([]Config, error) {
	rows, err := s.DB.Pool.Query(ctx, `SELECT c.id, COALESCE(d.enabled,false), COALESCE(d.stream,'sub'), COALESCE(d.fps,5), COALESCE(d.labels,'["person"]'), COALESCE(d.version,1)
		FROM channels c LEFT JOIN detection_configs d ON d.channel_id=c.id ORDER BY c.channel_no`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Config, 0)
	for rows.Next() {
		var item Config
		var labels []byte
		if err := rows.Scan(&item.ChannelID, &item.Enabled, &item.Stream, &item.FPS, &labels, &item.Version); err != nil {
			return nil, err
		}
		item.Labels = []string{}
		_ = json.Unmarshal(labels, &item.Labels)
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Visibility follows the configure grant: detection settings are a
	// configuration surface, and a forbidden channel must look absent.
	visible := out[:0]
	for _, item := range out {
		if err := s.Auth.RequireChannel(ctx, p, item.ChannelID, auth.Configure); err == nil {
			visible = append(visible, item)
		}
	}
	return visible, nil
}

// Runtime returns the worker's last capability snapshot. A service without a
// database (as in configuration-only rendering) has no snapshot to report.
func (s *Service) Runtime(ctx context.Context, _ auth.Principal) (RuntimeSnapshot, error) {
	var out RuntimeSnapshot
	if s.DB == nil || s.DB.Pool == nil {
		return RuntimeSnapshot{Reachable: false}, nil
	}
	var observed []byte
	var inference *float64
	err := s.DB.Pool.QueryRow(ctx, `SELECT version,detector,inference_ms,overloaded,reachable,to_char(observed_at,'YYYY-MM-DD"T"HH24:MI:SSOF') FROM detection_runtime WHERE id='current'`).
		Scan(&out.Version, &out.Detector, &inference, &out.Overloaded, &out.Reachable, &observed)
	if errors.Is(err, pgx.ErrNoRows) {
		return RuntimeSnapshot{Reachable: false}, nil
	}
	if err != nil {
		return out, err
	}
	out.InferenceMS = inference
	out.ObservedAt = string(observed)
	return out, nil
}

// Upsert validates and stores one channel's configuration. A change to the
// stream or enablement bumps the detection generation (CH-08): events from the
// old generation must not merge into the new one.
func (s *Service) Upsert(ctx context.Context, p auth.Principal, in Config) (Config, error) {
	if s.Auth == nil {
		return Config{}, auth.ErrNotFound
	}
	if in.Stream != "main" && in.Stream != "sub" {
		return Config{}, auth.ErrInvalid
	}
	if in.FPS < 1 || in.FPS > 30 {
		return Config{}, auth.ErrInvalid
	}
	if len(in.Labels) == 0 || len(in.Labels) > len(SupportedLabels) {
		return Config{}, auth.ErrInvalid
	}
	seen := map[string]bool{}
	for _, label := range in.Labels {
		if !supported[label] || seen[label] {
			return Config{}, ErrUnknownLabel
		}
		seen[label] = true
	}
	var out Config
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := s.Auth.RequireChannelTx(ctx, p, in.ChannelID, auth.Configure, tx); err != nil {
			if errors.Is(err, auth.ErrForbidden) {
				return auth.ErrNotFound
			}
			return err
		}
		var previous *Config
		var prevEnabled bool
		var prevStream string
		var prevVersion int64
		err := tx.QueryRow(ctx, `SELECT enabled,stream,version FROM detection_configs WHERE channel_id=$1`, in.ChannelID).
			Scan(&prevEnabled, &prevStream, &prevVersion)
		if err == nil {
			previous = &Config{Enabled: prevEnabled, Stream: prevStream}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if in.Version > 0 && previous != nil && in.Version != prevVersion {
			return ErrConfigConflict
		}
		labels, err := json.Marshal(in.Labels)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO detection_configs(channel_id,enabled,stream,fps,labels,version,updated_at)
			VALUES($1,$2,$3,$4,$5,1,now())
			ON CONFLICT (channel_id) DO UPDATE SET enabled=$2,stream=$3,fps=$4,labels=$5,version=detection_configs.version+1,updated_at=now()`,
			in.ChannelID, in.Enabled, in.Stream, in.FPS, labels); err != nil {
			return err
		}
		// The generation moves only when the detection source itself changes.
		if previous == nil || previous.Enabled != in.Enabled || previous.Stream != in.Stream {
			if _, err := tx.Exec(ctx, `UPDATE channels SET detection_generation=detection_generation+1 WHERE id=$1`, in.ChannelID); err != nil {
				return err
			}
		}
		var storedLabels []byte
		if err := tx.QueryRow(ctx, `SELECT channel_id,enabled,stream,fps,labels,version FROM detection_configs WHERE channel_id=$1`, in.ChannelID).
			Scan(&out.ChannelID, &out.Enabled, &out.Stream, &out.FPS, &storedLabels, &out.Version); err != nil {
			return err
		}
		out.Labels = []string{}
		return json.Unmarshal(storedLabels, &out.Labels)
	})
	return out, err
}

// PublishRendered writes the rendered config to disk and asks Frigate to
// reload. It returns the config hash; the caller records the revision row.
// The previous file survives any failure here (no partial writes, reload
// errors leave the on-disk config unchanged).
func (s *Service) PublishRendered(ctx context.Context, rendered []byte, actor id.ID, cameraCount int) (string, error) {
	if s.ConfigPath == "" || s.Reload == nil {
		return "", ErrPublishFailed
	}
	hash := sha256.Sum256(rendered)
	configHash := hex.EncodeToString(hash[:])
	// Keep the previous bytes so a failed reload can be rolled back: "failed
	// publish keeps the previous config" is a promise, not a description.
	previous, previousErr := os.ReadFile(s.ConfigPath)
	if err := writeFileAtomic(s.ConfigPath, rendered); err != nil {
		return "", ErrPublishFailed
	}
	if err := s.Reload(ctx); err != nil {
		// The file is in place but Frigate did not acknowledge. Restore the
		// previous file (or remove ours when there was none), record the
		// failure so the operator sees it, and let the next publish retry.
		if previousErr == nil {
			_ = writeFileAtomic(s.ConfigPath, previous)
		} else if os.IsNotExist(previousErr) {
			_ = os.Remove(s.ConfigPath)
		}
		s.recordRevision(ctx, configHash, "failed", cameraCount, actor, "reload_failed")
		if s.OnPublishFailure != nil {
			s.OnPublishFailure(ctx, "检测器未能重新加载配置（reload_failed）")
		}
		return "", ErrPublishFailed
	}
	s.recordRevision(ctx, configHash, "published", cameraCount, actor, "")
	if s.OnPublishSuccess != nil {
		s.OnPublishSuccess(ctx)
	}
	return configHash, nil
}

func (s *Service) recordRevision(ctx context.Context, hash, state string, cameras int, actor id.ID, code string) {
	revisionID, err := id.New()
	if err != nil {
		return
	}
	var actorRef any
	if _, err := id.Parse(string(actor)); err == nil {
		actorRef = actor
	}
	// Capture the per-channel versions this publication covers. The rendered
	// hash alone cannot answer "did anything change since?", because the render
	// depends on which channels currently have a live session.
	versions, err := s.configVersions(ctx)
	if err != nil {
		versions = []byte("{}")
	}
	_, _ = s.DB.Pool.Exec(ctx, `INSERT INTO detection_config_revisions(id,config_hash,state,camera_count,error_code,published_by,config_versions) VALUES($1,$2,$3,$4,NULLIF($5,''),$6,$7)`,
		revisionID, hash, state, cameras, code, actorRef, versions)
}

// configVersions is the current per-channel detection version map, used both
// when recording a publication and when deciding whether settings have moved
// on since the last one.
func (s *Service) configVersions(ctx context.Context) ([]byte, error) {
	rows, err := s.DB.Pool.Query(ctx, `SELECT channel_id, version FROM detection_configs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	versions := map[string]int64{}
	for rows.Next() {
		var channelID id.ID
		var version int64
		if err := rows.Scan(&channelID, &version); err != nil {
			return nil, err
		}
		versions[string(channelID)] = version
	}
	return json.Marshal(versions)
}

// Render builds the full Frigate config.yml from every channel's settings.
// It is a pure function of the rows so its golden test pins the exact bytes.
func Render(base map[string]any, cameras map[string]CameraConfig) ([]byte, error) {
	if base == nil {
		return nil, ErrPublishFailed
	}
	config := map[string]any{}
	for key, value := range base {
		config[key] = value
	}
	cameraConfig := map[string]any{}
	names := make([]string, 0, len(cameras))
	for name := range cameras {
		names = append(names, name)
	}
	// Deterministic order keeps the hash stable across identical inputs.
	sort.Strings(names)
	for _, name := range names {
		camera := cameras[name]
		if !camera.Enabled {
			continue
		}
		inputs := []map[string]any{{
			"path":       camera.InputURL,
			"roles":      []string{"detect"},
			"input_args": "preset-rtsp-generic",
		}}
		cameraConfig[name] = map[string]any{
			"enabled": true,
			"ffmpeg": map[string]any{
				"inputs": inputs,
			},
			// Detection only: recording stays with ZLM (PRD §10.2).
			"record": map[string]any{"enabled": false},
			"snapshots": map[string]any{
				"enabled":      true,
				"bounding_box": true,
				"retain":       map[string]any{"default": 14},
			},
			"detect": map[string]any{
				"enabled": true,
				"width":   camera.Width,
				"height":  camera.Height,
				"fps":     camera.FPS,
			},
			"objects": map[string]any{
				"track": camera.Labels,
			},
			"zones": renderZones(camera.Zones),
		}
	}
	config["cameras"] = cameraConfig
	return json.MarshalIndent(config, "", "  ")
}

// CameraConfig is one rendered camera entry.
type CameraConfig struct {
	Enabled  bool
	InputURL string
	FPS      int
	Width    int
	Height   int
	Labels   []string
	// Zones are the operator's drawings for this channel. The detector performs
	// the zone semantics; they are handed over, never evaluated here.
	Zones []zoneRender
}

// CameraName is the stable Frigate key of a channel: never derived from the
// display name, so a rename cannot orphan events (PRD §10.2).
func CameraName(channelNo int) string {
	return fmt.Sprintf("ch%02d", channelNo)
}

// writeFileAtomic writes the rendered config through a temp file in the same
// directory and renames it into place, so Frigate never observes a partial
// config and a crash mid-write leaves the previous file untouched.
func writeFileAtomic(path string, data []byte) error {
	directory := filepath.Dir(path)
	file, err := os.CreateTemp(directory, ".config-*.tmp")
	if err != nil {
		return err
	}
	temp := file.Name()
	defer os.Remove(temp)
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Chmod(temp, 0o600); err != nil {
		return err
	}
	return os.Rename(temp, path)
}

// renderZones converts the stored drawings into the detector's configuration
// form. An empty set stays an empty object so a channel without zones renders
// exactly as before.
//
// A zone's dwell threshold rides along as the detector's own loitering time.
// That is the only honest way to express "stop there for N seconds": the
// detector counts the object's frames inside the zone, and it is the side that
// can. Without this a dwell rule would silently behave like a plain entry.
func renderZones(zones []zoneRender) map[string]any {
	out := map[string]any{}
	for _, zone := range zones {
		entry := map[string]any{"coordinates": zone.Coordinates}
		if len(zone.Objects) > 0 {
			entry["objects"] = zone.Objects
		}
		if zone.Loitering > 0 {
			entry["loitering_time"] = zone.Loitering
		}
		out[zone.Name] = entry
	}
	return out
}
