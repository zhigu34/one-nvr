package detection

import "testing"

func square() []Point {
	return []Point{{X: 0.1, Y: 0.1}, {X: 0.4, Y: 0.1}, {X: 0.4, Y: 0.4}, {X: 0.1, Y: 0.4}}
}

func TestPolygonValidation(t *testing.T) {
	if err := validatedPolygon(square()); err != nil {
		t.Fatalf("a simple quad must be accepted: %v", err)
	}
	// A triangle is the smallest usable polygon.
	if err := validatedPolygon([]Point{{X: 0, Y: 0}, {X: 1, Y: 0}, {X: 0, Y: 1}}); err != nil {
		t.Fatalf("a triangle must be accepted: %v", err)
	}
	if err := validatedPolygon([]Point{{X: 0, Y: 0}, {X: 1, Y: 1}}); err != ErrZonePolygon {
		t.Fatal("two points are not a polygon")
	}
	// A bowtie crosses itself: the detector's zone semantics would be
	// meaningless, so it must be refused rather than stored.
	bowtie := []Point{{X: 0.1, Y: 0.1}, {X: 0.4, Y: 0.4}, {X: 0.4, Y: 0.1}, {X: 0.1, Y: 0.4}}
	if err := validatedPolygon(bowtie); err != ErrZonePolygon {
		t.Fatal("a self-intersecting polygon must be refused")
	}
	// Out-of-frame coordinates would address pixels that do not exist.
	outside := []Point{{X: 0.1, Y: 0.1}, {X: 1.4, Y: 0.1}, {X: 0.4, Y: 0.4}}
	if err := validatedPolygon(outside); err != ErrZonePolygon {
		t.Fatal("coordinates outside the frame must be refused")
	}
}

func TestZoneNameValidation(t *testing.T) {
	for _, name := range []string{"门口", "gate", "gate-1", "前门 A"} {
		if !validZoneName(name) {
			t.Fatalf("%q must be a valid zone name", name)
		}
	}
	for _, name := range []string{"", " padded", "padded ", "0123456789012345678901234567890123"} {
		if validZoneName(name) {
			t.Fatalf("%q must be refused", name)
		}
	}
}

func TestRenderZoneCoordinates(t *testing.T) {
	got := renderZoneCoordinates([]Point{{X: 0.1, Y: 0.2}, {X: 0.3, Y: 0.4}, {X: 0.5, Y: 0.6}})
	want := "0.10000,0.20000,0.30000,0.40000,0.50000,0.60000"
	if got != want {
		t.Fatalf("coordinate form must match the detector's notation: got %q want %q", got, want)
	}
}

func TestRenderZonesOmitsEmptyObjects(t *testing.T) {
	zones := renderZones([]zoneRender{{Name: "门口", Coordinates: "0.1,0.1,0.2,0.2,0.3,0.1"}, {Name: "院子", Coordinates: "0.5,0.5,0.6,0.6,0.7,0.5", Objects: []string{"person"}}})
	if _, ok := zones["门口"].(map[string]any)["objects"]; ok {
		t.Fatal("a zone without category restrictions must omit the key, not send an empty list")
	}
	if list := zones["院子"].(map[string]any)["objects"].([]string); len(list) != 1 {
		t.Fatal("declared categories must ride along")
	}
	// A zone with no dwell rule must not claim a loitering threshold: the
	// detector would otherwise delay entry for a rule nobody wrote.
	if _, ok := zones["门口"].(map[string]any)["loitering_time"]; ok {
		t.Fatal("a zone without a dwell rule must not send a loitering time")
	}
}

// A dwell threshold is only meaningful if it reaches the detector: Frigate
// enforces dwelling as the zone's own loitering time, and that is what makes a
// "dwell" rule behave differently from a plain entry.
func TestRenderZonesCarriesLoiteringTime(t *testing.T) {
	zones := renderZones([]zoneRender{{Name: "门口", Coordinates: "0.1,0.1,0.2,0.2,0.3,0.1", Loitering: 30}})
	entry := zones["门口"].(map[string]any)
	if entry["loitering_time"] != 30 {
		t.Fatalf("the dwell threshold must reach the detector, got %+v", entry)
	}
}

// The detector stretches each source into its own fixed frame, so a drawing
// survives a pure resolution change but not a change of aspect ratio: the same
// normalized coordinates then describe a different part of the view.
func TestNeedsCalibrationFollowsAspectRatio(t *testing.T) {
	cases := []struct {
		name                            string
		referenceWidth, referenceHeight int
		currentWidth, currentHeight     int
		want                            bool
	}{
		{"same size", 1920, 1080, 1920, 1080, false},
		{"pure downscale keeps the view", 1920, 1080, 960, 540, false},
		{"upstream rounding is not drift", 1920, 1080, 1920, 1088, false},
		{"same pixels, different shape", 640, 480, 704, 480, true},
		{"rotated sensor", 1920, 1080, 1080, 1920, true},
		{"unknown at drawing time is not drift", 0, 0, 1920, 1080, false},
		{"unknown now is not drift", 1920, 1080, 0, 0, false},
	}
	for _, test := range cases {
		if got := needsCalibration(test.referenceWidth, test.referenceHeight, test.currentWidth, test.currentHeight); got != test.want {
			t.Errorf("%s: got %v want %v", test.name, got, test.want)
		}
	}
}
