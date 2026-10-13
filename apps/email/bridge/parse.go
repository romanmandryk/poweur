package bridge

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/textproto"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	_ "github.com/emersion/go-message/charset" // decode legacy charsets to UTF-8
	"github.com/emersion/go-message/mail"

	"github.com/poweur/identity"
)

// Parse caps. A message within EMAIL_MAX_MESSAGE_BYTES can still decode into
// far more (base64 is the least of it), so every part is bounded.
const (
	maxParts       = 200
	maxTextBytes   = 2 << 20
	maxHTMLBytes   = 4 << 20
	maxDecodedPart = identity.MaxEmailBlobBytes
)

// Parsed is an inbound message taken apart: the payload the user will get
// (without attachment bytes) and the attachments, still to be inlined or
// stored as blobs.
type Parsed struct {
	Email       identity.EmailMessage
	Attachments []ParsedAttachment
}

// ParsedAttachment is one attachment's bytes and description.
type ParsedAttachment struct {
	Meta identity.EmailAttachment
	Data []byte
}

var (
	errTooManyParts = errors.New("message has too many parts")
	reAuthMethod    = regexp.MustCompile(`(?i)\b(spf|dkim|dmarc|arc)\s*=\s*([a-z]+)`)
	reSpamScore     = regexp.MustCompile(`(?i)score\s*=\s*(-?[0-9]+(?:\.[0-9]+)?)`)
)

// ParseMessage reads a raw RFC 5322 message. authServID is the
// authserv-id whose Authentication-Results the bridge trusts: its own mail
// server's. Results written by anyone else (including the sender) are
// ignored.
func ParseMessage(raw []byte, authServID string) (Parsed, error) {
	mr, err := mail.CreateReader(bytes.NewReader(raw))
	if err != nil && mr == nil {
		return Parsed{}, fmt.Errorf("parse message: %w", err)
	}
	h := mr.Header
	var p Parsed
	m := &p.Email
	m.Version = identity.EmailPayloadVersion

	if from, err := h.AddressList("From"); err == nil && len(from) > 0 {
		m.From = toAddress(from[0])
	} else if raw := strings.TrimSpace(h.Get("From")); raw != "" {
		m.From = identity.EmailAddress{Address: firstAddress(raw)}
	}
	m.To = addressList(h, "To")
	m.Cc = addressList(h, "Cc")
	m.ReplyTo = addressList(h, "Reply-To")
	if subject, err := h.Subject(); err == nil {
		m.Subject = oneLine(subject, identity.MaxEmailHeaderLen)
	}
	if date, err := h.Date(); err == nil && !date.IsZero() {
		m.Date = date.UTC().Format(time.RFC3339)
	}
	if id, err := h.MessageID(); err == nil {
		m.MessageID = oneLine(id, identity.MaxEmailHeaderLen)
	}
	if ids, err := h.MsgIDList("In-Reply-To"); err == nil && len(ids) > 0 {
		m.InReplyTo = oneLine(ids[0], identity.MaxEmailHeaderLen)
	}
	if ids, err := h.MsgIDList("References"); err == nil {
		if len(ids) > identity.MaxEmailReferences {
			// Keep the root and the most recent ones: the root is what
			// threads, the tail is what the next reply will cite.
			ids = append(ids[:1:1], ids[len(ids)-identity.MaxEmailReferences+1:]...)
		}
		for _, id := range ids {
			m.References = append(m.References, oneLine(id, identity.MaxEmailHeaderLen))
		}
	}
	m.Auth = authResults(h.Header.Values("Authentication-Results"), authServID)
	if authServID != "" {
		m.Auth.Spam, m.Auth.SpamScore = spamVerdict(h.Get("X-Spam-Status"), h.Get("X-Spam-Result"))
	}

	ct, ctParams, _ := h.ContentType()
	report := strings.EqualFold(ct, "multipart/report") && strings.EqualFold(ctParams["report-type"], "delivery-status")
	var bounce identity.EmailBounce

	parts := 0
	var text, html strings.Builder
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			// A broken trailing part is not worth losing the message over:
			// keep what parsed and say so.
			m.Truncated = true
			break
		}
		parts++
		if parts > maxParts {
			return Parsed{}, errTooManyParts
		}
		body, overflow := readCapped(part.Body, maxDecodedPart)
		switch ph := part.Header.(type) {
		case *mail.InlineHeader:
			pt, _, _ := ph.ContentType()
			switch {
			case strings.EqualFold(pt, "text/plain") || pt == "":
				appendCapped(&text, body, maxTextBytes, &m.Truncated)
				continue
			case strings.EqualFold(pt, "text/html"):
				appendCapped(&html, body, maxHTMLBytes, &m.Truncated)
				continue
			}
			if report && strings.EqualFold(pt, "text/rfc822-headers") {
				bounce.MessageID = originalMessageID(body)
			}
			_, dispParams, _ := ph.ContentDisposition()
			_, typeParams, _ := ph.ContentType()
			name := dispParams["filename"]
			if name == "" {
				name = typeParams["name"]
			}
			p.addAttachment(ph.Get("Content-ID"), pt, name, true, body, overflow)
		case *mail.AttachmentHeader:
			pt, _, _ := ph.ContentType()
			if report {
				switch strings.ToLower(pt) {
				case "message/delivery-status", "message/global-delivery-status":
					parseDeliveryStatus(body, &bounce)
				case "message/rfc822", "text/rfc822-headers", "message/rfc822-headers", "message/global-headers":
					bounce.MessageID = originalMessageID(body)
				}
			}
			name, _ := ph.Filename()
			disp, _, _ := ph.ContentDisposition()
			p.addAttachment(ph.Get("Content-ID"), pt, name, disp == "inline", body, overflow)
		}
	}
	m.Text = toUTF8(text.String())
	if html.Len() > 0 {
		m.HTML = SanitizeHTML(toUTF8(html.String()))
	}
	if report && (bounce.Action != "" || bounce.MessageID != "") {
		m.Bounce = &bounce
	}
	return p, nil
}

