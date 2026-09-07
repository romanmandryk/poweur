package identity

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"testing"

	"golang.org/x/crypto/curve25519"
)

func TestDeriveSeedKey_RejectsBadInput(t *testing.T) {
	good := bytes.Repeat([]byte{7}, SeedLen)
	for _, tc := range []struct {
		name string
		seed []byte
		info string
	}{
		{"short seed", bytes.Repeat([]byte{1}, 31), SeedInfoSigning},
		{"long seed", bytes.Repeat([]byte{1}, 33), SeedInfoSigning},
		{"nil seed", nil, SeedInfoSigning},
		{"empty info", good, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DeriveSeedKey(tc.seed, tc.info); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestDeriveSeedKey_DomainSeparation(t *testing.T) {
	seed := bytes.Repeat([]byte{9}, SeedLen)
	sign, err := DeriveSeedKey(seed, SeedInfoSigning)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := DeriveSeedKey(seed, SeedInfoEncryption)
	if err != nil {
		t.Fatal(err)
	}
	vault, err := DeriveSeedKey(seed, SeedInfoVault)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(sign, enc) || bytes.Equal(sign, vault) || bytes.Equal(enc, vault) {
		t.Fatal("info strings must produce independent keys")
	}
}

func TestDeriveSeedKey_Deterministic(t *testing.T) {
	seed := bytes.Repeat([]byte{3}, SeedLen)
	a, err := DeriveSeedKey(seed, SeedInfoSigning)
	if err != nil {
		t.Fatal(err)
	}
	b, err := DeriveSeedKey(seed, SeedInfoSigning)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("derivation must be deterministic")
	}
	// A one-bit seed change must change the output.
	seed2 := bytes.Repeat([]byte{3}, SeedLen)
	seed2[31] ^= 0x01
	c, err := DeriveSeedKey(seed2, SeedInfoSigning)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, c) {
		t.Fatal("distinct seeds must produce distinct keys")
	}
}

// Derived keys must be usable by the primitives the protocol already runs on:
// a signature verifies, and an X25519 exchange agrees in both directions.
func TestDerivedKeys_AreUsable(t *testing.T) {
	seed := bytes.Repeat([]byte{5}, SeedLen)

	pub, priv, err := DeriveSigningKey(seed)
	if err != nil {
		t.Fatal(err)
	}
	msg := []byte("poweur seed derivation")
	if !ed25519.Verify(pub, msg, ed25519.Sign(priv, msg)) {
		t.Fatal("derived signing key does not verify its own signature")
	}

	encPub, encPriv, err := DeriveEncryptionKey(seed)
	if err != nil {
		t.Fatal(err)
	}
	if len(encPub) != 32 || len(encPriv) != 32 {
		t.Fatalf("x25519 keys must be 32 bytes, got %d/%d", len(encPub), len(encPriv))
	}
	// Agreement against an independent keypair, both directions.
	otherPriv := bytes.Repeat([]byte{11}, 32)
	otherPub, err := curve25519.X25519(otherPriv, curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	s1, err := curve25519.X25519(encPriv, otherPub)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := curve25519.X25519(otherPriv, encPub)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(s1, s2) {
		t.Fatal("x25519 agreement failed for derived key")
	}
}

func TestNewSeed_IsRandomAndCorrectLength(t *testing.T) {
	a, err := NewSeed()
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != SeedLen {
		t.Fatalf("want %d bytes, got %d", SeedLen, len(a))
	}
	b, err := NewSeed()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("two seeds must not be identical")
	}
}

// ── Conformance vectors ──────────────────────────────────────────────────────

type seedDerivationVector struct {
	Name                string `json:"name"`
	Seed                string `json:"seed"`
	SigningPublicKey    string `json:"signing_public_key"`
	SigningPrivateSeed  string `json:"signing_private_seed"`
	EncryptionPublicKey string `json:"encryption_public_key"`
	EncryptionPrivate   string `json:"encryption_private_key"`
	VaultKey            string `json:"vault_key"`
	Signature           string `json:"signature"`
}

// VectorSeedMessage is signed by every derived signing key in the vectors so
// the TS suite can check derivation end-to-end, not just byte equality.
const VectorSeedMessage = "poweur/v1/seed-vector"

func TestVectors_SeedDerivation(t *testing.T) {
	b64 := base64.RawURLEncoding.EncodeToString

	cases := []struct {
		name string
		seed []byte
	}{
		{"zeros", make([]byte, SeedLen)},
		{"counter", VectorSeed},
		{"ones", bytes.Repeat([]byte{0xff}, SeedLen)},
	}

	vectors := make([]seedDerivationVector, 0, len(cases))
	for _, tc := range cases {
		signPub, signPriv, err := DeriveSigningKey(tc.seed)
		if err != nil {
			t.Fatalf("%s signing: %v", tc.name, err)
		}
		encPub, encPriv, err := DeriveEncryptionKey(tc.seed)
		if err != nil {
			t.Fatalf("%s encryption: %v", tc.name, err)
		}
		vault, err := DeriveVaultKey(tc.seed)
		if err != nil {
			t.Fatalf("%s vault: %v", tc.name, err)
		}
		vectors = append(vectors, seedDerivationVector{
			Name:                tc.name,
			Seed:                b64(tc.seed),
			SigningPublicKey:    FormatEd25519PublicKey(signPub),
			SigningPrivateSeed:  b64(signPriv.Seed()),
			EncryptionPublicKey: FormatX25519PublicKey(encPub),
			EncryptionPrivate:   b64(encPriv),
			VaultKey:            b64(vault),
			Signature:           b64(ed25519.Sign(signPriv, []byte(VectorSeedMessage))),
		})
	}

	WriteVectors(t, vectorsDir, "seed-derivation", map[string]any{
		"message": VectorSeedMessage,
		"info": map[string]string{
			"signing":    SeedInfoSigning,
			"encryption": SeedInfoEncryption,
			"vault":      SeedInfoVault,
		},
		"vectors": vectors,
	})
}
