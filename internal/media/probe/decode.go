package probe

// Full-decode verification: does the media actually play?

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DecodeReport is what a full decode established.
type DecodeReport struct {
	// Errors is the number of decode errors reported by the decoder.
	Errors int
	// FirstError is the first error line, kept short for a log or a CLI.
	FirstError string
	// Inspected is false when the check could not run at all; Errors then means
	// nothing and the caller must not treat the file as damaged.
	Inspected bool
}

// decodeTimeout bounds one full decode of a clip. Decoding is far cheaper than
// realtime for these files, and a wedged process must not hold a sweep forever.
const decodeTimeout = 30 * time.Minute

// DecodeCheck decodes the whole file and reports the decoder's complaints.
//
// ffprobe answers "is the container readable"; this answers "do the bytes
// actually decode". The difference is not academic: an event clip assembled by
// the pre-fix concatenation path passed every ffprobe check — codec, resolution,
// duration all sane — while its AAC track was invalid, so no player could open
// it. Only a decode reveals that.
func (r Runner) DecodeCheck(ctx context.Context, path string) DecodeReport {
	if r.FFmpeg == "" || path == "" {
		return DecodeReport{}
	}
	bounded, cancel := context.WithTimeout(ctx, decodeTimeout)
	defer cancel()
	cmd := exec.CommandContext(bounded, r.FFmpeg, "-nostdin", "-v", "error", "-i", path, "-f", "null", "-")
	cmd.WaitDelay = time.Second
	var stderr limitedBuffer
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	output := strings.TrimSpace(stderr.String())
	report := DecodeReport{Inspected: runErr == nil || output != ""}
	if output == "" {
		return report
	}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || isMuxerComplaint(line) {
			continue
		}
		report.Errors++
		if report.FirstError == "" {
			report.FirstError = line
		}
	}
	return report
}

// isMuxerComplaint reports whether a line is about the null OUTPUT sink rather
// than the input.
//
// `-f null` re-muxes the decoded frames as they are produced, and for some
// legitimate streams that muxer emits "Application provided invalid, non
// monotonically increasing dts to muxer in stream 0: 159 >= 159". The file plays
// fine; the complaint is about how this probe feeds its own discard sink.
// Counting those marked healthy clips for deletion — measured on the live site,
// three of the first twenty reported "corrupt" were only this.
func isMuxerComplaint(line string) bool {
	return strings.Contains(line, "non monotonically increasing dts to muxer")
}

// limitedBuffer keeps the first few kilobytes of stderr: a corrupt file can
// produce megabytes of complaints and only the beginning is informative.
type limitedBuffer struct {
	mu      sync.Mutex
	builder strings.Builder
	left    int
}

const decodeOutputLimit = 8 << 10

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.left == 0 {
		b.left = decodeOutputLimit
	}
	if len(p) > b.left {
		p = p[:b.left]
	}
	b.left -= len(p)
	b.builder.Write(p)
	return len(p), nil
}

func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.builder.String()
}

var _ = strconv.Itoa
