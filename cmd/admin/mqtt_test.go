package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestMQTTHashDoesNotHashExistingHash(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "passwords")
	count := filepath.Join(dir, "calls")
	os.WriteFile(file, []byte("one_nvr:test-only\n"), 0600)
	spy := filepath.Join(dir, "mosquitto_passwd")
	os.WriteFile(spy, []byte("#!/bin/sh\nprintf 'one_nvr:$7$test\\n' > \"$2\"\nprintf x >> \"$MQTT_SPY_COUNT\"\n"), 0700)
	for i := 0; i < 2; i++ {
		cmd := exec.Command("sh", "../../deploy/production/hash-mqtt.sh", file)
		cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "MQTT_SPY_COUNT="+count)
		if out, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("hash script: %s %v", out, e)
		}
	}
	calls, _ := os.ReadFile(count)
	if len(calls) != 1 {
		t.Fatalf("hasher called %d times, expected one", len(calls))
	}
}
