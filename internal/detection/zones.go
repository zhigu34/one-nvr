package detection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/audit"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/fault"
	"github.com/zhigu34/one-nvr/internal/id"
)

// Zone errors. A polygon the detector would reject is refused at save time,
// with a reason the operator can act on.
var (
	ErrZonePolygon = fault.New(422, "zone_polygon_invalid", "区域多边形无效：至少三个顶点、不可自交、坐标需在画面内")
	ErrZoneName    = fault.New(422, "zone_name_invalid", "区域名称无效或重复")
)

// Point is one polygon vertex in normalized frame coordinates (0..1).
type Point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Zone is a named polygon handed to the detector together with the source
// geometry it was drawn against. The detector, not this service, decides
// whether a target is inside it.
type Zone struct {
	ID          id.ID    `json:"id"`
	ChannelID   id.ID    `json:"channel_id"`
	Name        string   `json:"name"`
	Coordinates []Point  `json:"coordinates"`
	Objects     []string `json:"objects"`
	// ReferenceWidth/Height describe the analyzed stream's geometry when the
	// drawing was saved, as read from the source test. Zero means it was unknown
	// then and calibration cannot be judged.
	ReferenceWidth  int   `json:"reference_width"`
	ReferenceHeight int   `json:"reference_height"`
	Version         int64 `json:"version"`
	// NeedsCalibration is true when the analyzed stream's aspect ratio no
	// longer matches the one the drawing was made against: same numbers, a
	// different view behind them.
	NeedsCalibration bool `json:"needs_calibration"`
}

const zoneColumns = `z.id,z.channel_id,z.name,z.coordinates,z.objects,z.reference_width,z.reference_height,z.version`

func scanZone(row pgx.Row, currentWidth, currentHeight int) (Zone, error) {
	var out Zone
	var coordinates, objects []byte
	err := row.Scan(&out.ID, &out.ChannelID, &out.Name, &coordinates, &objects, &out.ReferenceWidth, &out.ReferenceHeight, &out.Version)
	if err != nil {
		return out, err
	}
	out.Coordinates = []Point{}
	if err := json.Unmarshal(coordinates, &out.Coordinates); err != nil {
		return out, err
	}
	out.Objects = []string{}
	_ = json.Unmarshal(objects, &out.Objects)
	out.NeedsCalibration = needsCalibration(out.ReferenceWidth, out.ReferenceHeight, currentWidth, currentHeight)
	return out, nil
}

// frameAspectTolerance absorbs integer rounding in the reported frame size.
// 1920x1080 and 960x540 are the same view; 704x480 and 640x480 are not.
const frameAspectTolerance = 0.02

// needsCalibration reports whether a drawing still describes the same part of
// the view. The detector stretches each source into its own fixed frame, so a
// drawing survives any pure resolution change — but when the source's aspect
// ratio changes (a different stream profile, a rotated sensor, a lens swap) the
// same normalized coordinates land on a different part of the scene, and only
// the operator knows where the zone now belongs.
func needsCalibration(referenceWidth, referenceHeight, currentWidth, currentHeight int) bool {
	if referenceWidth <= 0 || referenceHeight <= 0 || currentWidth <= 0 || currentHeight <= 0 {
		// Unknown on either side is not evidence of drift. Flagging here would
		// mark every drawing on a channel that has never been tested.
		return false
	}
	reference := float64(referenceWidth) / float64(referenceHeight)
	current := float64(currentWidth) / float64(currentHeight)
	ratio := reference / current
	return ratio < 1-frameAspectTolerance || ratio > 1+frameAspectTolerance
}

// validatedPolygon ensures the drawing is a usable polygon. Self-intersection
// is checked here because a filled or crossed shape would make the detector's
// zone semantics meaningless while still being valid JSON.
func validatedPolygon(points []Point) error {
	if len(points) < 3 || len(points) > 64 {
		return ErrZonePolygon
	}
	for _, point := range points {
		if math.IsNaN(point.X) || math.IsNaN(point.Y) || point.X < 0 || point.X > 1 || point.Y < 0 || point.Y > 1 {
			return ErrZonePolygon
		}
	}
	for i := range points {
		a1, a2 := points[i], points[(i+1)%len(points)]
		for j := i + 1; j < len(points); j++ {
			// Adjacent edges share a vertex by construction; only compare
			// non-adjacent ones.
			if j == i || (j+1)%len(points) == i || (i+1)%len(points) == j {
				continue
			}
			b1, b2 := points[j], points[(j+1)%len(points)]
			if segmentsIntersect(a1, a2, b1, b2) {
				return ErrZonePolygon
			}
		}
	}
	return nil
}

