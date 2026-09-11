package identity

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Typed messages, threads, expiry and metadata (EPIC-009 E09-T3).
//
// The envelope has always reserved `type`, `thread_id`, `expires_at` and
// `metadata`; this file is where they stop being reserved. Everything here is
// canonical Go — the relay, the CLI and `@poweur/client` all derive their
// behaviour from these rules, and the conformance vectors pin the TypeScript
// side to them.
//
// # Why these four are plaintext on the envelope
//
// The payload is E2E-encrypted and the relay must never read it. These four
// fields are the deliberate exception: the relay routes on `type` (inbox
// policy), will expire on `expires_at` (E09-T6), and `thread_id` is what lets
// a client group a conversation without opening every message first. Anything
// placed here is visible to both relays on the path. **Put nothing private in
// `metadata`** — it is addressing, not content.

// MsgTypeChatText is the default message type. An envelope with no `type` is
// exactly a `chat.text`: absence is the wire encoding of the default, and a
// receiving client normalizes it on read.
//
// Clients SHOULD leave `type` absent for ordinary chat rather than writing
// `chat.text` explicitly. Both are valid and mean the same thing, but the
// absent form is byte-identical to what every pre-typing client sends, which
// is what keeps old and new implementations signing the same string.
const MsgTypeChatText = "chat.text"

// System message types the platform owns. `sys.*` is reserved: no application
// may define one, and a relay rejects an unregistered `sys.*` envelope rather
// than routing it (see SystemMessageTypes).
//
// MsgTypeContactRequest / Accept / Block live in inboxpolicy.go, next to the
// policy that acts on them.
const (
	MsgTypeShareOffer   = "sys.share.offer"   // EPIC-005 pcp-0003
	MsgTypeShareAccept  = "sys.share.accept"  // EPIC-005 pcp-0003
	MsgTypeShareRevoked = "sys.share.revoked" // EPIC-005 pcp-0003
	MsgTypeSyncChanged  = "sys.sync.changed"  // EPIC-004 pcp-0005
	MsgTypeAbuseReport  = "sys.abuse.report"  // EPIC-007
)

// SysPrefix is the reserved platform namespace.
const SysPrefix = "sys."

// systemMessageTypes is the closed set of `sys.*` types this protocol
// revision knows. It is mirrored in conventions/registry.json, and
// TestConventionsRegistryValid asserts the two agree in both directions —
// registering a type in one place and forgetting the other is a test failure,
// not a runtime surprise.
var systemMessageTypes = []string{
	MsgTypeContactRequest,
	MsgTypeContactAccept,
	MsgTypeContactBlock,
	MsgTypeShareOffer,
	MsgTypeShareAccept,
	MsgTypeShareRevoked,
	MsgTypeSyncChanged,
	MsgTypeAbuseReport,
}

// SystemMessageTypes returns the reserved `sys.*` types, sorted.
func SystemMessageTypes() []string {
	out := append([]string(nil), systemMessageTypes...)
	sort.Strings(out)
	return out
}

// IsSystemType reports whether t is in the reserved platform namespace,
// registered or not.
func IsSystemType(t string) bool { return strings.HasPrefix(t, SysPrefix) }

// IsKnownSystemType reports whether t is a registered platform type.
func IsKnownSystemType(t string) bool {
	for _, known := range systemMessageTypes {
		if t == known {
			return true
		}
	}
	return false
}

// NormalizeMessageType maps the wire value to its meaning: absent is
// MsgTypeChatText. It never rewrites the envelope — the envelope is signed,
// and the absent form is part of what was signed.
func NormalizeMessageType(t string) string {
	if strings.TrimSpace(t) == "" {
		return MsgTypeChatText
	}
	return t
}

// Envelope extension limits. Deliberately small: these fields are routing
// metadata the relay stores in plaintext for every undelivered message, not a
// place to put content.
const (
	MaxMessageTypeLen = 64
	MaxThreadIDLen    = 128
	MaxMetadataKeys   = 16
	MaxMetadataKeyLen = 40
	MaxMetadataValLen = 256
	// MaxMetadataBytes caps the whole map once serialized, so sixteen
	// maximum-length values cannot together dwarf the envelope.
	MaxMetadataBytes = 2048
)

// ValidateMessageType checks an envelope type. The empty string is valid and
// means MsgTypeChatText.
//
// Shape: two or more lowercase dot-separated segments (`chat.text`,
// `sys.contact.request`, `net.poweur.tasks.assigned`). A single bare word is
// rejected so every type carries a namespace, which is what makes the
// registry able to say who owns it.
func ValidateMessageType(t string) error {
	if t == "" {
		return nil
	}
	if len(t) > MaxMessageTypeLen {
		return fmt.Errorf("message type too long (max %d)", MaxMessageTypeLen)
	}
	segments := strings.Split(t, ".")
	if len(segments) < 2 {
		return fmt.Errorf("message type %q must be namespaced (e.g. chat.text)", t)
	}
	for _, seg := range segments {
		if seg == "" {
			return fmt.Errorf("message type %q has an empty segment", t)
		}
		for i := 0; i < len(seg); i++ {
			c := seg[i]
			switch {
			case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			case c == '-' && i > 0 && i < len(seg)-1:
			default:
				return fmt.Errorf("message type %q: invalid character %q", t, string(seg[i]))
			}
		}
	}
	return nil
}

