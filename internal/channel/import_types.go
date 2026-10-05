package channel

import (
	"github.com/zhigu34/one-nvr/internal/id"
	"time"
)

type ImportItem struct {
	RequiresInitialRecordingMode bool     `json:"requires_initial_recording_mode"`
	Row                          int      `json:"row"`
	PasswordAction               string   `json:"password_action"`
	ChannelID                    *id.ID   `json:"channel_id"`
	ExpectedVersion              *int64   `json:"expected_version"`
	State                        string   `json:"state"`
	Differences                  []string `json:"differences"`
	Errors                       []string `json:"errors"`
	Warnings                     []string `json:"warnings"`
	JobID                        *id.ID   `json:"job_id"`
}
type ImportPreview struct {
	BatchID   id.ID        `json:"batch_id"`
	ExpiresAt time.Time    `json:"expires_at"`
	Items     []ImportItem `json:"items"`
}
type ImportProgress struct {
	ExpiresAt time.Time    `json:"expires_at"`
	BatchID   id.ID        `json:"batch_id"`
	State     string       `json:"state"`
	Items     []ImportItem `json:"items"`
}
type ImportSelection struct {
	Row                int     `json:"row"`
	ChannelID          id.ID   `json:"channel_id"`
	ExpectedVersion    int64   `json:"expected_version"`
	IdentityIntent     string  `json:"identity_intent"`
	HistorySourceID    id.ID   `json:"history_source_id,omitempty"`
	PasswordAction     string  `json:"password_action"`
	ImportName         bool    `json:"import_name"`
	FirstRecordingMode *string `json:"first_recording_mode,omitempty"`
}
