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

// ConcatPart is one input of a stream-copy concatenation. Duration feeds the
// ffconcat script (so the demuxer timestamps correctly) and the progress ratio.
type ConcatPart struct {
	Path     string
	Duration time.Duration
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
	for _, part := range parts {
		if part.Path == "" || part.Duration <= 0 {
			return out, ErrProbeFailed
		}
		total += part.Duration
	}
	list := output + ".ffconcat"
	script := &strings.Builder{}
	script.WriteString("ffconcat version 1.0\n")
	for _, part := range parts {
		// ffconcat quoting: a literal quote closes the quoted region, an
		// escaped quote and a reopened one carry it forward.
		script.WriteString(fmt.Sprintf("file '%s'\n", strings.ReplaceAll(part.Path, "'", `'\''`)))
		script.WriteString(fmt.Sprintf("duration %.3f\n", part.Duration.Seconds()))
	}
	if err := os.WriteFile(list, []byte(script.String()), 0o600); err != nil {
		return out, ErrProbeFailed
	}
	defer os.Remove(list)

	bounded, cancel := context.WithTimeout(ctx, concatTimeout)
	defer cancel()
	cmd := exec.CommandContext(bounded, r.FFmpeg, "-hide_banner", "-loglevel", "error", "-nostdin",
		"-f", "concat", "-safe", "0", "-i", list, "-c", "copy", "-nostats", "-progress", "pipe:1", "-y", output)
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
