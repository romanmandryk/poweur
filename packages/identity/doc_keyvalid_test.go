package identity

import (
	"testing"
	"time"
)

func TestKeyValidAtGrace(t *testing.T) {
	doc := IdentityDocument{
		PublicKey: "ed25519:newkey",
		PreviousKeys: []PreviousKey{{
			PublicKey:  "ed25519:oldkey",
			ValidUntil: time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
		}},
	}
	now := time.Now().UTC()
	if !doc.KeyValidAt("newkey", now) {
		t.Fatal("current key")
	}
	if !doc.KeyValidAt("oldkey", now) {
		t.Fatal("old key in grace")
	}
	if doc.KeyValidAt("oldkey", now.Add(2*time.Hour)) {
		t.Fatal("old key after grace")
	}
}
