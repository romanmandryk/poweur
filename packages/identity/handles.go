package identity

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Handles elsewhere (INT-006-T5, PCP-0011): `.poweur/public/handles.json`
// links a Poweur ID to identities on other networks so the ID's own name
// works there too. The relay serves what it says in each network's discovery
// format:
//
//	atproto_did   → GET https://<id>/.well-known/atproto-did      (Bluesky handle)
//	nostr_pubkey  → GET https://<id>/.well-known/nostr.json?name=_ (NIP-05 `_@<id>`)
//	                and, for a hosted ID, GET https://<domain>/.well-known/nostr.json?name=<label>
//	                (NIP-05 `<label>@<domain>`, the same text as the ID's email address)
//	fediverse_*   → for a hosted ID, GET https://<domain>/.well-known/webfinger?resource=acct:<label>@<domain>
//	                answers with the linked account (Mastodon follows it), so searching
//	                `@<label>@<domain>` finds `@alice@mastodon.social`
//
// Owner-written and public; the relay validates it on write. Absent means
// none: nothing is served for an ID that did not opt in.

// HandlesPath is the document's path in the identity's system files.
const HandlesPath = ".poweur/public/handles.json"

// MaxNostrRelays bounds the relay hints a NIP-05 answer carries.
const MaxNostrRelays = 8

// Handles is the schema of .poweur/public/handles.json.
type Handles struct {
	Version int `json:"version"`
	// ATProtoDID is the Bluesky account's DID (`did:plc:…` or `did:web:…`).
	ATProtoDID string `json:"atproto_did,omitempty"`
	// NostrPubkey is the Nostr public key as 64 lowercase hex characters
	// (NIP-05 form; clients convert an `npub` before writing).
	NostrPubkey string `json:"nostr_pubkey,omitempty"`
	// NostrRelays are optional `wss://` relay hints for that key.
	NostrRelays []string `json:"nostr_relays,omitempty"`
	// FediverseAcct is the linked Fediverse account, `user@server`.
	FediverseAcct string `json:"fediverse_acct,omitempty"`
	// FediverseActor is that account's ActivityPub actor URL (`https://…`),
	// looked up by the client when it links the account.
	FediverseActor string `json:"fediverse_actor,omitempty"`
}

var (
	didPLCRe      = regexp.MustCompile(`^did:plc:[a-z2-7]{24}$`)
	didWebRe      = regexp.MustCompile(`^did:web:[a-z0-9]([a-z0-9.-]*[a-z0-9])?(%3[aA][0-9]{1,5})?(:[A-Za-z0-9._~-]+)*$`)
	nostrPubkeyRe = regexp.MustCompile(`^[0-9a-f]{64}$`)
	acctRe        = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}@[a-z0-9]([a-z0-9.-]*[a-z0-9])?\.[a-z]{2,63}$`)
)

// ValidFediverseAcct reports whether s is a `user@server` account.
func ValidFediverseAcct(s string) bool { return len(s) <= 256 && acctRe.MatchString(s) }

// validActorURL accepts a plain https URL with a host.
func validActorURL(s string) bool {
	if len(s) > 512 || !strings.HasPrefix(s, "https://") || strings.ContainsAny(s, " \t\r\n\"<>#") {
		return false
	}
	host, _, _ := strings.Cut(strings.TrimPrefix(s, "https://"), "/")
	return host != "" && !strings.ContainsAny(host, "@?")
}

// ValidATProtoDID reports whether s is a DID Bluesky accepts for a handle.
func ValidATProtoDID(s string) bool {
	return len(s) <= 256 && (didPLCRe.MatchString(s) || didWebRe.MatchString(s))
}

// ValidNostrPubkey reports whether s is a hex Nostr public key.
func ValidNostrPubkey(s string) bool { return nostrPubkeyRe.MatchString(s) }

// Validate checks the document.
func (h Handles) Validate() error {
	if h.Version != 1 {
		return fmt.Errorf("unsupported handles version %d", h.Version)
	}
	if h.ATProtoDID != "" && !ValidATProtoDID(h.ATProtoDID) {
		return fmt.Errorf("atproto_did must be a did:plc or did:web identifier")
	}
	if h.NostrPubkey != "" && !ValidNostrPubkey(h.NostrPubkey) {
		return fmt.Errorf("nostr_pubkey must be 64 lowercase hex characters (convert an npub first)")
	}
	if len(h.NostrRelays) > 0 && h.NostrPubkey == "" {
		return fmt.Errorf("nostr_relays need a nostr_pubkey")
	}
	if len(h.NostrRelays) > MaxNostrRelays {
		return fmt.Errorf("too many nostr_relays (max %d)", MaxNostrRelays)
	}
	if (h.FediverseAcct == "") != (h.FediverseActor == "") {
		return fmt.Errorf("fediverse_acct and fediverse_actor go together")
	}
	if h.FediverseAcct != "" && !ValidFediverseAcct(h.FediverseAcct) {
		return fmt.Errorf("fediverse_acct must look like user@server.example")
	}
	if h.FediverseActor != "" && !validActorURL(h.FediverseActor) {
		return fmt.Errorf("fediverse_actor must be an https URL")
	}
	for _, r := range h.NostrRelays {
		if !strings.HasPrefix(r, "wss://") || len(r) > 256 || strings.ContainsAny(r, " \t\r\n\"<>") {
			return fmt.Errorf("nostr relay %q must be a wss:// URL", r)
		}
	}
	return nil
}

// ParseHandles decodes and validates handles.json.
func ParseHandles(raw []byte) (Handles, error) {
	var h Handles
	if err := json.Unmarshal(raw, &h); err != nil {
		return Handles{}, fmt.Errorf("invalid handles.json: %w", err)
	}
	if err := h.Validate(); err != nil {
		return Handles{}, err
	}
	return h, nil
}
