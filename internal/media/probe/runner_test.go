package probe

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProbeBoundedAndInternalOnly(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "bounded")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec yes fixture-output\n"), 0700); err != nil {
		t.Fatal(err)
	}
	r := Runner{FFmpeg: script, FFprobe: script}
	for _, raw := range []string{"rtsp://192.168.1.20/main", "rtsp://zlm/one_nvr/not-a-session", "rtsp://zlm/one_nvr/bb6f7f61-e7dd-4eee-a03c-a53167d6d688?url=http://outside", "http://zlm/one_nvr/bb6f7f61-e7dd-4eee-a03c-a53167d6d688"} {
		if _, err := r.FirstFrame(context.Background(), raw); err == nil {
			t.Fatal("arbitrary target allowed")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := r.FirstFrame(ctx, "rtsp://zlm:554/one_nvr/bb6f7f61-e7dd-4eee-a03c-a53167d6d688"); err == nil || strings.Contains(err.Error(), "fixture-output") {
		t.Fatal("unbounded output or leaked stderr", err)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("subprocess not terminated promptly")
	}
	f, err := os.CreateTemp(dir, "invalid-*.mp4")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err = r.InspectMP4(context.Background(), f); err == nil {
		t.Fatal("invalid MP4 accepted")
	}
}

func TestInspectMP4ActualStructure(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("native media tools absent; pinned runtime contract is mandatory CI gate")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("native media tools absent; pinned runtime contract is mandatory CI gate")
	}
	path := filepath.Join(t.TempDir(), "synthetic.mp4")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffmpeg, "-nostdin", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=5", "-frames:v", "10", "-c:v", "libx264", "-threads", "1", path)
	if err := cmd.Run(); err != nil {
		t.Fatal("could not create synthetic MP4", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	evidence, err := (Runner{FFmpeg: ffmpeg, FFprobe: ffprobe}).InspectMP4(ctx, f)
	if err != nil || !evidence.Readable || evidence.Video.Width != 320 || evidence.Video.Height != 180 || evidence.Video.Codec != "h264" || evidence.Duration != 2*time.Second || evidence.Size <= 0 {
		t.Fatal("real MP4 evidence invalid", evidence, err)
	}
}
