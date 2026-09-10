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

// Shareable blocklists (EPIC-007 E07-T5).
//
// A block in contacts.json protects exactly one person: the one who made it.
// That is the right default — consent is personal — but it means every member
// of a community pays the full cost of discovering the same abuser
// independently, which is precisely the asymmetry spam economics feed on.
// A blocklist is a block decision made *portable*: one document, signed by the
// person who made the calls, that somebody else can choose to adopt.
//
// Three properties are deliberate.
//
//   - **Signed, always.** An unsigned blocklist is an invitation to add names
//     to somebody else's list in transit. The publisher signs; the importer
//     verifies against the publisher's resolved identity key, which is the same
//     trust chain everything else here uses.
//   - **Not public.** The document lives in `shared/`, where nobody reads it
//     without an EPIC-005 grant — not in `public/`, which any Poweur ID may
//     read, and not in `poweur-sys/public/`, which the whole web may. A
//     blocklist names people; published to the world it is a denunciation
//     list, and the format should not make that the easy path.
//   - **Adoption is a copy, not a subscription.** Importing writes entries into
//     the importer's own contacts, where they can see and undo them. Nobody
//     ends up with a block they cannot explain because a list they subscribed
//     to grew overnight.

// BlocklistTreePath is where an exported blocklist lives in the owner's tree.
//
// `shared/` and not `poweur-sys/relay/`: the sys zone is owner-and-relay only
// — the permission layer refuses every visitor there, grant or no grant, so a
// blocklist published into it could never actually be adopted by anybody. A
// document whose whole purpose is to be handed to chosen people belongs in
// the one zone EPIC-005 grants reach.
const BlocklistTreePath = "shared/blocks.json"

// MaxBlocklistEntries bounds a blocklist document. Chosen against the 64 KB
// system-document budget the relay validates writes under, not plucked from
// the air: a larger cap would let a document pass schema validation and then
// be refused on size, which is the worst of both answers.
const MaxBlocklistEntries = 500

// MaxBlocklistName bounds the human label a publisher gives their list.
const MaxBlocklistName = 128

// BlockEntry is one blocked identity, optionally with the reason it was
// blocked (from the abuse-report vocabulary, so reports and blocklists sort
// the same way).
type BlockEntry struct {
	Identity string `json:"identity"`
	Reason   string `json:"reason,omitempty"`
	AddedAt  string `json:"added_at,omitempty"`
}

// Blocklist is a signed, shareable set of block decisions.
type Blocklist struct {
	Version   int          `json:"version"`
	Publisher string       `json:"publisher"`
	Name      string       `json:"name,omitempty"`
	Entries   []BlockEntry `json:"entries"`
	UpdatedAt string       `json:"updated_at"`
	Signature string       `json:"signature"`
}

// NewBlocklist builds an unsigned blocklist, normalising identities and
// dropping duplicates (last entry for an identity wins).
func NewBlocklist(publisher, name string, entries []BlockEntry) Blocklist {
	seen := make(map[string]int, len(entries))
	normalised := make([]BlockEntry, 0, len(entries))
	for _, e := range entries {
		e.Identity = strings.ToLower(strings.TrimSpace(e.Identity))
		if e.Identity == "" {
			continue
		}
		if at, ok := seen[e.Identity]; ok {
			normalised[at] = e
			continue
		}
		seen[e.Identity] = len(normalised)
		normalised = append(normalised, e)
	}
	sort.Slice(normalised, func(i, j int) bool { return normalised[i].Identity < normalised[j].Identity })
	return Blocklist{
		Version:   1,
		Publisher: strings.ToLower(strings.TrimSpace(publisher)),
		Name:      strings.TrimSpace(name),
		Entries:   normalised,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
}

// Validate checks everything except the signature.
func (b Blocklist) Validate() error {
	if b.Version != 0 && b.Version != 1 {
		return fmt.Errorf("unsupported blocklist version %d", b.Version)
	}
	publisher := strings.ToLower(strings.TrimSpace(b.Publisher))
	if publisher == "" {
		return fmt.Errorf("publisher is required")
	}
	if len(b.Name) > MaxBlocklistName {
		return fmt.Errorf("name too long (max %d)", MaxBlocklistName)
	}
	if len(b.Entries) > MaxBlocklistEntries {
		return fmt.Errorf("blocklist exceeds %d entries", MaxBlocklistEntries)
	}
	seen := make(map[string]bool, len(b.Entries))
	for i, e := range b.Entries {
		id := strings.ToLower(strings.TrimSpace(e.Identity))
		if id == "" {
			return fmt.Errorf("entry %d: identity is required", i)
		}
		if id == publisher {
			return fmt.Errorf("entry %d: a publisher cannot block themselves", i)
		}
		if seen[id] {
			return fmt.Errorf("duplicate entry %q", id)
		}
		seen[id] = true
		if e.AddedAt != "" {
			if _, err := time.Parse(time.RFC3339, e.AddedAt); err != nil {
				return fmt.Errorf("entry %q: invalid added_at: %w", id, err)
			}
		}
	}
	if strings.TrimSpace(b.UpdatedAt) == "" {
		return fmt.Errorf("updated_at is required")
	}
	if _, err := time.Parse(time.RFC3339, b.UpdatedAt); err != nil {
		return fmt.Errorf("invalid updated_at: %w", err)
	}
	return nil
}

// Canonical returns the string the publisher signs. Entries are sorted and
// rendered as `identity:reason`, so neither JSON key order nor list order can
// change what was signed — and re-labelling an entry does change it.
func (b Blocklist) Canonical() string {
	entries := make([]string, 0, len(b.Entries))
	for _, e := range b.Entries {
		entries = append(entries, strings.ToLower(strings.TrimSpace(e.Identity))+":"+strings.TrimSpace(e.Reason))
	}
	sort.Strings(entries)
	return strings.Join([]string{
		"poweur-blocklist",
		strings.ToLower(strings.TrimSpace(b.Publisher)),
		strings.TrimSpace(b.Name),
		strings.Join(entries, ","),
		b.UpdatedAt,
	}, "\n")
}

// Sign fills Signature using the publisher's identity key.
func (b *Blocklist) Sign(priv ed25519.PrivateKey) error {
	if err := b.Validate(); err != nil {
		return err
	}
	b.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, []byte(b.Canonical())))
	return nil
}

