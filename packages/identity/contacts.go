package identity

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Contacts & inbox policy (EPIC-007, schema home EPIC-006). Both files live
// in the relay-readable config zone: the relay must read them to enforce
// message acceptance; other users never see them.
//
//	.poweur/relay/contacts.json      owner-written, relay-read
//	.poweur/relay/inbox-policy.json  owner-written, relay-read

// Contact states.
const (
	ContactRequested = "requested"
	ContactAccepted  = "accepted"
	ContactBlocked   = "blocked"
)

// MaxContacts bounds contacts.json (documents are re-read per message).
const MaxContacts = 10000

// Contact is one entry in contacts.json. PinnedKey records the contact's
// signing key at accept time (TOFU): clients alert when the resolved key
// later differs and no signed rotation statement covers the change.
type Contact struct {
	Identity  string   `json:"identity"`
	State     string   `json:"state"`
	PinnedKey string   `json:"pinned_key,omitempty"` // "ed25519:<base64url>"
	Petname   string   `json:"petname,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	AddedAt   string   `json:"added_at,omitempty"`
	Source    string   `json:"source,omitempty"` // how the intro happened
}

// ContactsFile is the schema of .poweur/relay/contacts.json.
type ContactsFile struct {
	Version  int       `json:"version"`
	Contacts []Contact `json:"contacts"`
}

// Validate checks structural rules (identity syntax stays loose on purpose —
// the resolver decides what resolves).
func (f ContactsFile) Validate() error {
	if f.Version != 0 && f.Version != 1 {
		return fmt.Errorf("unsupported contacts version %d", f.Version)
	}
	if len(f.Contacts) > MaxContacts {
		return fmt.Errorf("contacts exceed %d entries", MaxContacts)
	}
	seen := make(map[string]bool, len(f.Contacts))
	for i, c := range f.Contacts {
		id := strings.ToLower(strings.TrimSpace(c.Identity))
		if id == "" {
			return fmt.Errorf("contact %d: identity is required", i)
		}
		if seen[id] {
			return fmt.Errorf("duplicate contact %q", id)
		}
		seen[id] = true
		switch c.State {
		case ContactRequested, ContactAccepted, ContactBlocked:
		default:
			return fmt.Errorf("contact %q: invalid state %q (want requested, accepted, blocked)", id, c.State)
		}
		if c.PinnedKey != "" {
			if _, err := ParseEd25519PublicKey(c.PinnedKey); err != nil {
				return fmt.Errorf("contact %q: invalid pinned_key: %w", id, err)
			}
		}
		if c.AddedAt != "" {
			if _, err := time.Parse(time.RFC3339, c.AddedAt); err != nil {
				return fmt.Errorf("contact %q: invalid added_at: %w", id, err)
			}
		}
	}
	return nil
}

// ParseContactsFile decodes and validates contacts.json.
func ParseContactsFile(raw []byte) (ContactsFile, error) {
	var f ContactsFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return ContactsFile{}, fmt.Errorf("invalid contacts.json: %w", err)
	}
	if err := f.Validate(); err != nil {
		return ContactsFile{}, err
	}
	return f, nil
}

// Find returns the contact entry for identity (case-insensitive).
func (f ContactsFile) Find(identity string) (Contact, bool) {
	for _, c := range f.Contacts {
		if strings.EqualFold(strings.TrimSpace(c.Identity), strings.TrimSpace(identity)) {
			return c, true
		}
	}
	return Contact{}, false
}

// Upsert replaces or appends the entry for c.Identity and returns the file.
func (f ContactsFile) Upsert(c Contact) ContactsFile {
	if f.Version == 0 {
		f.Version = 1
	}
	for i, existing := range f.Contacts {
		if strings.EqualFold(existing.Identity, c.Identity) {
			f.Contacts[i] = c
			return f
		}
	}
	f.Contacts = append(f.Contacts, c)
	return f
}

// Remove drops the entry for identity; reports whether it was present.
func (f *ContactsFile) Remove(identity string) bool {
	for i, c := range f.Contacts {
		if strings.EqualFold(c.Identity, identity) {
			f.Contacts = append(f.Contacts[:i], f.Contacts[i+1:]...)
			return true
		}
	}
	return false
}