// ValidateThreadID checks a thread identifier. Threads are opaque to the
// relay: it never invents one, never rewrites one, and only checks that the
// value is a single safe line — the canonical string puts it on a line of its
// own, so a control character in it would make the signing input ambiguous.
func ValidateThreadID(id string) error {
	if id == "" {
		return nil
	}
	if len(id) > MaxThreadIDLen {
		return fmt.Errorf("thread_id too long (max %d)", MaxThreadIDLen)
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '_', c == '-', c == '.', c == ':', c == '@', c == '+', c == '~':
		default:
			return fmt.Errorf("thread_id: invalid character %q (allowed: A-Z a-z 0-9 _ - . : @ + ~)", string(c))
		}
	}
	return nil
}

// ValidateExpiresAt checks the optional expiry stamp. Enforcement — refusing
// delivery of an expired envelope — is E09-T6; this revision only fixes the
// format and binds it into the signature so the value cannot be added,
// removed or moved by anyone on the path.
func ValidateExpiresAt(value string) error {
	if value == "" {
		return nil
	}
	if _, err := time.Parse(time.RFC3339, value); err != nil {
		return fmt.Errorf("expires_at must be RFC3339: %w", err)
	}
	return nil
}

// ValidateMetadata checks the envelope metadata map.
//
// Values are flat strings, not arbitrary JSON, and that is the whole point.
// Two implementations signing "the same JSON object" have to agree on key
// order, number formatting, unicode escaping and nesting; a flat map of
// printable strings has exactly one serialization in every language, which is
// what a signature over it needs. A caller wanting structure encodes it into
// a value itself and owns its stability.
//
// Control characters are rejected rather than escaped, which is what lets
// MetadataLines emit `meta:<key>:<value>` with no escaping scheme at all.
func ValidateMetadata(md map[string]string) error {
	if len(md) == 0 {
		return nil
	}
	if len(md) > MaxMetadataKeys {
		return fmt.Errorf("metadata has %d keys (max %d)", len(md), MaxMetadataKeys)
	}
	total := 0
	for key, value := range md {
		if err := validateMetadataKey(key); err != nil {
			return err
		}
		if len(value) > MaxMetadataValLen {
			return fmt.Errorf("metadata[%q]: value too long (max %d)", key, MaxMetadataValLen)
		}
		for i := 0; i < len(value); i++ {
			if value[i] < 0x20 || value[i] == 0x7f {
				return fmt.Errorf("metadata[%q]: value must not contain control characters", key)
			}
		}
		total += len(key) + len(value) + 6 // "meta:" + ":" per line
	}
	if total > MaxMetadataBytes {
		return fmt.Errorf("metadata too large (%d bytes, max %d)", total, MaxMetadataBytes)
	}
	return nil
}

func validateMetadataKey(key string) error {
	if key == "" {
		return fmt.Errorf("metadata: empty key")
	}
	if len(key) > MaxMetadataKeyLen {
		return fmt.Errorf("metadata key %q too long (max %d)", key, MaxMetadataKeyLen)
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case (c == '_' || c == '-' || c == '.') && i > 0:
		default:
			return fmt.Errorf("metadata key %q: invalid character %q (allowed: a-z 0-9 _ - . and not leading)", key, string(c))
		}
	}
	return nil
}

// MetadataLines renders metadata into canonical signing lines: one
// `meta:<key>:<value>` per entry, keys ascending.
//
// Keys are restricted to ASCII, so Go's byte-wise sort and JavaScript's
// default UTF-16 code-unit sort produce the same order — the two
// implementations cannot disagree about it.
//
// Callers must have validated the map (ValidateMetadata); an unvalidated map
// containing a newline would produce an ambiguous signing input, which is
// exactly why the relay rejects one at ingress.
func MetadataLines(md map[string]string) []string {
	if len(md) == 0 {
		return nil
	}
	keys := make([]string, 0, len(md))
	for k := range md {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		lines = append(lines, "meta:"+k+":"+md[k])
	}
	return lines
}

// ValidateEnvelopeExtensions checks the four E09-T3 fields together. Returned
// errors are safe to show a client: they name the field and the rule.
func ValidateEnvelopeExtensions(msgType, threadID, expiresAt string, metadata map[string]string) error {
	if err := ValidateMessageType(msgType); err != nil {
		return err
	}
	if err := ValidateThreadID(threadID); err != nil {
		return err
	}
	if err := ValidateExpiresAt(expiresAt); err != nil {
		return err
	}
	return ValidateMetadata(metadata)
}
