package recording

import (
	"math"
	"testing"
	"time"

	"github.com/zhigu34/one-nvr/internal/id"
)

func TestRecordingCapacitySafetyLineRequiresRecentPairedEvidence(t *testing.T) {
	now := time.Now().UTC()
	ch := id.ID("channel-one")
	sample := func(age time.Duration, rate int64) BitrateSample {
		return BitrateSample{ChannelID: ch, BytesPerSecond: rate, ObservedAt: now.Add(-age), Kind: "stream", Valid: true}
	}
	for _, input := range [][]BitrateSample{nil, {sample(time.Second, 100)}, {sample(31*time.Second, 100), sample(15*time.Second, 100)}, {sample(5*time.Second, 100), sample(6*time.Second, 100)}, {sample(15*time.Second, 0), sample(time.Second, 100)}} {
		if _, known := safetyLine(now, input); known {
			t.Fatal("unknown or insufficient bitrate was treated as zero/known")
		}
	}
	line, known := safetyLine(now, []BitrateSample{sample(15*time.Second, 100), sample(time.Second, 100)})
	if !known || line != 10<<30 {
		t.Fatal("10GiB floor incorrect", line, known)
	}
	line, known = safetyLine(now, []BitrateSample{sample(15*time.Second, 40<<20), sample(time.Second, 30<<20)})
	if !known || line != 600*(40<<20) {
		t.Fatal("conservative 600-second rate incorrect", line, known)
	}
}
func TestRecordingCapacityDoesNotDoubleCountAChannelAndUsesActualSegments(t *testing.T) {
	now := time.Now().UTC()
	a, b := id.ID("one"), id.ID("two")
	samples := []BitrateSample{
		{ChannelID: a, ObservedAt: now.Add(-20 * time.Second), BytesPerSecond: 20 << 20, Kind: "stream", Valid: true},
		{ChannelID: a, ObservedAt: now.Add(-time.Second), BytesPerSecond: 20 << 20, Kind: "stream", Valid: true},
		{ChannelID: a, ObservedAt: now.Add(-time.Second), BytesPerSecond: 20 << 20, Kind: "stream", Valid: true},
		{ChannelID: b, ObservedAt: now.Add(-20 * time.Second), BytesPerSecond: 10 << 20, Kind: "stream", Valid: true},
		{ChannelID: b, ObservedAt: now.Add(-time.Second), BytesPerSecond: 10 << 20, Kind: "stream", Valid: true},
		{ChannelID: b, ObservedAt: now.Add(-time.Minute), BytesPerSecond: 15 << 20, Kind: "segment", Valid: true},
	}
	line, known := safetyLine(now, samples)
	if !known || line != 600*(35<<20) {
		t.Fatal("shared-filesystem channel rate double counted or segment ignored", line, known)
	}
	samples = append(samples, BitrateSample{ChannelID: id.ID("unknown-channel"), Kind: "stream", ObservedAt: now, Valid: false})
	if _, known := safetyLine(now, samples); known {
		t.Fatal("a missing expected channel rate was silently omitted")
	}
}
func TestRecordingCapacitySaturatesOverflow(t *testing.T) {
	now := time.Now().UTC()
	samples := []BitrateSample{{ChannelID: "one", ObservedAt: now.Add(-20 * time.Second), Kind: "stream", BytesPerSecond: math.MaxInt64, Valid: true}, {ChannelID: "one", ObservedAt: now, Kind: "stream", BytesPerSecond: math.MaxInt64, Valid: true}}
	if line, known := safetyLine(now, samples); !known || line != math.MaxInt64 {
		t.Fatal("safety line overflowed", line, known)
	}
}

func TestSafetyLineUnknownChannelKeepsKnownLowerBound(t *testing.T) {
	now := time.Now().UTC()
	samples := []BitrateSample{{ChannelID: "a", Kind: "stream", Valid: true, BytesPerSecond: 100 << 20, ObservedAt: now.Add(-20 * time.Second)}, {ChannelID: "a", Kind: "stream", Valid: true, BytesPerSecond: 100 << 20, ObservedAt: now.Add(-time.Second)}, {ChannelID: "b"}}
	line, known := safetyLine(now, samples)
	if known || line != (100<<20)*600 {
		t.Fatal("unknown peer erased actual known write budget", line, known)
	}
}
