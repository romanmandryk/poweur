package drive

import (
	"crypto/ed25519"
	"testing"

	protocol "github.com/poweur/identity/drive"
)

func TestGuestCreateUsesOpaqueNameToken(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !nameIndexMatches(protocol.GuestAuthor(pub), "private-index", "opaque-token") {
		t.Fatal("guest create token was rejected")
	}
	if nameIndexMatches("alice.poweur.net", "private-index", "opaque-token") {
		t.Fatal("identity author bypassed the keyed name index")
	}
	if !nameIndexMatches("alice.poweur.net", "same", "same") {
		t.Fatal("matching owner index was rejected")
	}
}
