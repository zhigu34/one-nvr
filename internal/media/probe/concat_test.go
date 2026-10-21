package probe

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The concatenated timeline must span the real media, not the caller's estimate.
//
// Fragment durations arrive from file names and mtimes: whole seconds, each off
// by up to one. Writing those into the ffconcat script made ffmpeg build a
// 428-second timeline over 13 seconds of frames, which a player experiences as
// stuttering — it spends most of its time waiting out gaps that do not exist.
// On the live site this was the difference between 2 jumps with a 237-second
// hole and no jumps at all.
func TestConcatUsesMeasuredDurationsNotTheHint(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("native media tools absent; pinned runtime contract is mandatory CI gate")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("native media tools absent; pinned runtime contract is mandatory CI gate")
	}
	dir := t.TempDir()
	// Fragments cut the way a media server cuts them: built as one continuous
	// source and split with `-f segment`, so they share the source's frame grid
	// the way real ZLM segments do — separate lavfi runs would each start their
	// own clock and test something else entirely.
	source := filepath.Join(dir, "source.mp4")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	cmd := exec.CommandContext(ctx, ffmpeg, "-nostdin", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=160x120:rate=10", "-t", "9",
		"-c:v", "libx264", "-g", "13", "-threads", "1", "-y", source)
	runErr := cmd.Run()
	cancel()
	if runErr != nil {
		t.Fatal("could not create synthetic source", runErr)
	}
	// Segmented at 2.3 s with a 13-frame GOP: neither divides the other, so the
	// fragments come out at slightly different lengths exactly the way a media
	// server's do (the site measures 3.304-3.392 s). Uniform fragments would let
	// a naive concatenation look correct.
	ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
	cmd = exec.CommandContext(ctx, ffmpeg, "-nostdin", "-hide_banner", "-loglevel", "error",
		"-i", source, "-c", "copy", "-f", "segment", "-segment_time", "2.3", "-reset_timestamps", "1",
		"-y", filepath.Join(dir, "frag%d.mp4"))
	runErr = cmd.Run()
	cancel()
	if runErr != nil {
		t.Fatal("could not split the source", runErr)
	}
	// However many fragments the split produced; 6 s at 13 frames each is four
	// or five depending on keyframe placement, and the count is not the point.
	var paths []string
	for i := 0; ; i++ {
		path := filepath.Join(dir, "frag"+strconv.Itoa(i)+".mp4")
		if _, err := os.Stat(path); err != nil {
			break
		}
		paths = append(paths, path)
	}
	if len(paths) < 3 {
		t.Fatalf("expected at least three fragments, got %d", len(paths))
	}

	// The hint deliberately lies the way file names do: each part claims the
	// whole-second gap between names, not the media length.
	const lie = 3 * time.Second
	parts := make([]ConcatPart, 0, len(paths))
	for _, path := range paths {
		parts = append(parts, ConcatPart{Path: path, Duration: lie})
	}
	output := filepath.Join(dir, "joined.mp4")
	r := Runner{FFmpeg: ffmpeg, FFprobe: ffprobe}
	evidence, err := r.ConcatMP4(context.Background(), parts, output, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 9 seconds of source. If the hints had been used the file would claim far more.
	if evidence.Duration < 8900*time.Millisecond || evidence.Duration > 9200*time.Millisecond {
		t.Fatalf("the joined file must span the media (about 9s), got %v", evidence.Duration)
	}

	// And the frame timestamps must be continuous: no gap wider than a little
	// over one frame interval (100 ms at 10 fps).
	pts := videoTimestamps(t, ffprobe, output)
	if len(pts) < 80 {
		t.Fatalf("expected about 90 frames, got %d", len(pts))
	}
	for i := 1; i < len(pts); i++ {
		if gap := pts[i] - pts[i-1]; gap > 0.15 {
			t.Fatalf("timeline has a %.3fs hole at %.3fs; fragments were concatenated by their hints", gap, pts[i-1])
		}
	}
}

// videoTimestamps returns every video frame's presentation time.
func videoTimestamps(t *testing.T, ffprobe, path string) []float64 {
	t.Helper()
	cmd := exec.Command(ffprobe, "-v", "error", "-select_streams", "v:0",
		"-show_entries", "frame=pts_time", "-of", "csv=p=0", path)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	var times []float64
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		value, err := strconv.ParseFloat(line, 64)
		if err != nil {
			continue
		}
		times = append(times, value)
	}
	return times
}

// A part whose file has no readable duration and whose hint is missing is
// refused rather than silently producing a broken timeline.
func TestConcatRefusesAPartWithNoDurationAtAll(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "absent.mp4")
	r := Runner{FFmpeg: "ffmpeg", FFprobe: "ffprobe"}
	if _, err := r.ConcatMP4(context.Background(), []ConcatPart{{Path: missing}}, filepath.Join(dir, "out.mp4"), nil); err == nil {
		t.Fatal("a part with no measurable duration must be refused")
	}
	if _, err := os.Stat(filepath.Join(dir, "out.mp4")); err == nil {
		t.Fatal("no output must be produced for a refused concatenation")
	}
}
