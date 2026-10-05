package integration

import (
	"context"
	"github.com/zhigu34/one-nvr/internal/channel"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/media/zlm"
	"github.com/zhigu34/one-nvr/internal/recording"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type poolWritingMedia struct {
	*controlledMedia
	fixture publishFixture
	file    []byte
}

func (m poolWritingMedia) StartRecord(ctx context.Context, key zlm.StreamKey, path string, seconds int) error {
	if err := m.controlledMedia.StartRecord(ctx, key, path, seconds); err != nil {
		return err
	}
	target := filepath.Join(path, "closed.mp4")
	if err := os.WriteFile(target, m.file, 0600); err != nil {
		return err
	}
	return m.fixture.Service.Accept(ctx, recording.Completion{Key: key, MediaServerID: "one-nvr-" + string(m.fixture.Pool.SiteID), FilePath: target, Size: int64(len(m.file)), StartTime: time.Now().UTC(), Duration: 2 * time.Second})
}
func TestPoolCheckUsesReachableCandidate(t *testing.T) {
	for _, kind := range []string{"other_channel", "fresh_draft"} {
		t.Run(kind, func(t *testing.T) {
			f, media, _, _ := testedSource(t)
			ctx := context.Background()
			data, err := os.ReadFile(f.Completion.FilePath)
			if err != nil {
				t.Fatal(err)
			}
			f.Service.Media = poolWritingMedia{media, f, data}
			media.failURL = "rtsp://192.168.33.20:554/main"
			// Existing adopted fixture URL lacks the normalized port; only new probes use it.
			if kind == "other_channel" {
				if _, err := f.DB.Pool.Exec(ctx, "UPDATE source_tests SET observed_at=clock_timestamp()-interval '6 minutes',expires_at=clock_timestamp()-interval '1 minute'"); err != nil {
					t.Fatal(err)
				}
				var ch id.ID
				if err := f.DB.Pool.QueryRow(ctx, "SELECT id FROM channels WHERE channel_no=2").Scan(&ch); err != nil {
					t.Fatal(err)
				}
				draft, err := f.Service.Sources.CreateDraft(ctx, f.Admin, ch, 1, channel.DraftInput{Config: channel.SourceConfig{IP: "192.168.33.22", MainPath: "/main"}, IdentityIntent: "replace", Credentials: channel.CredentialInput{PasswordAction: "clear"}})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.DB.Pool.Exec(ctx, "UPDATE channels SET current_revision_id=$2 WHERE id=$1", ch, draft.ID); err != nil {
					t.Fatal(err)
				}
			}
			bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			if err := f.Service.CheckPool(bounded, f.Pool.ID); err != nil {
				t.Fatal("offline first source blocked healthy pool candidate", err)
			}
			var healthy bool
			if err := f.DB.Pool.QueryRow(ctx, "SELECT state='healthy' FROM storage_pool_checks WHERE pool_id=$1 AND service='zlm'", f.Pool.ID).Scan(&healthy); err != nil || !healthy {
				t.Fatal("no real pool file evidence", healthy, err)
			}
			var open int
			if err := f.DB.Pool.QueryRow(ctx, "SELECT count(*) FROM stream_sessions WHERE purpose='test' AND state NOT IN ('closed','failed')").Scan(&open); err != nil || open != 0 {
				t.Fatal("probe candidates leaked", open, err)
			}
		})
	}
}