func segmentsIntersect(a1, a2, b1, b2 Point) bool {
	d1 := cross(b1, b2, a1)
	d2 := cross(b1, b2, a2)
	d3 := cross(a1, a2, b1)
	d4 := cross(a1, a2, b2)
	if ((d1 > 0 && d2 < 0) || (d1 < 0 && d2 > 0)) && ((d3 > 0 && d4 < 0) || (d3 < 0 && d4 > 0)) {
		return true
	}
	// Collinear overlap counts as an intersection: two edges lying on top of
	// each other also make the shape degenerate.
	return d1 == 0 && onSegment(b1, b2, a1) ||
		d2 == 0 && onSegment(b1, b2, a2) ||
		d3 == 0 && onSegment(a1, a2, b1) ||
		d4 == 0 && onSegment(a1, a2, b2)
}

func cross(a, b, c Point) float64 { return (b.X-a.X)*(c.Y-a.Y) - (b.Y-a.Y)*(c.X-a.X) }

func onSegment(a, b, c Point) bool {
	return math.Min(a.X, b.X) <= c.X && c.X <= math.Max(a.X, b.X) &&
		math.Min(a.Y, b.Y) <= c.Y && c.Y <= math.Max(a.Y, b.Y)
}

func validZoneName(name string) bool {
	trimmed := strings.TrimSpace(name)
	return trimmed != "" && trimmed == name && len(name) <= 32
}

// ListZones returns one channel's zones, marking the ones that need
// recalibration against the frame they would be drawn on today.
func (s *Service) ListZones(ctx context.Context, p auth.Principal, channelID id.ID) ([]Zone, error) {
	if s.Auth == nil {
		return nil, auth.ErrNotFound
	}
	if err := s.Auth.RequireChannel(ctx, p, channelID, auth.Configure); err != nil {
		if errors.Is(err, auth.ErrForbidden) {
			return nil, auth.ErrNotFound
		}
		return nil, err
	}
	width, height := s.currentFrameSize(ctx, channelID)
	rows, err := s.DB.Pool.Query(ctx, `SELECT `+zoneColumns+` FROM channel_zones z WHERE z.channel_id=$1 ORDER BY z.name`, channelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Zone, 0)
	for rows.Next() {
		zone, err := scanZone(rows, width, height)
		if err != nil {
			return nil, err
		}
		out = append(out, zone)
	}
	return out, rows.Err()
}

// currentFrameSize reports the pixel size of the stream the detector analyzes,
// as recorded by the most recent successful source test of that stream. Zero
// means unknown, in which case calibration cannot be judged and nothing is
// flagged.
//
// The stored result holds one entry per stream ("main"/"sub"); the detection
// configuration decides which one the detector pulls, so the comparison uses
// the same one. Comparing a sub-stream drawing against a main-stream
// observation would flag every zone as stale.
func (s *Service) currentFrameSize(ctx context.Context, channelID id.ID) (int, int) {
	var width, height int
	err := s.DB.Pool.QueryRow(ctx, `SELECT COALESCE((t.result->CASE WHEN d.stream='sub' THEN 'sub' ELSE 'main' END->>'width')::int,0),
			COALESCE((t.result->CASE WHEN d.stream='sub' THEN 'sub' ELSE 'main' END->>'height')::int,0)
		FROM source_tests t LEFT JOIN detection_configs d ON d.channel_id=t.channel_id
		WHERE t.channel_id=$1 AND t.purpose='source' AND t.state='succeeded'
		ORDER BY t.created_at DESC,t.id DESC LIMIT 1`, channelID).Scan(&width, &height)
	if err != nil {
		return 0, 0
	}
	return width, height
}

