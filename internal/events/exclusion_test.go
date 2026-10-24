package events

import (
	"math"
	"testing"
	"time"
)

// The detector reports boxes in pixels of the detect frame; an operator's
// polygon is normalized to 0..1. The conversion belongs at the ingest boundary
// and must happen exactly once.
//
// This is the defect that made region exclusion a no-op on the live site: the
// MQTT envelope's box was never read at all (wrong JSON path). This test pins
// the other half — once it is read, a pixel value written verbatim would sit
// outside every polygon, which fails the same way and just as silently.
func TestDetectorBoxIsConvertedToFrameFractions(t *testing.T) {
	got := boxAsFractions([]float64{64, 36, 128, 72})
	want := []float64{0.1, 0.1, 0.2, 0.2}
	if len(got) != 4 {
		t.Fatalf("box = %v", got)
	}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 1e-9 {
			t.Fatalf("box[%d] = %v, want %v (full: %v)", i, got[i], want[i], got)
		}
	}
	// A box that is not four numbers cannot be located, and the caller must not
	// receive a half-converted value that looks usable.
	if boxAsFractions([]float64{1, 2, 3}) != nil {
		t.Fatal("a three-element box was accepted")
	}
	if boxAsFractions(nil) != nil {
		t.Fatal("an absent box was accepted")
	}
}

// A window that crosses midnight is the case this feature exists for: the
// scooter misreads happen at night, and "22:00–07:00" has to mean one continuous
// stretch, not two.
func TestWithinClockWindowCrossesMidnight(t *testing.T) {
	at := func(hhmm string) time.Time {
		value, err := time.Parse("15:04", hhmm)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	cases := []struct {
		from, to, at string
		want         bool
	}{
		// 22:00–07:00: night runs through midnight.
		{"22:00", "07:00", "23:30", true},
		{"22:00", "07:00", "01:00", true},
		{"22:00", "07:00", "06:59", true},
		{"22:00", "07:00", "07:00", false},
		{"22:00", "07:00", "21:59", false},
		{"22:00", "07:00", "12:00", false},
		// A same-day window stays confined to that day.
		{"09:00", "17:00", "12:00", true},
		{"09:00", "17:00", "18:00", false},
		{"09:00", "17:00", "08:59", false},
		// No window means always — a plain static mask.
		{"", "", "03:00", true},
	}
	for _, c := range cases {
		if got := withinClockWindow(c.from, c.to, at(c.at)); got != c.want {
			t.Errorf("window %s–%s at %s = %v, want %v", c.from, c.to, c.at, got, c.want)
		}
	}
}

// The membership test is on the box CENTRE: a person walking past the edge of a
// quiet region is not in it, and an overlap test would silence them.
func TestBoxInPolygonUsesTheBoxCentre(t *testing.T) {
	// A unit square covering the left half of the frame.
	polygon := [][2]float64{{0, 0}, {0.5, 0}, {0.5, 1}, {0, 1}}

	// Entirely inside.
	if !boxInPolygon([]float64{0.1, 0.1, 0.2, 0.2}, polygon) {
		t.Fatal("a box inside the polygon must be inside")
	}
	// Centre inside, box straddling the right edge.
	if !boxInPolygon([]float64{0.35, 0.4, 0.2, 0.2}, polygon) {
		t.Fatal("a box whose centre is inside belongs to the region")
	}
	// Centre outside, box straddling the right edge the other way: this is the
	// case an overlap test would wrongly exclude.
	if boxInPolygon([]float64{0.6, 0.4, 0.2, 0.2}, polygon) {
		t.Fatal("a box whose centre is outside must not be excluded")
	}
	// Entirely outside.
	if boxInPolygon([]float64{0.7, 0.1, 0.1, 0.1}, polygon) {
		t.Fatal("a box outside the polygon must not be excluded")
	}
	// A missing or malformed box cannot be located, so it keeps reporting.
	if boxInPolygon(nil, polygon) || boxInPolygon([]float64{0.1, 0.1}, polygon) {
		t.Fatal("an unlocatable box must not be excluded")
	}
}

func TestExclusionCoversRequiresCategoryClockAndGeometry(t *testing.T) {
	zone := [][2]float64{{0, 0}, {0.5, 0}, {0.5, 1}, {0, 1}}
	inside := []float64{0.1, 0.1, 0.2, 0.2}
	outside := []float64{0.7, 0.1, 0.1, 0.1}
	night := time.Date(2026, 10, 22, 23, 30, 0, 0, time.UTC)
	noon := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

	rule := Exclusion{
		Coordinates: zone,
		Categories:  []string{"person"},
		FromClock:   "22:00",
		ToClock:     "07:00",
		Reason:      "夜间排除",
	}
	if !rule.covers("person", inside, night) {
		t.Fatal("night + inside + person must be excluded")
	}
	// Every one of the three conditions is required.
	if rule.covers("person", inside, noon) {
		t.Fatal("the same box in daylight is not excluded")
	}
	if rule.covers("person", outside, night) {
		t.Fatal("a box outside the region is not excluded")
	}
	if rule.covers("car", inside, night) {
		t.Fatal("a category the rule does not name is not excluded")
	}
	// An empty category list covers everything, which is the common case.
	all := rule
	all.Categories = nil
	if !all.covers("car", inside, night) {
		t.Fatal("an exclusion with no categories covers every category")
	}
}

func TestValidateExclusionRejectsUnexpressibleRules(t *testing.T) {
	valid := Exclusion{Coordinates: [][2]float64{{0, 0}, {0.5, 0}, {0.5, 0.5}}}
	if err := validateExclusion(valid); err != nil {
		t.Fatalf("a plain triangle is valid: %v", err)
	}
	// Two vertices is not a polygon.
	tooFew := valid
	tooFew.Coordinates = [][2]float64{{0, 0}, {1, 1}}
	if err := validateExclusion(tooFew); err == nil {
		t.Fatal("fewer than three vertices must be refused")
	}
	// Coordinates outside the normalized frame cannot be matched against boxes.
	outOfRange := valid
	outOfRange.Coordinates = [][2]float64{{0, 0}, {1.5, 0}, {0.5, 0.5}}
	if err := validateExclusion(outOfRange); err == nil {
		t.Fatal("coordinates outside 0..1 must be refused")
	}
	// Half a window is not a window.
	halfWindow := valid
	halfWindow.FromClock = "22:00"
	if err := validateExclusion(halfWindow); err == nil {
		t.Fatal("a window with one end must be refused")
	}
	badClock := valid
	badClock.FromClock = "25:00"
	badClock.ToClock = "07:00"
	if err := validateExclusion(badClock); err == nil {
		t.Fatal("an impossible clock must be refused")
	}
}
