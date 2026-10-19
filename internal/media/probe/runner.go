// Package probe verifies media with bounded processes; stderr stays private.
package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/media/zlm"
)

var ErrProbeFailed = errors.New("media_probe_failed")
var ErrFileInvalid = errors.New("media_file_invalid")
var errProcessExit = errors.New("media_process_exit")
var ErrProbeTarget = errors.New("invalid_internal_media_target")

const outputLimit = 1 << 20

type Runner struct{ FFmpeg, FFprobe string }

func (Runner) String() string { return "<bounded media probe>" }

type VideoEvidence struct {
	FirstFrame bool    `json:"first_frame"`
	Codec      string  `json:"codec"`
	Width      int     `json:"width"`
	Height     int     `json:"height"`
	FPS        float64 `json:"fps"`
	// Profile is the codec profile the camera actually encodes with ("Main",
	// "High", "Baseline"). It decides what a decoder can do with the stream and
	// is the first thing to check when a camera's picture is unexpectedly soft:
	// a "Baseline" H.264 stream has no B-frames and no CABAC.
	Profile string `json:"profile,omitempty"`
	// Level is the codec level ("4.1", "5.1"). It caps resolution and frame rate;
	// a stream whose level is too low for its resolution is the reason some
	// decoders refuse it outright.
	Level string `json:"level,omitempty"`
	// BitrateKbps is the encoder's declared video bit rate. Zero means the stream
	// does not declare one (many encoders omit it), not that it is zero.
	BitrateKbps int `json:"bitrate_kbps,omitempty"`
	// PixelFormat is the raw pixel layout ("yuvj420p", "yuv420p10le", ...). The
	// full-range "j" variants and 10-bit formats matter: browsers and the WebRTC
	// path do not all accept them.
	PixelFormat string `json:"pixel_format,omitempty"`
	// SampleAspect is the stream's sample aspect ratio when it is not 1:1 — a
	// camera encoding anamorphic video stores a different display width than its
	// pixel width, and "704x576" alone then misdescribes the picture.
	SampleAspect string `json:"sample_aspect,omitempty"`
	// AudioSampleRate and AudioChannels describe the audio track the same way the
	// video fields describe the picture.
	AudioSampleRate int `json:"audio_sample_rate,omitempty"`
	AudioChannels   int `json:"audio_channels,omitempty"`
	// AudioBitrateKbps is the declared audio bit rate, zero when undeclared.
	AudioBitrateKbps int `json:"audio_bitrate_kbps,omitempty"`
	// AudioCodec is the first input audio stream's codec as printed by ffmpeg
	// ("pcm_alaw", "aac", ...). A non-nil empty string means the stream
	// genuinely carries no audio; nil means the evidence predates this capture
	// and the question was never asked.
	AudioCodec *string   `json:"audio_codec,omitempty"`
	ObservedAt time.Time `json:"observed_at"`
}
type FileEvidence struct {
	Video    VideoEvidence
	Duration time.Duration
	Size     int64
	Readable bool
}

func InternalURL(key zlm.StreamKey, token string) (string, error) {
	if key.Validate() != nil || len(token) > 256 || strings.ContainsAny(token, "\x00\r\n") {
		return "", ErrProbeTarget
	}
	u := url.URL{Scheme: "rtsp", Host: "zlm:554", Path: "/" + key.App + "/" + key.Stream}
	if token != "" {
		q := url.Values{"probe_token": {token}}
		u.RawQuery = q.Encode()
	}
	return u.String(), nil
}
func validInternalURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "rtsp" || u.Host != "zlm:554" || u.User != nil || u.Fragment != "" || u.Opaque != "" || !strings.HasPrefix(u.Path, "/one_nvr/") {
		return false
	}
	if _, err := id.Parse(strings.TrimPrefix(u.Path, "/one_nvr/")); err != nil {
		return false
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(q) > 1 {
		return false
	}
	for k, v := range q {
		if k != "probe_token" || len(v) != 1 || len(v[0]) > 256 || strings.ContainsAny(v[0], "\x00\r\n") {
			return false
		}
	}
	return true
}

func run(ctx context.Context, binary string, args []string, files []*os.File) ([]byte, []byte, error) {
	if binary == "" {
		return nil, nil, ErrProbeFailed
	}
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(bounded, binary, args...)
	cmd.ExtraFiles = files
	cmd.WaitDelay = time.Second
	// Separate budgets prevent os/exec's concurrent copy goroutines racing.
	stdout := limitedOutput{left: outputLimit / 2}
	stderr := limitedOutput{left: outputLimit / 2}
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exited *exec.ExitError
		if bounded.Err() == nil && stdout.left > 0 && stderr.left > 0 && errors.As(err, &exited) {
			return nil, nil, errors.Join(ErrProbeFailed, errProcessExit)
		}
		return nil, nil, ErrProbeFailed
	}
	return stdout.Bytes(), stderr.Bytes(), nil
}

