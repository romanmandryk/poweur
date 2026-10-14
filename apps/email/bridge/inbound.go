package bridge

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"

	"github.com/poweur/identity"
)

// envelopeBudget is how much JSON plaintext an `email.message` may carry so
// that, encrypted and base64-encoded inside the envelope, it stays under the
// relay's 512 KB cap with room for the envelope itself.
const envelopeBudget = 360 * 1024

// Inbound delivers accepted mail to Poweur inboxes.
type Inbound struct {
	cfg     Config
	dir     *Directory
	relay   *Relay
	store   *Store
	blobs   *Blobs
	log     *slog.Logger
	metrics *Metrics
	// authenticator checks SPF/DKIM/DMARC in the direct inbound mode.
	authenticator Authenticator
	// onBounce is told about delivery reports for the bridge's own sends.
	onBounce func(ctx context.Context, send SendRecord, b identity.EmailBounce)
}

// Delivery errors in SMTP terms.
var (
	errTempFail = &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 3, 0}, Message: "temporary failure delivering to Poweur, try again later"}
	errNoUser   = &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}, Message: "no such mailbox"}
	errNoRelay  = &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 7, 1}, Message: "relaying denied"}
	errOverDay  = &smtp.SMTPError{Code: 452, EnhancedCode: smtp.EnhancedCode{4, 2, 2}, Message: "mailbox has received its daily maximum, try again later"}
	errRefused  = &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 7, 1}, Message: "the recipient does not accept this mail"}
)

// rcptError maps a directory answer to an SMTP reply.
func rcptError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNoMailbox):
		return errNoUser
	case errors.Is(err, ErrNotServed):
		return errNoRelay
	default:
		return errTempFail
	}
}

// Check is the RCPT-time decision shared by the hook and the LMTP listener.
func (in *Inbound) Check(ctx context.Context, address string) (Recipient, error) {
	r, err := in.dir.Lookup(ctx, address)
	if err != nil {
		return r, rcptError(err)
	}
	if in.cfg.InDailyPerRecipient > 0 {
		if n, err := in.store.Peek(ctx, "in:"+r.Identity); err == nil && n >= in.cfg.InDailyPerRecipient {
			return r, errOverDay
		}
	}
	return r, nil
}

// Deliver hands one parsed message to one recipient. A nil error means it is
// in their inbox (or was already); an *smtp.SMTPError says what to tell the
// mail server.
func (in *Inbound) Deliver(ctx context.Context, raw []byte, parsed Parsed, envelopeTo string, r Recipient) error {
	digest := sha256.Sum256(raw)
	key := hex.EncodeToString(digest[:]) + "\n" + r.Identity
	if seen, err := in.store.SeenInbound(ctx, key); err != nil {
		return errTempFail
	} else if seen {
		in.metrics.Inc("email_inbound_total", "result", "duplicate")
		return nil
	}
	encKey, err := in.relay.EncryptionKey(ctx, r.Identity)
	if err != nil {
		in.log.Warn("inbound: no encryption key", "err", err)
		return errTempFail
	}

	msg := parsed.Email
	if in.filtered(ctx, r.Identity, msg) {
		// Accepted and dropped: no bounce, and the sender learns nothing.
		in.metrics.Inc("email_inbound_total", "result", "filtered")
		return nil
	}
	msg.EnvelopeTo = strings.ToLower(envelopeTo)
	msg.Tag = r.Tag
	if msg.Bounce != nil && msg.Bounce.MessageID != "" {
		if send, ok, _ := in.store.SendByMessageID(ctx, msg.Bounce.MessageID); ok && strings.EqualFold(send.Sender, r.Identity) {
			msg.Bounce.SendID = send.SendID
			if in.onBounce != nil {
				in.onBounce(ctx, send, *msg.Bounce)
			}
		}
	}
	payload, err := in.fit(ctx, msg, parsed.Attachments, raw, r.Identity, encKey)
	if err != nil {
		in.log.Warn("inbound: could not prepare payload", "err", err)
		return errTempFail
	}

	id := "msg_mail_" + hex.EncodeToString(digest[:8]) + hex.EncodeToString(sha256Short(r.Identity))
	if _, err := in.relay.Send(ctx, r.Identity, identity.MsgTypeEmailMessage, payload, id); err != nil {
		var re *RelayError
		if errors.As(err, &re) && re.Status == 409 {
			// Already delivered under this id before a crash lost the mark.
		} else if IsPermanent(err) {
			// The recipient's relay refused it — their policy changed since
			// RCPT time. Forget the cached opt-in; the mail server bounces.
			in.dir.Forget(r.Identity)
			in.metrics.Inc("email_inbound_total", "result", "refused")
			in.log.Info("inbound: relay refused", "status", re.Status, "code", re.Code)
			return errRefused
		} else {
			in.metrics.Inc("email_inbound_total", "result", "deferred")
			in.log.Warn("inbound: relay unavailable", "err", err)
			return errTempFail
		}
	}
	_ = in.store.MarkInbound(ctx, key)
	_, _ = in.store.Count(ctx, "in:"+r.Identity, 1)
	if msg.From.Address != "" {
		_ = in.store.Remember(ctx, r.Identity, msg.From.Address)
	}
	in.metrics.Inc("email_inbound_total", "result", "delivered")
	return nil
}

