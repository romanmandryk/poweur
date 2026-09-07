package crypto

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Conformance vectors for the canonical signing strings (EPIC-017 E17-T5).
//
// This generator lives beside signing.go on purpose: change a canonical
// string here and the regenerated fixture no longer matches what
// packages/client-ts derives, so CI fails in the same change set rather than
// silently splitting the Go and TypeScript clients.
//
// Output goes to the shared vectors directory that packages/identity owns;
// the TS suite reads all of them from one place. Regenerate with
// `go test ./apps/api/internal/crypto/...`.
//
// The seed and timestamp match packages/identity/vectors_test.go so the TS
// side needs a single key to verify every vector.

// apps/api/internal/crypto → repo root is four levels up.
const vectorsDir = "../../../../packages/identity/testdata/vectors"

const vectorTime = "2026-01-15T09:30:00Z"

var vectorSeed = []byte{
	1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16,
	17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32,
}

type canonicalVector struct {
	Name string `json:"name"`
	// Inputs are echoed so the TS test can call the same function with the
	// same arguments rather than hard-coding a second copy of the string.
	Inputs    map[string]any `json:"inputs"`
	Canonical string         `json:"canonical"`
	// Signature is base64url (RawURLEncoding); the relay accepts any base64
	// variant, and the TS client emits standard base64 on some paths, so the
	// TS test compares decoded bytes rather than the encoded string.
	Signature string `json:"signature"`
}