type limitedOutput struct {
	bytes.Buffer
	left int
}

func (w *limitedOutput) Write(p []byte) (int, error) {
	if len(p) > w.left {
		n := w.left
		w.Buffer.Write(p[:n])
		w.left = 0
		return n, ErrProbeFailed
	}
	w.left -= len(p)
	return w.Buffer.Write(p)
}

var dimensions = regexp.MustCompile(`\bs:(\d+)x(\d+)\b`)
var codec = regexp.MustCompile(`Video: ([a-zA-Z0-9_]+)`)
var audio = regexp.MustCompile(`Audio: ([a-zA-Z0-9_]+)`)
var rate = regexp.MustCompile(`([0-9.]+) fps`)

// The remaining fields come from the same "Stream #0:0" line, whose shape ffmpeg
// keeps stable: codec, then the profile in brackets, the container tag, the
// pixel format and dimensions in brackets, then bit rate, fps and level.
//
// Each pattern is deliberately narrow. A missing match leaves the field empty,
// which the UI prints as absent rather than as a guess — a camera that does not
// declare its bit rate must not appear to have a measured one.
var (
	profile     = regexp.MustCompile(`Video: [a-zA-Z0-9_]+ \(([^)]+)\)`)
	pixelFormat = regexp.MustCompile(`Video: [^,]+, ([a-zA-Z0-9]+)(?:\([^)]*\))?, \d+x\d+`)
	// videoStreamLine matches any ffmpeg stream-info line. The bit rate must
	// come from the INPUT one: ffmpeg also prints an output stream for its null
	// sink, and that line carries a placeholder rate of 200 kb/s that has nothing
	// to do with the camera. Measured on a live site, every row reported exactly
	// 200 while the real streams ran at 243-3446 kbps.
	videoStreamLine = regexp.MustCompile(`(?i)stream #0:\d+[^\n]*`)
	videoRate       = regexp.MustCompile(`, (\d+) kb/s`)
	// nullSinkDecoder names the decoder ffmpeg uses for its discard sink, which
	// is how the output line is recognised. Go's regexp has no lookahead, so the
	// line is filtered after matching rather than excluded by the pattern.
	nullSinkDecoder = "wrapped_avframe"
	sampleAspect    = regexp.MustCompile(`\[SAR (\d+:\d+) DAR`)
	codecLevel      = regexp.MustCompile(`Video: [^,]+.*?\((?:Main|High|Baseline|Constrained Baseline|Main 10|High 10|Rext)[^)]*\).*?\b(\d+\.\d+)`)
	audioRate       = regexp.MustCompile(`Audio: [^,]+, (\d+) Hz, (\w+)`)
)

func (r Runner) FirstFrame(ctx context.Context, raw string) (VideoEvidence, error) {
	if !validInternalURL(raw) {
		return VideoEvidence{}, ErrProbeTarget
	}
	out, logs, err := run(ctx, r.FFmpeg, []string{"-nostdin", "-hide_banner", "-loglevel", "info", "-threads", "2", "-rtsp_transport", "tcp", "-timeout", "8000000", "-i", raw, "-map", "0:v:0", "-an", "-vf", "showinfo", "-frames:v", "1", "-progress", "pipe:1", "-f", "null", "-"}, nil)
	if err != nil {
		return VideoEvidence{}, err
	}
	if !regexp.MustCompile(`(?m)^frame=1$`).Match(out) {
		return VideoEvidence{}, ErrProbeFailed
	}
	dim := dimensions.FindSubmatch(logs)
	c := codec.FindSubmatch(logs)
	if len(dim) != 3 || len(c) != 2 {
		return VideoEvidence{}, ErrProbeFailed
	}
	width, _ := strconv.Atoi(string(dim[1]))
	height, _ := strconv.Atoi(string(dim[2]))
	if width < 1 || height < 1 || width > 16384 || height > 16384 {
		return VideoEvidence{}, ErrProbeFailed
	}
	fps := 0.0
	if m := rate.FindSubmatch(logs); len(m) == 2 {
		fps, _ = strconv.ParseFloat(string(m[1]), 64)
	}
	// The input stream table lists every stream, so a successful video parse
	// with no audio line is itself evidence that the camera sends no audio.
	audioCodec := ""
	if m := audio.FindSubmatch(logs); len(m) == 2 {
		audioCodec = string(m[1])
	}
	evidence := VideoEvidence{
		FirstFrame:   true,
		Codec:        string(c[1]),
		Width:        width,
		Height:       height,
		FPS:          fps,
		AudioCodec:   &audioCodec,
		ObservedAt:   time.Now().UTC(),
		PixelFormat:  firstGroup(pixelFormat, logs),
		SampleAspect: firstGroup(sampleAspect, logs),
		Level:        firstGroup(codecLevel, logs),
	}
	// The profile sits in the first parenthesised group after the codec name on
	// the INPUT line. Reading it from the whole log risks the null sink's own
	// line, whose decoder name is not a codec profile at all.
	if input := firstInputStreamLine(logs); input != nil && codec.Find(input) != nil {
		evidence.Profile = firstGroup(profile, input)
	}
	// Every number below comes from the INPUT line, never from the null sink's
	// output line (see videoStreamLine above).
	if input := firstInputStreamLine(logs); input != nil {
		if m := videoRate.FindSubmatch(input); len(m) == 2 {
			evidence.BitrateKbps, _ = strconv.Atoi(string(m[1]))
		}
	}
	if m := audioRate.FindSubmatch(logs); len(m) == 3 {
		evidence.AudioSampleRate, _ = strconv.Atoi(string(m[1]))
		evidence.AudioChannels = channelCount(string(m[2]))
	}
	return evidence, nil
}

