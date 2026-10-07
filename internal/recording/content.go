package recording

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/audit"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/fault"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/storage"
)

// Media read-out failures are transport-visible fault values. They are returned
// as-is by the HTTP layer so that neither a status code nor an error body can
// distinguish "outside your authorized scope" from "not published yet": only
// auth.ErrNotFound is ever produced for the former.
var (
	ErrContentNotReady    = fault.New(409, "recording_not_ready", "录像尚未完成发布，暂时无法播放")
	ErrContentMissing     = fault.New(409, "recording_missing", "录像文件不存在或已被移走")
	ErrContentDamaged     = fault.New(409, "recording_damaged", "录像文件与发布记录不一致")
	ErrContentPool        = fault.New(503, "pool_unavailable", "录像所在存储池暂时不可用")
	ErrRangeUnsatisfiable = fault.New(416, "range_not_satisfiable", "请求的字节范围超出录像长度")
)

// Content is a verified, read-only handle to one published recording segment.
// It deliberately carries no pool root, no relative path and no upstream
// identifier, so a caller cannot route around the authorized read path.
type Content struct {
	SegmentID  id.ID
	ChannelID  id.ID
	Start, End time.Time
	Size       int64
	Modified   time.Time
	File       *os.File
}

func (c *Content) Close() error { return c.File.Close() }

// ETag is a strong validator over the published bytes: a different segment, or
// the same segment republished with different content, never reuses one.
func (c *Content) ETag() string {
	sum := sha256.Sum256([]byte(string(c.SegmentID) + "\x00" + strconv.FormatInt(c.Size, 10) + "\x00" + strconv.FormatInt(c.Modified.UnixNano(), 10)))
	return `"` + hex.EncodeToString(sum[:16]) + `"`
}

// BytePlan is exactly the byte window one response may carry.
type BytePlan struct {
	Start, End int64
	Partial    bool
}

func (p BytePlan) Length() int64 { return p.End - p.Start + 1 }
func (p BytePlan) ContentRange(size int64) string {
	return fmt.Sprintf("bytes %d-%d/%d", p.Start, p.End, size)
}

// OpenSegment resolves one segment into a readable handle after proving the
// caller holds playback on its channel. Authorization and resolution share a
// single transaction, so a revoked grant cannot be raced by a concurrent read.
func (s *Service) OpenSegment(ctx context.Context, p auth.Principal, recordingID id.ID) (*Content, error) {
	if s.Auth == nil || s.Pools == nil {
		return nil, auth.ErrNotFound
	}
	var found publication
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		var channelID id.ID
		if err := tx.QueryRow(ctx, "SELECT channel_id FROM recording_segments WHERE id=$1", recordingID).Scan(&channelID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return auth.ErrNotFound
			}
			return err
		}
		if err := s.Auth.RequireChannelTx(ctx, p, channelID, auth.Playback, tx); err != nil {
			if errors.Is(err, auth.ErrForbidden) {
				return auth.ErrNotFound
			}
			return err
		}
		item, err := scanPublication(tx.QueryRow(ctx, "SELECT "+publicationColumns+" FROM recording_segments s WHERE s.id=$1", recordingID))
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return auth.ErrNotFound
			}
			return err
		}
		found = item
		return nil
	})
	if err != nil {
		return nil, err
	}
	if found.State != "ready" {
		return nil, ErrContentNotReady
	}
	root, _, err := s.Pools.OpenMediaRoot(ctx, found.PoolID)
	if err != nil {
		return nil, ErrContentPool
	}
	file, err := storage.OpenMediaFile(root, found.Target)
	if err != nil {
		_, statErr := root.Lstat(found.Target)
		root.Close()
		if os.IsNotExist(statErr) {
			return nil, ErrContentMissing
		}
		return nil, ErrContentDamaged
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		root.Close()
		return nil, ErrContentDamaged
	}
	// The handle is independent of the root directory descriptor, so the pool
	// can be released before any byte is written to a client.
	root.Close()
	if !found.Identity.matches(info) {
		file.Close()
		return nil, ErrContentDamaged
	}
	return &Content{SegmentID: found.ID, ChannelID: found.ChannelID, Start: found.Start, End: found.End, Size: info.Size(), Modified: info.ModTime(), File: file}, nil
}

