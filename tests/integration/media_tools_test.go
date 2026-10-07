package integration

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func mediaTestTool(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err == nil {
		return path
	}
	if os.Getenv("ONE_NVR_REQUIRE_MEDIA_TOOLS") == "yes" {
		t.Fatalf("required media test tool unavailable: %s", name)
	}
	t.Skip("native media tools unavailable; complete media regressions require FFmpeg and FFprobe")
	return ""
}

func TestRequiredMediaToolsFailClosed(t *testing.T) {
	child := exec.Command(os.Args[0], "-test.run=^TestRecordingPublishDuplicateAndHistory$", "-test.v")
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "PATH=") && !strings.HasPrefix(entry, "ONE_NVR_REQUIRE_MEDIA_TOOLS=") {
			child.Env = append(child.Env, entry)
		}
	}
	child.Env = append(child.Env, "PATH="+t.TempDir(), "ONE_NVR_REQUIRE_MEDIA_TOOLS=yes")
	out, err := child.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "required media test tool unavailable") {
		t.Fatal("mandatory media regression silently skipped without FFmpeg", err, string(out))
	}
}
