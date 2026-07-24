package identity

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/bits"
	"strings"
	"time"
)

// Proof-of-work challenges (EPIC-014 E14-T2): a Hashcash-style rate limiter
// for "stranger wants in" surfaces — anonymous messages, contact requests
// from unknown relays, hosted registration. Difficulty is measured in
// leading zero BITS of sha256 (each +1 bit doubles the expected work):
// ~12 bits is milliseconds, ~20 bits ≈ a second, ~26 bits ≈ a minute on a
// laptop; browser JS is ~5–10× slower, so recipient-facing UIs should cap
// their dials accordingly. PoW rate-limits — it does not authenticate.
//
// Challenges are STATELESS on the issuing side: the token is an
// HMAC-sealed claim {purpose, bits, expires_at, nonce} keyed by a secret
// only the issuer holds, so a flood of unsolved challenges stores nothing.
// Verified solutions must be single-use — the issuer keeps a small
// seen-cache until token expiry (see the relay's implementation).
//
// Solving: find an ASCII nonce such that
//
//	sha256(token + "." + nonce)
//
// has at least `bits` leading zero bits. The same contract is implemented
// by the browser solver (apps/web/js/pow.js).

// PoW difficulty window. Issuers clamp requested difficulties into
// [PowMinBits, PowMaxBits]; 0 anywhere means "use the default".
const (
	PowMinBits     = 8
	PowMaxBits     = 30
	PowDefaultBits = 16
)

// PowAlgo names the challenge algorithm in envelopes ("algo" field).
const PowAlgo = "sha256-lead0"

// PowChallenge is the sealed claim carried inside a challenge token.
type PowChallenge struct {
	// Purpose binds a token to one surface ("msg:<recipient>",
	// "registration") so a solution cannot be replayed across surfaces.
	Purpose   string `json:"purpose"`
	Bits      int    `json:"bits"`
	ExpiresAt int64  `json:"expires_at"` // unix seconds
	Nonce     string `json:"nonce"`      // issuer randomness (uniqueness)
}

// ClampPowBits normalizes a requested difficulty into the allowed window.
func ClampPowBits(bits int) int {
	if bits <= 0 {
		return PowDefaultBits
	}
	if bits < PowMinBits {
		return PowMinBits
	}
	if bits > PowMaxBits {
		return PowMaxBits
	}
	return bits
}

func powMAC(secret []byte, payload []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	return mac.Sum(nil)
}

// NewPowChallenge mints a sealed challenge token.
func NewPowChallenge(secret []byte, purpose string, bitsWanted int, ttl time.Duration) (token string, challenge PowChallenge, err error) {
	if len(secret) == 0 {
		return "", PowChallenge{}, fmt.Errorf("pow secret is empty")
	}
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "", PowChallenge{}, err
	}
	challenge = PowChallenge{
		Purpose:   strings.ToLower(strings.TrimSpace(purpose)),
		Bits:      ClampPowBits(bitsWanted),
		ExpiresAt: time.Now().Add(ttl).Unix(),
		Nonce:     base64.RawURLEncoding.EncodeToString(buf),
	}
	payload, err := json.Marshal(challenge)
	if err != nil {
		return "", PowChallenge{}, err
	}
	token = base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString(powMAC(secret, payload))
	return token, challenge, nil
}

// ParsePowToken verifies the token's seal and expiry and returns the claim.
func ParsePowToken(secret []byte, token string) (PowChallenge, error) {
	payloadB64, macB64, ok := strings.Cut(token, ".")
	if !ok {
		return PowChallenge{}, fmt.Errorf("malformed challenge token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return PowChallenge{}, fmt.Errorf("malformed challenge token")
	}
	mac, err := base64.RawURLEncoding.DecodeString(macB64)
	if err != nil {
		return PowChallenge{}, fmt.Errorf("malformed challenge token")
	}
	if subtle.ConstantTimeCompare(mac, powMAC(secret, payload)) != 1 {
		return PowChallenge{}, fmt.Errorf("challenge token seal invalid")
	}
	var challenge PowChallenge
	if err := json.Unmarshal(payload, &challenge); err != nil {
		return PowChallenge{}, fmt.Errorf("malformed challenge token")
	}
	if time.Now().Unix() > challenge.ExpiresAt {
		return PowChallenge{}, fmt.Errorf("challenge expired")
	}
	return challenge, nil
}

// leadingZeroBits counts leading zero bits of a digest.
func leadingZeroBits(digest []byte) int {
	n := 0
	for _, b := range digest {
		if b == 0 {
			n += 8
			continue
		}
		n += bits.LeadingZeros8(b)
		break
	}
	return n
}

// CheckPowSolution reports whether solution satisfies the token's
// difficulty (structure only — pair with ParsePowToken for the seal).
func CheckPowSolution(token, solution string, bitsRequired int) bool {
	digest := sha256.Sum256([]byte(token + "." + solution))
	return leadingZeroBits(digest[:]) >= bitsRequired
}

// VerifyPowSolution verifies seal, expiry, purpose and difficulty in one
// call. Single-use bookkeeping is the caller's job (the token's Nonce is
// the natural cache key).
func VerifyPowSolution(secret []byte, token, solution, purpose string) (PowChallenge, error) {
	challenge, err := ParsePowToken(secret, token)
	if err != nil {
		return PowChallenge{}, err
	}
	if challenge.Purpose != strings.ToLower(strings.TrimSpace(purpose)) {
		return PowChallenge{}, fmt.Errorf("challenge purpose mismatch")
	}
	if !CheckPowSolution(token, solution, challenge.Bits) {
		return PowChallenge{}, fmt.Errorf("solution does not meet difficulty (%d bits)", challenge.Bits)
	}
	return challenge, nil
}

// SolvePow searches for a satisfying nonce. Cancellable via ctx; the
// expected work is 2^bits hashes.
func SolvePow(ctx context.Context, token string, bitsRequired int) (string, error) {
	prefix := []byte(token + ".")
	var counter uint64
	buf := make([]byte, 8)
	for {
		if counter%4096 == 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			default:
			}
		}
		binary.BigEndian.PutUint64(buf, counter)
		nonce := base64.RawURLEncoding.EncodeToString(buf)
		digest := sha256.Sum256(append(prefix, nonce...))
		if leadingZeroBits(digest[:]) >= bitsRequired {
			return nonce, nil
		}
		counter++
	}
}