// filtered applies the recipient's email.settings: a blocked sender (by
// address or domain) never gets through; in "contacts" mode only allowed
// senders, people the user has emailed, replies to the user's own mail and
// delivery reports do. No settings: everyone may write.
func (in *Inbound) filtered(ctx context.Context, id string, msg identity.EmailMessage) bool {
	f, ok, err := in.store.Filters(ctx, id)
	if err != nil || !ok {
		return false
	}
	from := msg.From.Address
	if from != "" && in.store.Blocks(id, f, from) {
		return true
	}
	if !f.Contacts || msg.Bounce != nil {
		return false
	}
	if from != "" {
		if in.store.Allows(id, f, from) {
			return false
		}
		if sent, _ := in.store.SentTo(ctx, id, from); sent {
			return false
		}
	}
	for _, ref := range append([]string{msg.InReplyTo}, msg.References...) {
		if ref == "" {
			continue
		}
		if send, ok, _ := in.store.SendByMessageID(ctx, ref); ok && strings.EqualFold(send.Sender, id) {
			return false
		}
	}
	return true
}

func sha256Short(s string) []byte {
	sum := sha256.Sum256([]byte(strings.ToLower(s)))
	return sum[:4]
}

// fit builds the payload within the envelope budget: small attachments
// inline, larger ones (and the original message) as blobs sealed to the
// recipient, and text/HTML shortened only when nothing else is left to move.
func (in *Inbound) fit(ctx context.Context, msg identity.EmailMessage, atts []ParsedAttachment, raw []byte, recipient string, encKey []byte) ([]byte, error) {
	msg.Attachments = nil
	if in.blobs.Enabled() {
		ref, err := in.blobs.Put(ctx, recipient, encKey, raw)
		if err != nil {
			return nil, err
		}
		msg.Raw = &ref
	}
	base, _ := json.Marshal(msg)
	used := len(base)
	// Inline the smallest attachments first: an inline image a mail renders
	// is usually small, and every one inlined is one fetch the client saves.
	order := make([]int, len(atts))
	for i := range order {
		order[i] = i
	}
	for i := 1; i < len(order); i++ {
		for j := i; j > 0 && len(atts[order[j]].Data) < len(atts[order[j-1]].Data); j-- {
			order[j], order[j-1] = order[j-1], order[j]
		}
	}
	placed := make([]identity.EmailAttachment, len(atts))
	for _, i := range order {
		a := atts[i]
		meta := a.Meta
		inlineCost := base64.StdEncoding.EncodedLen(len(a.Data)) + 200
		if used+inlineCost <= envelopeBudget {
			meta.Data = base64.StdEncoding.EncodeToString(a.Data)
			used += inlineCost
		} else if in.blobs.Enabled() {
			ref, err := in.blobs.Put(ctx, recipient, encKey, a.Data)
			if err != nil {
				return nil, err
			}
			meta.Blob = &ref
			used += 400
		} else {
			msg.Truncated = true
			used += 200
		}
		placed[i] = meta
	}
	msg.Attachments = placed
	out, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	for len(out) > envelopeBudget {
		// Still too big: the bodies themselves. Halve the larger one; the
		// original (Raw) keeps every byte.
		msg.Truncated = true
		if len(msg.HTML) >= len(msg.Text) && msg.HTML != "" {
			msg.HTML = ""
		} else if msg.Text != "" {
			msg.Text = msg.Text[:len(msg.Text)/2]
		} else {
			msg.Attachments = nil
		}
		if out, err = json.Marshal(msg); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// --- Listeners -----------------------------------------------------------
//
// Two inbound modes (EMAIL_INBOUND):
//
//   - lmtp: behind a mail server (Stalwart). The mail server owns port 25,
//     authentication checks, spam scoring and the queue; it asks the RCPT
//     hook and delivers here over LMTP with AUTH.
//   - smtp: the bridge is the MX itself — the setup that needs nothing but
//     an MX record. It listens on :25, takes no AUTH, checks SPF, DKIM and
//     DMARC itself, and limits connections per IP.

type listenerBackend struct {
	in     *Inbound
	direct bool
	auth   Authenticator

	mu     sync.Mutex
	active map[string]int
	recent map[string][]time.Time
}

// Per-IP limits for the direct mode (a mail server in front has its own).
const (
	maxSessionsPerIP  = 5
	maxSessionsPerMin = 30
)

// NewLMTPServer builds the listener a mail server delivers to: LMTP
// (per-recipient replies), AUTH PLAIN with the configured credential — over
// TLS unless EMAIL_LMTP_INSECURE says the network is private — from an
// allowed address.
func (in *Inbound) NewLMTPServer() (*smtp.Server, error) {
	s := smtp.NewServer(&listenerBackend{in: in})
	s.LMTP = true
	s.Domain = in.cfg.LMTPHostname
	s.AllowInsecureAuth = in.cfg.LMTPInsecure
	return in.finishServer(s, in.cfg.LMTPTLSCert, in.cfg.LMTPTLSKey)
}

// NewSMTPServer builds the public MX listener for the direct mode.
func (in *Inbound) NewSMTPServer() (*smtp.Server, error) {
	s := smtp.NewServer(&listenerBackend{in: in, direct: true, auth: in.authenticator, active: map[string]int{}, recent: map[string][]time.Time{}})
	s.Domain = in.cfg.MXHostname
	return in.finishServer(s, in.cfg.SMTPTLSCert, in.cfg.SMTPTLSKey)
}

func (in *Inbound) finishServer(s *smtp.Server, certFile, keyFile string) (*smtp.Server, error) {
	s.MaxMessageBytes = in.cfg.MaxMessageBytes
	s.MaxRecipients = 50
	s.ReadTimeout = 2 * time.Minute
	s.WriteTimeout = 2 * time.Minute
	s.ErrorLog = slogLogger{in.log}
	if certFile != "" {
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("TLS certificate: %w", err)
		}
		s.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	}
	return s, nil
}

type slogLogger struct{ l *slog.Logger }

func (s slogLogger) Printf(format string, v ...interface{}) { s.l.Debug(fmt.Sprintf(format, v...)) }
func (s slogLogger) Println(v ...interface{})               { s.l.Debug(fmt.Sprint(v...)) }

func remoteIP(c *smtp.Conn) net.IP {
	host, _, _ := net.SplitHostPort(c.Conn().RemoteAddr().String())
	return net.ParseIP(host)
}

func (b *listenerBackend) NewSession(c *smtp.Conn) (smtp.Session, error) {
	ip := remoteIP(c)
	if b.direct {
		release, err := b.admit(ip)
		if err != nil {
			b.in.metrics.Inc("email_smtp_sessions_total", "result", "limited")
			return nil, err
		}
		return &directSession{inner: &lmtpSession{in: b.in, authed: true}, backend: b, conn: c, ip: ip, release: release}, nil
	}
	if len(b.in.cfg.LMTPAllow) > 0 {
		allowed := false
		for _, n := range b.in.cfg.LMTPAllow {
			if ip != nil && n.Contains(ip) {
				allowed = true
				break
			}
		}
		if !allowed {
			b.in.metrics.Inc("email_lmtp_sessions_total", "result", "denied")
			return nil, &smtp.SMTPError{Code: 554, EnhancedCode: smtp.EnhancedCode{5, 7, 1}, Message: "not allowed"}
		}
	}
	return &lmtpSession{in: b.in}, nil
}

// admit applies the per-IP limits and returns the release for the session.
func (b *listenerBackend) admit(ip net.IP) (func(), error) {
	key := "?"
	if ip != nil {
		key = ip.String()
		if ip.To4() == nil && len(ip) == net.IPv6len {
			key = ip.Mask(net.CIDRMask(64, 128)).String() // one /64 is one sender
		}
	}
	now := time.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	kept := b.recent[key][:0]
	for _, t := range b.recent[key] {
		if now.Sub(t) < time.Minute {
			kept = append(kept, t)
		}
	}
	if b.active[key] >= maxSessionsPerIP || len(kept) >= maxSessionsPerMin {
		b.recent[key] = kept
		return nil, &smtp.SMTPError{Code: 421, EnhancedCode: smtp.EnhancedCode{4, 7, 0}, Message: "too many connections, try again later"}
	}
	b.recent[key] = append(kept, now)
	b.active[key]++
	if len(b.recent) > 50000 {
		b.recent = map[string][]time.Time{}
	}
	return func() {
		b.mu.Lock()
		b.active[key]--
		if b.active[key] <= 0 {
			delete(b.active, key)
		}
		b.mu.Unlock()
	}, nil
}

type lmtpSession struct {
	in     *Inbound
	authed bool
	from   string
	rcpts  []Recipient
	to     []string
}

func (s *lmtpSession) AuthMechanisms() []string { return []string{sasl.Plain} }

func (s *lmtpSession) Auth(mech string) (sasl.Server, error) {
	return sasl.NewPlainServer(func(_, username, password string) error {
		userOK := subtle.ConstantTimeCompare([]byte(username), []byte(s.in.cfg.LMTPUsername)) == 1
		passOK := subtle.ConstantTimeCompare([]byte(password), []byte(s.in.cfg.LMTPPassword)) == 1
		if !userOK || !passOK {
			s.in.metrics.Inc("email_lmtp_sessions_total", "result", "bad_auth")
			return &smtp.SMTPError{Code: 535, EnhancedCode: smtp.EnhancedCode{5, 7, 8}, Message: "authentication failed"}
		}
		s.authed = true
		return nil
	}), nil
}

func (s *lmtpSession) Mail(from string, _ *smtp.MailOptions) error {
	if !s.authed {
		return &smtp.SMTPError{Code: 530, EnhancedCode: smtp.EnhancedCode{5, 7, 0}, Message: "authentication required"}
	}
	s.from = from
	return nil
}

func (s *lmtpSession) Rcpt(to string, _ *smtp.RcptOptions) error {
	if !s.authed {
		return &smtp.SMTPError{Code: 530, EnhancedCode: smtp.EnhancedCode{5, 7, 0}, Message: "authentication required"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	r, err := s.in.Check(ctx, to)
	if err != nil {
		return err
	}
	s.rcpts = append(s.rcpts, r)
	s.to = append(s.to, to)
	return nil
}

func (s *lmtpSession) read(r io.Reader) ([]byte, *Parsed, error) {
	raw, err := io.ReadAll(io.LimitReader(r, s.in.cfg.MaxMessageBytes+1))
	if err != nil {
		return nil, nil, errTempFail
	}
	if int64(len(raw)) > s.in.cfg.MaxMessageBytes {
		return nil, nil, &smtp.SMTPError{Code: 552, EnhancedCode: smtp.EnhancedCode{5, 3, 4}, Message: "message too big"}
	}
	parsed, err := ParseMessage(raw, s.in.cfg.AuthServID)
	if err != nil {
		s.in.metrics.Inc("email_inbound_total", "result", "unparseable")
		return nil, nil, &smtp.SMTPError{Code: 554, EnhancedCode: smtp.EnhancedCode{5, 6, 0}, Message: "message could not be parsed"}
	}
	return raw, &parsed, nil
}

// deliverAll is the plain-SMTP form: one answer for all recipients, so any
// temporary failure asks for a retry of all (already-delivered ones are
// recognised and skipped).
func (s *lmtpSession) deliverAll(raw []byte, parsed *Parsed) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var firstErr error
	for i, rc := range s.rcpts {
		if err := s.in.Deliver(ctx, raw, *parsed, s.to[i], rc); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (s *lmtpSession) Data(r io.Reader) error {
	raw, parsed, err := s.read(r)
	if err != nil {
		return err
	}
	return s.deliverAll(raw, parsed)
}

// LMTPData answers per recipient.
func (s *lmtpSession) LMTPData(r io.Reader, status smtp.StatusCollector) error {
	raw, parsed, err := s.read(r)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for i, rc := range s.rcpts {
		status.SetStatus(s.to[i], s.in.Deliver(ctx, raw, *parsed, s.to[i], rc))
	}
	return nil
}

func (s *lmtpSession) Reset() {
	s.from, s.rcpts, s.to = "", nil, nil
}

func (s *lmtpSession) Logout() error { return nil }

// directSession is the public MX session: no AUTH (it does not implement
// smtp.AuthSession, so none is offered), and the bridge's own checks.
type directSession struct {
	// inner is held, not embedded: embedding would promote its AUTH
	// methods and the public MX would offer AUTH.
	inner   *lmtpSession
	backend *listenerBackend
	conn    *smtp.Conn
	ip      net.IP
	release func()
}

func (s *directSession) Mail(from string, opts *smtp.MailOptions) error {
	return s.inner.Mail(from, opts)
}

func (s *directSession) Rcpt(to string, opts *smtp.RcptOptions) error {
	return s.inner.Rcpt(to, opts)
}

func (s *directSession) Reset() { s.inner.Reset() }

func (s *directSession) Data(r io.Reader) error {
	raw, parsed, err := s.inner.read(r)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fromDomain := domainOf(parsed.Email.From.Address)
	auth := s.backend.auth.Check(ctx, s.ip, s.conn.Hostname(), s.inner.from, raw, fromDomain)
	// The message's own Authentication-Results mean nothing here: ours win.
	parsed.Email.Auth = auth
	if s.backend.auth.DMARCReject(ctx, auth, fromDomain) {
		s.inner.in.metrics.Inc("email_inbound_total", "result", "dmarc_reject")
		return &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 7, 1}, Message: "rejected by the sender domain's DMARC policy"}
	}
	return s.inner.deliverAll(raw, parsed)
}

func (s *directSession) Logout() error {
	if s.release != nil {
		s.release()
		s.release = nil
	}
	return nil
}

var _ smtp.Session = (*directSession)(nil)
