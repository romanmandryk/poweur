package bridge

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"html"
	"strings"
	"time"

	"github.com/emersion/go-message/mail"

	"github.com/poweur/identity"
)

// OutAttachment is an attachment's bytes, resolved from inline data or an upload.
type OutAttachment struct {
	Name, MIME string
	Data       []byte
}

// Outgoing is a message ready to build.
type Outgoing struct {
	From        identity.EmailAddress
	To, Cc      []identity.EmailAddress
	Subject     string
	Text, HTML  string
	InReplyTo   string
	References  []string
	MessageID   string
	Date        time.Time
	Attachments []OutAttachment
	// Footer is appended to the text (and HTML) part, above any quoted reply.
	FooterText, FooterHTML string
}

// NewMessageID mints `<random>@domain` (returned without brackets).
func NewMessageID(domain string) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b) + "@" + domain
}

func toMailAddresses(list []identity.EmailAddress) []*mail.Address {
	out := make([]*mail.Address, 0, len(list))
	for _, a := range list {
		out = append(out, &mail.Address{Name: a.Name, Address: a.Address})
	}
	return out
}

// insertFooter places the footer above a quoted reply ("On … wrote:" or a
// first line starting with '>'), or at the end.
func insertFooter(text, footer string) string {
	if footer == "" {
		return text
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, ">") || (strings.HasPrefix(t, "On ") && strings.HasSuffix(t, "wrote:")) {
			head := strings.TrimRight(strings.Join(lines[:i], "\n"), "\n")
			return head + "\n\n-- \n" + footer + "\n\n" + strings.Join(lines[i:], "\n")
		}
	}
	return strings.TrimRight(text, "\n") + "\n\n-- \n" + footer + "\n"
}

func insertHTMLFooter(body, footer string) string {
	if footer == "" {
		return body
	}
	block := `<p style="color:#666;font-size:12px">` + footer + `</p>`
	if i := strings.Index(strings.ToLower(body), "<blockquote"); i >= 0 {
		return body[:i] + block + body[i:]
	}
	if i := strings.LastIndex(strings.ToLower(body), "</body>"); i >= 0 {
		return body[:i] + block + body[i:]
	}
	return body + block
}

// textToHTML is used only for the footer link when the user sent HTML.
func textToHTML(s string) string {
	return strings.ReplaceAll(html.EscapeString(s), "\n", "<br>")
}

// BuildMIME renders the message: text, optionally HTML as an alternative,
// and attachments, with RFC 2047-encoded headers where needed.
func BuildMIME(o Outgoing) ([]byte, error) {
	var h mail.Header
	h.SetDate(o.Date)
	h.SetAddressList("From", []*mail.Address{{Name: o.From.Name, Address: o.From.Address}})
	if len(o.To) > 0 {
		h.SetAddressList("To", toMailAddresses(o.To))
	}
	if len(o.Cc) > 0 {
		h.SetAddressList("Cc", toMailAddresses(o.Cc))
	}
	h.SetSubject(o.Subject)
	h.SetMessageID(o.MessageID)
	if o.InReplyTo != "" {
		h.SetMsgIDList("In-Reply-To", []string{strings.Trim(o.InReplyTo, "<>")})
	}
	if len(o.References) > 0 {
		refs := make([]string, 0, len(o.References))
		for _, r := range o.References {
			refs = append(refs, strings.Trim(r, "<>"))
		}
		h.SetMsgIDList("References", refs)
	}
	h.Set("MIME-Version", "1.0")
	h.Set("X-Mailer", "Poweur email bridge "+Version)

	text := insertFooter(o.Text, o.FooterText)
	htmlBody := ""
	if o.HTML != "" {
		htmlBody = insertHTMLFooter(o.HTML, o.FooterHTML)
	}

	var buf bytes.Buffer
	writeBodies := func(iw *mail.InlineWriter) error {
		var th mail.InlineHeader
		th.Set("Content-Type", "text/plain; charset=utf-8")
		w, err := iw.CreatePart(th)
		if err != nil {
			return err
		}
		if _, err := w.Write([]byte(text)); err != nil {
			return err
		}
		if err := w.Close(); err != nil {
			return err
		}
		if htmlBody != "" {
			var hh mail.InlineHeader
			hh.Set("Content-Type", "text/html; charset=utf-8")
			w, err := iw.CreatePart(hh)
			if err != nil {
				return err
			}
			if _, err := w.Write([]byte(htmlBody)); err != nil {
				return err
			}
			if err := w.Close(); err != nil {
				return err
			}
		}
		return iw.Close()
	}

	if len(o.Attachments) == 0 {
		if htmlBody == "" {
			h.Set("Content-Type", "text/plain; charset=utf-8")
			w, err := mail.CreateSingleInlineWriter(&buf, h)
			if err != nil {
				return nil, err
			}
			if _, err := w.Write([]byte(text)); err != nil {
				return nil, err
			}
			if err := w.Close(); err != nil {
				return nil, err
			}
			return buf.Bytes(), nil
		}
		iw, err := mail.CreateInlineWriter(&buf, h)
		if err != nil {
			return nil, err
		}
		if err := writeBodies(iw); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}

	mw, err := mail.CreateWriter(&buf, h)
	if err != nil {
		return nil, err
	}
	iw, err := mw.CreateInline()
	if err != nil {
		return nil, err
	}
	if err := writeBodies(iw); err != nil {
		return nil, err
	}
	for _, a := range o.Attachments {
		var ah mail.AttachmentHeader
		mimeType := a.MIME
		if mimeType == "" || strings.ContainsAny(mimeType, "\r\n;") {
			mimeType = "application/octet-stream"
		}
		ah.Set("Content-Type", mimeType)
		ah.SetFilename(a.Name)
		w, err := mw.CreateAttachment(ah)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(a.Data); err != nil {
			return nil, err
		}
		if err := w.Close(); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// RenderFooter fills the footer template.
func RenderFooter(template, id, signupURL string) string {
	return strings.NewReplacer("{id}", id, "{profile_url}", "https://"+id, "{signup_url}", signupURL).Replace(template)
}
