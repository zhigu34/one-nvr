package events

import (
	"fmt"
	"strings"
	"time"
)

// Exclusion is one time-of-day window in which a region's detections are not
// reported. See migrations/0059 for why this exists as its own rule rather than
// as a mask or a global threshold.
type Exclusion struct {
	ID          string
	Name        string
	Coordinates [][2]float64
	// Categories the exclusion covers. Empty covers every category.
	Categories []string
	// FromClock and ToClock are site-local "HH:MM". Both empty means the
	// exclusion is always active (a plain static mask).
	FromClock string
	ToClock   string
	Reason    string
}

// withinClockWindow reports whether a wall-clock time falls inside a window.
//
// Same convention as the rule arming window, and for the same reason: a night
// window crosses midnight, and 22:00–07:00 is the case that matters here. An
// empty window is always inside.
func withinClockWindow(from, to string, local time.Time) bool {
	if from == "" || to == "" {
		return true
	}
	now := local.Format("15:04")
	if from <= to {
		return now >= from && now < to
	}
	return now >= from || now < to
}

// covers reports whether this exclusion applies to one detection.
//
// The order is deliberate: category and clock are cheap and decisive, so they
// run before the polygon test. A category mismatch means the exclusion was never
// about this kind of object — geometry cannot rescue it.
func (e Exclusion) covers(category string, box []float64, local time.Time) bool {
	if len(e.Categories) > 0 && !containsFold(e.Categories, category) {
		return false
	}
	if !withinClockWindow(e.FromClock, e.ToClock, local) {
		return false
	}
	return boxInPolygon(box, e.Coordinates)
}

func containsFold(list []string, value string) bool {
	for _, item := range list {
		if strings.EqualFold(item, value) {
			return true
		}
	}
	return false
}

// boxInPolygon reports whether a detection's bounding box belongs to a polygon.
//
// The test is on the box's CENTRE, not on any overlap. An exclusion is "this
// thing is in the region I want quiet" — a person walking past the edge of a
// masked area is not in it, and an overlap test would silence them. The centre
// is also the point the detector itself uses for zone membership, so an operator
// who drew a zone and an exclusion sees them agree.
//
// Boxes are [x, y, w, h] in frame fractions, the same form Frigate reports and
// the form stored on events.raw. A box that is absent or malformed cannot be
// located, so it is NOT excluded: the safe direction is to keep reporting.
func boxInPolygon(box []float64, polygon [][2]float64) bool {
	if len(box) != 4 || len(polygon) < 3 {
		return false
	}
	cx := box[0] + box[2]/2
	cy := box[1] + box[3]/2
	return pointInPolygon(cx, cy, polygon)
}

// pointInPolygon is the standard ray-casting test.
//
// A point exactly on an edge is counted as inside by this implementation for the
// horizontal edges it lands on, which is the forgiving direction — an operator
// who drew a boundary through a parked scooter means to quiet it.
func pointInPolygon(x, y float64, polygon [][2]float64) bool {
	inside := false
	for i, j := 0, len(polygon)-1; i < len(polygon); j, i = i, i+1 {
		xi, yi := polygon[i][0], polygon[i][1]
		xj, yj := polygon[j][0], polygon[j][1]
		// Does the horizontal ray at y cross the edge j→i?
		if (yi > y) != (yj > y) {
			at := (xj-xi)*(y-yi)/(yj-yi) + xi
			if x < at {
				inside = !inside
			}
		}
	}
	return inside
}

// validateExclusion rejects drawings and windows the rest of the system cannot
// interpret, so a stored rule is always evaluable.
func validateExclusion(in Exclusion) error {
	if len(in.Coordinates) < 3 {
		return fmt.Errorf("exclusion needs at least three vertices")
	}
	for _, point := range in.Coordinates {
		if point[0] < 0 || point[0] > 1 || point[1] < 0 || point[1] > 1 {
			return fmt.Errorf("exclusion coordinates must be normalized to 0..1")
		}
	}
	if (in.FromClock == "") != (in.ToClock == "") {
		return fmt.Errorf("both ends of an exclusion window are required")
	}
	for _, clock := range []string{in.FromClock, in.ToClock} {
		if clock == "" {
			continue
		}
		var hour, minute int
		if _, err := fmt.Sscanf(clock, "%02d:%02d", &hour, &minute); err != nil ||
			hour > 23 || minute > 59 || len(clock) != 5 {
			return fmt.Errorf("exclusion clock %q is not HH:MM", clock)
		}
	}
	return nil
}
