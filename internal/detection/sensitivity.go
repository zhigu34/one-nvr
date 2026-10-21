// Package detection owns the detector configuration and its publication.
package detection

// Sensitivity is one of three fixed profiles for how readily the detector calls
// something a detection.
//
// Three numbers move together, which is why this is an enum and not three
// fields on the page: raising min_score without raising threshold produces
// flicker, and shrinking min_area without lowering min_score produces noise.
// A profile keeps the combination coherent.
//
//	             min_score  threshold  min_area
//	sensitive       0.35       0.45       800
//	balanced        0.50       0.55      1500
//	strict          0.65       0.70      3000
//
// min_score is the score a box needs on one frame. threshold is the score at
// which Frigate starts a tracked object (lower than min_score is not allowed:
// a detection must also be sustained). min_area is in detection-resolution
// pixels, so it filters distant specks rather than nearby people.
type Sensitivity string

const (
	// SensitivitySensitive catches small or briefly visible movement — a person
	// at the far end of a driveway, someone partly behind a car. Costs more
	// false positives from foliage and shifting shadows.
	SensitivitySensitive Sensitivity = "sensitive"
	// SensitivityBalanced is Frigate's own default, made explicit so the page can
	// say what "middle" means.
	SensitivityBalanced Sensitivity = "balanced"
	// SensitivityStrict requires a clear, sustained target. For a scene with a
	// lot of irrelevant motion (a busy pavement, a swinging sign).
	SensitivityStrict Sensitivity = "strict"
)

// FilterProfile is the Frigate `objects.filters.<label>` block these values
// render into.
type FilterProfile struct {
	MinScore  float64
	Threshold float64
	MinArea   int
}

// profiles is the single definition of what each sensitivity means. Anything
// that needs the numbers reads them from here; nothing hard-codes them.
var profiles = map[Sensitivity]FilterProfile{
	SensitivitySensitive: {MinScore: 0.35, Threshold: 0.45, MinArea: 800},
	SensitivityBalanced:  {MinScore: 0.50, Threshold: 0.55, MinArea: 1500},
	SensitivityStrict:    {MinScore: 0.65, Threshold: 0.70, MinArea: 3000},
}

// Valid reports whether the value names a profile.
func (s Sensitivity) Valid() bool {
	_, ok := profiles[s]
	return ok
}

// Profile returns the numbers this sensitivity renders into. An unknown value
// falls back to balanced rather than to a zero profile: a zero min_score would
// detect everything, and a wrong guess must not make the detector noisier than
// the operator asked for.
func (s Sensitivity) Profile() FilterProfile {
	if profile, ok := profiles[s]; ok {
		return profile
	}
	return profiles[SensitivityBalanced]
}

// FilterLabels returns the labels a filter block is rendered for. Every tracked
// label gets the same profile: the sensitivity describes the scene (how much
// irrelevant movement it contains), not the object class.
func (s Sensitivity) FilterLabels(tracked []string) map[string]FilterProfile {
	out := make(map[string]FilterProfile, len(tracked))
	profile := s.Profile()
	for _, label := range tracked {
		out[label] = profile
	}
	return out
}
