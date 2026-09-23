package identity

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Pairing v2: moving an identity's seed to a new device (EPIC-011 E11-T8).
//
// The relay is untrusted for this: it must never be able to read the seed,
// nor substitute a key of its own for the new device's. The new device makes
// an ephemeral X25519 key K and a random r and first sends only
// C = SHA-256("poweur/v2/enroll-commit" ‖ K ‖ r). The approving device then
// contributes a random n. Only after that does the new device reveal K and r.
//
//   - Scanned: the QR carries C itself, so the approver checks K against a
//     value the relay never touched — no digits to compare.
//   - Typed: both screens show PairingSAS(C, K, r, n). A relay that swaps in
//     its own key had to commit to it before n existed; the digits depend on
//     n, so the swap matches with probability 10^-PairingSASDigits, once, in
//     front of the user.
//
// v1 derived the digits from K alone. The relay sees K, so it could grind a
// key with the same digits offline in seconds; this construction exists
// because that one was broken.

// PairingSASDigits is the length of the comparison code.
const PairingSASDigits = 6

// ErrPairing is returned for malformed pairing values.
var ErrPairing = errors.New("invalid pairing value")

// PairingCommitment is C for an ephemeral public key and commit nonce, both
// base64url as sent on the wire.
func PairingCommitment(ephemeralPublicKey, commitNonce string) string {
	sum := sha256.Sum256([]byte("poweur/v2/enroll-commit\n" + ephemeralPublicKey + "\n" + commitNonce))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// PairingSAS is the six digits both devices show on the typed path.
func PairingSAS(commitment, ephemeralPublicKey, commitNonce, approverNonce string) string {
	sum := sha256.Sum256([]byte("poweur/v2/enroll-sas\n" + commitment + "\n" + ephemeralPublicKey + "\n" +
		commitNonce + "\n" + approverNonce))
	return fmt.Sprintf("%06d", binary.BigEndian.Uint64(sum[:8])%1_000_000)
}

// VerifyPairingReveal checks that a revealed key and nonce open the
// commitment the approver saw first.
func VerifyPairingReveal(commitment, ephemeralPublicKey, commitNonce string) error {
	if err := checkB64Len(ephemeralPublicKey, 32); err != nil {
		return fmt.Errorf("%w: ephemeral key: %v", ErrPairing, err)
	}
	if err := checkB64Len(commitNonce, 32); err != nil {
		return fmt.Errorf("%w: commit nonce: %v", ErrPairing, err)
	}
	if PairingCommitment(ephemeralPublicKey, commitNonce) != commitment {
		return fmt.Errorf("%w: the revealed key does not match the commitment — do not approve", ErrPairing)
	}
	return nil
}

// CheckPairingCommitment validates a commitment's shape.
func CheckPairingCommitment(commitment string) error {
	if err := checkB64Len(commitment, sha256.Size); err != nil {
		return fmt.Errorf("%w: commitment: %v", ErrPairing, err)
	}
	return nil
}

func checkB64Len(v string, n int) error {
	raw, err := base64.RawURLEncoding.DecodeString(v)
	if err != nil {
		return errors.New("not base64url")
	}
	if len(raw) != n {
		return fmt.Errorf("want %d bytes, got %d", n, len(raw))
	}
	return nil
}

// PairingLink is what the new device's QR carries: the approver's app, with
// the code, the commitment and the identity being joined in the fragment, so
// none of it reaches a server log. The identity tells an app holding several
// which one to unlock, and lets an approver refuse a link for someone else.
func PairingLink(appURL, identity, code, commitment string) string {
	link := strings.TrimRight(appURL, "#") + "#pair=" + code + "." + commitment
	if identity != "" {
		link += "&id=" + url.QueryEscape(strings.ToLower(identity))
	}
	return link
}

// PairingAppLink is the same pairing for the Poweur app: a phone's camera
// opens poweur:// in the app, where an https link would open a website that
// holds no keys. The values ride in the query: some scanners drop fragments
// of custom-scheme links.
func PairingAppLink(identity, code, commitment string) string {
	link := "poweur://pair?pair=" + code + "." + commitment
	if identity != "" {
		link += "&id=" + url.QueryEscape(strings.ToLower(identity))
	}
	return link
}

// PairingLinkParts is what a pairing link says.
type PairingLinkParts struct {
	Code       string
	Commitment string
	Identity   string // empty when the link does not name one
}

// ParsePairingLink reads a scanned or pasted pairing link — the web link
// (values in the fragment), the app link (values in the query), or
// "CODE.COMMITMENT".
func ParsePairingLink(input string) (PairingLinkParts, error) {
	v := strings.TrimSpace(input)
	if i := strings.Index(v, "#"); i >= 0 {
		v = v[i+1:]
	} else if i := strings.Index(v, "?"); i >= 0 {
		v = v[i+1:]
	}
	var parts PairingLinkParts
	if frag, perr := url.ParseQuery(v); perr == nil && frag.Get("pair") != "" {
		v = frag.Get("pair")
		parts.Identity = strings.ToLower(strings.TrimSpace(frag.Get("id")))
	}
	v = strings.TrimPrefix(v, "pair=")
	dot := strings.LastIndex(v, ".")
	if dot < 0 {
		return PairingLinkParts{}, fmt.Errorf("%w: not a pairing link", ErrPairing)
	}
	code, err := NormalizeShortCode(v[:dot])
	if err != nil {
		return PairingLinkParts{}, fmt.Errorf("%w: %v", ErrPairing, err)
	}
	parts.Code, parts.Commitment = code, v[dot+1:]
	if err := CheckPairingCommitment(parts.Commitment); err != nil {
		return PairingLinkParts{}, err
	}
	return parts, nil
}
