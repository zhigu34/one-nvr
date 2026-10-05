package channel

import (
	"time"

	"github.com/zhigu34/one-nvr/internal/id"
)

// SourceConfig contains structured connection settings, never userinfo or a
// complete URL. It is only delivered to callers with configure permission.
type SourceConfig struct {
	IP        string `json:"ip"`
	RTSPPort  int    `json:"rtsp_port"`
	MainPath  string `json:"main_path"`
	SubPath   string `json:"sub_path"`
	Transport string `json:"transport"`
	ONVIFPort *int   `json:"onvif_port,omitempty"`
}

type CredentialInput struct {
	Username       *string `json:"username,omitempty"`
	Password       *string `json:"password,omitempty"`
	PasswordAction string  `json:"password_action"`
}

func (CredentialInput) String() string { return "<source credentials redacted>" }

type DraftInput struct {
	Config          SourceConfig    `json:"config"`
	Credentials     CredentialInput `json:"credentials"`
	IdentityIntent  string          `json:"identity_intent"`
	HistorySourceID id.ID           `json:"history_source_id,omitempty"`
}
type SourceRevision struct {
	ID              id.ID        `json:"id"`
	ChannelID       id.ID        `json:"channel_id"`
	SourceID        id.ID        `json:"source_id"`
	Number          int64        `json:"number"`
	Config          SourceConfig `json:"config"`
	UsernameSummary string       `json:"username_summary"`
	PasswordState   string       `json:"password_state"`
	CreatedAt       time.Time    `json:"created_at"`
}
type RevisionPage struct {
	Items      []SourceRevision `json:"items"`
	NextCursor *id.ID           `json:"next_cursor"`
}
type SourceApplyInput struct {
	RevisionID         id.ID   `json:"revision_id"`
	TestID             id.ID   `json:"test_id"`
	ExpectedVersion    int64   `json:"-"`
	FirstRecordingMode *string `json:"first_recording_mode,omitempty"`
}
type Change struct {
	TestID *id.ID `json:"test_id,omitempty"`
	JobID  id.ID  `json:"job_id"`
	State  string `json:"state"`
}
type StreamTest struct {
	State      string  `json:"state"`
	FirstFrame bool    `json:"first_frame"`
	Codec      string  `json:"codec,omitempty"`
	Width      int     `json:"width,omitempty"`
	Height     int     `json:"height,omitempty"`
	FPS        float64 `json:"fps,omitempty"`
	Reason     string  `json:"reason,omitempty"`
}
type SourceTestResult struct {
	ID         id.ID      `json:"id"`
	RevisionID id.ID      `json:"revision_id"`
	State      string     `json:"state"`
	Main       StreamTest `json:"main"`
	Sub        StreamTest `json:"sub"`
	ObservedAt *time.Time `json:"observed_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
}
type ObservedStatus struct {
	State      string     `json:"state"`
	Reason     string     `json:"reason"`
	ObservedAt *time.Time `json:"observed_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
}
type Status struct {
	ChannelID         id.ID          `json:"channel_id"`
	CurrentRevisionID *id.ID         `json:"current_revision_id"`
	DesiredRevisionID *id.ID         `json:"desired_revision_id"`
	StoragePoolID     *id.ID         `json:"storage_pool_id"`
	Enabled           bool           `json:"enabled"`
	Version           int64          `json:"version"`
	Main              ObservedStatus `json:"main"`
	Sub               ObservedStatus `json:"sub"`
	Recording         ObservedStatus `json:"recording"`
}
