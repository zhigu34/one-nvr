package notify

import (
	"bufio"
	"context"
	"encoding/base64"
	"io"
	"mime/quotedprintable"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhigu34/one-nvr/internal/id"
)

// fakeSMTP is a minimal SMTP server: enough of RFC 5321 for this adapter, and it
// records what it was told so the test can assert on the envelope rather than on
// the client's good intentions.
type fakeSMTP struct {
	listener net.Listener
	mu       sync.Mutex
	sessions int
	from     string
	rcpts    []string
	data     string
	authUser string
	authPass string
	// failRcpt refuses any recipient containing this substring, to model a
	// server that rejects one mailbox.
	failRcpt string
	// failAuth answers every AUTH attempt with a rejection.
	failAuth bool
}

func newFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &fakeSMTP{listener: listener}
	go server.serve()
	t.Cleanup(func() { listener.Close() })
	return server
}

func (f *fakeSMTP) addr() (string, int) {
	host, port, _ := net.SplitHostPort(f.listener.Addr().String())
	var n int
	for _, c := range port {
		n = n*10 + int(c-'0')
	}
	return host, n
}

func (f *fakeSMTP) serve() {
	for {
		conn, err := f.listener.Accept()
		if err != nil {
			return
		}
		go f.handle(conn)
	}
}

func (f *fakeSMTP) handle(conn net.Conn) {
	defer conn.Close()
	f.mu.Lock()
	f.sessions++
	f.mu.Unlock()
	reader := bufio.NewReader(conn)
	write := func(line string) { _, _ = conn.Write([]byte(line + "\r\n")) }
	write("220 fake ESMTP")
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		command := strings.TrimSpace(line)
		upper := strings.ToUpper(command)
		// The argument keeps its original case: addresses and base64 are
		// case-sensitive, and uppercasing them silently changes what the test
		// asserts on.
		argument := func(prefix string) string {
			return trimAddr(strings.TrimSpace(command[len(prefix):]))
		}
		switch {
		case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
			// No STARTTLS advertised: this server models a plain LAN relay, so the
			// adapter must proceed without upgrading.
			write("250-fake")
			write("250 AUTH PLAIN LOGIN")
		case strings.HasPrefix(upper, "AUTH"):
			// AUTH PLAIN carries the credentials base64-encoded inline.
			parts := strings.Fields(command)
			f.mu.Lock()
			if f.failAuth {
				f.mu.Unlock()
				write("535 5.7.8 authentication rejected")
				continue
			}
			if len(parts) >= 3 {
				f.authUser, f.authPass = decodePlain(parts[2])
			}
			f.mu.Unlock()
			write("235 2.7.0 accepted")
		case strings.HasPrefix(upper, "MAIL FROM:"):
			f.mu.Lock()
			f.from = argument("MAIL FROM:")
			f.mu.Unlock()
			write("250 2.1.0 ok")
		case strings.HasPrefix(upper, "RCPT TO:"):
			address := argument("RCPT TO:")
			f.mu.Lock()
			refuse := f.failRcpt != "" && strings.Contains(address, f.failRcpt)
			if !refuse {
				f.rcpts = append(f.rcpts, address)
			}
			f.mu.Unlock()
			if refuse {
				write("550 5.1.1 no such user")
				continue
			}
			write("250 2.1.5 ok")
		case upper == "DATA":
			write("354 end with <CRLF>.<CRLF>")
			var body strings.Builder
			for {
				line, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				if strings.TrimRight(line, "\r\n") == "." {
					break
				}
				body.WriteString(line)
			}
			f.mu.Lock()
			f.data = body.String()
			f.mu.Unlock()
			write("250 2.0.0 accepted")
		case upper == "QUIT":
			write("221 bye")
			return
		default:
			write("250 ok")
		}
	}
}

func trimAddr(raw string) string {
	return strings.Trim(raw, "<>")
}

