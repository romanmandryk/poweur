package identity

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Groups (EPIC-005 E05-T5, EPIC-009 E09-T5): a signed member list. The
// format and canonical string are unchanged from storage v1, so existing
// signatures stay valid.

// GroupSelfDoc is where a group identity keeps its own membership: the
// group is a hosted identity, and this is the one system file in its drive
// that makes it a group (E05-T5). The relay reads it to expand group
// messages (EPIC-009 E09-T5).
const GroupSelfDoc = ".poweur/relay/group.json"

// MaxGroupMembers bounds a group's member and admin lists.
const MaxGroupMembers = 1000

// ShareGroup is a signed member list. It covers both kinds of group,
// because they are the same document:
//
//   - **Owner-local groups**: `Group` is a bare name like "team", signed by
//     the owner. It means something only inside that owner's own sharing
//     (storage v1 grants used it; storage v2 shares may again).
//   - **Group identities** (E05-T5): `Group` and `Owner` are both the
//     group's own Poweur ID, the document lives at GroupSelfDoc in the
//     *group's* drive, and it is signed by the group's identity key. It is
//     addressable, and (EPIC-009) it can receive messages.
//
// A group identity additionally carries Admins and Epoch. Those two fields
// append to the canonical string only when Admins is set, so every
// owner-local group signs exactly the five lines it always did.
type ShareGroup struct {
	Group   string   `json:"group"`
	Owner   string   `json:"owner"`
	Members []string `json:"members"`
	// Admins are the identities entitled to update this group's membership.
	// Present only on group identities; its presence is what marks the
	// document as one.
	Admins []string `json:"admins,omitempty"`
	// Epoch is a monotonic membership version. It exists so a relay can
	// refuse a rollback to an older member list, and so EPIC-009's group
	// key agreement (E09-T5) has a membership version to bind keys to
	// without inventing a second counter.
	Epoch     int    `json:"epoch,omitempty"`
	UpdatedAt string `json:"updated_at"`
	Signature string `json:"signature"`
}

// IsGroupIdentity reports whether this document describes an addressable
// group identity rather than an owner-local group.
func (gr ShareGroup) IsGroupIdentity() bool { return len(gr.Admins) > 0 }

// IsGroupIdentityName reports whether a group name refers to a group
// *identity* rather than an owner-local group.
//
// The rule is the presence of a dot: a Poweur ID is a domain name and
// always has one, and owner-local group names are forbidden from having
// one (see Validate). So the two namespaces cannot collide.
func IsGroupIdentityName(name string) bool {
	return strings.Contains(strings.TrimSpace(name), ".")
}

// HasAdmin reports whether identity may update this group's membership.
func (gr ShareGroup) HasAdmin(identity string) bool {
	for _, a := range gr.Admins {
		if strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(identity)) {
			return true
		}
	}
	return false
}

// Validate checks everything except the signature.
func (gr ShareGroup) Validate() error {
	if strings.TrimSpace(gr.Group) == "" {
		return fmt.Errorf("group name is required")
	}
	if strings.ContainsAny(gr.Group, "/\\") {
		return fmt.Errorf("group name must not contain slashes")
	}
	if strings.TrimSpace(gr.Owner) == "" {
		return fmt.Errorf("owner is required")
	}
	if len(gr.Members) > MaxGroupMembers {
		return fmt.Errorf("group exceeds %d members", MaxGroupMembers)
	}
	if gr.UpdatedAt != "" {
		if _, err := time.Parse(time.RFC3339, gr.UpdatedAt); err != nil {
			return fmt.Errorf("invalid updated_at: %w", err)
		}
	}
	return gr.validateIdentityFields()
}

