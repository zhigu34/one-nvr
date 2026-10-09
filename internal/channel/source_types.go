package channel

import (
	"time"

	"github.com/zhigu34/one-nvr/internal/auth"
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

// PolicyItem is one row of a batch recording-mode request. The version travels
// with the row so a batch never adopts a channel that changed since the page
// was read.
type PolicyItem struct {
	ChannelID       id.ID  `json:"channel_id"`
	ExpectedVersion int64  `json:"expected_version"`
	Mode            string `json:"mode"`
}

// PolicyOutcome reports one row's result. A rejected row carries only a public
// error code; it never reports another channel's state or identifiers.
type PolicyOutcome struct {
	ChannelID id.ID  `json:"channel_id"`
	State     string `json:"state"`
	JobID     *id.ID `json:"job_id,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`
}

type PolicyBatch struct {
	Items []PolicyOutcome `json:"items"`
}
type StreamTest struct {
	State      string  `json:"state"`
	FirstFrame bool    `json:"first_frame"`
	Codec      string  `json:"codec,omitempty"`
	Width      int     `json:"width,omitempty"`
	Height     int     `json:"height,omitempty"`
	FPS        float64 `json:"fps,omitempty"`
	// AudioCodec mirrors the media probe: nil for results written before audio
	// capture existed, "" when the stream carries no audio, otherwise the
	// ffmpeg codec name (pcm_alaw, aac, ...).
	AudioCodec *string `json:"audio_codec,omitempty"`
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
	RequiresInitialRecordingMode bool           `json:"requires_initial_recording_mode"`
	ChannelID                    id.ID          `json:"channel_id"`
	CurrentRevisionID            *id.ID         `json:"current_revision_id"`
	DesiredRevisionID            *id.ID         `json:"desired_revision_id"`
	StoragePoolID                *id.ID         `json:"storage_pool_id"`
	Enabled                      bool           `json:"enabled"`
	Version                      int64          `json:"version"`
	Main                         ObservedStatus `json:"main"`
	Sub                          ObservedStatus `json:"sub"`
	Recording                    ObservedStatus `json:"recording"`
}

// MediaParams is what the most recent successful test of the applied revision
// observed for one stream. It describes the source itself rather than its
// current connection state, so it survives an unreachable camera; nil means
// the applied revision has no successful test on record.
type MediaParams struct {
	Codec      string     `json:"codec,omitempty"`
	Width      int        `json:"width,omitempty"`
	Height     int        `json:"height,omitempty"`
	FPS        float64    `json:"fps,omitempty"`
	AudioCodec *string    `json:"audio_codec,omitempty"`
	ObservedAt *time.Time `json:"observed_at,omitempty"`
}

// SummaryItem is one row of the channel list: the business attributes plus the
// per-kind status the list view shows. Detection status is absent because the
// detection milestone is not delivered; it is not reported as healthy or as a
// placeholder. Bitrate is the latest valid sample, never an assumption.
type SummaryItem struct {
	ChannelID    id.ID         `json:"channel_id"`
	ChannelNo    int           `json:"channel_no"`
	ChannelName  string        `json:"channel_name"`
	ChannelGroup string        `json:"channel_group"`
	Enabled      bool          `json:"enabled"`
	Version      int64         `json:"version"`
	Permissions  []auth.Action `json:"permissions"`
	// Named the same as in Status so the list and the detail page describe a
	// channel identically.
	CurrentRevisionID *id.ID         `json:"current_revision_id"`
	SourceIP          string         `json:"source_ip"`
	MainPath          string         `json:"main_path"`
	SubPath           string         `json:"sub_path"`
	// OnvifPort is the ONVIF management port of the applied revision. Nil
	// means the revision was saved as RTSP-only.
	OnvifPort *int           `json:"onvif_port,omitempty"`
	Main      ObservedStatus `json:"main"`
	Sub       ObservedStatus `json:"sub"`
	Recording ObservedStatus `json:"recording"`
	LastError string         `json:"last_error"`
	BitrateKbps *int64       `json:"bitrate_kbps"`
	// Media parameters of the applied revision's newest successful test. The
	// list shows them as reference information; they are never mixed with the
	// observed connection state above.
	MainMedia *MediaParams `json:"main_media,omitempty"`
	SubMedia  *MediaParams `json:"sub_media,omitempty"`
	UpdatedAt *time.Time   `json:"updated_at"`
}

type SummaryPage struct {
	Items []SummaryItem `json:"items"`
}