// decodePlain pulls the user and password out of an AUTH PLAIN argument.
func decodePlain(encoded string) (string, string) {
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", ""
	}
	raw := string(decoded)
	parts := strings.Split(raw, "\x00")
	if len(parts) < 3 {
		return "", ""
	}
	return parts[1], parts[2]
}

func (f *fakeSMTP) snapshot() (from string, rcpts []string, data string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.from, append([]string(nil), f.rcpts...), f.data
}

func emailConfig(host string, port int) ChannelConfig {
	return ChannelConfig{
		ID:   "channel-1",
		Kind: "email",
		Name: "值班邮箱",
		Email: &SMTPConfig{
			SenderName:    "门岗值班",
			SenderAddress: "nvr@example.com",
			Host:          host,
			Port:          port,
			UseAuth:       false,
		},
		Recipients: []string{"a@example.com", "b@example.com"},
	}
}

func emailSampleMessage(t *testing.T) Message {
	t.Helper()
	delivery, err := id.New()
	if err != nil {
		t.Fatal(err)
	}
	return Message{
		DeliveryID:    delivery,
		SiteName:      "一号站",
		ChannelNo:     3,
		Category:      "person",
		CategoryLabel: "人",
		Severity:      "high",
		StartedAt:     time.Now().UTC(),
		StartedLocal:  "2026-10-22 09:15:00",
		Title:         "一号站 · CH03 后门 人 高",
		Body:          "CH03 后门 人 高 · 置信 88% · 2026-10-22 09:15:00（进行中）",
		SnapshotURL:   "https://nvr.example.com/api/v1/events/e1/snapshot",
		ReplayURL:     "https://nvr.example.com/recordings?channel=c3&at=2026-10-22T09:14:50Z",
	}
}

// One session carries the whole list: a server that takes the connection will
// take every RCPT, and opening a session per mailbox would multiply handshakes
// for no gain.
func TestEmailSendsToEveryRecipientInOneSession(t *testing.T) {
	server := newFakeSMTP(t)
	host, port := server.addr()
	adapter, err := newEmail(emailConfig(host, port))
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Send(context.Background(), emailSampleMessage(t)); err != nil {
		t.Fatal(err)
	}
	from, rcpts, data := server.snapshot()
	if from != "nvr@example.com" {
		t.Fatalf("envelope sender is %q, want the configured address", from)
	}
	if len(rcpts) != 2 || rcpts[0] != "a@example.com" || rcpts[1] != "b@example.com" {
		t.Fatalf("recipients are %v, want both configured mailboxes", rcpts)
	}
	if server.sessions != 1 {
		t.Fatalf("%d sessions for one send; the list belongs in one session", server.sessions)
	}
	// The body must carry the message text and both links; a mail that only says
	// "an event happened" makes the recipient go hunting. It is quoted-printable
	// on the wire so CJK survives a legacy client, so decode before asserting.
	decoded := decodeQP(t, data)
	for _, want := range []string{"CH03 后门", "snapshot", "recordings?channel=c3"} {
		if !strings.Contains(decoded, want) {
			t.Fatalf("body is missing %q:\n%s", want, decoded)
		}
	}
}