// AuditPlayback records one authorized media read. Playback must not proceed
// when the record cannot be persisted, matching the credential-reveal rule.
func (s *Service) AuditPlayback(ctx context.Context, p auth.Principal, c *Content, plan BytePlan) error {
	if s.DB == nil || s.DB.Pool == nil {
		return ErrContentPool
	}
	details, err := json.Marshal(struct {
		ChannelID id.ID `json:"channel_id"`
		From      int64 `json:"byte_from"`
		To        int64 `json:"byte_to"`
		Segment   int64 `json:"segment_bytes"`
		Partial   bool  `json:"partial"`
	}{c.ChannelID, plan.Start, plan.End, c.Size, plan.Partial})
	if err != nil {
		return err
	}
	return s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		return audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "recording.streamed", ObjectID: c.SegmentID, Details: details})
	})
}

// PlanRange resolves the single byte window a response may carry. Only one
// range is served: a well-formed request for several ranges is refused with
// ErrRangeUnsatisfiable instead of being silently collapsed, because a client
// asking for two ranges would otherwise receive wrong media. Malformed or
// non-`bytes` headers are ignored and the full body is returned, as HTTP
// requires. An If-Range that no longer matches turns the request back into a
// full read.
func (c *Content) PlanRange(header, ifRange string) (BytePlan, error) {
	if c.Size <= 0 {
		return BytePlan{}, ErrContentDamaged
	}
	full := BytePlan{End: c.Size - 1}
	if !c.rangeAllowed(ifRange) {
		return full, nil
	}
	spec, present, ok := parseRangeHeader(header)
	if !present {
		return full, nil
	}
	if !ok {
		return BytePlan{}, ErrRangeUnsatisfiable
	}
	if spec.suffix > 0 {
		start := c.Size - spec.suffix
		if start < 0 {
			start = 0
		}
		return BytePlan{Start: start, End: c.Size - 1, Partial: true}, nil
	}
	if spec.start >= c.Size {
		return BytePlan{}, ErrRangeUnsatisfiable
	}
	end := c.Size - 1
	if spec.end > 0 && spec.end-1 < end {
		end = spec.end - 1
	}
	return BytePlan{Start: spec.start, End: end, Partial: true}, nil
}

func (c *Content) rangeAllowed(ifRange string) bool {
	if ifRange == "" {
		return true
	}
	if ifRange == c.ETag() {
		return true
	}
	if at, err := http.ParseTime(ifRange); err == nil {
		return !c.Modified.Truncate(time.Second).After(at)
	}
	return false
}

type rangeSpec struct {
	// end is exclusive when resolved from an explicit "a-b" first-last pair,
	// which is how later bounds are compared without an off-by-one.
	start, end int64
	suffix     int64
}

// parseRangeHeader reports whether a byte range was requested at all, and
// whether this API is willing to serve it. present=false means "serve the whole
// body"; ok=false only ever accompanies present=true and yields 416.
func parseRangeHeader(header string) (spec rangeSpec, present, ok bool) {
	header = strings.TrimSpace(header)
	if header == "" {
		return rangeSpec{}, false, false
	}
	unit, rest, found := strings.Cut(header, "=")
	if !found || !strings.EqualFold(strings.TrimSpace(unit), "bytes") {
		return rangeSpec{}, false, false
	}
	fields := strings.Split(rest, ",")
	if len(fields) != 1 {
		return rangeSpec{}, true, false
	}
	first, last, found := strings.Cut(strings.TrimSpace(fields[0]), "-")
	if !found {
		// A well-formed byte-range-spec always contains "-"; anything else is
		// malformed and HTTP requires it to be ignored, not refused.
		return rangeSpec{}, false, false
	}
	first, last = strings.TrimSpace(first), strings.TrimSpace(last)
	if first == "" {
		length, err := strconv.ParseInt(last, 10, 64)
		if err != nil || length < 0 {
			return rangeSpec{}, false, false
		}
		if length == 0 {
			return rangeSpec{}, true, false
		}
		return rangeSpec{suffix: length}, true, true
	}
	start, err := strconv.ParseInt(first, 10, 64)
	if err != nil || start < 0 {
		return rangeSpec{}, false, false
	}
	if last == "" {
		return rangeSpec{start: start}, true, true
	}
	end, err := strconv.ParseInt(last, 10, 64)
	if err != nil || end < start {
		return rangeSpec{}, false, false
	}
	return rangeSpec{start: start, end: end + 1}, true, true
}
