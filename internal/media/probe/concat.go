package probe

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// concatTimeout bounds one stream-copy remux. Copying is I/O-bound and normally
// far faster than the material itself; the job lease renews independently, and
// this ceiling stops a wedged process from holding a worker forever.
const concatTimeout = 15 * time.Minute

// ConcatPart is one input of a stream-copy concatenation.
//
// Duration is only a hint for the progress ratio. It is deliberately NOT written
// into the ffconcat script: the caller's value comes from fragment file names and
// mtimes, which are whole seconds and therefore wrong by up to a second each.
// Four fragments whose names say 4+3+3 seconds but whose frames span 13.3 made
// ffmpeg emit a 606-second timeline with a 237-second hole — a recording that
// stutters because the player is waiting out a gap that never existed. The real
// durations are measured from the files instead.
type ConcatPart struct {
	Path     string
	Duration time.Duration
}

// mediaDuration reads a file's real duration. Used instead of the caller's hint,
// which for fragments comes from whole-second file names and mtimes.
func (r Runner) mediaDuration(ctx context.Context, path string) (time.Duration, error) {
	if r.FFprobe == "" {
		return 0, ErrProbeFailed
	}
	bounded, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(bounded, r.FFprobe, "-v", "error",
		"-show_entries", "format=duration", "-of", "csv=p=0", path)
	cmd.WaitDelay = time.Second
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return 0, ErrProbeFailed
	}
	seconds, err := strconv.ParseFloat(strings.TrimSpace(stdout.String()), 64)
	if err != nil || seconds <= 0 {
		return 0, ErrProbeFailed
	}
	return time.Duration(seconds * float64(time.Second)), nil
}

// ConcatMP4 remuxes whole MP4 files into one output with stream copy. It never
// transcodes: parameter compatibility is the caller's gate, and the returned
// evidence is the output's own ffprobe verification. progress receives the
// completed portion of the summed input durations.
func (r Runner) ConcatMP4(ctx context.Context, parts []ConcatPart, output string, progress func(float64)) (FileEvidence, error) {
	var out FileEvidence
	if r.FFmpeg == "" || len(parts) == 0 || output == "" {
		return out, ErrProbeFailed
	}
	var total time.Duration
	durations := make([]time.Duration, len(parts))
	for i, part := range parts {
		if part.Path == "" {
			return out, ErrProbeFailed
		}
		// Measure the real duration. The caller's hint cannot be used for the
		// timeline: see ConcatPart.
		measured, err := r.mediaDuration(ctx, part.Path)
		if err != nil || measured <= 0 {
			// Fall back to the hint so a caller with accurate values still works,
			// but never silently: a zero here means the concat is skipped.
			if part.Duration <= 0 {
				return out, ErrProbeFailed
			}
			measured = part.Duration
		}
		durations[i] = measured
		total += measured
	}
	list := output + ".ffconcat"
	script := &strings.Builder{}
	script.WriteString("ffconcat version 1.0\n")
	for _, part := range parts {
		// ffconcat quoting: a literal quote closes the quoted region, an
		// escaped quote and a reopened one carry it forward.
		//
		// No `duration` directive, deliberately. The directive tells the
		// demuxer how long the part occupies the timeline; the measured value
		// is the container's duration, which includes the last frame's
		// display time and therefore overshoots the final frame's timestamp by
		// one frame interval. Writing it left a hole at every seam — measured
		// as 0.2 s per seam on synthetic 10 fps material and 0.17-0.88 s on
		// the site's 15 fps fragments. Without the directive ffmpeg derives
		// each part's span from its frames and the seams close exactly.
		script.WriteString(fmt.Sprintf("file '%s'\n", strings.ReplaceAll(part.Path, "'", `'\''`)))
	}
	if err := os.WriteFile(list, []byte(script.String()), 0o600); err != nil {
		return out, ErrProbeFailed
	}
	defer os.Remove(list)

	bounded, cancel := context.WithTimeout(ctx, concatTimeout)
	defer cancel()
	// `-fflags +genpts` makes the demuxer regenerate packet timestamps from the
	// frame rate instead of trusting each part's own clock. Every fragment starts
	// at 0 and ends at its last frame; spliced by their own clocks the seams
	// leave a gap of one frame interval, which accumulates into the visible
	// stutter this exists to remove. Regenerating timestamps is still a stream
	// copy — no frame is re-encoded.
	cmd := exec.CommandContext(bounded, r.FFmpeg, "-hide_banner", "-loglevel", "error", "-nostdin",
		"-fflags", "+genpts", "-f", "concat", "-safe", "0", "-i", list,
		"-c", "copy", "-avoid_negative_ts", "make_zero", "-nostats", "-progress", "pipe:1", "-y", output)
	cmd.WaitDelay = time.Second
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return out, ErrProbeFailed
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return out, ErrProbeFailed
	}
	go reportProgress(pipe, total, progress)
	if err := cmd.Wait(); err != nil {
		// stderr stays private: it names local paths and encoder internals.
		return out, ErrProbeFailed
	}
	file, err := os.Open(output)
	if err != nil {
		return out, ErrFileInvalid
	}
	defer file.Close()
	verified, cancelCheck := context.WithTimeout(ctx, 30*time.Second)
	defer cancelCheck()
	evidence, err := r.InspectMP4(verified, file)
	if err != nil {
		return out, ErrFileInvalid
	}
	return evidence, nil
}

// reportProgress drains ffmpeg's -progress key=value stream. The demuxer
// reports out_time_us as it copies, so the ratio against the summed input
// duration is real progress, not a heartbeat.
func reportProgress(pipe io.ReadCloser, total time.Duration, progress func(float64)) {
	if progress == nil || total <= 0 {
		return
	}
	scanner := bufio.NewScanner(pipe)
	scanner.Buffer(make([]byte, 0, 64*1024), 64*1024)
	for scanner.Scan() {
		line := scanner.Text()
		const key = "out_time_us="
		if !strings.HasPrefix(line, key) {
			continue
		}
		us, err := strconv.ParseInt(strings.TrimPrefix(line, key), 10, 64)
		if err != nil {
			continue
		}
		portion := float64(us) / 1e6 / total.Seconds()
		if portion < 0 {
			portion = 0
		}
		if portion > 1 {
			portion = 1
		}
		progress(portion)
	}
}
