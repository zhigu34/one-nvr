// Package events owns the normalized detection-event index: MQTT ingestion
// from Frigate, the handling trail, snapshots and the 90-day expiry.
package events

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"time"
)

var (
	ErrMQTTUnavailable = errors.New("event_mqtt_unavailable")
	errMQTTProtocol    = errors.New("event_mqtt_protocol")
)

// maxPacket bounds one MQTT frame. Frigate's event and update payloads are a
// few kilobytes; anything larger is malformed or hostile, and buffering it
// would be the whole attack.
const maxPacket = 256 << 10

const keepAlive = 30 * time.Second

// Client is the narrow MQTT 3.1.1 subset the event stream needs: CONNECT,
// SUBSCRIBE, QoS-0 PUBLISH reception and PINGREQ keepalive. It is deliberately
// not a general client library — one topic, one consumer, bounded frames.
type Client struct {
	conn    net.Conn
	reader  *bufio.Reader
	nextID  uint16
	lastPkt time.Time
	// pending holds PUBLISH frames that arrived before the SUBACK; Receive
	// drains them first so a retained message is never dropped.
	pending []pendingPublish
}

// Dial performs CONNECT/CONNACK against the broker.
func Dial(ctx context.Context, address, username, password, clientID string) (*Client, error) {
	if username == "" || password == "" || len(username) > 128 || len(password) > 256 || len(clientID) > 64 {
		return nil, ErrMQTTUnavailable
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, ErrMQTTUnavailable
	}
	client := &Client{conn: conn, reader: bufio.NewReaderSize(conn, 16<<10), nextID: 1, lastPkt: time.Now()}
	body := []byte{0, 4, 'M', 'Q', 'T', 'T', 4, 0xc2, 0, byte(keepAlive / time.Second)}
	for _, field := range []string{clientID, username, password} {
		if len(field) > 65535 {
			conn.Close()
			return nil, ErrMQTTUnavailable
		}
		raw := make([]byte, len(field)+2)
		binary.BigEndian.PutUint16(raw, uint16(len(field)))
		copy(raw[2:], field)
		body = append(body, raw...)
	}
	if err := client.writePacket(0x10, body); err != nil {
		conn.Close()
		return nil, ErrMQTTUnavailable
	}
	deadline, _ := ctx.Deadline()
	if deadline.IsZero() {
		deadline = time.Now().Add(5 * time.Second)
	}
	_ = conn.SetReadDeadline(deadline)
	packetType, payload, err := client.readPacket()
	if err != nil || packetType != 0x20 || len(payload) != 2 || payload[1] != 0 {
		conn.Close()
		return nil, ErrMQTTUnavailable
	}
	return client, nil
}

// Subscribe registers one topic filter at QoS 0 and waits for SUBACK.
func (c *Client) Subscribe(ctx context.Context, topic string) error {
	if len(topic) == 0 || len(topic) > 256 {
		return ErrMQTTUnavailable
	}
	packetID := c.takeID()
	body := []byte{byte(packetID >> 8), byte(packetID)}
	body = append(body, []byte(topic)...)
	body = append(body, 0x00)
	if err := c.writePacket(0x82, body); err != nil {
		return ErrMQTTUnavailable
	}
	deadline, _ := ctx.Deadline()
	if deadline.IsZero() {
		deadline = time.Now().Add(5 * time.Second)
	}
	_ = c.conn.SetReadDeadline(deadline)
	for {
		packetType, payload, err := c.readPacket()
		if err != nil {
			return ErrMQTTUnavailable
		}
		if packetType == 0x90 {
			if len(payload) < 3 || binary.BigEndian.Uint16(payload) != packetID || payload[2] == 0x80 {
				return errMQTTProtocol
			}
			return nil
		}
		// Anything else arriving before the SUBACK (a retained publish, say)
		// is delivered by the next Receive call instead of being dropped.
		if packetType>>4 == 3 {
			c.pending = append(c.pending, pendingPublish{payload: payload})
			continue
		}
	}
}

type pendingPublish struct{ payload []byte }

// Receive blocks for the next PUBLISH body, keeping the connection alive with
// PINGREQ while idle. A clean context cancellation returns ctx.Err().
func (c *Client) Receive(ctx context.Context) ([]byte, error) {
	for {
		if queued := c.popPending(); queued != nil {
			return queued, nil
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		_ = c.conn.SetReadDeadline(time.Now().Add(keepAlive / 2))
		packetType, payload, err := c.readPacket()
		if err != nil {
			if isTimeout(err) {
				// Idle: a PINGREQ keeps the broker from dropping the session.
				if time.Since(c.lastPkt) >= keepAlive/2 {
					if pingErr := c.writePacket(0xc0, nil); pingErr != nil {
						return nil, ErrMQTTUnavailable
					}
					c.lastPkt = time.Now()
				}
				continue
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, ErrMQTTUnavailable
		}
		c.lastPkt = time.Now()
		switch packetType >> 4 {
		case 3: // PUBLISH (QoS 0 on this subscription)
			if len(payload) < 3 {
				return nil, errMQTTProtocol
			}
			topicLen := binary.BigEndian.Uint16(payload)
			if int(topicLen)+2 > len(payload) {
				return nil, errMQTTProtocol
			}
			return payload[2+int(topicLen):], nil
		case 13: // PINGRESP
			continue
		default:
			continue
		}
	}
}

// Publish sends one QoS-0 message. The detection publisher uses it for the
// Frigate reload command; the event subscriber never publishes.
func (c *Client) Publish(ctx context.Context, topic string, payload []byte) error {
	if len(topic) == 0 || len(topic) > 256 || len(payload) > maxPacket {
		return ErrMQTTUnavailable
	}
	body := make([]byte, 2+len(topic)+len(payload))
	binary.BigEndian.PutUint16(body, uint16(len(topic)))
	copy(body[2:], topic)
	copy(body[2+len(topic):], payload)
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.writePacket(0x30, body)
}

func (c *Client) Close() error {
	_, _ = c.conn.Write([]byte{0xe0, 0x00})
	return c.conn.Close()
}

func (c *Client) takeID() uint16 {
	c.nextID++
	if c.nextID == 0 {
		c.nextID = 1
	}
	return c.nextID
}

func (c *Client) popPending() []byte {
	if len(c.pending) == 0 {
		return nil
	}
	item := c.pending[0]
	c.pending = c.pending[1:]
	return item.payload
}

func (c *Client) writePacket(first byte, body []byte) error {
	frame := []byte{first}
	n := len(body)
	for {
		b := byte(n % 128)
		n /= 128
		if n > 0 {
			b |= 128
		}
		frame = append(frame, b)
		if n == 0 {
			break
		}
	}
	frame = append(frame, body...)
	_ = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err := c.conn.Write(frame)
	return err
}

func (c *Client) readPacket() (byte, []byte, error) {
	header, err := c.reader.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	length := 0
	multiplier := 1
	for range 4 {
		b, err := c.reader.ReadByte()
		if err != nil {
			return 0, nil, err
		}
		length += int(b&0x7f) * multiplier
		if b&0x80 == 0 {
			break
		}
		multiplier *= 128
	}
	if length < 0 || length > maxPacket {
		return 0, nil, errMQTTProtocol
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(c.reader, body); err != nil {
		return 0, nil, err
	}
	return header, body, nil
}

func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}
