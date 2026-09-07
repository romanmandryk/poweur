package crypto

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Conformance vectors for message encryption (EPIC-017 E17-T5).
//
// Beside encryption.go so the AEAD contract — X25519 ECDH, HKDF-SHA256 with
// salt = ephPub||recipientPub and info "poweur/msg/v1", ChaCha20-Poly1305 with
// the AAD prefix — is pinned where it is defined. The TypeScript client
// decrypts these fixtures with the recorded private key; a change to any of
// those inputs turns the TS suite red.
//
// Ciphertexts are *not* deterministic (fresh ephemeral key and nonce per
// seal), so this file is regenerated on every run and its content churns.
// That is fine: what matters is that TS can open what Go sealed. The
// round-trip direction (TS seals, Go opens) is covered by the live-relay
// tests.
//
// Regenerate with `go test ./apps/cli/internal/crypto/...`.

// apps/cli/internal/crypto → repo root is four levels up.
const vectorsDir = "../../../../packages/identity/testdata/vectors"

type encryptionVector struct {
	Name string `json:"name"`
	// RecipientPrivateKey is a throwaway test key, base64url, 32 bytes.
	RecipientPrivateKey string `json:"recipient_private_key"`
	RecipientPublicKey  string `json:"recipient_public_key"`
	Plaintext           string `json:"plaintext"`
	Alg                 string `json:"alg"`
	Ciphertext          string `json:"ciphertext"`
	EphemeralPublicKey  string `json:"ephemeral_public_key"`
	Nonce               string `json:"nonce"`
}

func TestVectors_Encryption(t *testing.T) {
	// A fixed recipient key so the fixture is self-contained; it never
	// protects anything real.
	priv := []byte{
		32, 31, 30, 29, 28, 27, 26, 25, 24, 23, 22, 21, 20, 19, 18, 17,
		16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1,
	}
	pub, err := PublicFromPrivate(priv)
	if err != nil {
		t.Fatalf("derive public key: %v", err)
	}

	vectors := []encryptionVector{}
	for _, entry := range []struct {
		name      string
		plaintext string
	}{
		{"ascii", "hello from the Go CLI"},
		{"empty", ""},
		{"unicode", "héllo — 世界 🔒"},
		{"long", string(make([]byte, 4096))},
	} {
		sealed, err := Encrypt(pub, []byte(entry.plaintext))
		if err != nil {
			t.Fatalf("encrypt %s: %v", entry.name, err)
		}
		// Prove Go can open what Go sealed before asking TS to.
		opened, err := Decrypt(priv, sealed)
		if err != nil {
			t.Fatalf("decrypt %s: %v", entry.name, err)
		}
		if string(opened) != entry.plaintext {
			t.Fatalf("%s: round-trip mismatch", entry.name)
		}
		vectors = append(vectors, encryptionVector{
			Name:                entry.name,
			RecipientPrivateKey: base64.RawURLEncoding.EncodeToString(priv),
			RecipientPublicKey:  EncodePublicKey(pub),
			// base64 so the fixture survives JSON regardless of byte content.
			Plaintext:          base64.RawURLEncoding.EncodeToString([]byte(entry.plaintext)),
			Alg:                AlgName,
			Ciphertext:         sealed.Ciphertext,
			EphemeralPublicKey: sealed.EphemeralPublicKey,
			Nonce:              sealed.Nonce,
		})
	}

	if err := os.MkdirAll(vectorsDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", vectorsDir, err)
	}
	raw, err := json.MarshalIndent(vectors, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	path := filepath.Join(vectorsDir, "encryption.json")
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