// VerifySignature checks the signature against the publisher's public key.
func (b Blocklist) VerifySignature(pub ed25519.PublicKey) error {
	sig, err := base64.RawURLEncoding.DecodeString(b.Signature)
	if err != nil {
		return fmt.Errorf("invalid signature encoding: %w", err)
	}
	if !ed25519.Verify(pub, []byte(b.Canonical()), sig) {
		return fmt.Errorf("blocklist signature verification failed")
	}
	return nil
}

// ParseBlocklist decodes and validates a blocklist document. An unsigned one
// is refused outright: the caller cannot verify what it was not given, and a
// blocklist is exactly the document an attacker wants to append to.
func ParseBlocklist(raw []byte) (Blocklist, error) {
	var b Blocklist
	if err := json.Unmarshal(raw, &b); err != nil {
		return Blocklist{}, fmt.Errorf("invalid blocklist document: %w", err)
	}
	if err := b.Validate(); err != nil {
		return Blocklist{}, err
	}
	if strings.TrimSpace(b.Signature) == "" {
		return Blocklist{}, fmt.Errorf("blocklist is unsigned")
	}
	return b, nil
}

// BlocklistFromContacts exports the blocked entries of a contacts file.
func BlocklistFromContacts(publisher, name string, contacts ContactsFile) Blocklist {
	entries := make([]BlockEntry, 0)
	for _, c := range contacts.Contacts {
		if c.State != ContactBlocked {
			continue
		}
		entries = append(entries, BlockEntry{Identity: c.Identity, AddedAt: c.AddedAt})
	}
	return NewBlocklist(publisher, name, entries)
}

// BlocklistMerge is the outcome of importing someone else's list.
type BlocklistMerge struct {
	// Blocked are identities newly written into contacts as blocked.
	Blocked []string
	// AlreadyBlocked were already blocked locally.
	AlreadyBlocked []string
	// SkippedAccepted are people the importer has accepted as contacts. A
	// blocklist must never quietly cut somebody off from a person they chose
	// — that is how an adopted list becomes an attack on its adopter.
	SkippedAccepted []string
	// SkippedSelf is set when the list names the importer.
	SkippedSelf bool
}

// ApplyBlocklist merges a verified blocklist into the importer's contacts and
// reports exactly what changed. force adopts entries for identities the
// importer has already accepted; without it those are skipped and named.
func ApplyBlocklist(contacts ContactsFile, importer string, list Blocklist, force bool) (ContactsFile, BlocklistMerge) {
	importer = strings.ToLower(strings.TrimSpace(importer))
	var merge BlocklistMerge
	now := time.Now().UTC().Format(time.RFC3339)
	for _, entry := range list.Entries {
		id := strings.ToLower(strings.TrimSpace(entry.Identity))
		if id == "" {
			continue
		}
		if id == importer {
			merge.SkippedSelf = true
			continue
		}
		existing, found := contacts.Find(id)
		if found && existing.State == ContactBlocked {
			merge.AlreadyBlocked = append(merge.AlreadyBlocked, id)
			continue
		}
		if found && existing.State == ContactAccepted && !force {
			merge.SkippedAccepted = append(merge.SkippedAccepted, id)
			continue
		}
		blocked := Contact{Identity: id, State: ContactBlocked, AddedAt: now}
		if found {
			blocked.AddedAt = existing.AddedAt
			blocked.Petname = existing.Petname
			blocked.PinnedKey = existing.PinnedKey
		}
		// Provenance: months later "why is this person blocked?" has an
		// answer that names the list rather than the importer's own memory.
		blocked.Source = "blocklist:" + strings.ToLower(strings.TrimSpace(list.Publisher))
		contacts = contacts.Upsert(blocked)
		merge.Blocked = append(merge.Blocked, id)
	}
	return contacts, merge
}
