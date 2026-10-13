package bridge

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
)

// Transport submits a built message. The bridge's job ends at the mail
// server's 250: DKIM, queueing, retries towards the recipient's MX and the
// relay through an outbound provider are the mail server's.
type Transport interface {
	Send(ctx context.Context, from string, to []string, msg []byte) error
}

// SubmitError is a refusal from the mail server. Permanent ones (5xx) end
// the send; temporary ones are retried.
type SubmitError struct {
	Code      int
	Message   string
	Permanent bool
}

func (e *SubmitError) Error() string { return fmt.Sprintf("mail server %d: %s", e.Code, e.Message) }

// NewTransport builds the configured transport.
func NewTransport(cfg Config) (Transport, error) {
	switch cfg.Transport {
	case "smtp":
		return &smtpTransport{cfg: cfg}, nil
	case "log":
		if err := os.MkdirAll(cfg.LogDir, 0o700); err != nil {
			return nil, err
		}
		return &LogTransport{Dir: cfg.LogDir}, nil
	}
	return nil, fmt.Errorf("unknown transport %q", cfg.Transport)
}

type smtpTransport struct{ cfg Config }

func (t *smtpTransport) Send(ctx context.Context, from string, to []string, msg []byte) error {
	addr := net.JoinHostPort(t.cfg.SMTPHost, strconv.Itoa(t.cfg.SMTPPort))
	tlsConfig := &tls.Config{ServerName: t.cfg.SMTPHost, MinVersion: tls.VersionTLS12, InsecureSkipVerify: t.cfg.SMTPInsecureSkipVerify} //nolint:gosec // opt-in for private test servers
	dialer := &net.Dialer{Timeout: 20 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(2 * time.Minute))
	}
	var c *smtp.Client
	switch t.cfg.SMTPTLS {
	case "tls":
		c = smtp.NewClient(tls.Client(conn, tlsConfig))
	case "starttls":
		c, err = smtp.NewClientStartTLS(conn, tlsConfig)
		if err != nil {
			conn.Close()
			return classify(err)
		}
	default:
		c = smtp.NewClient(conn)
	}
	defer c.Close()
	// STARTTLS already said EHLO (go-smtp does it to learn the extension);
	// otherwise introduce ourselves by the domain we mint Message-IDs on.
	if t.cfg.SMTPTLS != "starttls" {
		if err := c.Hello(t.cfg.MessageIDDomain); err != nil {
			return classify(err)
		}
	}
	if t.cfg.SMTPUsername != "" {
		if err := c.Auth(sasl.NewPlainClient("", t.cfg.SMTPUsername, t.cfg.SMTPPassword)); err != nil {
			return classify(err)
		}
	}
	if err := c.SendMail(from, to, bytes.NewReader(msg)); err != nil {
		return classify(err)
	}
	return c.Quit()
}

// classify turns protocol replies into SubmitErrors; anything else
// (network) stays as is and counts as temporary.
func classify(err error) error {
	var se *smtp.SMTPError
	if errors.As(err, &se) {
		return &SubmitError{Code: se.Code, Message: se.Message, Permanent: se.Code >= 500}
	}
	return err
}

// LogTransport writes each message to a directory (development and tests).
type LogTransport struct {
	Dir  string
	Fail func(to []string) error
}

func (t *LogTransport) Send(_ context.Context, from string, to []string, msg []byte) error {
	if t.Fail != nil {
		if err := t.Fail(to); err != nil {
			return err
		}
	}
	name := fmt.Sprintf("%d-%s.eml", time.Now().UnixNano(), NewMessageID("x")[:12])
	envelope := fmt.Sprintf("X-Envelope-From: %s\r\nX-Envelope-To: %v\r\n", from, to)
	return os.WriteFile(filepath.Join(t.Dir, name), append([]byte(envelope), msg...), 0o600)
}