// validateIdentityFields applies the group-identity rules (E05-T5).
func (gr ShareGroup) validateIdentityFields() error {
	name := strings.TrimSpace(gr.Group)
	if !gr.IsGroupIdentity() {
		// An owner-local group name must stay out of the identity
		// namespace, or {"group": "team.acme.poweur.net"} would be
		// ambiguous.
		if IsGroupIdentityName(name) {
			return fmt.Errorf("an owner-local group name must not look like a Poweur ID (no dots); a group identity needs an admins list")
		}
		if gr.Epoch != 0 {
			return fmt.Errorf("epoch belongs to group identities, which need an admins list")
		}
		return nil
	}
	// A group identity is its own owner: the membership document lives in
	// the group's drive and is signed by the group's identity key, so there
	// is nobody else it could belong to.
	if !strings.EqualFold(name, strings.TrimSpace(gr.Owner)) {
		return fmt.Errorf("a group identity's group and owner must both be its own Poweur ID")
	}
	if !IsGroupIdentityName(name) {
		return fmt.Errorf("a group identity's name must be a Poweur ID")
	}
	if len(gr.Admins) > MaxGroupMembers {
		return fmt.Errorf("group exceeds %d admins", MaxGroupMembers)
	}
	if gr.Epoch < 0 {
		return fmt.Errorf("epoch must not be negative")
	}
	for _, a := range gr.Admins {
		if strings.TrimSpace(a) == "" {
			return fmt.Errorf("admins must not contain an empty entry")
		}
	}
	return nil
}

// Canonical returns the string the signer commits to (members sorted).
//
// A group identity appends three more lines — the marker, the sorted admin
// list and the epoch — so an admin list or a membership version cannot be
// edited off a signed document by whoever stores it. A group with no
// admins signs exactly the five lines it always did, so introducing group
// identities invalidated no existing owner-local group signature.
func (gr ShareGroup) Canonical() string {
	members := make([]string, 0, len(gr.Members))
	for _, m := range gr.Members {
		members = append(members, strings.ToLower(strings.TrimSpace(m)))
	}
	sort.Strings(members)
	fields := []string{
		"poweur-share-group",
		strings.ToLower(strings.TrimSpace(gr.Group)),
		strings.ToLower(strings.TrimSpace(gr.Owner)),
		strings.Join(members, ","),
		gr.UpdatedAt,
	}
	if gr.IsGroupIdentity() {
		admins := make([]string, 0, len(gr.Admins))
		for _, a := range gr.Admins {
			admins = append(admins, strings.ToLower(strings.TrimSpace(a)))
		}
		sort.Strings(admins)
		fields = append(fields,
			"poweur-group-identity",
			strings.Join(admins, ","),
			strconv.Itoa(gr.Epoch),
		)
	}
	return strings.Join(fields, "\n")
}

// Sign fills Signature using the owner's identity key.
func (gr *ShareGroup) Sign(priv ed25519.PrivateKey) error {
	if err := gr.Validate(); err != nil {
		return err
	}
	gr.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, []byte(gr.Canonical())))
	return nil
}

// VerifySignature checks the signature against the owner's public key.
func (gr ShareGroup) VerifySignature(pub ed25519.PublicKey) error {
	sig, err := base64.RawURLEncoding.DecodeString(gr.Signature)
	if err != nil {
		return fmt.Errorf("invalid signature encoding: %w", err)
	}
	if !ed25519.Verify(pub, []byte(gr.Canonical()), sig) {
		return fmt.Errorf("group signature verification failed")
	}
	return nil
}

// ParseShareGroup decodes and validates one group document.
func ParseShareGroup(raw []byte) (ShareGroup, error) {
	var gr ShareGroup
	if err := json.Unmarshal(raw, &gr); err != nil {
		return ShareGroup{}, fmt.Errorf("invalid group document: %w", err)
	}
	if err := gr.Validate(); err != nil {
		return ShareGroup{}, err
	}
	if strings.TrimSpace(gr.Signature) == "" {
		return ShareGroup{}, fmt.Errorf("group document is unsigned")
	}
	return gr, nil
}

// HasMember reports whether identity is in the group (case-insensitive).
func (gr ShareGroup) HasMember(identity string) bool {
	for _, m := range gr.Members {
		if strings.EqualFold(strings.TrimSpace(m), identity) {
			return true
		}
	}
	return false
}
