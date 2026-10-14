package identity

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"
)

// The email bridge (EPIC-023, PCP-0010).
//
// A hosted identity `john.poweur.net` that opts in gets the address
// `john@poweur.net`. The bridge is itself a Poweur identity: it delivers
// every inbound email to John as an end-to-end encrypted `email.message`, and
// John asks it to send mail by sending it an encrypted `email.send`. The relay
// keeps storing only ciphertext; the bridge holds no relay credential.
//
// Opting in writes two documents with the user's own key:
//
//  1. the public, signed `.poweur/public/email.json` (EmailOptIn), which the
//     bridge resolves before it accepts mail for the address or sends from it;
//  2. the `email` block of the private inbox policy (EmailPolicy), which lets
//     the named bridge deliver `email.*` even into a closed inbox.
//
// A client shows a message as email only when it is an `email.*` type sent by
// the bridge the user's own opt-in names. Anyone else sending `email.message`
// gets an ordinary untrusted message — a Poweur user cannot forge "an email
// from your bank".

// Email message types. `email.*` is not `sys.*`: the relay routes them like
// any application type, and admits them through a closed inbox only from the
// bridge the recipient named (InboxPolicy.TrustsEmailBridge).
const (
	MsgTypeEmailMessage = "email.message" // bridge → user: an inbound email
	MsgTypeEmailSend    = "email.send"    // user → bridge: send this email
	MsgTypeEmailStatus  = "email.status"  // bridge → user: what happened to a send
	EmailTypePrefix     = "email."
)

// IsEmailType reports whether t is one of the bridge's message types.
func IsEmailType(t string) bool { return strings.HasPrefix(t, EmailTypePrefix) }

const (
	// EmailOptInPath is the opt-in document in the identity's drive.
	EmailOptInPath = ".poweur/public/email.json"
	// WellKnownEmailPath is where the world reads it.
	WellKnownEmailPath = "/.well-known/poweur/email.json"
	// EmailOptInVersion is the document schema version.
	EmailOptInVersion = 1
)

// EmailOptIn is `.poweur/public/email.json`: the identity's signed statement
// that `Address` is theirs, delivered and sent through `Bridge`.
type EmailOptIn struct {
	Version   int    `json:"version"`
	Identity  string `json:"identity"`
	Address   string `json:"address"`
	Bridge    string `json:"bridge"`
	CreatedAt string `json:"created_at"`
	Signature string `json:"signature,omitempty"`
}

// Validate checks the shape of the document and that Address is the one the
// identity's name gives it (EmailAddressFor).
func (o EmailOptIn) Validate() error {
	if o.Version != EmailOptInVersion {
		return fmt.Errorf("unsupported email.json version %d", o.Version)
	}
	id := strings.ToLower(strings.TrimSpace(o.Identity))
	if err := ValidateIdentityName(id); err != nil {
		return fmt.Errorf("email.json identity: %w", err)
	}
	want, err := EmailAddressFor(id)
	if err != nil {
		return err
	}
	if !strings.EqualFold(strings.TrimSpace(o.Address), want) {
		return fmt.Errorf("email.json address %q is not %s's address (%s)", o.Address, id, want)
	}
	if err := ValidateIdentityName(strings.ToLower(strings.TrimSpace(o.Bridge))); err != nil {
		return fmt.Errorf("email.json bridge: %w", err)
	}
	if _, err := time.Parse(time.RFC3339, o.CreatedAt); err != nil {
		return fmt.Errorf("email.json created_at must be RFC3339: %w", err)
	}
	return nil
}

// Canonical is the string the identity signs: fixed order, names lowercased.
func (o EmailOptIn) Canonical() string {
	return strings.Join([]string{
		"poweur-email-optin",
		fmt.Sprintf("v%d", o.Version),
		strings.ToLower(strings.TrimSpace(o.Identity)),
		strings.ToLower(strings.TrimSpace(o.Address)),
		strings.ToLower(strings.TrimSpace(o.Bridge)),
		o.CreatedAt,
	}, "\n")
}

// Sign fills Signature with the identity key.
func (o *EmailOptIn) Sign(priv ed25519.PrivateKey) error {
	if err := o.Validate(); err != nil {
		return err
	}
	o.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, []byte(o.Canonical())))
	return nil
}