func (p *Parsed) addAttachment(contentID, mimeType, name string, inline bool, body []byte, overflow bool) {
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	if overflow {
		p.Email.Truncated = true
	}
	name = safeFileName(name)
	if name == "" {
		name = "attachment"
		if exts, _ := mime.ExtensionsByType(mimeType); len(exts) > 0 {
			name += exts[0]
		}
	}
	cid := strings.Trim(strings.TrimSpace(contentID), "<>")
	p.Attachments = append(p.Attachments, ParsedAttachment{
		Meta: identity.EmailAttachment{Name: name, MIME: strings.ToLower(mimeType), Size: int64(len(body)), ContentID: oneLine(cid, 256), Inline: inline && cid != ""},
		Data: body,
	})
}

// safeFileName keeps a display name, never a path.
func safeFileName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Base(strings.TrimSpace(name))
	if name == "." || name == "/" {
		return ""
	}
	return oneLine(name, identity.MaxEmailNameLen)
}

func readCapped(r io.Reader, limit int64) ([]byte, bool) {
	body, _ := io.ReadAll(io.LimitReader(r, limit+1))
	if int64(len(body)) > limit {
		return body[:limit], true
	}
	return body, false
}

func appendCapped(b *strings.Builder, body []byte, limit int, truncated *bool) {
	if b.Len()+len(body) > limit {
		body = body[:max(0, limit-b.Len())]
		*truncated = true
	}
	if b.Len() > 0 && len(body) > 0 {
		b.WriteString("\n")
	}
	b.Write(body)
}

func toUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return strings.ToValidUTF8(s, "�")
}