func TestVectors_CanonicalStrings(t *testing.T) {
	priv := ed25519.NewKeyFromSeed(vectorSeed)
	pubStr := base64.RawURLEncoding.EncodeToString(priv.Public().(ed25519.PublicKey))
	encStr := base64.RawURLEncoding.EncodeToString(vectorSeed)
	nonce := "dmVjdG9yLW5vbmNlLTAwMQ"

	enc := &EncryptionMeta{
		Alg:                "x25519-chacha20-poly1305",
		EphemeralPublicKey: "ZXBoZW1lcmFsLXB1YmxpYy1rZXk",
		Nonce:              "bm9uY2UtMTIzNDU2Nzg",
	}
	// EncryptionMeta carries no json tags (it is an internal signing helper),
	// so echo the wire shape explicitly — the fixture must speak the same
	// field names the TypeScript client sees on the wire.
	encInput := map[string]string{
		"alg":                  enc.Alg,
		"ephemeral_public_key": enc.EphemeralPublicKey,
		"nonce":                enc.Nonce,
	}

	vectors := []canonicalVector{
		{
			Name: "message-minimal",
			Inputs: map[string]any{
				"sender": "alice.poweur.net", "recipient": "bob.example.org",
				"timestamp": vectorTime, "payload": "Y2lwaGVydGV4dA",
			},
			Canonical: CanonicalMessageTyped(
				"alice.poweur.net", "bob.example.org", vectorTime, "Y2lwaGVydGV4dA",
				"", "", "", nil),
		},
		{
			Name: "message-session-encrypted",
			Inputs: map[string]any{
				"sender": "alice.poweur.net", "recipient": "bob.example.org",
				"timestamp": vectorTime, "payload": "Y2lwaGVydGV4dA",
				"id": "msg_1768469400000_abcdefghi", "session_id": "sess_0011",
				"encryption": encInput,
			},
			Canonical: CanonicalMessageTyped(
				"alice.poweur.net", "bob.example.org", vectorTime, "Y2lwaGVydGV4dA",
				"msg_1768469400000_abcdefghi", "sess_0011", "", enc),
		},
		{
			Name: "message-typed",
			Inputs: map[string]any{
				"sender": "alice.poweur.net", "recipient": "bob.example.org",
				"timestamp": vectorTime, "payload": "Y2lwaGVydGV4dA",
				"id": "msg_1768469400000_abcdefghi", "session_id": "",
				"encryption": encInput, "type": "sys.contact.request",
			},
			Canonical: CanonicalMessageTyped(
				"alice.poweur.net", "bob.example.org", vectorTime, "Y2lwaGVydGV4dA",
				"msg_1768469400000_abcdefghi", "", "sys.contact.request", enc),
		},
		{
			Name: "ack",
			Inputs: map[string]any{
				"id": "ack_1768469400000_jklmnopqr", "message_id": "msg_1768469400000_abcdefghi",
				"state": "delivered_client", "sender": "bob.example.org",
				"recipient": "alice.poweur.net", "timestamp": vectorTime, "session_id": "",
			},
			Canonical: CanonicalAck("ack_1768469400000_jklmnopqr", "msg_1768469400000_abcdefghi",
				"delivered_client", "bob.example.org", "alice.poweur.net", vectorTime, ""),
		},
		{
			Name: "ack-session",
			Inputs: map[string]any{
				"id": "ack_1768469400000_jklmnopqr", "message_id": "msg_1768469400000_abcdefghi",
				"state": "delivered_client", "sender": "bob.example.org",
				"recipient": "alice.poweur.net", "timestamp": vectorTime, "session_id": "sess_0011",
			},
			Canonical: CanonicalAck("ack_1768469400000_jklmnopqr", "msg_1768469400000_abcdefghi",
				"delivered_client", "bob.example.org", "alice.poweur.net", vectorTime, "sess_0011"),
		},
		{
			Name: "identity-registration",
			Inputs: map[string]any{
				"identity": "alice.poweur.net", "public_key": pubStr,
				"encryption_public_key": encStr, "relay_address": "poweur.net",
				"issued_at": vectorTime, "nonce": nonce,
			},
			Canonical: CanonicalIdentityRegistration("alice.poweur.net", pubStr, encStr,
				"poweur.net", vectorTime, nonce),
		},
		{
			Name: "identity-export",
			Inputs: map[string]any{
				"identity": "alice.poweur.net", "issued_at": vectorTime, "nonce": nonce,
			},
			Canonical: CanonicalIdentityExport("alice.poweur.net", vectorTime, nonce),
		},
		{
			Name: "identity-rotation",
			Inputs: map[string]any{
				"identity": "alice.poweur.net", "old_public_key": pubStr,
				"new_public_key": encStr, "issued_at": vectorTime, "nonce": nonce,
			},
			Canonical: CanonicalIdentityRotation("alice.poweur.net", pubStr, encStr, vectorTime, nonce),
		},
		{
			Name: "identity-encryption-key",
			Inputs: map[string]any{
				"identity": "alice.poweur.net", "encryption_public_key": encStr,
				"issued_at": vectorTime, "nonce": nonce,
			},
			Canonical: CanonicalEncryptionKeyUpdate("alice.poweur.net", encStr, vectorTime, nonce),
		},
		{
			Name: "dav-token",
			Inputs: map[string]any{
				"identity": "alice.poweur.net", "audience": "bob.example.org",
				"scope": "dav:rw:shared/project-x", "issued_at": vectorTime, "nonce": nonce,
			},
			Canonical: CanonicalDAVToken("alice.poweur.net", "bob.example.org",
				"dav:rw:shared/project-x", vectorTime, nonce),
		},
		{
			Name: "session-registration",
			Inputs: map[string]any{
				"identity": "alice.poweur.net", "session_public_key": encStr,
				"issued_at": vectorTime, "expires_at": "2026-01-16T09:30:00Z", "nonce": nonce,
			},
			Canonical: CanonicalSessionRegistration("alice.poweur.net", encStr, vectorTime,
				"2026-01-16T09:30:00Z", nonce),
		},
		{
			Name: "session-revocation",
			Inputs: map[string]any{
				"identity": "alice.poweur.net", "session_id": "sess_0011",
				"issued_at": vectorTime, "nonce": nonce,
			},
			Canonical: CanonicalSessionRevocation("alice.poweur.net", "sess_0011", vectorTime, nonce),
		},
	}

	pub := priv.Public().(ed25519.PublicKey)
	for i := range vectors {
		sig := ed25519.Sign(priv, []byte(vectors[i].Canonical))
		vectors[i].Signature = base64.RawURLEncoding.EncodeToString(sig)
		// Prove the relay's own verifier accepts what we just emitted.
		if err := VerifySignature(pub, vectors[i].Canonical, vectors[i].Signature); err != nil {
			t.Fatalf("%s: self-verify failed: %v", vectors[i].Name, err)
		}
	}

	writeVectors(t, "canonical", map[string]any{
		"seed_base64url":       base64.RawURLEncoding.EncodeToString(vectorSeed),
		"public_key_base64url": pubStr,
		"vectors":              vectors,
	})
}

func writeVectors(t *testing.T, name string, v any) {
	t.Helper()
	if err := os.MkdirAll(vectorsDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", vectorsDir, err)
	}
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal %s: %v", name, err)
	}
	path := filepath.Join(vectorsDir, name+".json")
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
