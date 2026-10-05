package recording

import (
	"errors"
	"testing"
)

func TestSubFailureReasonKeepsStageWithoutUpstreamCredentials(t *testing.T) {
	for _, stage := range []string{"inspect", "credentials", "add_proxy", "first_frame"} {
		got := subFailureReason(sourceFailure(stage, errors.New("rtsp://fixture:isolated-password@192.168.33.20/main")))
		if got != "sub_source_"+stage+"_unavailable" {
			t.Fatalf("unsafe or missing sub diagnostic: %s", got)
		}
	}
	if got := subFailureReason(errors.New("raw-upstream-content")); got != "sub_source_unavailable" {
		t.Fatal("untyped upstream error escaped")
	}
}