// UpsertZone creates or replaces one zone. Replacing keeps the id so rules
// bound to it stay bound.
//
// The reference geometry is read from the channel's source test rather than
// taken from the request: the drawing is claimed to describe what the detector
// analyzes, and the server is the side that knows what that is. A drawing made
// before any successful test stores zero, which suppresses the calibration
// warning instead of inventing one.
func (s *Service) UpsertZone(ctx context.Context, p auth.Principal, in Zone) (Zone, error) {
	if s.Auth == nil {
		return Zone{}, auth.ErrNotFound
	}
	if err := validatedPolygon(in.Coordinates); err != nil {
		return Zone{}, err
	}
	if !validZoneName(in.Name) {
		return Zone{}, ErrZoneName
	}
	width, height := s.currentFrameSize(ctx, in.ChannelID)
	objects, err := json.Marshal(in.Objects)
	if err != nil {
		return Zone{}, auth.ErrInvalid
	}
	coordinates, err := json.Marshal(in.Coordinates)
	if err != nil {
		return Zone{}, auth.ErrInvalid
	}
	var out Zone
	err = s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := s.Auth.RequireChannelTx(ctx, p, in.ChannelID, auth.Configure, tx); err != nil {
			if errors.Is(err, auth.ErrForbidden) {
				return auth.ErrNotFound
			}
			return err
		}
		zoneID := in.ID
		if zoneID == "" {
			zoneID, err = id.New()
			if err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO channel_zones(id,channel_id,name,coordinates,objects,reference_width,reference_height)
			VALUES($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT (id) DO UPDATE SET name=$3,coordinates=$4,objects=$5,reference_width=$6,reference_height=$7,version=channel_zones.version+1,updated_at=now()`,
			zoneID, in.ChannelID, in.Name, coordinates, objects, width, height); err != nil {
			if strings.Contains(err.Error(), "channel_zones_channel_id_name_key") {
				return ErrZoneName
			}
			return err
		}
		details, _ := json.Marshal(map[string]any{"name": in.Name, "vertices": len(in.Coordinates)})
		if err := audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "zone.saved", ObjectID: zoneID, Details: details}); err != nil {
			return err
		}
		var scanErr error
		out, scanErr = scanZone(tx.QueryRow(ctx, `SELECT `+zoneColumns+` FROM channel_zones z WHERE z.id=$1`, zoneID), width, height)
		return scanErr
	})
	if err != nil {
		return Zone{}, err
	}
	return out, nil
}

// DeleteZone removes a zone and, by cascade, the rules that depended on it.
func (s *Service) DeleteZone(ctx context.Context, p auth.Principal, channelID, zoneID id.ID) error {
	if s.Auth == nil {
		return auth.ErrNotFound
	}
	return s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := s.Auth.RequireChannelTx(ctx, p, channelID, auth.Configure, tx); err != nil {
			if errors.Is(err, auth.ErrForbidden) {
				return auth.ErrNotFound
			}
			return err
		}
		tag, err := tx.Exec(ctx, `DELETE FROM channel_zones WHERE id=$1 AND channel_id=$2`, zoneID, channelID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return auth.ErrNotFound
		}
		return audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "zone.deleted", ObjectID: zoneID})
	})
}

// renderZoneCoordinates is the detector's polygon notation: a flat
// "x1,y1,x2,y2,…" string, matching the Frigate configuration contract.
func renderZoneCoordinates(points []Point) string {
	parts := make([]string, 0, len(points)*2)
	for _, point := range points {
		parts = append(parts, fmt.Sprintf("%.5f", point.X), fmt.Sprintf("%.5f", point.Y))
	}
	return strings.Join(parts, ",")
}

// zoneRender is what the renderer needs per camera: the zone name, its
// detector-facing form, and the dwell threshold the detector should enforce.
type zoneRender struct {
	Name        string
	Coordinates string
	Objects     []string
	// Loitering is the zone's dwell threshold in seconds, zero when no dwell
	// rule uses this zone. The detector holds one threshold per zone and gates
	// both "present now" and "has entered" on it, which is why only one value
	// can be in force.
	Loitering int
}

// loadZoneRenders reads one channel's zones for rendering, ordered by name so
// the rendered configuration is deterministic. A nil transaction reads through
// the pool, which is what the renderer needs: it must not hold a transaction
// open while it renders and writes files.
func (s *Service) loadZoneRenders(ctx context.Context, tx pgx.Tx, channelID id.ID) ([]zoneRender, error) {
	// The dwell threshold comes from the enabled dwell rules of the zone; rule
	// saving refuses two different thresholds on one zone, so this single value
	// is the whole story.
	query := `SELECT z.name,z.coordinates,z.objects,COALESCE((SELECT r.dwell_seconds FROM detection_rules r
			WHERE r.zone_id=z.id AND r.kind='dwell' AND r.enabled AND r.dwell_seconds IS NOT NULL LIMIT 1),0)
		FROM channel_zones z WHERE z.channel_id=$1 ORDER BY z.name`
	var rows pgx.Rows
	var err error
	if tx != nil {
		rows, err = tx.Query(ctx, query, channelID)
	} else {
		rows, err = s.DB.Pool.Query(ctx, query, channelID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []zoneRender{}
	for rows.Next() {
		var name string
		var coordinates, objects []byte
		var loitering int
		if err := rows.Scan(&name, &coordinates, &objects, &loitering); err != nil {
			return nil, err
		}
		var points []Point
		if err := json.Unmarshal(coordinates, &points); err != nil {
			return nil, err
		}
		var categories []string
		if err := json.Unmarshal(objects, &categories); err != nil {
			return nil, err
		}
		sort.Strings(categories)
		out = append(out, zoneRender{Name: name, Coordinates: renderZoneCoordinates(points), Objects: categories, Loitering: loitering})
	}
	return out, rows.Err()
}
