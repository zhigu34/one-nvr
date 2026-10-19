package probe

import "testing"

// The extended fields come from ffmpeg's stream line. This pins the shapes seen
// on real cameras: HEVC Main with a declared bit rate, H.264 High without one,
// and 4:3 anamorphic SD.
func TestParseStreamLineFields(t *testing.T) {
	cases := []struct {
		line, profile, level, pix, sar string
		kbps                           int
	}{
		{`  Stream #0:0(und): Video: hevc (Main) (hev1 / 0x31766568), yuv420p(tv, progressive), 2560x1920 [SAR 1:1 DAR 4:3], 3446 kb/s, 20 fps, 20 tbr`, "Main", "", "yuv420p", "1:1", 3446},
		{`  Stream #0:0: Video: h264 (High), yuvj420p(pc, bt709), 1920x1080, 15 fps, 15 tbr`, "High", "", "yuvj420p", "", 0},
		{`  Stream #0:0: Video: h264 (Main), yuv420p, 704x576 [SAR 12:11 DAR 4:3], 512 kb/s, 15 fps`, "Main", "", "yuv420p", "12:11", 512},
	}
	for _, c := range cases {
		logs := []byte(c.line + "\n")
		if got := firstGroup(profile, logs); got != c.profile {
			t.Fatalf("profile = %q, want %q (%s)", got, c.profile, c.line)
		}
		if got := firstGroup(pixelFormat, logs); got != c.pix {
			t.Fatalf("pixel format = %q, want %q", got, c.pix)
		}
		if got := firstGroup(sampleAspect, logs); got != c.sar {
			t.Fatalf("SAR = %q, want %q", got, c.sar)
		}
		kbps := 0
		if m := videoRate.FindSubmatch(logs); len(m) == 2 {
			kbps = atoiForTest(string(m[1]))
		}
		if kbps != c.kbps {
			t.Fatalf("bitrate = %d, want %d (%s)", kbps, c.kbps, c.line)
		}
	}
}

func TestChannelCountReadsLayout(t *testing.T) {
	if channelCount("mono") != 1 || channelCount("stereo") != 2 || channelCount("5.1") != 0 {
		t.Fatal("channel layout mapping is wrong")
	}
}

func atoiForTest(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return n
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// The bit rate must come from the INPUT stream line.
//
// ffmpeg prints a second "Stream #0:0" line for its null sink, and that line
// carries a placeholder rate of 200 kb/s. Reading the first "kb/s" in the log
// picked it up: measured on a live site, every channel reported exactly 200 while
// the real streams ran at 243-3446 kbps — a wrong number stated with total
// confidence, which is worse than no number.
func TestBitrateIgnoresTheNullSinkOutputLine(t *testing.T) {
	logs := []byte(`  Stream #0:0[0x1](und): Video: hevc (Main) (hev1 / 0x31766568), yuvj420p(pc), 2560x1920, 3446 kb/s, 20 fps, 20 tbr
  Stream #0:0 -> #0:0 (hevc (native) -> wrapped_avframe (native))
  Stream #0:0(und): Video: wrapped_avframe, yuvj420p(pc), 2560x1920, q=2-31, 200 kb/s, 20 fps, 20 tbn
`)
	got := 0
	for _, line := range videoStreamLine.FindAll(logs, -1) {
		if bytesContains(line, nullSinkDecoder) {
			continue
		}
		if m := videoRate.FindSubmatch(line); len(m) == 2 {
			got = atoiForTest(string(m[1]))
			break
		}
	}
	if got != 3446 {
		t.Fatalf("bitrate = %d, want 3446 (the input line); 200 is the null sink's placeholder", got)
	}
}

// An input line without a container tag still parses: the tag form is not the
// only shape a camera's stream can take.
func TestBitrateParsesAnUntaggedInputLine(t *testing.T) {
	logs := []byte(`  Stream #0:0: Video: h264 (High), yuvj420p(pc), 1920x1080, 4096 kb/s, 25 fps
  Stream #0:0(und): Video: wrapped_avframe, yuvj420p(pc), 1920x1080, q=2-31, 200 kb/s, 25 fps
`)
	got := 0
	for _, line := range videoStreamLine.FindAll(logs, -1) {
		if bytesContains(line, nullSinkDecoder) {
			continue
		}
		if m := videoRate.FindSubmatch(line); len(m) == 2 {
			got = atoiForTest(string(m[1]))
			break
		}
	}
	if got != 4096 {
		t.Fatalf("bitrate = %d, want 4096", got)
	}
}

func bytesContains(line []byte, needle string) bool {
	return len(line) >= len(needle) && string(line) != "" && containsSub(string(line), needle)
}

func containsSub(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
