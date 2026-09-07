package identity

import (
	"errors"
	"fmt"
	"strings"

	bip39 "github.com/tyler-smith/go-bip39"
)

// Recovery kit encoding (EPIC-011 E11-T1).
//
// A kit is the master seed rendered for whoever has to carry it. BIP39 is an
// *encoding of the same 32 bytes*, not a second secret: 24 words with a
// checksum that catches transcription errors and a wordlist chosen so no two
// words share a four-letter prefix. It earns its keep only where a human
// copies the seed by hand — for scripts and secret stores, base64url
// (EncodeSeed) remains the right form.
//
// The carrier is deliberately not specified. A printed card, a PDF, a text
// file and a password-manager entry are all valid; this package only converts.

// MnemonicWords is the word count for a 32-byte seed (256 bits + 8 checksum).
const MnemonicWords = 24

// ErrInvalidMnemonic is returned when a mnemonic fails its checksum or shape.
var ErrInvalidMnemonic = errors.New("invalid recovery mnemonic")

// SeedToMnemonic encodes a master seed as a 24-word BIP39 mnemonic.
func SeedToMnemonic(seed []byte) (string, error) {
	if len(seed) != SeedLen {
		return "", ErrInvalidSeed
	}
	mnemonic, err := bip39.NewMnemonic(seed)
	if err != nil {
		return "", fmt.Errorf("encode mnemonic: %w", err)
	}
	return mnemonic, nil
}

// MnemonicToSeed decodes a 24-word BIP39 mnemonic back to the master seed.
// Whitespace and case are normalized first, so a hand-typed kit with odd
// spacing or capitals still works — the checksum is what rejects a typo.
func MnemonicToSeed(mnemonic string) ([]byte, error) {
	normalized := NormalizeMnemonic(mnemonic)
	if normalized == "" {
		return nil, ErrInvalidMnemonic
	}
	if got := len(strings.Fields(normalized)); got != MnemonicWords {
		return nil, fmt.Errorf("%w: expected %d words, got %d", ErrInvalidMnemonic, MnemonicWords, got)
	}
	seed, err := bip39.EntropyFromMnemonic(normalized)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidMnemonic, err)
	}
	if len(seed) != SeedLen {
		return nil, fmt.Errorf("%w: decoded %d bytes", ErrInvalidSeed, len(seed))
	}
	return seed, nil
}

// NormalizeMnemonic lowercases and collapses whitespace. BIP39 mnemonics are
// compared as space-separated lowercase words; a kit read off paper rarely
// arrives that way.
func NormalizeMnemonic(mnemonic string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(mnemonic))), " ")
}

// ValidMnemonic reports whether a mnemonic decodes to a usable seed.
func ValidMnemonic(mnemonic string) bool {
	_, err := MnemonicToSeed(mnemonic)
	return err == nil
}

// RecoveryKit is everything needed to restore an identity, minus the trust
// decision about where to keep it.
type RecoveryKit struct {
	Identity string `json:"identity"`
	Relay    string `json:"relay,omitempty"`
	Mnemonic string `json:"mnemonic"`
	Seed     string `json:"seed"`
}

// NewRecoveryKit builds a kit from a seed. Both encodings are included: the
// mnemonic for paper, the base64url seed for machines.
func NewRecoveryKit(identity, relay string, seed []byte) (RecoveryKit, error) {
	mnemonic, err := SeedToMnemonic(seed)
	if err != nil {
		return RecoveryKit{}, err
	}
	return RecoveryKit{
		Identity: identity,
		Relay:    relay,
		Mnemonic: mnemonic,
		Seed:     EncodeSeed(seed),
	}, nil
}

// ParseSeedOrMnemonic accepts either encoding, so every "--seed" flag can take
// a pasted mnemonic without a second option. Multi-word input is treated as a
// mnemonic; anything else as base64url.
func ParseSeedOrMnemonic(value string) ([]byte, error) {
	trimmed := strings.TrimSpace(value)
	if strings.Contains(trimmed, " ") {
		return MnemonicToSeed(trimmed)
	}
	return ParseSeed(trimmed)
}
