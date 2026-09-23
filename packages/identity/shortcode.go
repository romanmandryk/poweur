package identity

import (
	"crypto/rand"
	"errors"
	"strings"
)

// Short codes are what a person reads off one screen and types into another:
// an add-device pairing (E11-T8) and a sign-in request by reference
// (E08-T6). Eight characters of Crockford base32 is 40 bits — unguessable
// within the minutes a code lives, behind per-identity and per-address
// limits — and forgiving to type: no I, L, O or U, case-insensitive, and the
// letters people confuse with digits read as those digits.
//
// A short code locates something; it never protects a secret by itself.

const (
	// ShortCodeLen is the number of characters in a short code.
	ShortCodeLen = 8
	shortAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
)

// ErrShortCode is returned for input that is not a short code.
var ErrShortCode = errors.New("not a valid code")

// NewShortCode returns a fresh random code, unformatted ("K7QM4XP2").
func NewShortCode() (string, error) {
	var raw [ShortCodeLen]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	out := make([]byte, ShortCodeLen)
	for i, b := range raw {
		out[i] = shortAlphabet[int(b)%len(shortAlphabet)] // 256 is a multiple of 32: no bias
	}
	return string(out), nil
}

// NormalizeShortCode turns what a person typed into the canonical code:
// spaces and dashes (including the unicode ones phone keyboards substitute)
// dropped, upper-cased, O read as 0 and I or L as 1.
func NormalizeShortCode(input string) (string, error) {
	var b strings.Builder
	for _, r := range input {
		switch {
		case r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '-' || r == '_' || r == '.':
			continue
		case r == '\u00a0' || r == '\u202f' || r == '\u2007' || r == '\u200b' || r == '\u200c' || r == '\u200d' || r == '\ufeff':
			continue
		case r >= '\u2010' && r <= '\u2015', r == '\u2212', r == '\ufe58', r == '\ufe63', r == '\uff0d':
			continue
		}
		c := r
		if c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		switch c {
		case 'O':
			c = '0'
		case 'I', 'L':
			c = '1'
		}
		if c > 127 || !strings.ContainsRune(shortAlphabet, c) {
			return "", ErrShortCode
		}
		b.WriteRune(c)
	}
	if b.Len() != ShortCodeLen {
		return "", ErrShortCode
	}
	return b.String(), nil
}

// FormatShortCode renders a code for reading: "K7QM-4XP2".
func FormatShortCode(code string) string {
	if len(code) != ShortCodeLen {
		return code
	}
	return code[:4] + "-" + code[4:]
}
