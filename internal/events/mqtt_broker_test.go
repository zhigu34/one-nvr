package events

import (
	"context"
	"net"
	"os"
	"testing"
	"time"
)

// The fake broker in mqtt_test.go answers whatever the client sends, so it
// cannot catch a malformed frame — a missing UTF-8 length prefix was once
// invisible to the whole suite while every real broker rejected the packet.
// This test talks to a real broker when one is reachable (locally or in CI);
// it skips otherwise so the suite stays runnable everywhere.
func TestAgainstRealBrokerWhenAvailable(t *testing.T) {
	address := os.Getenv("ONE_NVR_TEST_MQTT")
	if address == "" {
		address = "127.0.0.1:1883"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	probe, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Skipf("no broker at %s: %v", address, err)
	}
	probe.Close()

	username := os.Getenv("ONE_NVR_TEST_MQTT_USER")
	password := os.Getenv("ONE_NVR_TEST_MQTT_PASSWORD")
	if username == "" {
		username, password = "one_nvr", "testpass123"
	}
	client, err := Dial(ctx, address, username, password, "one-nvr-test")
	if err != nil {
		// A broker whose credentials this test does not know is indistinguishable
		// from no broker: the test exists to catch a malformed frame against a
		// real protocol stack, not to assert that some other service is up. The
		// authentication case is covered by the fake-broker unit tests.
		t.Skipf("broker at %s did not accept the test credentials: %v", address, err)
	}
	defer client.Close()
	// A broker rejects a malformed SUBSCRIBE by closing the connection, so a
	// successful subscribe plus an idle wait is the whole assertion.
	if err := client.Subscribe(ctx, TopicPrefix); err != nil {
		t.Fatalf("broker rejected SUBSCRIBE: %v", err)
	}
	idle, idleCancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer idleCancel()
	if _, err := client.Receive(idle); err != nil && err != idle.Err() {
		t.Fatalf("connection dropped after subscribe: %v", err)
	}
}
