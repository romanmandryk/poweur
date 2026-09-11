package identity

import (
	"crypto/ed25519"
	"encoding/base64"
	"testing"
)

func TestDIDWeb(t *testing.T) {
	pub := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)).Public().(ed25519.PublicKey)
	doc := NewDocument("Alice.Example.COM.", FormatEd25519PublicKey(pub), FormatX25519PublicKey(make([]byte, 32)), "relay.example.com", nil)
	got, err := DIDWeb(doc)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "did:web:alice.example.com" || got.VerificationMethod[0].Controller != got.ID {
		t.Fatalf("bad DID projection: %#v", got)
	}
	if got.VerificationMethod[0].PublicKeyJWK.X != base64.RawURLEncoding.EncodeToString(pub) {
		t.Fatalf("signing key changed in projection")
	}
	if len(got.KeyAgreement) != 1 || got.VerificationMethod[1].PublicKeyJWK.CRV != "X25519" {
		t.Fatalf("encryption key missing: %#v", got)
	}
	if got.Service[0].ServiceEndpoint != "https://relay.example.com" {
		t.Fatalf("relay service = %q", got.Service[0].ServiceEndpoint)
	}
}

func TestDIDWebRejectsInvalidInput(t *testing.T) {
	tests := []IdentityDocument{
		{Identity: "not a name", PublicKey: FormatEd25519PublicKey(make([]byte, 32))},
		{Identity: "alice.example.com", PublicKey: "not-a-key"},
		{Identity: "alice.example.com", PublicKey: FormatEd25519PublicKey(make([]byte, 32)), EncryptionPublicKey: "bad"},
	}
	for _, doc := range tests {
		if _, err := DIDWeb(doc); err == nil {
			t.Fatalf("DIDWeb(%#v) succeeded", doc)
		}
	}
}
