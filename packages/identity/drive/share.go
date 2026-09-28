package drive

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"
	"time"

	identity "github.com/poweur/identity"
)

// Shares (E20-T7). A share grants one member — an identity, or the holder of
// a link — a role on one node and, through inheritance, on everything below
// it. It is signed by its issuer (the drive owner, or a member holding admin
// on the node) and carries the node's private key sealed for the member, so
// the member can open names, keys and content the relay cannot.
//
// Roles are capabilities, not a ladder: `append` lets a member add records
// to an append file and `create` lets them add new children to a folder,
// and neither lets them read. `read` opens content; `write` also replaces,
// moves and removes; `admin` also shares, trims and rotates keys. A member's
// effective access is the union of every share on the node and its
// ancestors.

// Share roles.
const (
	RoleRead   = "read"
	RoleWrite  = "write"
	RoleAppend = "append"
	RoleCreate = "create"
	RoleAdmin  = "admin"
)

// ShareKDF names the link-password derivation: argon2id with 64 MiB memory,
// three passes and one lane over the password and the share's salt,
// producing 64 bytes. The first 32 bytes are mixed into the link's
// decryption key; the last 32 are the verifier a viewer presents. The relay
// stores only SHA-256 of the verifier and never sees the password.
const ShareKDF = "argon2id-m65536-t3-p1"

// Caps limit what a member or link may add, and how often a link may be
// used. Zero means unlimited.
type Caps struct {
	Bytes     uint64 `json:"bytes,omitempty"`
	Files     uint64 `json:"files,omitempty"`
	Records   uint64 `json:"records,omitempty"`
	Downloads uint64 `json:"downloads,omitempty"`
	PerHour   uint64 `json:"per_hour,omitempty"`
}

// Share is one signed grant.
type Share struct {
	Format     int    `json:"format"`
	Drive      string `json:"drive"`
	ID         string `json:"id"`
	Node       string `json:"node"`
	Member     string `json:"member,omitempty"`
	Link       string `json:"link,omitempty"`
	Role       string `json:"role"`
	Generation uint64 `json:"generation"`
	// NodeKey is the node's private key sealed to the member's encryption
	// key (or, for a link, to a key derived from the link's fragment). It is
	// absent for append- and create-only shares, which need only NodePublic.
	NodeKey    *identity.SealedPayload `json:"node_key,omitempty"`
	NodePublic string                  `json:"node_public"`
	Expires    string                  `json:"expires,omitempty"`
	Caps       Caps                    `json:"caps"`
	// Link passwords (see ShareKDF): the salt and SHA-256 of the verifier.
	KDF          string `json:"kdf,omitempty"`
	Salt         string `json:"salt,omitempty"`
	VerifierHash string `json:"verifier_hash,omitempty"`
	// PoW is the proof-of-work difficulty (leading zero bits) an anonymous
	// link write must carry; zero for none.
	PoW       uint64 `json:"pow,omitempty"`
	Issuer    string `json:"issuer"`
	Issued    string `json:"issued"`
	Signature string `json:"signature"`
}

// NewShareID returns a random share or link ID.
func NewShareID() (string, error) { return randomHex(16) }

// RoleGrants reports whether holding role satisfies need.
func RoleGrants(role, need string) bool {
	switch need {
	case RoleRead:
		return role == RoleRead || role == RoleWrite || role == RoleAdmin
	case RoleWrite:
		return role == RoleWrite || role == RoleAdmin
	case RoleAppend:
		return role == RoleAppend || role == RoleWrite || role == RoleAdmin
	case RoleCreate:
		return role == RoleCreate || role == RoleWrite || role == RoleAdmin
	case RoleAdmin:
		return role == RoleAdmin
	}
	return false
}

// KeyBearing reports whether a role receives the node's private key.
func KeyBearing(role string) bool {
	return role == RoleRead || role == RoleWrite || role == RoleAdmin
}

func validTime(value string) bool {
	parsed, err := time.Parse(time.RFC3339, value)
	return err == nil && parsed.UTC().Format(time.RFC3339) == value
}

