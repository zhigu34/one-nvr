package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/smtp"
	"net/textproto"
	"strings"
	"time"

	"github.com/zhigu34/one-nvr/internal/id"
)

// email carries a Message to a set of mailboxes over SMTP.
//
// The parameters follow what an NVR operator already knows from a Hikvision-style
// Email tab: sender name and address, SMTP server and port, an SSL switch, a
// server-authentication switch with username and password, and a recipient list.
// The names in the UI are the operator's; the semantics are SMTP's.
type email struct {
	senderName    string
	senderAddress string
	host          string
	port          int
	implicitTLS   bool
	useAuth       bool
	username      string
	password      string
	recipients    []string
	// subjectPrefix labels the mail in a shared inbox. It is the site name, which
	// the message already carries.
}

func newEmail(in ChannelConfig) (Channel, error) {
	cfg := in.Email
	if cfg == nil {
		return nil, fmt.Errorf("email channel has no SMTP settings")
	}
	if strings.TrimSpace(cfg.SenderAddress) == "" || strings.TrimSpace(cfg.Host) == "" {
		return nil, fmt.Errorf("email channel needs a sender address and an SMTP server")
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		return nil, fmt.Errorf("email channel needs a valid SMTP port")
	}
	if len(in.Recipients) == 0 {
		return nil, fmt.Errorf("email channel needs at least one recipient")
	}
	addresses := make([]string, 0, len(in.Recipients))
	for _, recipient := range in.Recipients {
		if strings.TrimSpace(recipient) == "" {
			continue
		}
		addresses = append(addresses, strings.TrimSpace(recipient))
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("email channel needs at least one recipient")
	}
	if cfg.UseAuth && (cfg.Username == "" || len(in.Secret) == 0) {
		return nil, fmt.Errorf("email channel authenticates but has no username or password")
	}
	password := ""
	if len(in.Secret) > 0 {
		password = string(in.Secret)
	}
	return &email{
		senderName:    strings.TrimSpace(cfg.SenderName),
		senderAddress: strings.TrimSpace(cfg.SenderAddress),
		host:          strings.TrimSpace(cfg.Host),
		port:          cfg.Port,
		implicitTLS:   cfg.TLS,
		useAuth:       cfg.UseAuth,
		username:      cfg.Username,
		password:      password,
		recipients:    addresses,
	}, nil
}

func (e *email) Kind() string { return "email" }

// Send delivers one message to every recipient in one SMTP session.
//
// One session for the whole list: a server that accepts the connection will take
// all the RCPTs, and opening a session per mailbox would multiply the handshakes
// (and the chances of tripping a rate limit) for no gain. A recipient the server
// rejects fails the whole delivery, which is honest — the operator bound those
// addresses together.
func (e *email) Send(ctx context.Context, m Message) error {
	body, err := e.compose(m)
	if err != nil {
		// Composing is our own work; a failure here is a defect, not a receiver.
		return &PermanentError{Reason: "email body could not be built: " + err.Error()}
	}
	from := e.senderAddress
	to := append([]string(nil), e.recipients...)
	client, err := e.dial(ctx)
	if err != nil {
		return err
	}
	defer client.Close()
	if e.useAuth {
		if ok, _ := client.Extension("AUTH"); ok {
			auth := smtp.PlainAuth("", e.username, e.password, e.host)
			if err := client.Auth(auth); err != nil {
				// A rejected credential is permanent: retrying locks the account.
				return &PermanentError{Reason: redactCredentials(smtpReason("authentication rejected", err), e.password)}
			}
		} else if e.useAuth {
			// The operator asked for authentication and the server cannot do it;
			// sending unauthenticated would be a silent downgrade.
			return &PermanentError{Reason: "server does not offer AUTH but authentication is enabled"}
		}
	}
	if err := client.Mail(from); err != nil {
		return &PermanentError{Reason: smtpReason("sender refused", err)}
	}
	for _, address := range to {
		if err := client.Rcpt(address); err != nil {
			return &PermanentError{Reason: smtpReason("recipient refused: "+address, err)}
		}
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := writer.Write(body); err != nil {
		writer.Close()
		return fmt.Errorf("smtp body: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("smtp body close: %w", err)
	}
	// Quit flushes the server's final acceptance; a failure here means the server
	// never confirmed, so the message cannot be claimed as delivered.
	if err := client.Quit(); err != nil {
		return fmt.Errorf("smtp quit: %w", err)
	}
	return nil
}

// dial opens the session, choosing implicit TLS or STARTTLS.
//
// The distinction is the operator's "启用SSL" checkbox: port 465 speaks TLS from
// the first byte, port 587 expects a plaintext greeting followed by STARTTLS. We
// attempt STARTTLS whenever the server advertises it even when the box is clear,
// because refusing to upgrade would send the password in the clear on a relay
// that offered better.
func (e *email) dial(ctx context.Context) (*smtp.Client, error) {
	address := net.JoinHostPort(e.host, fmt.Sprintf("%d", e.port))
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	var conn net.Conn
	var err error
	if e.implicitTLS {
		conn, err = tls.DialWithDialer(dialer, "tcp", address, &tls.Config{ServerName: e.host})
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", address)
	}
	if err != nil {
		// A transport failure says nothing about the message: retry.
		return nil, fmt.Errorf("smtp unreachable: %w", err)
	}
	client, err := smtp.NewClient(conn, e.host)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("smtp handshake: %w", err)
	}
	if !e.implicitTLS {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(&tls.Config{ServerName: e.host}); err != nil {
				client.Close()
				return nil, fmt.Errorf("smtp starttls: %w", err)
			}
		}
	}
	return client, nil
}