// VerifySignature checks the signature against the identity's public key.
func (o EmailOptIn) VerifySignature(pub ed25519.PublicKey) error {
	sig, err := base64.RawURLEncoding.DecodeString(o.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("email.json: invalid signature encoding")
	}
	if !ed25519.Verify(pub, []byte(o.Canonical()), sig) {
		return fmt.Errorf("email.json signature verification failed")
	}
	return nil
}

// NewEmailOptIn builds an unsigned opt-in for identity through bridge.
func NewEmailOptIn(identity, bridge string, now time.Time) (EmailOptIn, error) {
	identity = strings.ToLower(strings.TrimSpace(identity))
	address, err := EmailAddressFor(identity)
	if err != nil {
		return EmailOptIn{}, err
	}
	o := EmailOptIn{
		Version:   EmailOptInVersion,
		Identity:  identity,
		Address:   address,
		Bridge:    strings.ToLower(strings.TrimSpace(bridge)),
		CreatedAt: now.UTC().Format(time.RFC3339),
	}
	return o, o.Validate()
}

// ParseEmailOptIn decodes and validates email.json. The signature is checked
// separately (VerifySignature) by whoever resolved the identity's key.
func ParseEmailOptIn(raw []byte) (EmailOptIn, error) {
	var o EmailOptIn
	if err := json.Unmarshal(raw, &o); err != nil {
		return EmailOptIn{}, fmt.Errorf("invalid email.json: %w", err)
	}
	if err := o.Validate(); err != nil {
		return EmailOptIn{}, err
	}
	if o.Signature == "" {
		return EmailOptIn{}, fmt.Errorf("email.json is not signed")
	}
	return o, nil
}

// EmailAddressFor is the address an identity's name gives it: the leftmost
// label at the rest (`john.poweur.net` → `john@poweur.net`). The identity
// needs at least three labels, so the domain part is itself a name with a dot.
func EmailAddressFor(identity string) (string, error) {
	identity = strings.ToLower(strings.TrimSpace(identity))
	if err := ValidateIdentityName(identity); err != nil {
		return "", err
	}
	local, domain, _ := strings.Cut(identity, ".")
	if !strings.Contains(domain, ".") {
		return "", fmt.Errorf("%s has no email address: its parent %s is not a domain with a dot", identity, domain)
	}
	return local + "@" + domain, nil
}

// EmailRoute is an inbound address taken apart.
type EmailRoute struct {
	Local  string // the identity's leftmost label
	Tag    string // subaddress after '+', kept for client-side filters
	Domain string // the domain the mail was addressed to
}

// MaxEmailTagLen caps a subaddress tag.
const MaxEmailTagLen = 64

// ParseEmailAddress splits `john+news@poweur.net` into its route. The local
// part follows the identity name rules (`.` is significant, not ignored:
// identities are DNS labels), so anything that could not be a label is
// refused rather than guessed at.
func ParseEmailAddress(address string) (EmailRoute, error) {
	address = strings.ToLower(strings.TrimSpace(address))
	address = strings.TrimSuffix(strings.TrimPrefix(address, "<"), ">")
	at := strings.LastIndex(address, "@")
	if at <= 0 || at == len(address)-1 {
		return EmailRoute{}, fmt.Errorf("not an email address")
	}
	local, domain := address[:at], strings.TrimSuffix(address[at+1:], ".")
	if err := ValidateIdentityName(domain); err != nil {
		return EmailRoute{}, fmt.Errorf("domain: %w", err)
	}
	tag := ""
	if plus := strings.Index(local, "+"); plus >= 0 {
		local, tag = local[:plus], local[plus+1:]
		if len(tag) > MaxEmailTagLen {
			return EmailRoute{}, fmt.Errorf("subaddress too long")
		}
		for i := 0; i < len(tag); i++ {
			c := tag[i]
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
				return EmailRoute{}, fmt.Errorf("subaddress has an invalid character")
			}
		}
	}
	if err := validateLabel(local); err != nil {
		return EmailRoute{}, fmt.Errorf("local part: %w", err)
	}
	return EmailRoute{Local: local, Tag: tag, Domain: domain}, nil
}

