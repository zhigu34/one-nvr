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
}
type Query struct {
	ChannelID  id.ID
	Start, End time.Time
	Cursor     id.ID
	Limit      int
}
type Page struct {
	Items      []Segment `json:"items"`
	NextCursor *id.ID    `json:"next_cursor"`
}
