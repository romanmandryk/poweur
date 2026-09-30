package identity

import (
	"crypto/ed25519"
	"encoding/json"
	"testing"
)

func groupKeyFixture(t *testing.T) (ed25519.PrivateKey, map[string][2][]byte) {
	t.Helper()
	_, groupKey, _ := ed25519.GenerateKey(nil)
	people := map[string][2][]byte{}
	for _, id := range []string{"atlas.poweur.net", "bob.poweur.net", "carol.example.org"} {
		pub, priv, err := GenerateX25519Keypair()
		if err != nil {
			t.Fatal(err)
		}
		people[id] = [2][]byte{pub, priv}
	}
	return groupKey, people
}

func recipientsOf(people map[string][2][]byte, ids ...string) map[string][]byte {
	out := map[string][]byte{}
	for _, id := range ids {
		out[id] = people[id][0]
	}
	return out
}

func TestGroupKeyringRoundTripAndHistory(t *testing.T) {
	groupKey, people := groupKeyFixture(t)
	ring1, key1, err := NewGroupKeyring("atlas.poweur.net", 1, recipientsOf(people, "atlas.poweur.net", "bob.poweur.net", "carol.example.org"), nil, "2026-09-30T00:00:00Z")
	if err != nil || ring1.Sign(groupKey) != nil {
		t.Fatal(err)
	}
	// Carol leaves: epoch 2 is sealed to the rest and carries epoch 1.
	ring2, key2, err := NewGroupKeyring("atlas.poweur.net", 2, recipientsOf(people, "atlas.poweur.net", "bob.poweur.net"), []GroupEpochKey{key1}, "2026-09-30T01:00:00Z")
	if err != nil || ring2.Sign(groupKey) != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(ring2)
	parsed, err := ParseGroupKeyring(raw)
	if err != nil || parsed.VerifySignature(groupKey.Public().(ed25519.PublicKey)) != nil {
		t.Fatalf("parse/verify: %v", err)
	}
	keys, err := parsed.Open("bob.poweur.net", people["bob.poweur.net"][1])
	if err != nil || len(keys) != 2 || keys[0].Epoch != 2 || string(keys[0].Private) != string(key2.Private) || keys[1].Epoch != 1 || string(keys[1].Private) != string(key1.Private) {
		t.Fatalf("bob's keys: %v %+v", err, keys)
	}
	if _, err := parsed.Open("carol.example.org", people["carol.example.org"][1]); err == nil {
		t.Fatal("a removed member opened the new epoch")
	}
	// A key sealed to one member cannot be opened as another.
	parsed.Sealed["carol.example.org"] = parsed.Sealed["bob.poweur.net"]
	if _, err := parsed.Open("carol.example.org", people["carol.example.org"][1]); err == nil {
		t.Fatal("a copied seal opened for another recipient")
	}
}

func TestGroupKeyringRejectsTampering(t *testing.T) {
	groupKey, people := groupKeyFixture(t)
	ring, _, _ := NewGroupKeyring("atlas.poweur.net", 3, recipientsOf(people, "bob.poweur.net"), nil, "2026-09-30T00:00:00Z")
	if err := ring.Sign(groupKey); err != nil {
		t.Fatal(err)
	}
	pub := groupKey.Public().(ed25519.PublicKey)
	for name, mutate := range map[string]func(*GroupKeyring){
		"epoch":     func(k *GroupKeyring) { k.Epoch = 4 },
		"public":    func(k *GroupKeyring) { k.Public = ring.Sealed["bob.poweur.net"].EphemeralPublicKey },
		"recipient": func(k *GroupKeyring) { k.Sealed["mallory.poweur.net"] = k.Sealed["bob.poweur.net"] },
	} {
		copyRing := ring
		copyRing.Sealed = map[string]SealedPayload{}
		for k, v := range ring.Sealed {
			copyRing.Sealed[k] = v
		}
		mutate(&copyRing)
		if copyRing.VerifySignature(pub) == nil {
			t.Fatalf("%s: tampered keyring verified", name)
		}
	}
	doc := ring.PublicDocument()
	if err := doc.Sign(groupKey); err != nil || doc.VerifySignature(pub) != nil {
		t.Fatalf("public doc: %v", err)
	}
	doc.Epoch = 9
	if doc.VerifySignature(pub) == nil {
		t.Fatal("tampered public doc verified")
	}
	bad := GroupKeyring{Version: 1, Group: "atlas.poweur.net", Epoch: 2, Public: ring.Public, Sealed: ring.Sealed,
		Previous: []GroupPreviousKey{{Epoch: 2, Public: ring.Public}}}
	if bad.Validate() == nil {
		t.Fatal("an earlier epoch not below the current one validated")
	}
}