// EmailPolicy is the `email` block of the inbox policy: the bridge allowed to
// deliver `email.*` messages. Absent or Allow=false: no bridge is admitted
// through a closed inbox, and clients do not treat `email.*` as mail.
type EmailPolicy struct {
	Allow  bool   `json:"allow"`
	Bridge string `json:"bridge,omitempty"`
}

// Validate checks the block.
func (e EmailPolicy) Validate() error {
	if !e.Allow && e.Bridge == "" {
		return nil
	}
	if err := ValidateIdentityName(strings.ToLower(strings.TrimSpace(e.Bridge))); err != nil {
		return fmt.Errorf("email.bridge: %w", err)
	}
	return nil
}

// TrustsEmailBridge reports whether sender is the bridge the recipient opted
// into, so its `email.*` messages are admitted and shown as mail.
func (p InboxPolicy) TrustsEmailBridge(sender string) bool {
	if p.Email == nil || !p.Email.Allow {
		return false
	}
	sender = strings.ToLower(strings.TrimSpace(sender))
	return sender != "" && sender == strings.ToLower(strings.TrimSpace(p.Email.Bridge))
}

// Email payloads. Each is the JSON plaintext of the matching message type,
// encrypted end to end like any message. Version 1 throughout.
const EmailPayloadVersion = 1

// Limits on the payloads. The envelope itself is capped at 512 KB by the
// relay; these bound the parts a reader iterates.
const (
	MaxEmailRecipients  = 50
	MaxEmailAttachments = 50
	MaxEmailReferences  = 50
	MaxEmailNameLen     = 256
	MaxEmailAddressLen  = 320
	MaxEmailHeaderLen   = 998
	// MaxEmailOutboundBytes caps a message the bridge will send: the sum of
	// text, HTML and attachments before MIME encoding.
	MaxEmailOutboundBytes = 20 * 1024 * 1024
	// MaxEmailBlobBytes caps one stored blob (attachment or raw message).
	MaxEmailBlobBytes = 32 * 1024 * 1024
)

// EmailAddress is a mailbox with an optional display name.
type EmailAddress struct {
	Name    string `json:"name,omitempty"`
	Address string `json:"address"`
}

// String formats the mailbox for a header (RFC 5322, encoded when needed).
func (a EmailAddress) String() string {
	return (&mail.Address{Name: a.Name, Address: a.Address}).String()
}

// Validate checks the mailbox syntax.
func (a EmailAddress) Validate() error {
	if len(a.Address) > MaxEmailAddressLen || len(a.Name) > MaxEmailNameLen {
		return fmt.Errorf("email address too long")
	}
	if !utf8.ValidString(a.Name) || strings.ContainsAny(a.Name, "\r\n") {
		return fmt.Errorf("email display name must be one line of UTF-8")
	}
	parsed, err := mail.ParseAddress(a.Address)
	if err != nil || parsed.Address != a.Address || parsed.Name != "" {
		return fmt.Errorf("invalid email address %q", a.Address)
	}
	return nil
}

func validateAddressList(field string, list []EmailAddress) error {
	if len(list) > MaxEmailRecipients {
		return fmt.Errorf("%s has too many addresses (max %d)", field, MaxEmailRecipients)
	}
	for _, a := range list {
		if err := a.Validate(); err != nil {
			return fmt.Errorf("%s: %w", field, err)
		}
	}
	return nil
}

// EmailAuth is what the receiving mail server concluded about a message. The
// bridge copies it from the trusted `Authentication-Results` header of its
// own MTA; anything the sender wrote into the message is ignored.
type EmailAuth struct {
	SPF   string `json:"spf,omitempty"`   // pass | fail | softfail | neutral | none | temperror | permerror
	DKIM  string `json:"dkim,omitempty"`  // same vocabulary
	DMARC string `json:"dmarc,omitempty"` // same vocabulary
	ARC   string `json:"arc,omitempty"`
	// Verdict summarizes for display: "verified" only when DMARC passed, so
	// the From domain is aligned with an authenticated one; "failed" when
	// DMARC failed; otherwise "unverified".
	Verdict string `json:"verdict"`
	// Spam is the MTA's spam classification: "ham", "spam" or "".
	Spam      string  `json:"spam,omitempty"`
	SpamScore float64 `json:"spam_score,omitempty"`
}