func (s Share) Validate() error {
	if s.Format != 1 {
		return errors.New("unsupported share format")
	}
	if !validIdentity(s.Drive) || !validIdentity(s.Issuer) {
		return errors.New("invalid drive or issuer")
	}
	if !validHex(s.ID, 16) || !validHex(s.Node, 16) {
		return errors.New("invalid share or node ID")
	}
	if (s.Member == "") == (s.Link == "") {
		return errors.New("a share names a member or a link, exclusively")
	}
	if s.Member != "" && !validIdentity(s.Member) || s.Link != "" && !validHex(s.Link, 16) {
		return errors.New("invalid member or link")
	}
	switch s.Role {
	case RoleRead, RoleWrite, RoleAppend, RoleCreate, RoleAdmin:
	default:
		return errors.New("invalid role")
	}
	if s.Link != "" && s.Role == RoleAdmin {
		return errors.New("links cannot administer")
	}
	if s.Generation == 0 || s.Generation > MaxCounter {
		return errors.New("invalid generation")
	}
	if KeyBearing(s.Role) != (s.NodeKey != nil) {
		return errors.New("read, write and admin shares carry the node key; append and create do not")
	}
	if s.NodeKey != nil {
		if err := validatePayload(*s.NodeKey); err != nil {
			return err
		}
	}
	if raw, err := base64.RawURLEncoding.Strict().DecodeString(s.NodePublic); err != nil || len(raw) != 32 || base64.RawURLEncoding.EncodeToString(raw) != s.NodePublic {
		return errors.New("invalid node public key")
	}
	if s.Expires != "" && !validTime(s.Expires) || !validTime(s.Issued) {
		return errors.New("invalid share times")
	}
	for _, v := range []uint64{s.Caps.Bytes, s.Caps.Files, s.Caps.Records, s.Caps.Downloads, s.Caps.PerHour, s.PoW} {
		if v > MaxCounter {
			return errors.New("share limit out of range")
		}
	}
	if s.PoW > uint64(identity.PowMaxBits) {
		return errors.New("proof-of-work difficulty above the protocol maximum")
	}
	password := s.KDF != "" || s.Salt != "" || s.VerifierHash != ""
	if password {
		salt, err := base64.RawURLEncoding.Strict().DecodeString(s.Salt)
		if s.Link == "" || s.KDF != ShareKDF || err != nil || len(salt) != 16 || !validHex(s.VerifierHash, 32) {
			return errors.New("invalid link password parameters")
		}
	}
	return nil
}

// Canonical encodes the share domain and length-prefixed fields: format,
// drive, id, node, member, link, role, generation, node key (three fields),
// node public key, expires, caps (bytes, files, records, downloads, per
// hour), kdf, salt, verifier hash, pow, issuer, issued.
func (s Share) Canonical() ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	n := func(v uint64) string { return strconv.FormatUint(v, 10) }
	fields := []string{"1", s.Drive, s.ID, s.Node, s.Member, s.Link, s.Role, n(s.Generation)}
	fields = append(fields, sealedFields(s.NodeKey)...)
	fields = append(fields, s.NodePublic, s.Expires, n(s.Caps.Bytes), n(s.Caps.Files), n(s.Caps.Records),
		n(s.Caps.Downloads), n(s.Caps.PerHour), s.KDF, s.Salt, s.VerifierHash, n(s.PoW), s.Issuer, s.Issued)
	return lengthPrefixed("poweur/drive/share/v1\n", fields), nil
}

func (s *Share) Sign(privateKey ed25519.PrivateKey) error {
	if len(privateKey) != ed25519.PrivateKeySize {
		return errors.New("invalid signing key length")
	}
	raw, err := s.Canonical()
	if err != nil {
		return err
	}
	s.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, raw))
	return nil
}

func (s Share) Verify(publicKey ed25519.PublicKey) error {
	raw, err := s.Canonical()
	if err != nil {
		return err
	}
	sig, err := base64.RawURLEncoding.Strict().DecodeString(s.Signature)
	if err != nil || base64.RawURLEncoding.EncodeToString(sig) != s.Signature || len(publicKey) != ed25519.PublicKeySize || len(sig) != ed25519.SignatureSize || !ed25519.Verify(publicKey, raw, sig) {
		return errors.New("share signature verification failed")
	}
	return nil
}

// Hash identifies the signed share.
func (s Share) Hash() (string, error) {
	raw, err := s.Canonical()
	if err != nil {
		return "", err
	}
	sig, err := base64.RawURLEncoding.Strict().DecodeString(s.Signature)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append(append([]byte("poweur/drive/share-hash/v1\n"), raw...), sig...))
	return hex.EncodeToString(sum[:]), nil
}

// ExpiredAt reports whether the share has expired at now.
func (s Share) ExpiredAt(now time.Time) bool {
	if s.Expires == "" {
		return false
	}
	expires, err := time.Parse(time.RFC3339, s.Expires)
	return err != nil || !now.Before(expires)
}

// VerifierHash is SHA-256 of a link-password verifier, lowercase hex.
func VerifierHash(verifier []byte) string {
	sum := sha256.Sum256(verifier)
	return hex.EncodeToString(sum[:])
}
