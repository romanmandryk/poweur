package identity

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Abuse reports (EPIC-007 E07-T5).
//
// Inbox policy gives a person a way to refuse a sender. It gives nobody a way
// to tell that sender's *operator* what happened, which means an operator
// hosting a spammer learns about it from Twitter or not at all. `sys.abuse.report`
// is that missing channel: a signed complaint, addressed to the relay that
// hosts the subject, about traffic that already arrived.
//
// # Why it is a document delivered to an endpoint and not a message
//
// Every other `sys.*` type rides the message envelope to an identity's inbox.
// This one cannot: its recipient is a relay *operator*, who is not an identity
// and has no encryption key to seal an envelope to. So the type names the
// document, and the document is POSTed to the subject's relay. The distinction
// is worth keeping straight — a report is not private mail to a person, it is
// a signed statement to an infrastructure operator.
//
// # What a report deliberately does not carry
//
// No message content. The traffic being reported is end-to-end encrypted; the
// operator could not read it anyway, and shipping the plaintext would hand a
// third party a copy of the reporter's own private conversation in the name of
// protecting them. A report carries message *IDs* — which the operator can
// corroborate against their own delivery logs without ever seeing what was
// said — plus a reason and an optional note the reporter chose to write.
//
// The signature is what makes the report worth acting on: an anonymous
// complaint stream is itself an abuse vector, and a signed one lets an
// operator weigh reporters, notice a brigade, and answer.

// MsgTypeAbuseReport is declared in msgtypes.go, alongside the rest of the
// reserved `sys.*` namespace and the systemMessageTypes list that
// conventions_test.go checks the registry against (EPIC-009 E09-T3).

// Abuse report reasons. Deliberately a short closed set: an operator triaging
// reports needs to sort them, and free text does not sort. Anything that does
// not fit is AbuseReasonOther plus a note.
const (
	AbuseReasonSpam        = "spam"
	AbuseReasonHarassment  = "harassment"
	AbuseReasonPhishing    = "phishing"
	AbuseReasonMalware     = "malware"
	AbuseReasonImpersonate = "impersonation"
	AbuseReasonOther       = "other"
)

// Limits on a report document.
const (
	MaxAbuseNote       = 2048
	MaxAbuseMessageIDs = 32
)

// AbuseReasons lists every accepted reason, for clients that offer a choice.
func AbuseReasons() []string {
	return []string{
		AbuseReasonSpam, AbuseReasonHarassment, AbuseReasonPhishing,
		AbuseReasonMalware, AbuseReasonImpersonate, AbuseReasonOther,
	}
}

// AbuseReport is a signed complaint about a subject, addressed to the relay
// that hosts them.
type AbuseReport struct {
	Version  int    `json:"version"`
	Type     string `json:"type"`
	Reporter string `json:"reporter"`
	Subject  string `json:"subject"`
	Reason   string `json:"reason"`
	// MessageIDs point at traffic the operator can find in their own logs.
	// Evidence, not content.
	MessageIDs []string `json:"message_ids,omitempty"`
	Note       string   `json:"note,omitempty"`
	CreatedAt  string   `json:"created_at"`
	Signature  string   `json:"signature"`
}

// NewAbuseReport builds an unsigned report with the fixed fields filled in.
func NewAbuseReport(reporter, subject, reason string, messageIDs []string, note string) AbuseReport {
	return AbuseReport{
		Version:    1,
		Type:       MsgTypeAbuseReport,
		Reporter:   strings.ToLower(strings.TrimSpace(reporter)),
		Subject:    strings.ToLower(strings.TrimSpace(subject)),
		Reason:     strings.ToLower(strings.TrimSpace(reason)),
		MessageIDs: messageIDs,
		Note:       note,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
	}
}

// Validate checks everything except the signature.
func (a AbuseReport) Validate() error {
	if a.Version != 0 && a.Version != 1 {
		return fmt.Errorf("unsupported abuse report version %d", a.Version)
	}
	if a.Type != "" && a.Type != MsgTypeAbuseReport {
		return fmt.Errorf("abuse report type must be %q", MsgTypeAbuseReport)
	}
	reporter := strings.ToLower(strings.TrimSpace(a.Reporter))
	subject := strings.ToLower(strings.TrimSpace(a.Subject))
	if reporter == "" {
		return fmt.Errorf("reporter is required")
	}
	if subject == "" {
		return fmt.Errorf("subject is required")
	}
	if reporter == subject {
		return fmt.Errorf("an identity cannot report itself")
	}
	valid := false
	for _, r := range AbuseReasons() {
		if a.Reason == r {
			valid = true
			break
		}
	}
	if !valid {
		return fmt.Errorf("invalid reason %q (want one of %s)", a.Reason, strings.Join(AbuseReasons(), ", "))
	}
	if len(a.MessageIDs) > MaxAbuseMessageIDs {
		return fmt.Errorf("too many message_ids (max %d)", MaxAbuseMessageIDs)
	}
	for i, id := range a.MessageIDs {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("message_ids[%d] is empty", i)
		}
	}
	if len(a.Note) > MaxAbuseNote {
		return fmt.Errorf("note too long (max %d)", MaxAbuseNote)
	}
	if strings.TrimSpace(a.CreatedAt) == "" {
		return fmt.Errorf("created_at is required")
	}
	if _, err := time.Parse(time.RFC3339, a.CreatedAt); err != nil {
		return fmt.Errorf("invalid created_at: %w", err)
	}
	return nil
}

// Canonical returns the string the reporter signs. Field order is fixed and
// message IDs are sorted, so JSON key order and list order cannot change what
// was signed.
func (a AbuseReport) Canonical() string {
	ids := make([]string, 0, len(a.MessageIDs))
	for _, id := range a.MessageIDs {
		ids = append(ids, strings.TrimSpace(id))
	}
	sort.Strings(ids)
	return strings.Join([]string{
		"poweur-abuse-report",
		strings.ToLower(strings.TrimSpace(a.Reporter)),
		strings.ToLower(strings.TrimSpace(a.Subject)),
		strings.ToLower(strings.TrimSpace(a.Reason)),
		strings.Join(ids, ","),
		a.Note,
		a.CreatedAt,
	}, "\n")
}

// Sign fills Signature using the reporter's identity key.
func (a *AbuseReport) Sign(priv ed25519.PrivateKey) error {
	if err := a.Validate(); err != nil {
		return err
	}
	a.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, []byte(a.Canonical())))
	return nil
}

// VerifySignature checks the signature against the reporter's public key.
func (a AbuseReport) VerifySignature(pub ed25519.PublicKey) error {
	sig, err := base64.RawURLEncoding.DecodeString(a.Signature)
	if err != nil {
		return fmt.Errorf("invalid signature encoding: %w", err)
	}
	if !ed25519.Verify(pub, []byte(a.Canonical()), sig) {
		return fmt.Errorf("abuse report signature verification failed")
	}
	return nil
}

// ParseAbuseReport decodes and validates a report document. The signature is
// verified separately, by the caller that resolved the reporter's key.
func ParseAbuseReport(raw []byte) (AbuseReport, error) {
	var a AbuseReport
	if err := json.Unmarshal(raw, &a); err != nil {
		return AbuseReport{}, fmt.Errorf("invalid abuse report: %w", err)
	}
	if err := a.Validate(); err != nil {
		return AbuseReport{}, err
	}
	if strings.TrimSpace(a.Signature) == "" {
		return AbuseReport{}, fmt.Errorf("abuse report is unsigned")
	}
	return a, nil
}