// Email auth verdicts.
const (
	EmailVerified   = "verified"
	EmailUnverified = "unverified"
	EmailFailed     = "failed"
)

// EmailBlobRef points at a sealed blob the bridge stores for a while: an
// attachment too large for the envelope, or the original message. The bytes
// at URL are an identity.SealedPayload sealed to the recipient with
// EmailBlobSealDomain and the blob ID as context; SHA256 is of the plaintext.
type EmailBlobRef struct {
	ID        string `json:"id"`
	URL       string `json:"url"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

// EmailAttachment is one attachment of an inbound email. Small ones travel in
// Data (base64, standard alphabet); large ones as a Blob reference.
type EmailAttachment struct {
	Name      string        `json:"name"`
	MIME      string        `json:"mime"`
	Size      int64         `json:"size"`
	ContentID string        `json:"content_id,omitempty"`
	Inline    bool          `json:"inline,omitempty"`
	Data      string        `json:"data,omitempty"`
	Blob      *EmailBlobRef `json:"blob,omitempty"`
}

// EmailMessage is the payload of `email.message`.
type EmailMessage struct {
	Version    int            `json:"version"`
	MessageID  string         `json:"message_id,omitempty"`
	InReplyTo  string         `json:"in_reply_to,omitempty"`
	References []string       `json:"references,omitempty"`
	From       EmailAddress   `json:"from"`
	To         []EmailAddress `json:"to,omitempty"`
	Cc         []EmailAddress `json:"cc,omitempty"`
	ReplyTo    []EmailAddress `json:"reply_to,omitempty"`
	// EnvelopeTo is the address the mail was delivered to, as the MTA saw
	// it (shows which alias or subaddress was used).
	EnvelopeTo string    `json:"envelope_to,omitempty"`
	Tag        string    `json:"tag,omitempty"`
	Subject    string    `json:"subject,omitempty"`
	Date       string    `json:"date,omitempty"` // RFC3339, from the Date header
	Auth       EmailAuth `json:"auth"`
	Text       string    `json:"text,omitempty"`
	// HTML is already sanitized by the bridge. Clients sanitize it again and
	// render it isolated: the bridge is not trusted with the user's screen.
	HTML        string            `json:"html,omitempty"`
	Attachments []EmailAttachment `json:"attachments,omitempty"`
	// Raw is the original message, for "Show original".
	Raw *EmailBlobRef `json:"raw,omitempty"`
	// Truncated says the bridge shortened text or HTML to fit the envelope;
	// Raw has the whole message.
	Truncated bool `json:"truncated,omitempty"`
	// Bounce is set when this message is a delivery report for a send.
	Bounce *EmailBounce `json:"bounce,omitempty"`
}

// EmailBounce summarizes a delivery status notification.
type EmailBounce struct {
	SendID     string `json:"send_id,omitempty"`
	MessageID  string `json:"message_id,omitempty"`
	Recipient  string `json:"recipient,omitempty"`
	Action     string `json:"action,omitempty"` // failed | delayed | delivered
	Status     string `json:"status,omitempty"` // e.g. 5.1.1
	Diagnostic string `json:"diagnostic,omitempty"`
}

// Validate checks an inbound payload's shape.
func (m EmailMessage) Validate() error {
	if m.Version != EmailPayloadVersion {
		return fmt.Errorf("unsupported email.message version %d", m.Version)
	}
	if err := m.From.Validate(); err != nil {
		return fmt.Errorf("from: %w", err)
	}
	for field, list := range map[string][]EmailAddress{"to": m.To, "cc": m.Cc, "reply_to": m.ReplyTo} {
		if err := validateAddressList(field, list); err != nil {
			return err
		}
	}
	if len(m.Attachments) > MaxEmailAttachments {
		return fmt.Errorf("too many attachments (max %d)", MaxEmailAttachments)
	}
	if len(m.References) > MaxEmailReferences {
		return fmt.Errorf("too many references (max %d)", MaxEmailReferences)
	}
	if !utf8.ValidString(m.Subject) || len(m.Subject) > MaxEmailHeaderLen {
		return fmt.Errorf("invalid subject")
	}
	return nil
}

// EmailConversationRoot is the message a mail thread hangs from: the first
// reference, else the message answered, else the message itself. Clients
// group the Emails tray by it, so a reply lands beside what it answers even
// when the subject changed.
func EmailConversationRoot(messageID, inReplyTo string, references []string) string {
	for _, r := range references {
		if r = strings.TrimSpace(r); r != "" {
			return r
		}
	}
	if r := strings.TrimSpace(inReplyTo); r != "" {
		return r
	}
	return strings.TrimSpace(messageID)
}

// EmailThreadKeyPrefix starts the read-state thread key of a mail thread.
const EmailThreadKeyPrefix = "mail:"

// EmailThreadKey is the read-state thread key (ReadState.Threads) of a mail
// thread: its conversation root, or the archived message's own id when the
// mail names none. Mail has no envelope thread — subjects and message ids are
// content — so clients key mail read marks by this instead.
func EmailThreadKey(m EmailMessage, recordID string) string {
	if root := EmailConversationRoot(m.MessageID, m.InReplyTo, m.References); root != "" {
		return EmailThreadKeyPrefix + root
	}
	return EmailThreadKeyPrefix + recordID
}

// EmailOutAttachment is one attachment of an `email.send`: inline Data
// (base64) or an Upload the client sealed to the bridge beforehand.
type EmailOutAttachment struct {
	Name   string `json:"name"`
	MIME   string `json:"mime"`
	Size   int64  `json:"size"`
	Data   string `json:"data,omitempty"`
	Upload string `json:"upload,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

// EmailSend is the payload of `email.send`. The sender is the envelope
// sender; the From address is always the one their opt-in names.
type EmailSend struct {
	Version     int                  `json:"version"`
	SendID      string               `json:"send_id"`
	FromName    string               `json:"from_name,omitempty"`
	To          []EmailAddress       `json:"to"`
	Cc          []EmailAddress       `json:"cc,omitempty"`
	Bcc         []EmailAddress       `json:"bcc,omitempty"`
	Subject     string               `json:"subject"`
	Text        string               `json:"text"`
	HTML        string               `json:"html,omitempty"`
	InReplyTo   string               `json:"in_reply_to,omitempty"`
	References  []string             `json:"references,omitempty"`
	Attachments []EmailOutAttachment `json:"attachments,omitempty"`
}

// MaxSendIDLen bounds the client's idempotency key.
const MaxSendIDLen = 64

// Validate checks an outbound request.
func (s EmailSend) Validate() error {
	if s.Version != EmailPayloadVersion {
		return fmt.Errorf("unsupported email.send version %d", s.Version)
	}
	if s.SendID == "" || len(s.SendID) > MaxSendIDLen || ValidateThreadID(s.SendID) != nil {
		return fmt.Errorf("send_id is required (max %d of A-Z a-z 0-9 _ - . : @ + ~)", MaxSendIDLen)
	}
	if len(s.To)+len(s.Cc)+len(s.Bcc) == 0 {
		return fmt.Errorf("no recipients")
	}
	if len(s.To)+len(s.Cc)+len(s.Bcc) > MaxEmailRecipients {
		return fmt.Errorf("too many recipients (max %d)", MaxEmailRecipients)
	}
	for field, list := range map[string][]EmailAddress{"to": s.To, "cc": s.Cc, "bcc": s.Bcc} {
		if err := validateAddressList(field, list); err != nil {
			return err
		}
	}
	if len(s.FromName) > MaxEmailNameLen || strings.ContainsAny(s.FromName, "\r\n") || !utf8.ValidString(s.FromName) {
		return fmt.Errorf("from_name must be one line of at most %d bytes", MaxEmailNameLen)
	}
	if strings.ContainsAny(s.Subject, "\r\n") || len(s.Subject) > MaxEmailHeaderLen || !utf8.ValidString(s.Subject) {
		return fmt.Errorf("subject must be one line of at most %d bytes", MaxEmailHeaderLen)
	}
	if strings.ContainsAny(s.InReplyTo, "\r\n") || len(s.InReplyTo) > MaxEmailHeaderLen {
		return fmt.Errorf("invalid in_reply_to")
	}
	if len(s.References) > MaxEmailReferences {
		return fmt.Errorf("too many references (max %d)", MaxEmailReferences)
	}
	for _, r := range s.References {
		if strings.ContainsAny(r, "\r\n \t") || len(r) > MaxEmailHeaderLen {
			return fmt.Errorf("invalid reference")
		}
	}
	if len(s.Attachments) > MaxEmailAttachments {
		return fmt.Errorf("too many attachments (max %d)", MaxEmailAttachments)
	}
	total := int64(len(s.Text) + len(s.HTML))
	for _, a := range s.Attachments {
		if a.Name == "" || len(a.Name) > MaxEmailNameLen || strings.ContainsAny(a.Name, "\r\n/\\") {
			return fmt.Errorf("invalid attachment name")
		}
		if (a.Data == "") == (a.Upload == "") {
			return fmt.Errorf("attachment %q needs exactly one of data or upload", a.Name)
		}
		if a.Size < 0 {
			return fmt.Errorf("attachment %q has a negative size", a.Name)
		}
		total += a.Size
	}
	if total > MaxEmailOutboundBytes {
		return fmt.Errorf("message too large (max %d MB)", MaxEmailOutboundBytes/1024/1024)
	}
	return nil
}

// Email send states, in the order a send moves through them.
const (
	EmailStateAccepted   = "accepted"   // the bridge took the request
	EmailStateSent       = "sent"       // the mail server accepted the message
	EmailStateDeferred   = "deferred"   // a temporary failure; the bridge will retry
	EmailStateBounced    = "bounced"    // a recipient's server refused it
	EmailStateComplained = "complained" // a recipient reported it as spam
	EmailStateRefused    = "refused"    // the bridge will not send it (quota, policy, invalid)
)

// EmailStatus is the payload of `email.status`.
type EmailStatus struct {
	Version   int    `json:"version"`
	SendID    string `json:"send_id"`
	State     string `json:"state"`
	MessageID string `json:"message_id,omitempty"`
	Recipient string `json:"recipient,omitempty"`
	Reason    string `json:"reason,omitempty"`
	// RetryAt, on a deferred send, is when the bridge will try it next
	// (sending is paced; see the bridge's limits).
	RetryAt string `json:"retry_at,omitempty"`
	At      string `json:"at"`
}

// Validate checks a status payload.
func (s EmailStatus) Validate() error {
	if s.Version != EmailPayloadVersion {
		return fmt.Errorf("unsupported email.status version %d", s.Version)
	}
	switch s.State {
	case EmailStateAccepted, EmailStateSent, EmailStateDeferred, EmailStateBounced, EmailStateComplained, EmailStateRefused:
	default:
		return fmt.Errorf("invalid email.status state %q", s.State)
	}
	if s.SendID == "" {
		return fmt.Errorf("email.status needs send_id")
	}
	if _, err := time.Parse(time.RFC3339, s.At); err != nil {
		return fmt.Errorf("email.status at must be RFC3339")
	}
	return nil
}

// Seal domains for the bridge's blobs. The context is the blob or upload ID,
// so a blob cannot be swapped for another one sealed to the same key.
const (
	EmailBlobSealDomain   = "poweur/email/blob/v1"   // bridge → user (attachments, raw)
	EmailUploadSealDomain = "poweur/email/upload/v1" // user → bridge (outbound attachments)
)

// EmailBlobContext is the seal context for blob or upload id.
func EmailBlobContext(id string) []byte { return []byte("email-blob:" + id) }

// CanonicalEmailUpload is the string a user signs to upload an outbound
// attachment to their bridge: who, which upload, what bytes, when.
func CanonicalEmailUpload(identity, uploadID, sha256Hex string, size int64, issuedAt, nonce string) string {
	return strings.Join([]string{
		"poweur-email-upload",
		strings.ToLower(strings.TrimSpace(identity)),
		uploadID,
		strings.ToLower(sha256Hex),
		fmt.Sprintf("%d", size),
		issuedAt,
		nonce,
	}, "\n")
}
