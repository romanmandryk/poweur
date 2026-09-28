package drive

import (
	"crypto/ed25519"
	"encoding/base32"
	"strings"
)

// Guest authors (E20-T7). An anonymous writer through a link — a file
// request, a public form — has no identity, yet every manifest and record is
// signed by a resolvable author. A guest author is self-certifying: the name
// carries an ephemeral Ed25519 public key, `g<base32 key>.guest.invalid`, so
// the key is read from the name and never looked up. `.invalid` is reserved
// (RFC 2606): the name can never be registered or resolved as an identity.
// Relays accept guest authors only on writes authorized by a link.

// GuestSuffix ends every guest author name.
const GuestSuffix = ".guest.invalid"

var guestEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// GuestAuthor is the author name for an ephemeral signing key.
func GuestAuthor(publicKey ed25519.PublicKey) string {
	return "g" + strings.ToLower(guestEncoding.EncodeToString(publicKey)) + GuestSuffix
}

// GuestKey returns the key a guest author name carries.
func GuestKey(author string) (ed25519.PublicKey, bool) {
	label, ok := strings.CutSuffix(author, GuestSuffix)
	if !ok || !strings.HasPrefix(label, "g") || label != strings.ToLower(label) {
		return nil, false
	}
	raw, err := guestEncoding.DecodeString(strings.ToUpper(label[1:]))
	if err != nil || len(raw) != ed25519.PublicKeySize || GuestAuthor(raw) != author {
		return nil, false
	}
	return ed25519.PublicKey(raw), true
}

// IsGuest reports whether author is a guest author name.
func IsGuest(author string) bool {
	_, ok := GuestKey(author)
	return ok
}
