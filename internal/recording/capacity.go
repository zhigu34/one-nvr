package recording

import (
	"math"
	"time"

	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/storage"
)

// A stream sample is one main RTSP stream observation, not one protocol view.
// Invalid placeholders keep an expected channel from disappearing from totals.
type BitrateSample struct {
	ChannelID      id.ID
	ObservedAt     time.Time
	BytesPerSecond int64
	Kind           string
	Valid          bool
}

func SafetyLine(samples []BitrateSample) (int64, bool) { return safetyLine(time.Now().UTC(), samples) }
func safetyLine(now time.Time, samples []BitrateSample) (int64, bool) {
	type evidence struct {
		fresh []time.Time
		rate  int64
	}
	channels := map[id.ID]*evidence{}
	for _, sample := range samples {
		ch := channels[sample.ChannelID]
		if ch == nil {
			ch = &evidence{}
			channels[sample.ChannelID] = ch
		}
		if !sample.Valid || sample.BytesPerSecond <= 0 || sample.ObservedAt.After(now) || sample.ObservedAt.Before(now.Add(-5*time.Minute)) {
			continue
		}
		if sample.Kind != "stream" && sample.Kind != "segment" {
			continue
		}
		if sample.BytesPerSecond > ch.rate {
			ch.rate = sample.BytesPerSecond
		}
		if sample.Kind == "stream" && !sample.ObservedAt.Before(now.Add(-30*time.Second)) {
			ch.fresh = append(ch.fresh, sample.ObservedAt)
		}
	}
	if len(channels) == 0 {
		return storage.MinimumFreeBytes, false
	}
	total := int64(0)
	known := true
	for _, ch := range channels {
		paired := false
		for _, a := range ch.fresh {
			for _, b := range ch.fresh {
				if a.Sub(b) >= 10*time.Second {
					paired = true
				}
			}
		}
		if !paired {
			known = false
		}
		if ch.rate > math.MaxInt64-total {
			total = math.MaxInt64
		} else {
			total += ch.rate
		}
	}
	if total > math.MaxInt64/600 {
		return math.MaxInt64, known
	}
	return max(storage.MinimumFreeBytes, total*600), known
}
