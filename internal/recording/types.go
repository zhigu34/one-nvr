// Package recording owns immutable recording provenance, publication and index.
package recording

import (
	"time"

	"github.com/zhigu34/one-nvr/internal/id"
)

type Run struct {
	ID, SiteID, ChannelID, SourceRevisionID, SessionID, PoolID id.ID
	WorkRelativePath, Purpose, State                           string
	CreatedAt                                                  time.Time
}
type FrozenPath struct {
	RelativePath     string
	Timezone         string
	UTCOffsetSeconds int
	OriginalStart    time.Time
}

// Segment excludes both raw native paths and upstream management identifiers.
type Segment struct {
	ID               id.ID     `json:"id"`
	ChannelID        id.ID     `json:"channel_id"`
	SourceRevisionID id.ID     `json:"source_revision_id"`
	PoolID           id.ID     `json:"pool_id"`
	RunID            id.ID     `json:"run_id"`
	Start            time.Time `json:"start"`
	End              time.Time `json:"end"`
	Bytes            int64     `json:"bytes"`
	State            string    `json:"state"`
	NamingTimezone   string    `json:"naming_timezone"`
	UTCOffsetSeconds int       `json:"utc_offset_seconds"`
	CheckState       string    `json:"check_state"`
	// Protected marks evidence the operator asked to keep: retention and manual
	// deletion both skip it until it is released.
	Protected bool `json:"protected"`
	// Media restates the verification evidence captured at publication, so the
	// management view reports codec and resolution without a second probe. Zero
	// values mean the evidence predates that capture, not that it is absent.
	Media MediaParams `json:"media"`
	// Archived reports a verified cloud copy in the archive index (M2.1): the
	// management view shows 本地+云端 from it without a second query.
	Archived bool `json:"archived"`
}
type MediaParams struct {
	Codec      string  `json:"codec"`
	Width      int     `json:"width"`
	Height     int     `json:"height"`
	FPS        float64 `json:"fps"`
	DurationMS int64   `json:"duration_ms"`
}
type Query struct {
	ChannelID  id.ID
	Start, End time.Time
	Cursor     id.ID
	Limit      int
	// Optional narrowing for the management view. Each zero value means "no
	// restriction", so the playback caller is unaffected.
	SourceRevisionID id.ID
	States           []string
	PoolID           id.ID
	Protected        *bool
}
type Page struct {
	Items      []Segment `json:"items"`
	NextCursor *id.ID    `json:"next_cursor"`
}

// Stats summarises the same filter window without paging, so the management view
// can show what a selection covers before anything is deleted. Logical size is
// what the index describes and DeletedBytes is what deliberate deletion already
// reclaimed; neither counts a segment twice.
type Stats struct {
	Segments     int        `json:"segments"`
	Ready        int        `json:"ready"`
	Anomalous    int        `json:"anomalous"`
	Protected    int        `json:"protected"`
	Deleted      int        `json:"deleted"`
	// CloudOnly counts segments whose local copy is gone (deleted) but a
	// verified cloud copy exists: still playable, different location (FILE-01).
	CloudOnly    int        `json:"cloud_only"`
	DurationMS   int64      `json:"duration_ms"`
	LogicalBytes int64      `json:"logical_bytes"`
	DeletedBytes int64      `json:"deleted_bytes"`
	OldestStart  *time.Time `json:"oldest_start"`
	NewestEnd    *time.Time `json:"newest_end"`
}
