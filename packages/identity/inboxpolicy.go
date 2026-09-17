package identity

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ReadReceiptPolicy controls third-tick receipts. Absence means enabled for
// backward-compatible messaging UX; users may disable them globally or for
// specific contacts. The list is intentionally identity names, not petnames.
type ReadReceiptPolicy struct {
	Enabled     bool     `json:"enabled"`
	DisabledFor []string `json:"disabled_for,omitempty"`
}

func (p ReadReceiptPolicy) Validate() error {
	if len(p.DisabledFor) > 1000 {
		return fmt.Errorf("read_receipts.disabled_for has too many entries (max 1000)")
	}
	seen := make(map[string]bool, len(p.DisabledFor))
	for _, raw := range p.DisabledFor {
		name := strings.ToLower(strings.TrimSpace(raw))
		if err := ValidateIdentityName(name); err != nil {
			return fmt.Errorf("read_receipts.disabled_for: %w", err)
		}
		if seen[name] {
			return fmt.Errorf("read_receipts.disabled_for contains duplicate %q", name)
		}
		seen[name] = true
	}
	return nil
}

// SendsReadReceiptsTo applies the opt-out policy for one peer.
func (p InboxPolicy) SendsReadReceiptsTo(peer string) bool {
	if p.ReadReceipts == nil {
		return true
	}
	if !p.ReadReceipts.Enabled {
		return false
	}
	peer = strings.ToLower(strings.TrimSpace(peer))
	for _, disabled := range p.ReadReceipts.DisabledFor {
		if strings.EqualFold(strings.TrimSpace(disabled), peer) {
			return false
		}
	}
	return true
}

// Inbox policy modes (EPIC-007). The relay evaluates the recipient's policy
// after signature verification on every inbound message.
//
//   - open: any valid Poweur ID may message (today's behavior). Contact
//     request and accept envelopes still ride the requests queue, so the
//     handshake does not depend on who may send chat
//   - contacts_only: only accepted contacts; everyone else policy_rejected
//     — including contact requests
//   - contacts_and_requests: accepted contacts message normally; a
//     stranger's first sys.contact.request lands in the requests queue,
//     anything else is rejected until accepted
//
// Absence of inbox-policy.json means DefaultInboxMode — open, for backward
// compatibility with pre-policy identities. Clients SHOULD write an
// explicit policy at identity creation (contacts_and_requests is the
// recommended human default). Anonymous (unsigned) ingress is a separate
// opt-in block specified in EPIC-014 and always defaults to deny.
const (
	InboxOpen                = "open"
	InboxContactsOnly        = "contacts_only"
	InboxContactsAndRequests = "contacts_and_requests"

	DefaultInboxMode = InboxOpen
)

// Anonymous sender challenges (EPIC-014). "verified" and "payment" are
// designed policy slots — the relay answers with the typed challenge
// envelope but enforcement is not implemented in v1.
const (
	AnonChallengeNone     = "none"
	AnonChallengePow      = "pow"
	AnonChallengeVerified = "verified"
	AnonChallengePayment  = "payment"

	// Defaults applied when the anonymous block leaves fields zero.
	AnonDefaultMaxBytes  = 4096
	AnonDefaultMaxPerDay = 20
)

// AnonymousPolicy is the opt-in block for unsigned (anonymous) senders
// (EPIC-014). Absent block or Allow=false = deny — the default is always
// closed.
type AnonymousPolicy struct {
	Allow bool `json:"allow"`
	// Challenge the sender must pass: none | pow | verified | payment
	// ("" = none).
	Challenge string `json:"challenge,omitempty"`
	// PowBits is the recipient's difficulty dial (0 = issuer default;
	// clamped into [PowMinBits, PowMaxBits]).
	PowBits int `json:"pow_bits,omitempty"`
	// MaxBytes caps the anonymous message payload (0 = 4096).
	MaxBytes int `json:"max_bytes,omitempty"`
	// MaxPerDay caps accepted anonymous messages per day (0 = 20).
	MaxPerDay int `json:"max_per_day,omitempty"`
}

// EffectiveChallenge normalizes the challenge field.
func (a AnonymousPolicy) EffectiveChallenge() string {
	if a.Challenge == "" {
		return AnonChallengeNone
	}
	return a.Challenge
}

// EffectiveMaxBytes returns the payload cap with the default applied.
func (a AnonymousPolicy) EffectiveMaxBytes() int {
	if a.MaxBytes <= 0 {
		return AnonDefaultMaxBytes
	}
	return a.MaxBytes
}

