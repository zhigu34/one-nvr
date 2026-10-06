package recording

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/channel"
	"github.com/zhigu34/one-nvr/internal/database"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/media/probe"
	"github.com/zhigu34/one-nvr/internal/media/zlm"
	"github.com/zhigu34/one-nvr/internal/secrets"
	"github.com/zhigu34/one-nvr/internal/storage"
)

var ErrCompletionInvalid = errors.New("invalid_recording_completion")
var ErrCompletionUnavailable = errors.New("recording_completion_unavailable")

type Media interface {
	AddProxy(context.Context, zlm.ProxyInput) (zlm.ProxyRef, error)
	RemoveProxy(context.Context, zlm.ProxyRef) error
	Inspect(context.Context, zlm.StreamKey) (zlm.StreamSnapshot, error)
	StartRecord(context.Context, zlm.StreamKey, string, int) error
	StopRecord(context.Context, zlm.StreamKey) error
}
type Probe interface {
	FirstFrame(context.Context, string) (probe.VideoEvidence, error)
	InspectMP4(context.Context, *os.File) (probe.FileEvidence, error)
}
type Service struct {
	DB             *database.DB
	Auth           *auth.Service
	Files          FilePublisher
	Media          Media
	Probe          Probe
	Roots          []string
	DataDir        string
	Pools          *storage.Service
	Sources        *channel.SourceService
	FreshNetwork   func(context.Context) (channel.NetworkPolicy, error)
	ProbeToken     string
	siteID         id.ID
	discoveryMu    sync.Mutex
	discoveryAfter id.ID
	discoveryTime  time.Time
	discoveryFiles map[id.ID]string
}

func New(db *database.DB, media Media, checker Probe, roots []string, dataDir string) *Service {
	secret, err := secrets.Load(dataDir)
	var siteID id.ID
	if err == nil {
		siteID = secret.SiteID
	}
	pools := storage.New(db, nil, roots)
	pools.MediaInspector = checker
	return &Service{Pools: pools, DB: db, Media: media, Probe: checker, Roots: append([]string(nil), roots...), DataDir: dataDir, siteID: siteID}
}

type Completion struct {
	Key           zlm.StreamKey
	MediaServerID string
	FilePath      string
	Size          int64
	StartTime     time.Time
	Duration      time.Duration
	TimeEvidence  string `json:",omitempty"`
}

func (Completion) String() string { return "<private recording completion>" }
func (c Completion) validate() error {
	if c.TimeEvidence != "" && c.TimeEvidence != "completion" && c.TimeEvidence != "recovered" && c.TimeEvidence != "provisional" {
		return ErrCompletionInvalid
	}
	if c.Key.Validate() != nil || c.MediaServerID == "" || len(c.MediaServerID) > 128 || c.Size <= 0 || c.StartTime.IsZero() || c.Duration <= 0 || c.Duration > 24*time.Hour || len(c.FilePath) > 4096 || !filepath.IsAbs(c.FilePath) || filepath.Clean(c.FilePath) != c.FilePath || filepath.Ext(c.FilePath) != ".mp4" || strings.ContainsAny(c.FilePath, "\x00\r\n") {
		return ErrCompletionInvalid
	}
	return nil
}
func (c Completion) sourceKey() string {
	raw, _ := json.Marshal([]string{c.MediaServerID, c.Key.VHost, c.Key.App, c.Key.Stream, c.FilePath})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func (s *Service) validate(c Completion) error {
	if c.validate() != nil || s.siteID == "" || c.MediaServerID != "one-nvr-"+string(s.siteID) {
		return ErrCompletionInvalid
	}
	return nil
}
func (s *Service) Accept(ctx context.Context, c Completion) error {
	if err := s.validate(c); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := s.acceptDB(ctx, c); err == nil {
		return nil
	} else if errors.Is(err, ErrCompletionInvalid) {
		return err
	}
	slog.Warn("recording completion database deferred", "context_cancelled", ctx.Err() != nil)
	if err := (&Spool{Dir: filepath.Join(s.DataDir, "recording-spool")}).Put(ctx, c); err != nil {
		return ErrCompletionUnavailable
	}
	slog.Info("recording completion spooled")
	return nil
}
func (s *Service) acceptDB(ctx context.Context, c Completion) error {
	if s.DB == nil || s.DB.Pool == nil {
		return ErrCompletionUnavailable
	}
	bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return s.DB.WithinTx(bounded, func(tx pgx.Tx) error {
		var matches bool
		if err := tx.QueryRow(bounded, "SELECT EXISTS(SELECT 1 FROM sites WHERE id=$1)", s.siteID).Scan(&matches); err != nil {
			return err
		}
		if !matches {
			return ErrCompletionInvalid
		}
		rows, err := tx.Query(bounded, `SELECT r.id,r.work_relative_path,p.canonical_path FROM recording_runs r JOIN stream_sessions ss ON ss.id=r.stream_session_id JOIN storage_pools p ON p.id=r.pool_id WHERE r.site_id=$1 AND ss.vhost=$2 AND ss.app=$3 AND ss.stream=$4`, s.siteID, c.Key.VHost, c.Key.App, c.Key.Stream)
		if err != nil {
			return err
		}
		var runID *id.ID
		for rows.Next() {
			var candidate id.ID
			var relative, poolPath string
			if err := rows.Scan(&candidate, &relative, &poolPath); err != nil {
				rows.Close()
				return err
			}
			work := filepath.Join(poolPath, relative)
			if strings.HasPrefix(c.FilePath, work+string(filepath.Separator)) {
				if runID != nil {
					rows.Close()
					return ErrCompletionInvalid
				}
				runID = &candidate
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		state := "pending"
		var reason *string
		if runID == nil {
			state = "diagnostic"
			r := "unregistered_recording_run"
			reason = &r
		}
		raw, err := json.Marshal(c)
		if err != nil {
			return err
		}
		inboxID, err := id.New()
		if err != nil {
			return err
		}
		_, err = tx.Exec(bounded, `INSERT INTO hook_inbox(id,source_key,run_id,payload,state,error_code) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(source,source_key) DO NOTHING`, inboxID, c.sourceKey(), runID, raw, state, reason)
		return err
	})
}
func (s *Service) DrainSpool(ctx context.Context) error {
	return (&Spool{Dir: filepath.Join(s.DataDir, "recording-spool")}).Drain(ctx, func(ctx context.Context, c Completion) error {
		if err := s.validate(c); err != nil {
			return err
		}
		return s.acceptDB(ctx, c)
	})
}
