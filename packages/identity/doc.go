// Package identity defines the Poweur Identity Document and web-first
// resolution (HTTPS /.well-known/poweur/ then DNS TXT).
package identity

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// PreviousKey is a retired signing key that remains valid for verification
// until ValidUntil (RFC3339). Empty ValidUntil means informational only.
type PreviousKey struct {
	PublicKey  string `json:"public_key"`
	ValidUntil string `json:"valid_until,omitempty"`
}

// IdentityDocument is the signed, machine-readable description of a Poweur ID.
// Clients sign at registration; relays store and serve but never sign.
type IdentityDocument struct {
	Version             int           `json:"version"`
	Identity            string        `json:"identity"`
	PublicKey           string        `json:"public_key"`
	EncryptionPublicKey string        `json:"encryption_public_key,omitempty"`
	Relay               string        `json:"relay"`
	Capabilities        []string      `json:"capabilities,omitempty"`
	PreviousKeys        []PreviousKey `json:"previous_keys,omitempty"`
	// MovedTo, when set, is a permanent redirect target for resolvers
	// (hosted migration tombstone).
	MovedTo   string `json:"moved_to,omitempty"`
	UpdatedAt string `json:"updated_at"`
	Signature string `json:"signature,omitempty"`
}

// NewDocument builds an unsigned v1 document ready for Sign.
func NewDocument(identity, publicKey, encryptionPublicKey, relay string, caps []string) IdentityDocument {
	if caps == nil {
		caps = []string{"messaging"}
	}
	return IdentityDocument{
		Version:             1,
		Identity:            identity,
		PublicKey:           publicKey,
		EncryptionPublicKey: encryptionPublicKey,
		Relay:               relay,
		Capabilities:        caps,
		UpdatedAt:           time.Now().UTC().Format(time.RFC3339),
	}
}

// CanonicalBytes returns deterministic JSON for signing: sorted object keys,
// no insignificant whitespace, signature field omitted.
func (d IdentityDocument) CanonicalBytes() ([]byte, error) {
	m := map[string]any{
		"version":   d.Version,
		"identity":  d.Identity,
		"public_key": d.PublicKey,
		"relay":     d.Relay,
		"updated_at": d.UpdatedAt,
	}
	if d.EncryptionPublicKey != "" {
		m["encryption_public_key"] = d.EncryptionPublicKey
	}
	if len(d.Capabilities) > 0 {
		caps := make([]any, len(d.Capabilities))
		for i, c := range d.Capabilities {
			caps[i] = c
		}
		m["capabilities"] = caps
	}
	if len(d.PreviousKeys) > 0 {
		pk := make([]any, len(d.PreviousKeys))
		for i, k := range d.PreviousKeys {
			entry := map[string]any{"public_key": k.PublicKey}
			if k.ValidUntil != "" {
				entry["valid_until"] = k.ValidUntil
			}
			pk[i] = entry
		}
		m["previous_keys"] = pk
	}
	if d.MovedTo != "" {
		m["moved_to"] = d.MovedTo
	}
	return marshalCanonical(m)
}

// KeyValidAt reports whether pubKey (bare or ed25519:) is the current key or
// a previous key still within its grace window at instant at.
func (d IdentityDocument) KeyValidAt(pubKey string, at time.Time) bool {
	want := NormalizePublicKeyKey(pubKey)
	if NormalizePublicKeyKey(d.PublicKey) == want {
		return true
	}
	for _, pk := range d.PreviousKeys {
		if NormalizePublicKeyKey(pk.PublicKey) != want {
			continue
		}
		if pk.ValidUntil == "" {
			return false
		}
		until, err := time.Parse(time.RFC3339, pk.ValidUntil)
		if err != nil {
			return false
		}
		return !at.After(until)
	}
	return false
}

