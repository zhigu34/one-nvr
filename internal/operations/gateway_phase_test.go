package operations

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestGatewayPhaseWaitsForLifecycleBudget(t *testing.T) {
	raw, err := os.ReadFile("../../deploy/production/test-gateway.sh")
	if err != nil {
		t.Fatal(err)
	}
	code := string(raw)
	start, end := strings.Index(code, "phase() {"), strings.Index(code, "mark() {")
	if start < 0 || end < start {
		t.Fatal("gateway phase helper missing")
	}
	program := `set -Eeuo pipefail
phase_poll_count=0
docker(){ if [[ ${4:-} == test ]]; then phase_poll_count=$((phase_poll_count+1)); [[ $phase_poll_count -gt 120 ]]; else return 0; fi; }
sleep(){ :; }
kill(){ return 0; }
compose=(docker)
runner_pid=1
` + code[start:end] + `phase lifecycle-done`
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "bash", "-c", program).CombinedOutput()
	if err != nil {
		t.Fatal("gateway phase discarded a still-running allowed lifecycle", err, string(out))
	}
}