// firstInputStreamLine returns the first stream-info line that belongs to the
// INPUT, skipping the line ffmpeg prints for its own null sink.
func firstInputStreamLine(logs []byte) []byte {
	for _, line := range videoStreamLine.FindAll(logs, -1) {
		if bytes.Contains(line, []byte(nullSinkDecoder)) {
			continue
		}
		return line
	}
	return nil
}

// firstGroup returns capture group 1 of the first match, or "".
func firstGroup(pattern *regexp.Regexp, data []byte) string {
	if m := pattern.FindSubmatch(data); len(m) == 2 {
		return strings.TrimSpace(string(m[1]))
	}
	return ""
}

// channelCount maps ffmpeg's channel layout word to a number. Unknown layouts
// report 0, which the UI omits rather than printing a wrong count.
func channelCount(layout string) int {
	switch layout {
	case "mono":
		return 1
	case "stereo":
		return 2
	}
	return 0
}
func (r Runner) InspectMP4(ctx context.Context, file *os.File) (FileEvidence, error) {
	if file == nil {
		return FileEvidence{}, ErrProbeTarget
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 {
		return FileEvidence{}, ErrProbeFailed
	}
	path := "/proc/self/fd/3"
	if runtime.GOOS == "darwin" {
		path = "/dev/fd/3"
	}
	out, _, err := run(ctx, r.FFprobe, []string{"-v", "error", "-protocol_whitelist", "file,pipe", "-show_entries", "format=format_name,duration:stream=codec_type,codec_name,width,height,avg_frame_rate", "-of", "json", path}, []*os.File{file})
	if err != nil {
		if errors.Is(err, errProcessExit) {
			return FileEvidence{}, ErrFileInvalid
		}
		return FileEvidence{}, ErrProbeFailed
	}
	var result struct {
		Format struct {
			Name     string `json:"format_name"`
			Duration string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			Type   string `json:"codec_type"`
			Codec  string `json:"codec_name"`
			Width  int    `json:"width"`
			Height int    `json:"height"`
			Rate   string `json:"avg_frame_rate"`
		} `json:"streams"`
	}
	if json.Unmarshal(out, &result) != nil || !strings.Contains(result.Format.Name, "mp4") {
		return FileEvidence{}, ErrFileInvalid
	}
	seconds, err := strconv.ParseFloat(result.Format.Duration, 64)
	if err != nil || seconds <= 0 || seconds > 86400 {
		return FileEvidence{}, ErrFileInvalid
	}
	for _, s := range result.Streams {
		if s.Type != "video" {
			continue
		}
		if s.Width <= 0 || s.Height <= 0 || s.Codec == "" {
			return FileEvidence{}, ErrFileInvalid
		}
		fps := 0.0
		var n, d float64
		if _, err := fmt.Sscanf(s.Rate, "%f/%f", &n, &d); err == nil && d > 0 {
			fps = n / d
		}
		return FileEvidence{Video: VideoEvidence{Codec: s.Codec, Width: s.Width, Height: s.Height, FPS: fps, ObservedAt: time.Now().UTC()}, Duration: time.Duration(seconds * float64(time.Second)), Size: info.Size(), Readable: true}, nil
	}
	return FileEvidence{}, ErrFileInvalid
}

var _ io.Writer = (*limitedOutput)(nil)
