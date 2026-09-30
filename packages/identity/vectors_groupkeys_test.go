package identity

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"testing"
)

type groupKeyVector struct {
	Name            string         `json:"name"`
	Keyring         GroupKeyring   `json:"keyring"`
	Canonical       string         `json:"canonical"`
	Public          GroupPublicKey `json:"public"`
	PublicCanonical string         `json:"public_canonical"`
	Member          string         `json:"member"`
	MemberPrivate   string         `json:"member_private"`
	// Opened are the epoch private keys the member reaches, newest first.
	Opened []groupOpenedKey `json:"opened"`
}

type groupOpenedKey struct {
	Epoch   int    `json:"epoch"`
	Private string `json:"private"`
}

func fixedX25519(fill byte) (pub, priv []byte) {
	priv = bytes.Repeat([]byte{fill}, 32)
	pub, _ = X25519PublicFromPrivate(priv)
	return pub, priv
}

// TestVectors_GroupKeys pins the E24-T3 keyring: canonical strings, the
// signature and the seals, so the TS client opens exactly what Go sealed.
func TestVectors_GroupKeys(t *testing.T) {
	priv := vectorKey()
	pub := priv.Public().(ed25519.PublicKey)
	b64 := base64.RawURLEncoding.EncodeToString
	random := bytes.NewReader(bytes.Repeat([]byte{0x5a, 0xa5, 0x3c, 0xc3}, 4096))

	bobPub, bobPriv := fixedX25519(0x11)
	groupEncPub, _ := fixedX25519(0x22)
	epoch1Pub, epoch1Priv := fixedX25519(0x31)
	epoch2Pub, epoch2Priv := fixedX25519(0x32)
	const group = "atlas.poweur.net"

	seal := func(recipient, plaintext, context []byte) SealedPayload {
		out, err := SealWithReader(recipient, plaintext, GroupKeyDomain, context, random)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	ring := GroupKeyring{Version: 1, Group: group, Epoch: 2, Public: b64(epoch2Pub), UpdatedAt: VectorTime,
		Sealed: map[string]SealedPayload{
			"bob.poweur.net": seal(bobPub, epoch2Priv, groupMemberSealContext(group, 2, "bob.poweur.net")),
			group:            seal(groupEncPub, epoch2Priv, groupMemberSealContext(group, 2, group)),
		},
		Previous: []GroupPreviousKey{{Epoch: 1, Public: b64(epoch1Pub), Sealed: seal(epoch2Pub, epoch1Priv, groupPreviousSealContext(group, 1, 2))}},
	}
	if err := ring.Sign(priv); err != nil {
		t.Fatal(err)
	}
	if err := ring.VerifySignature(pub); err != nil {
		t.Fatal(err)
	}
	opened, err := ring.Open("bob.poweur.net", bobPriv)
	if err != nil || len(opened) != 2 {
		t.Fatalf("open: %v", err)
	}
	doc := ring.PublicDocument()
	if err := doc.Sign(priv); err != nil {
		t.Fatal(err)
	}
	WriteVectors(t, vectorsDir, "group-keys", []groupKeyVector{{
		Name: "two-epochs", Keyring: ring, Canonical: ring.Canonical(), Public: doc, PublicCanonical: doc.Canonical(),
		Member: "bob.poweur.net", MemberPrivate: b64(bobPriv),
		Opened: []groupOpenedKey{{2, b64(opened[0].Private)}, {1, b64(opened[1].Private)}},
	}})
}
