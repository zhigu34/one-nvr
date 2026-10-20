package recording

import (
	"path/filepath"
	"testing"
)

// Ring-buffer fragments must be filtered out of the completion inbox.
//
// The media server reports every 3-second fragment of every buffering channel —
// measured at about 5,400 an hour on the live site. None is a recording the index
// should hold: the fragments are disposable and only the assembled clip is
// published. Recording each as 'unregistered_recording_run' produced 144,000
// diagnostic rows and 60 MB of payload, growing ~55 MB a day with no retention.
func TestIsEventCachePath(t *testing.T) {
	root := "/cache"
	service := &Service{EventCacheDir: root}

	inside := []string{
		"/cache",
		"/cache/events/abc/record/one_nvr/s1/2026-10-20/2026-10-20-10-00-00-0.mp4",
		"/cache/events/abc/record/one_nvr/s1/2026-10-20/nested/x.mp4",
	}
	for _, path := range inside {
		if !service.isEventCachePath(path) {
			t.Fatalf("%q must be recognised as buffer traffic", path)
		}
	}

	outside := []string{
		// A published recording: the anomaly the diagnostic state exists for.
		"/storage/pool-1/recordings/CH01/2026-10-20/CH01_20261020_100000_run.mp4",
		// A sibling that merely shares the prefix must NOT match: the check is on
		// path components, not on a string prefix.
		"/cache-other/events/abc/x.mp4",
		"",
	}
	for _, path := range outside {
		if service.isEventCachePath(path) {
			t.Fatalf("%q was wrongly treated as buffer traffic", path)
		}
	}

	// A deployment with no cache reserved has no buffer traffic at all.
	unconfigured := &Service{}
	if unconfigured.isEventCachePath(filepath.Join(root, "events", "x.mp4")) {
		t.Fatal("without a configured cache nothing can be buffer traffic")
	}
}
