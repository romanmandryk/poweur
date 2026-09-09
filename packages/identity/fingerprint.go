package identity

import (
	"crypto/ed25519"
	"crypto/sha256"
	"fmt"
	"strings"
)

// Short authentication strings (EPIC-007 E07-T4).
//
// Key pinning is only as good as the out-of-band comparison that bootstraps
// it, and nobody compares 43 characters of base64url over the phone. A
// fingerprint is the human-comparable projection of a public key: short
// enough to read aloud, long enough that forging a colliding key is not
// worth doing.
//
// # Why digits and not emoji
//
// Emoji short-auth-strings look friendlier and are marginally denser per
// symbol, but they lose on every axis that matters to *this* protocol:
//
//   - Poweur's primary comparison surface is a terminal. Emoji rendering in
//     terminals ranges from correct to double-width-misaligned to a row of
//     tofu, and the CLI cannot detect which it got.
//   - The comparison is usually spoken — a phone call, a hallway, a video
//     call. Digits are pronounceable identically in every language a user
//     might share with their contact; emoji names are not (🙂 is "slightly
//     smiling face", "sonriendo", or "the smiley one", depending who reads).
//   - Digits are typeable, so a fingerprint can be dictated *into* a client,
//     searched for in a chat log, and printed on paper.
//   - An emoji alphabet is a versioned dependency: adding or reordering one
//     symbol silently changes everybody's fingerprint. Digits have no such
//     table.
//
// So: **numeric**, in the Signal safety-number tradition — four groups of
// five digits, e.g. `48213 90577 10466 82395`.
//
// # Derivation
//
//	canonical   = "poweur-fingerprint-v1" LF <normalized key string>
//	digest      = SHA-256(canonical)
//	group i     = uint40(digest[5i : 5i+5]) mod 100000, zero-padded to 5
//	fingerprint = groups 0..3 joined with a single space
//
// The normalized key string carries its algorithm prefix (`ed25519:…`,
// `x25519:…`), so a signing key and an encryption key with the same bytes
// cannot share a fingerprint, and the base64 variants a document might use
// all converge on one value. 20 digits is ~66 bits — a second-preimage
// search an attacker must run against a *specific* victim's pin, which is
// the threat pinning actually faces.
const (
	// FingerprintDomain separates this hash from every other SHA-256 in the
	// protocol. Changing it changes every fingerprint, so it is versioned.
	FingerprintDomain = "poweur-fingerprint-v1"

	// FingerprintGroups is how many digit groups a fingerprint has.
	FingerprintGroups = 4
	// FingerprintGroupDigits is the digit count per group.
	FingerprintGroupDigits = 5

	// fingerprintChunkBytes is how many digest bytes feed one group (40 bits
	// reduced mod 100000 — the modulo bias is under 2^-23 per group).
	fingerprintChunkBytes = 5
)

// KeyFingerprint returns the short auth string for an Ed25519 signing key.
// Accepts any form ParseEd25519PublicKey accepts ("ed25519:<base64url>" or
// bare base64, any padding variant); all of them fingerprint identically.
func KeyFingerprint(key string) (string, error) {
	pub, err := ParseEd25519PublicKey(key)
	if err != nil {
		return "", fmt.Errorf("cannot fingerprint signing key: %w", err)
	}
	return fingerprintOf(FormatEd25519PublicKey(pub)), nil
}

// KeyFingerprintBytes is KeyFingerprint for an already-parsed key.
func KeyFingerprintBytes(pub ed25519.PublicKey) string {
	return fingerprintOf(FormatEd25519PublicKey(pub))
}

// EncryptionKeyFingerprint returns the short auth string for an X25519
// encryption key. Distinct from KeyFingerprint even for identical bytes:
// the algorithm prefix is inside the hash.
func EncryptionKeyFingerprint(key string) (string, error) {
	pub, err := ParseX25519PublicKey(key)
	if err != nil {
		return "", fmt.Errorf("cannot fingerprint encryption key: %w", err)
	}
	return fingerprintOf(FormatX25519PublicKey(pub)), nil
}

// FingerprintOrKey is the display helper clients should use: the
// fingerprint when the key parses, the raw string when it does not, so a
// malformed pin still shows the user *something* to compare rather than
// vanishing from the output.
func FingerprintOrKey(key string) string {
	if fp, err := KeyFingerprint(key); err == nil {
		return fp
	}
	return key
}

// fingerprintOf hashes an already-normalized key string.
func fingerprintOf(normalizedKey string) string {
	sum := sha256.Sum256([]byte(FingerprintDomain + "\n" + normalizedKey))
	groups := make([]string, 0, FingerprintGroups)
	for i := 0; i < FingerprintGroups; i++ {
		var n uint64
		for _, b := range sum[i*fingerprintChunkBytes : (i+1)*fingerprintChunkBytes] {
			n = n<<8 | uint64(b)
		}
		groups = append(groups, fmt.Sprintf("%0*d", FingerprintGroupDigits, n%100000))
	}
	return strings.Join(groups, " ")
}