func oneLine(s string, limit int) string {
	s = strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == '\t' {
			return ' '
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, toUTF8(s))
	s = strings.TrimSpace(s)
	for len(s) > limit {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	return s
}

func toAddress(a *mail.Address) identity.EmailAddress {
	return identity.EmailAddress{Name: oneLine(a.Name, identity.MaxEmailNameLen), Address: oneLine(a.Address, identity.MaxEmailAddressLen)}
}

func addressList(h mail.Header, key string) []identity.EmailAddress {
	list, err := h.AddressList(key)
	if err != nil {
		return nil
	}
	var out []identity.EmailAddress
	for _, a := range list {
		if len(out) >= identity.MaxEmailRecipients {
			break
		}
		addr := toAddress(a)
		if addr.Validate() == nil {
			out = append(out, addr)
		}
	}
	return out
}

func firstAddress(raw string) string {
	if i := strings.LastIndex(raw, "<"); i >= 0 {
		if j := strings.Index(raw[i:], ">"); j > 0 {
			return oneLine(raw[i+1:i+j], identity.MaxEmailAddressLen)
		}
	}
	return oneLine(raw, identity.MaxEmailAddressLen)
}

// authResults reads the first Authentication-Results header written by the
// trusted server (RFC 8601 §5: a server removes forged copies of its own
// authserv-id, so the first one with ours is ours).
func authResults(values []string, authServID string) identity.EmailAuth {
	auth := identity.EmailAuth{Verdict: identity.EmailUnverified}
	if authServID == "" {
		return auth
	}
	for _, v := range values {
		id, rest, _ := strings.Cut(v, ";")
		fields := strings.Fields(id)
		if len(fields) == 0 || !strings.EqualFold(fields[0], authServID) {
			continue
		}
		for _, match := range reAuthMethod.FindAllStringSubmatch(rest, -1) {
			method, result := strings.ToLower(match[1]), strings.ToLower(match[2])
			switch method {
			case "spf":
				if auth.SPF == "" {
					auth.SPF = result
				}
			case "dkim":
				if auth.DKIM == "" || result == "pass" {
					auth.DKIM = result
				}
			case "dmarc":
				if auth.DMARC == "" {
					auth.DMARC = result
				}
			case "arc":
				if auth.ARC == "" {
					auth.ARC = result
				}
			}
		}
		break
	}
	switch auth.DMARC {
	case "pass":
		auth.Verdict = identity.EmailVerified
	case "fail":
		auth.Verdict = identity.EmailFailed
	}
	return auth
}

// spamVerdict reads the mail server's spam headers (`X-Spam-Status: Yes,
// score=12.3` is SpamAssassin's and Stalwart's form).
func spamVerdict(status, result string) (string, float64) {
	v := strings.TrimSpace(status)
	if v == "" {
		v = strings.TrimSpace(result)
	}
	if v == "" {
		return "", 0
	}
	verdict := "ham"
	if lower := strings.ToLower(v); strings.HasPrefix(lower, "yes") || strings.HasPrefix(lower, "spam") {
		verdict = "spam"
	}
	score := 0.0
	if m := reSpamScore.FindStringSubmatch(v); len(m) == 2 {
		score, _ = strconv.ParseFloat(m[1], 64)
	}
	return verdict, score
}

// parseDeliveryStatus reads a message/delivery-status body (RFC 3464): one
// per-message block and per-recipient blocks; the first recipient wins.
func parseDeliveryStatus(body []byte, b *identity.EmailBounce) {
	r := textproto.NewReader(bufio.NewReader(bytes.NewReader(append(bytes.TrimLeft(body, "\r\n"), '\r', '\n', '\r', '\n'))))
	for i := 0; i < 20; i++ {
		block, err := r.ReadMIMEHeader()
		if len(block) > 0 {
			if v := block.Get("Final-Recipient"); v != "" && b.Recipient == "" {
				_, addr, _ := strings.Cut(v, ";")
				b.Recipient = oneLine(addr, identity.MaxEmailAddressLen)
				b.Action = strings.ToLower(oneLine(block.Get("Action"), 32))
				b.Status = oneLine(block.Get("Status"), 32)
				diag := block.Get("Diagnostic-Code")
				if _, d, ok := strings.Cut(diag, ";"); ok {
					diag = d
				}
				b.Diagnostic = oneLine(diag, 512)
			}
		}
		if err != nil {
			return
		}
	}
}

func originalMessageID(body []byte) string {
	r := textproto.NewReader(bufio.NewReader(bytes.NewReader(append(bytes.TrimLeft(body, "\r\n"), '\r', '\n', '\r', '\n'))))
	h, _ := r.ReadMIMEHeader()
	return strings.Trim(oneLine(h.Get("Message-Id"), identity.MaxEmailHeaderLen), "<>")
}
