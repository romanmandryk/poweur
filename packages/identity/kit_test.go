package identity

import (
	"bytes"
	"strings"
	"testing"
)

func TestMnemonic_RoundTrip(t *testing.T) {
	for _, seed := range [][]byte{
		make([]byte, SeedLen),
		VectorSeed,
		bytes.Repeat([]byte{0xff}, SeedLen),
	} {
		mnemonic, err := SeedToMnemonic(seed)
		if err != nil {
			t.Fatal(err)
		}
		if got := len(strings.Fields(mnemonic)); got != MnemonicWords {
			t.Fatalf("want %d words, got %d", MnemonicWords, got)
		}
		back, err := MnemonicToSeed(mnemonic)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(back, seed) {
			t.Fatal("mnemonic did not round-trip")
		}
	}
}

// A kit read off paper arrives with odd spacing and capitals; only a real
// typo should be rejected.
func TestMnemonic_NormalizesHumanInput(t *testing.T) {
	mnemonic, err := SeedToMnemonic(VectorSeed)
	if err != nil {
		t.Fatal(err)
	}
	words := strings.Fields(mnemonic)
	messy := "  " + strings.ToUpper(strings.Join(words[:4], "  ")) + "\n" +
		strings.Join(words[4:], "\t") + "  "
	seed, err := MnemonicToSeed(messy)
	if err != nil {
		t.Fatalf("messy but valid mnemonic rejected: %v", err)
	}
	if !bytes.Equal(seed, VectorSeed) {
		t.Fatal("normalized mnemonic decoded to the wrong seed")
	}
}

// The checksum is the reason to use words at all.
func TestMnemonic_RejectsTypos(t *testing.T) {
	mnemonic, err := SeedToMnemonic(VectorSeed)
	if err != nil {
		t.Fatal(err)
	}
	words := strings.Fields(mnemonic)

	// A single swapped word must fail the checksum.
	swapped := append([]string{}, words...)
	if swapped[0] == "zoo" {
		swapped[0] = "abandon"
	} else {
		swapped[0] = "zoo"
	}
	if ValidMnemonic(strings.Join(swapped, " ")) {
		t.Fatal("a substituted word must fail the checksum")
	}

	// Transposed words: same set, wrong order.
	transposed := append([]string{}, words...)
	transposed[0], transposed[1] = transposed[1], transposed[0]
	if transposed[0] != transposed[1] && ValidMnemonic(strings.Join(transposed, " ")) {
		t.Fatal("transposed words must fail the checksum")
	}

	for _, bad := range []string{
		"", "   ",
		strings.Join(words[:23], " "),                    // too short
		strings.Join(append(words, words[0]), " "),       // too long
		strings.Repeat("notaword ", MnemonicWords),       // off-wordlist
	} {
		if ValidMnemonic(bad) {
			t.Fatalf("invalid mnemonic accepted: %q", bad)
		}
	}
}

func TestSeedToMnemonic_RejectsWrongSeedLength(t *testing.T) {
	if _, err := SeedToMnemonic(make([]byte, 16)); err == nil {
		t.Fatal("expected error for a 16-byte seed")
	}
}

// Every --seed flag accepts either encoding, so a pasted kit just works.
func TestParseSeedOrMnemonic_AcceptsBothEncodings(t *testing.T) {
	mnemonic, err := SeedToMnemonic(VectorSeed)
	if err != nil {
		t.Fatal(err)
	}
	fromWords, err := ParseSeedOrMnemonic(mnemonic)
	if err != nil {
		t.Fatal(err)
	}
	fromB64, err := ParseSeedOrMnemonic(EncodeSeed(VectorSeed))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fromWords, VectorSeed) || !bytes.Equal(fromB64, VectorSeed) {
		t.Fatal("both encodings must decode to the same seed")
	}
	if _, err := ParseSeedOrMnemonic("not a valid anything at all here ok"); err == nil {
		t.Fatal("expected error for gibberish")
	}
}

func TestNewRecoveryKit(t *testing.T) {
	kit, err := NewRecoveryKit("alice.poweur.net", "relay.poweur.net", VectorSeed)
	if err != nil {
		t.Fatal(err)
	}
	if kit.Seed != EncodeSeed(VectorSeed) {
		t.Fatal("kit seed mismatch")
	}
	back, err := MnemonicToSeed(kit.Mnemonic)
	if err != nil || !bytes.Equal(back, VectorSeed) {
		t.Fatal("kit mnemonic does not decode to the seed")
	}
	// Both encodings must name the same secret — a kit whose halves disagree
	// would be worse than no kit.
	fromSeed, err := ParseSeed(kit.Seed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fromSeed, back) {
		t.Fatal("kit encodings disagree")
	}
}

// Conformance vectors: the TS client must produce identical words.
func TestVectors_RecoveryKit(t *testing.T) {
	type kitVector struct {
		Name     string `json:"name"`
		Seed     string `json:"seed"`
		Mnemonic string `json:"mnemonic"`
	}
	cases := []struct {
		name string
		seed []byte
	}{
		{"zeros", make([]byte, SeedLen)},
		{"counter", VectorSeed},
		{"ones", bytes.Repeat([]byte{0xff}, SeedLen)},
	}
	vectors := make([]kitVector, 0, len(cases))
	for _, tc := range cases {
		mnemonic, err := SeedToMnemonic(tc.seed)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		vectors = append(vectors, kitVector{
			Name: tc.name, Seed: EncodeSeed(tc.seed), Mnemonic: mnemonic,
		})
	}
	WriteVectors(t, vectorsDir, "recovery-kit", map[string]any{
		"words":   MnemonicWords,
		"vectors": vectors,
	})
}
