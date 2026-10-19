package probe

import "testing"

// The extended fields come from ffmpeg's stream line. This pins the shapes seen
// on real cameras: HEVC Main with a declared bit rate, H.264 High without one,
// and 4:3 anamorphic SD.
func TestParseStreamLineFields(t *testing.T) {
	cases := []struct{ line, profile, level, pix, sar string; kbps int }{
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