// Authentication is sent only when the operator asked for it, and the password
// comes from the sealed secret rather than the SMTP block.
func TestEmailAuthenticatesWhenConfigured(t *testing.T) {
	server := newFakeSMTP(t)
	host, port := server.addr()
	config := emailConfig(host, port)
	config.Email.UseAuth = true
	config.Email.Username = "relay-user"
	config.Secret = []byte("relay-pass")
	adapter, err := newEmail(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Send(context.Background(), emailSampleMessage(t)); err != nil {
		t.Fatal(err)
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.authUser != "relay-user" || server.authPass != "relay-pass" {
		t.Fatalf("AUTH carried %q/%q, want the configured username and sealed password", server.authUser, server.authPass)
	}
}

// A rejected credential is permanent: retrying locks the mailbox account.
func TestEmailRejectedAuthIsPermanent(t *testing.T) {
	server := newFakeSMTP(t)
	server.failAuth = true
	host, port := server.addr()
	config := emailConfig(host, port)
	config.Email.UseAuth = true
	config.Email.Username = "relay-user"
	config.Secret = []byte("wrong")
	adapter, err := newEmail(config)
	if err != nil {
		t.Fatal(err)
	}
	err = adapter.Send(context.Background(), emailSampleMessage(t))
	if err == nil {
		t.Fatal("a rejected credential was reported as delivered")
	}
	if !IsPermanent(err) {
		t.Fatalf("a rejected credential must not be retried: %v", err)
	}
}

// A refused address fails the send and says which one: the operator bound those
// mailboxes together and needs to know which is wrong.
func TestEmailRefusedRecipientNamesTheAddress(t *testing.T) {
	server := newFakeSMTP(t)
	server.failRcpt = "b@example.com"
	host, port := server.addr()
	adapter, err := newEmail(emailConfig(host, port))
	if err != nil {
		t.Fatal(err)
	}
	err = adapter.Send(context.Background(), emailSampleMessage(t))
	if err == nil {
		t.Fatal("a refused recipient was reported as delivered")
	}
	if !strings.Contains(err.Error(), "b@example.com") {
		t.Fatalf("the failure does not name the refused mailbox: %v", err)
	}
}

// An unreachable server is retryable: the message was never judged.
func TestEmailUnreachableIsRetryable(t *testing.T) {
	// A port nobody listens on. 1 is reserved and refused, not "closed-but-there".
	adapter, err := newEmail(emailConfig("127.0.0.1", 1))
	if err != nil {
		t.Fatal(err)
	}
	sendErr := adapter.Send(context.Background(), emailSampleMessage(t))
	if sendErr == nil {
		t.Fatal("an unreachable server was reported as delivered")
	}
	if IsPermanent(sendErr) {
		t.Fatalf("an unreachable server must be retried: %v", sendErr)
	}
}

// The configured parameters are the ones the operator recognised from an NVR's
// Email tab; a channel missing any of them cannot deliver and is refused at build
// time rather than at the first alarm.
func TestEmailRequiresItsParameters(t *testing.T) {
	base := emailConfig("smtp.example.com", 25)
	cases := map[string]func(*ChannelConfig){
		"no sender":     func(c *ChannelConfig) { c.Email.SenderAddress = "" },
		"no host":       func(c *ChannelConfig) { c.Email.Host = "" },
		"no port":       func(c *ChannelConfig) { c.Email.Port = 0 },
		"no recipients": func(c *ChannelConfig) { c.Recipients = nil },
		"no password":   func(c *ChannelConfig) { c.Email.UseAuth = true; c.Email.Username = "u" },
	}
	for name, breakIt := range cases {
		config := base
		emailCopy := *base.Email
		config.Email = &emailCopy
		breakIt(&config)
		if _, err := newEmail(config); err == nil {
			t.Fatalf("%s: an incomplete channel was accepted", name)
		}
	}
}

// decodeQP reads the quoted-printable body back. The wire form wraps long lines
// with a soft break and encodes non-ASCII, both of which must be undone before
// asserting on the text a recipient would read.
func decodeQP(t *testing.T, raw string) string {
	t.Helper()
	_, body, ok := strings.Cut(raw, "\r\n\r\n")
	if !ok {
		return raw
	}
	reader := quotedprintable.NewReader(strings.NewReader(body))
	decoded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return string(decoded)
}

// A relay may answer with anything, including a fragment of what it was sent.
// The error text is persisted on a delivery row and shown to an operator, so the
// password must not survive it.
func TestEmailErrorsNeverCarryThePassword(t *testing.T) {
	severed := redactCredentials("authentication rejected: 535 bad secret s3cr3t-value", "s3cr3t-value")
	if strings.Contains(severed, "s3cr3t-value") {
		t.Fatalf("the password survived redaction: %s", severed)
	}
	if !strings.Contains(severed, "<redacted>") {
		t.Fatalf("redaction left no trace of having happened: %s", severed)
	}
}