// EffectiveMaxPerDay returns the daily cap with the default applied.
func (a AnonymousPolicy) EffectiveMaxPerDay() int {
	if a.MaxPerDay <= 0 {
		return AnonDefaultMaxPerDay
	}
	return a.MaxPerDay
}

// Validate checks the anonymous block.
func (a AnonymousPolicy) Validate() error {
	switch a.EffectiveChallenge() {
	case AnonChallengeNone, AnonChallengePow, AnonChallengeVerified, AnonChallengePayment:
	default:
		return fmt.Errorf("invalid anonymous challenge %q (want none, pow, verified or payment)", a.Challenge)
	}
	if a.PowBits < 0 || a.PowBits > 64 {
		return fmt.Errorf("pow_bits out of range")
	}
	if a.MaxBytes < 0 || a.MaxBytes > 64*1024 {
		return fmt.Errorf("max_bytes out of range (max 65536)")
	}
	if a.MaxPerDay < 0 || a.MaxPerDay > 10000 {
		return fmt.Errorf("max_per_day out of range (max 10000)")
	}
	return nil
}

// InboxPolicy is the schema of poweur-sys/relay/inbox-policy.json.
type InboxPolicy struct {
	Version int    `json:"version"`
	Mode    string `json:"mode"`
	// Anonymous opts into unsigned senders (EPIC-014); nil = deny.
	Anonymous *AnonymousPolicy `json:"anonymous,omitempty"`
	// ReadReceipts controls third-tick acknowledgements; nil = enabled.
	ReadReceipts *ReadReceiptPolicy `json:"read_receipts,omitempty"`
	// TrustedAuthServices lists the Poweur IDs (OAuth bridges, EPIC-022) that
	// may deliver `sys.auth.request` sign-in prompts. It admits that one type
	// and nothing else: a listed service is not a contact and cannot chat,
	// share or send any other `sys.*` message. Empty = no service may.
	TrustedAuthServices []string `json:"trusted_auth_services,omitempty"`
}

// MaxTrustedAuthServices caps the list; a sign-in prompt source is a
// deliberate, rare choice.
const MaxTrustedAuthServices = 16

// TrustsAuthService reports whether sender may deliver sign-in prompts.
func (p InboxPolicy) TrustsAuthService(sender string) bool {
	sender = strings.ToLower(strings.TrimSpace(sender))
	for _, s := range p.TrustedAuthServices {
		if strings.ToLower(strings.TrimSpace(s)) == sender && sender != "" {
			return true
		}
	}
	return false
}

// Validate checks the policy document.
func (p InboxPolicy) Validate() error {
	if p.Version != 0 && p.Version != 1 {
		return fmt.Errorf("unsupported inbox-policy version %d", p.Version)
	}
	switch p.Mode {
	case InboxOpen, InboxContactsOnly, InboxContactsAndRequests:
	default:
		return fmt.Errorf("invalid inbox policy mode %q (want %s, %s or %s)",
			p.Mode, InboxOpen, InboxContactsOnly, InboxContactsAndRequests)
	}
	if p.Anonymous != nil {
		if err := p.Anonymous.Validate(); err != nil {
			return err
		}
	}
	if p.ReadReceipts != nil {
		if err := p.ReadReceipts.Validate(); err != nil {
			return err
		}
	}
	if len(p.TrustedAuthServices) > MaxTrustedAuthServices {
		return fmt.Errorf("trusted_auth_services has too many entries (max %d)", MaxTrustedAuthServices)
	}
	seen := map[string]bool{}
	for _, s := range p.TrustedAuthServices {
		n := strings.ToLower(strings.TrimSpace(s))
		if err := ValidateIdentityName(n); err != nil {
			return fmt.Errorf("invalid trusted auth service %q: %w", s, err)
		}
		if seen[n] {
			return fmt.Errorf("duplicate trusted auth service %q", n)
		}
		seen[n] = true
	}
	return nil
}

// ParseInboxPolicy decodes and validates inbox-policy.json.
func ParseInboxPolicy(raw []byte) (InboxPolicy, error) {
	var p InboxPolicy
	if err := json.Unmarshal(raw, &p); err != nil {
		return InboxPolicy{}, fmt.Errorf("invalid inbox-policy.json: %w", err)
	}
	if err := p.Validate(); err != nil {
		return InboxPolicy{}, err
	}
	return p, nil
}

// System message types the relay routes on (EPIC-007; envelope-level so the
// relay can act without reading E2E-encrypted payloads). Registered in
// conventions/registry.json (EPIC-006).
const (
	MsgTypeContactRequest = "sys.contact.request"
	MsgTypeContactAccept  = "sys.contact.accept"
	MsgTypeContactBlock   = "sys.contact.block"
)
