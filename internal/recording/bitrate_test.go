package recording

import (
	"testing"

	"github.com/zhigu34/one-nvr/internal/media/zlm"
)

// videoFrames is the sampler's only measure of "is this stream producing
// pictures", so what it ignores matters as much as what it counts.
//
// The bug this guards against: requiring track.Ready made a stream that was
// actively ingesting a keyframe — the moment it was busiest — report zero
// frames. The sampler read that as a stall and tore the pull down, which is how
// one channel's sub stream ended up in a forty-second rebuild loop while every
// other channel was fine.
func TestVideoFramesCountsAProgressingTrackThatIsNotMarkedReady(t *testing.T) {
	snapshot := zlm.StreamSnapshot{Tracks: []zlm.Track{
		{Codec: "h264", Ready: false, Width: 640, Height: 480, Frames: 1234},
	}}
	if got := videoFrames(snapshot); got != 1234 {
		t.Fatalf("videoFrames = %d, want 1234; a track mid-keyframe reports "+
			"ready=false while its counter still advances", got)
	}
}

// Dimensions are what separate a video track from a data or metadata track that
// carries a frame counter of its own, so a dimensionless track must not count.
func TestVideoFramesIgnoresTracksWithoutDimensions(t *testing.T) {
	snapshot := zlm.StreamSnapshot{Tracks: []zlm.Track{
		{Codec: "meta", Ready: true, Frames: 99999},
		{Codec: "h264", Ready: true, Width: 640, Height: 480, Frames: 42},
	}}
	if got := videoFrames(snapshot); got != 42 {
		t.Fatalf("videoFrames = %d, want 42; a track without dimensions must not "+
			"stand in for the video track", got)
	}
}

// The highest counter wins: a stream may carry several video tracks, and the one
// that has decoded most is the evidence of progress.
func TestVideoFramesTakesTheHighestCounter(t *testing.T) {
	snapshot := zlm.StreamSnapshot{Tracks: []zlm.Track{
		{Width: 640, Height: 480, Frames: 10},
		{Width: 320, Height: 240, Frames: 500},
	}}
	if got := videoFrames(snapshot); got != 500 {
		t.Fatalf("videoFrames = %d, want 500", got)
	}
}

func TestVideoFramesEmptyStreamIsZero(t *testing.T) {
	if got := videoFrames(zlm.StreamSnapshot{}); got != 0 {
		t.Fatalf("videoFrames = %d, want 0", got)
	}
}
