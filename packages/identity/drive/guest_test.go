package drive

import (
	"bytes"
	"crypto/ed25519"
	"strings"
	"testing"

	identity "github.com/poweur/identity"
)

func TestGuestAuthor(t *testing.T) {
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{5}, 32))
	pub := priv.Public().(ed25519.PublicKey)
	author := GuestAuthor(pub)
	if err := identity.ValidateIdentityName(author); err != nil || !validIdentity(author) {
		t.Fatalf("%s is not a valid author name: %v", author, err)
	}
	key, ok := GuestKey(author)
	if !ok || !bytes.Equal(key, pub) {
		t.Fatal("key does not round-trip")
	}
	for _, bad := range []string{"alice.poweur.net", strings.ToUpper(author[:5]) + author[5:], "g" + GuestSuffix, "gaaaa" + GuestSuffix, author + ".x"} {
		if IsGuest(bad) {
			t.Errorf("%s accepted as a guest", bad)
		}
	}
	// A guest signs records like anyone else.
	r := AppendRecord{Format: 1, Drive: "alice.poweur.net", Node: strings.Repeat("a", 32), Author: author, Generation: 1, Sequence: 1,
		Chunks: []ChunkRef{{ID: strings.Repeat("b", 64), Size: 40 + PaddingBucket}}}
	if err := r.Sign(priv); err != nil {
		t.Fatal(err)
	}
	if err := r.Verify(key); err != nil {
		t.Fatal(err)
	}
}
