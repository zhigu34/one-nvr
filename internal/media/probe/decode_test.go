package probe

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// ffmpegForTest skips when the toolchain is absent, matching media_tools_test.
func ffmpegForTest(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable; decode verification needs the real tool")
	}
	return path
}

// A well-formed clip decodes without complaint. Without this case a check that
// simply reported every file corrupt would look correct.
func TestDecodeCheckAcceptsAHealthyClip(t *testing.T) {
	ffmpeg := ffmpegForTest(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "healthy.mp4")
	if err := exec.Command(ffmpeg, "-nostdin", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=25", "-f", "lavfi", "-i", "sine=frequency=440:duration=2",
		"-t", "2", "-c:v", "libx264", "-threads", "1", "-c:a", "aac", "-y", path).Run(); err != nil {
		t.Fatal("synthetic clip creation failed", err)
	}
	report := Runner{FFmpeg: ffmpeg}.DecodeCheck(context.Background(), path)
	if !report.Inspected {
		t.Fatal("the decoder could not inspect a healthy clip")
	}
	if report.Errors != 0 {
		t.Fatalf("a healthy clip reported %d decode errors: %s", report.Errors, report.FirstError)
	}
}

// A clip whose audio payload is replaced with noise must be reported. This is
// the shape of the defect: ffprobe reads a sane container (codec, duration,
// resolution all fine) while no decoder can read the samples.
func TestDecodeCheckRejectsInvalidAudio(t *testing.T) {
	ffmpeg := ffmpegForTest(t)
	dir := t.TempDir()
	source := filepath.Join(dir, "source.mp4")
	if err := exec.Command(ffmpeg, "-nostdin", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=25", "-f", "lavfi", "-i", "sine=frequency=440:duration=6",
		"-t", "6", "-c:v", "libx264", "-threads", "1", "-c:a", "aac", "-y", source).Run(); err != nil {
		t.Fatal("synthetic clip creation failed", err)
	}
	// Corrupt the audio samples in place, leaving the container intact: every
	// structural check still passes, only decoding fails.
	raw, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	// Walk backwards past the moov box so the mdat payload is what gets hit.
	for i := len(raw) / 2; i < len(raw)/2+4096 && i < len(raw); i++ {
		raw[i] ^= 0xff
	}
	path := filepath.Join(dir, "corrupt.mp4")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	report := Runner{FFmpeg: ffmpeg}.DecodeCheck(context.Background(), path)
	if !report.Inspected {
		t.Fatal("the decoder could not inspect the corrupted clip")
	}
	if report.Errors == 0 {
		t.Fatal("corrupted audio decoded without complaint; the check cannot detect the defect it exists for")
	}
}

// The null sink complains about non-monotonic DTS on some legitimate streams.
// Counting that as corruption would delete healthy recordings: measured on the
// live site, three of the first twenty files reported "corrupt" carried this
// warning and nothing else.
func TestDecodeCheckIgnoresMuxerComplaints(t *testing.T) {
	if !isMuxerComplaint("[null @ 0x5592c944e980] Application provided invalid, non monotonically increasing dts to muxer in stream 0: 159 >= 159") {
		t.Fatal("the null-sink DTS warning must not be counted as a decode error")
	}
	if isMuxerComplaint("[aac @ 0x5603451d32c0] Number of scalefactor bands in group (47) exceeds limit (40).") {
		t.Fatal("a real AAC error was classified as a muxer complaint")
	}
}
