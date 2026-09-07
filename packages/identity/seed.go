package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
)

// Master-seed key derivation (EPIC-011 E11-T1).
//
// One 32-byte seed derives every long-lived key an identity owns, so recovery
// has exactly one artifact to protect. Derivation is HKDF-SHA256 with an empty
// salt and a distinct info string per purpose; the info strings are protocol
// constants and must never change without a version bump.
//
// Go is the canonical implementation. packages/client-ts/src/crypto/seed.ts
// conforms to it and is pinned by testdata/vectors/seed-derivation.json.

// SeedLen is the length of a master seed in bytes.
const SeedLen = 32

// HKDF info strings. Versioned so a future scheme can coexist.
const (
	SeedInfoSigning    = "poweur/v1/sign"
	SeedInfoEncryption = "poweur/v1/enc"
	SeedInfoVault      = "poweur/v1/vault"
)

// ErrInvalidSeed is returned when a seed is not exactly SeedLen bytes.
var ErrInvalidSeed = errors.New("seed must be 32 bytes")

// NewSeed returns a fresh random master seed.
func NewSeed() ([]byte, error) {
	seed := make([]byte, SeedLen)
	if _, err := io.ReadFull(rand.Reader, seed); err != nil {
		return nil, fmt.Errorf("read random seed: %w", err)
	}
	return seed, nil
}

// ParseSeed decodes an unpadded base64url master seed and checks its length.
// This is the wire/storage form used by recovery kits, keystore entries and
// the CLI's --seed flag.
func ParseSeed(encoded string) ([]byte, error) {
	seed, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return nil, fmt.Errorf("seed must be unpadded base64url: %w", err)
	}
	if len(seed) != SeedLen {
		return nil, fmt.Errorf("%w: decoded %d bytes", ErrInvalidSeed, len(seed))
	}
	return seed, nil
}

// EncodeSeed renders a master seed in the form ParseSeed accepts.
func EncodeSeed(seed []byte) string {
	return base64.RawURLEncoding.EncodeToString(seed)
}

// DeriveSeedKey expands a master seed into 32 bytes for the given info string.
// Callers should prefer the typed helpers below; this is exported for the vault
// key (E11-T6) and for tests that pin the raw expansion.
func DeriveSeedKey(seed []byte, info string) ([]byte, error) {
	if len(seed) != SeedLen {
		return nil, ErrInvalidSeed
	}
	if info == "" {
		return nil, errors.New("derivation info must not be empty")
	}
	// Empty (nil) salt: RFC 5869 substitutes HashLen zero bytes. The TS client
	// relies on the same default — the conformance vectors pin it.
	out := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, seed, nil, []byte(info)), out); err != nil {
		return nil, fmt.Errorf("hkdf expand %q: %w", info, err)
	}
	return out, nil
}

// DeriveSigningKey derives the identity's Ed25519 keypair from the master seed.
// The derived 32 bytes are used as the Ed25519 seed, so the returned private
// key is the standard 64-byte expanded form.
func DeriveSigningKey(seed []byte) (ed25519.PublicKey, ed25519.PrivateKey, error) {
	material, err := DeriveSeedKey(seed, SeedInfoSigning)
	if err != nil {
		return nil, nil, err
	}
	priv := ed25519.NewKeyFromSeed(material)
	return priv.Public().(ed25519.PublicKey), priv, nil
}

// DeriveEncryptionKey derives the identity's X25519 keypair from the master
// seed. The private key is the raw 32-byte HKDF output, stored unclamped to
// match GenerateX25519Keypair in the CLI — curve25519.X25519 clamps internally,
// so the two are interchangeable everywhere a private key is used.
func DeriveEncryptionKey(seed []byte) (publicKey, privateKey []byte, err error) {
	priv, err := DeriveSeedKey(seed, SeedInfoEncryption)
	if err != nil {
		return nil, nil, err
	}
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		return nil, nil, fmt.Errorf("derive x25519 public key: %w", err)
	}
	return pub, priv, nil
}

// DeriveVaultKey derives the symmetric key protecting poweur-sys/private/vault
// (E11-T6). Defined here so the info string lives with its siblings.
func DeriveVaultKey(seed []byte) ([]byte, error) {
	return DeriveSeedKey(seed, SeedInfoVault)
}
