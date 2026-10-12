package events

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

// fakeBroker speaks just enough MQTT 3.1.1 to exercise the client: it accepts
// CONNECT, answers SUBACK, then hands over scripted PUBLISH frames. Frame bytes
// are read and written raw so a client bug in either direction fails here.
func fakeBroker(t *testing.T, script func(packetType byte, payload []byte) [][]byte) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		for {
			packetType, payload, err := readRawPacket(reader)
			if err != nil {
				return
			}
			switch packetType >> 4 {
			case 1: // CONNECT
				// Variable header: protocol name/level/flags/keepalive (10
				// bytes) then client id — CONNACK regardless of credentials,
				// the client only checks the return code.
				_, _ = conn.Write([]byte{0x20, 0x02, 0x00, 0x00})
			case 8: // SUBSCRIBE
				packetID := binary.BigEndian.Uint16(payload)
				_, _ = conn.Write([]byte{0x90, 0x03, byte(packetID >> 8), byte(packetID), 0x00})
				for _, frame := range script(packetType, payload) {
					_, _ = conn.Write(frame)
				}
			case 12: // PINGREQ
				_, _ = conn.Write([]byte{0xd0, 0x00})
			case 14: // DISCONNECT
				return
			}
		}
	}()
	return listener.Addr().String(), func() { listener.Close(); <-done }
}

func readRawPacket(reader *bufio.Reader) (byte, []byte, error) {
	header, err := reader.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	length := 0
	multiplier := 1
	for {
		b, err := reader.ReadByte()
		if err != nil {
			return 0, nil, err
		}
		length += int(b&0x7f) * multiplier
		if b&0x80 == 0 {
			break
		}
		multiplier *= 128
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(reader, body); err != nil {
		return 0, nil, err
	}
	return header, body, nil
}

func publishFrame(topic string, payload []byte) []byte {
	body := make([]byte, 2+len(topic)+len(payload))
	binary.BigEndian.PutUint16(body, uint16(len(topic)))
	copy(body[2:], topic)
	copy(body[2+len(topic):], payload)
	header := []byte{0x30}
	n := len(body)
	for {
		b := byte(n % 128)
		n /= 128
		if n > 0 {
			b |= 128
		}
		header = append(header, b)
		if n == 0 {
			break
		}
	}
	return append(header, body...)
}

func TestMQTTReceivePublishAfterSubscribe(t *testing.T) {
	address, stop := fakeBroker(t, func(_ byte, _ []byte) [][]byte {
		return [][]byte{publishFrame(TopicPrefix, []byte(`{"type":"new"}`))}
	})
	defer stop()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := Dial(ctx, address, "user", "pass", "test-client")
	if err != nil {
		t.Fatal("dial failed", err)
	}
	defer client.Close()
	if err := client.Subscribe(ctx, TopicPrefix); err != nil {
		t.Fatal("subscribe failed", err)
	}
	payload, err := client.Receive(ctx)
	if err != nil {
		t.Fatal("receive failed", err)
	}
	if string(payload) != `{"type":"new"}` {
		t.Fatal("payload mismatch", string(payload))
	}
}

func TestMQTTRejectsBadCredentialsAtDial(t *testing.T) {
	address, stop := fakeBroker(t, func(_ byte, _ []byte) [][]byte { return nil })
	defer stop()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := Dial(ctx, address, "", "pass", "test"); err != ErrMQTTUnavailable {
		t.Fatal("empty username must be refused", err)
	}
}

func TestMQTTKeepalivePing(t *testing.T) {
	address, stop := fakeBroker(t, func(_ byte, _ []byte) [][]byte { return nil })
	defer stop()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	client, err := Dial(ctx, address, "user", "pass", "test-client")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Subscribe(ctx, TopicPrefix); err != nil {
		t.Fatal(err)
	}
	// The fake broker answers PINGREQ; a client that dies on idle timeouts
	// would error here before the deadline instead of simply waiting.
	idleCtx, idleCancel := context.WithTimeout(ctx, 4*time.Second)
	defer idleCancel()
	if _, err := client.Receive(idleCtx); err == nil || err != idleCtx.Err() {
		t.Fatal("an idle stream must just keep waiting", err)
	}
}
