package identity

import (
	"encoding/json"
	"fmt"
)

// Inbox policy modes (EPIC-007). The relay evaluates the recipient's policy
// after signature verification on every inbound message.
//
//   - open: any valid Poweur ID may message (today's behavior)
//   - contacts_only: only accepted contacts; everyone else policy_rejected
//   - contacts_and_requests: accepted contacts message normally; a
//     non-contact's first sys.contact.request lands in the requests queue,
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

// InboxPolicy is the schema of poweur-sys/relay/inbox-policy.json.
type InboxPolicy struct {
	Version int    `json:"version"`
	Mode    string `json:"mode"`
}

// Validate checks the policy document.
func (p InboxPolicy) Validate() error {
	if p.Version != 0 && p.Version != 1 {
		return fmt.Errorf("unsupported inbox-policy version %d", p.Version)
	}
	switch p.Mode {
	case InboxOpen, InboxContactsOnly, InboxContactsAndRequests:
		return nil
	default:
		return fmt.Errorf("invalid inbox policy mode %q (want %s, %s or %s)",
			p.Mode, InboxOpen, InboxContactsOnly, InboxContactsAndRequests)
	}
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