// compose renders the RFC 5322 document. The message layer already produced a
// Title and a Body, so this function is deliberately dumb: it lays those out and
// adds the machine-readable facts the operator may want to build on.
func (e *email) compose(m Message) ([]byte, error) {
	var buffer bytes.Buffer
	from := mime.QEncoding.Encode("utf-8", e.senderAddress)
	if e.senderName != "" {
		from = mime.QEncoding.Encode("utf-8", e.senderName) + " <" + e.senderAddress + ">"
	}
	headers := textproto.MIMEHeader{}
	headers.Set("From", from)
	headers.Set("To", strings.Join(e.recipients, ", "))
	// A notification is about one moment; threading by delivery id keeps the
	// retries of one notification in one thread and distinct events apart.
	headers.Set("Subject", mime.QEncoding.Encode("utf-8", m.Title))
	headers.Set("Date", time.Now().Format(time.RFC1123Z))
	headers.Set("Message-ID", "<"+string(m.DeliveryID)+"@"+e.host+">")
	// The delivery id travels in a header too: a receiver that archives mail can
	// deduplicate exactly as a webhook receiver does.
	headers.Set("X-OneNVR-Delivery", string(m.DeliveryID))
	if !m.StartedAt.IsZero() {
		headers.Set("X-OneNVR-Event", string(m.EventID))
	}

	// The body is plain text, quoted-printable so CJK survives a legacy client.
	// A snapshot is offered as a link rather than an attachment: the picture is
	// served behind the site's own authorization, and embedding it would put
	// footage in every recipient's mailbox forever.
	var content bytes.Buffer
	fmt.Fprintf(&content, "%s\r\n\r\n", m.Body)
	if m.SnapshotURL != "" {
		fmt.Fprintf(&content, "抓拍：%s\r\n", m.SnapshotURL)
	}
	if m.ReplayURL != "" {
		fmt.Fprintf(&content, "回放：%s\r\n", m.ReplayURL)
	}
	fmt.Fprintf(&content, "\r\n---\r\n投递 ID：%s\r\n", m.DeliveryID)

	headers.Set("Content-Type", "text/plain; charset=utf-8")
	headers.Set("Content-Transfer-Encoding", "quoted-printable")
	headers.Set("MIME-Version", "1.0")

	for key, values := range headers {
		for _, value := range values {
			fmt.Fprintf(&buffer, "%s: %s\r\n", key, value)
		}
	}
	buffer.WriteString("\r\n")
	writer := quotedprintable.NewWriter(&buffer)
	if _, err := writer.Write(content.Bytes()); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// smtpReason keeps the server's own words. "451 4.7.1 try again later" is
// actionable; "failed" is not.
func smtpReason(prefix string, err error) string {
	text := strings.TrimSpace(err.Error())
	if len(text) > 200 {
		text = text[:200]
	}
	return prefix + ": " + text
}

// SMTPConfig is the stored set of parameters, read back from the channel row.
// It lives here rather than in the service so an adapter is constructed from one
// value, exactly like a webhook's URL.
type SMTPConfig struct {
	SenderName    string `json:"sender_name"`
	SenderAddress string `json:"sender_address"`
	Host          string `json:"smtp_host"`
	Port          int    `json:"smtp_port"`
	TLS           bool   `json:"smtp_tls"`
	UseAuth       bool   `json:"smtp_auth"`
	Username      string `json:"username"`
}

// Recipient is one mailbox on an email channel.
type Recipient struct {
	ID        id.ID           `json:"id"`
	Name      string          `json:"name"`
	Address   string          `json:"address"`
	LastCheck json.RawMessage `json:"last_check,omitempty"`
}

// redactCredentials removes anything that looks like the password from text that
// is about to be stored on a delivery row and shown to an operator.
//
// net/smtp does not echo the credential itself, but a relay is free to answer
// with whatever it likes — including a fragment of what was sent — and the error
// string becomes a persisted column. Scrubbing the one value we know is cheap;
// discovering it in a delivery row later is not.
func redactCredentials(text, password string) string {
	if password == "" {
		return text
	}
	return strings.ReplaceAll(text, password, "<redacted>")
}