func marshalCanonical(v any) ([]byte, error) {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var buf bytes.Buffer
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			kb, err := json.Marshal(k)
			if err != nil {
				return nil, err
			}
			buf.Write(kb)
			buf.WriteByte(':')
			vb, err := marshalCanonical(x[k])
			if err != nil {
				return nil, err
			}
			buf.Write(vb)
		}
		buf.WriteByte('}')
		return buf.Bytes(), nil
	case []any:
		var buf bytes.Buffer
		buf.WriteByte('[')
		for i, el := range x {
			if i > 0 {
				buf.WriteByte(',')
			}
			eb, err := marshalCanonical(el)
			if err != nil {
				return nil, err
			}
			buf.Write(eb)
		}
		buf.WriteByte(']')
		return buf.Bytes(), nil
	default:
		return json.Marshal(x)
	}
}

// Sign fills Signature using the identity Ed25519 private key.
func (d *IdentityDocument) Sign(priv ed25519.PrivateKey) error {
	canon, err := d.CanonicalBytes()
	if err != nil {
		return err
	}
	sig := ed25519.Sign(priv, canon)
	d.Signature = base64.RawURLEncoding.EncodeToString(sig)
	return nil
}

// Verify checks the document signature against PublicKey.
func (d IdentityDocument) Verify() error {
	if d.Version != 1 {
		return fmt.Errorf("unsupported identity document version %d", d.Version)
	}
	if d.Identity == "" || d.PublicKey == "" || d.Relay == "" || d.UpdatedAt == "" {
		return errors.New("identity document missing required fields")
	}
	if d.Signature == "" {
		return errors.New("identity document missing signature")
	}
	pub, err := ParseEd25519PublicKey(d.PublicKey)
	if err != nil {
		return err
	}
	canon, err := d.CanonicalBytes()
	if err != nil {
		return err
	}
	sig, err := decodeBase64(d.Signature)
	if err != nil {
		return fmt.Errorf("invalid signature encoding: %w", err)
	}
	if !ed25519.Verify(pub, canon, sig) {
		return errors.New("identity document signature invalid")
	}
	return nil
}

// ParseEd25519PublicKey accepts raw base64url or "ed25519:<base64url>".
func ParseEd25519PublicKey(s string) (ed25519.PublicKey, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "ed25519:")
	raw, err := decodeBase64(s)
	if err != nil {
		return nil, err
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, errors.New("invalid ed25519 public key length")
	}
	return ed25519.PublicKey(raw), nil
}

// FormatEd25519PublicKey returns "ed25519:<base64url>".
func FormatEd25519PublicKey(pub ed25519.PublicKey) string {
	return "ed25519:" + base64.RawURLEncoding.EncodeToString(pub)
}

// FormatX25519PublicKey returns "x25519:<base64url>".
func FormatX25519PublicKey(pub []byte) string {
	return "x25519:" + base64.RawURLEncoding.EncodeToString(pub)
}

// ParseX25519PublicKey accepts raw base64url or "x25519:<base64url>".
func ParseX25519PublicKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "x25519:")
	raw, err := decodeBase64(s)
	if err != nil {
		return nil, err
	}
	if len(raw) != 32 {
		return nil, errors.New("invalid x25519 public key length")
	}
	return raw, nil
}

// NormalizePublicKeyKey returns bare base64url without scheme prefix.
func NormalizePublicKeyKey(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "ed25519:")
	return s
}

func decodeBase64(value string) ([]byte, error) {
	if decoded, err := base64.RawURLEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	if decoded, err := base64.URLEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	if decoded, err := base64.RawStdEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	return base64.StdEncoding.DecodeString(value)
}

// MarshalJSONDocument returns pretty JSON for storage/HTTP responses.
func (d IdentityDocument) MarshalJSONDocument() ([]byte, error) {
	return json.Marshal(d)
}

// ParseDocument parses and optionally verifies an identity document.
func ParseDocument(data []byte, verify bool) (IdentityDocument, error) {
	var d IdentityDocument
	if err := json.Unmarshal(data, &d); err != nil {
		return IdentityDocument{}, err
	}
	if verify {
		if err := d.Verify(); err != nil {
			return IdentityDocument{}, err
		}
	}
	return d, nil
}
