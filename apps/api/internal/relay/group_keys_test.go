package relay

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	idpkg "github.com/poweur/identity"
)

func (fx *groupFixture) keyring(t *testing.T, epoch int, recipients ...string) (idpkg.GroupKeyring, []byte) {
	t.Helper()
	encs := map[string][]byte{}
	for _, r := range recipients {
		pub, _, _ := idpkg.GenerateX25519Keypair()
		encs[r] = pub
	}
	ring, _, err := idpkg.NewGroupKeyring(fx.groupName, epoch, encs, nil, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		t.Fatal(err)
	}
	if err := ring.Sign(fx.group.priv); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(ring)
	return ring, raw
}

func (fx *groupFixture) writeRule(t *testing.T, path string, raw []byte) error {
	t.Helper()
	_, validate, err := fx.server.sysWriteRule(fx.groupName, path)
	if err != nil {
		return err
	}
	return validate(raw)
}

func TestGroupKeyringValidation(t *testing.T) {
	fx := newGroupFixture(t)
	everyone := []string{fx.groupName, fx.dana.name, fx.alice.name, fx.bob.name, fx.carol.name}

	ring, raw := fx.keyring(t, 1, everyone...)
	if err := fx.writeRule(t, idpkg.GroupKeysDoc, raw); err != nil {
		t.Fatalf("valid keyring refused: %v", err)
	}
	if _, stale := fx.keyring(t, 2, everyone...); fx.writeRule(t, idpkg.GroupKeysDoc, stale) == nil {
		t.Fatal("a keyring at another epoch than the roster was accepted")
	}
	if _, short := fx.keyring(t, 1, fx.groupName, fx.alice.name); fx.writeRule(t, idpkg.GroupKeysDoc, short) == nil {
		t.Fatal("a keyring missing members was accepted")
	}
	if _, extra := fx.keyring(t, 1, append(everyone, fx.stranger.name)...); fx.writeRule(t, idpkg.GroupKeysDoc, extra) == nil {
		t.Fatal("a keyring sealed to a stranger was accepted")
	}
	forged := ring
	if err := forged.Sign(fx.alice.priv); err != nil {
		t.Fatal(err)
	}
	forgedRaw, _ := json.Marshal(forged)
	if fx.writeRule(t, idpkg.GroupKeysDoc, forgedRaw) == nil {
		t.Fatal("a keyring signed by a member, not the group, was accepted")
	}
	// A person's own drive has no group keys.
	if _, validate, _ := fx.server.sysWriteRule(fx.alice.name, idpkg.GroupKeysDoc); validate(raw) == nil {
		t.Fatal("a keyring was accepted for a non-group identity")
	}

	// The public key must follow the stored keyring.
	doc := ring.PublicDocument()
	if err := doc.Sign(fx.group.priv); err != nil {
		t.Fatal(err)
	}
	docRaw, _ := json.Marshal(doc)
	if fx.writeRule(t, idpkg.GroupPublicKeyDoc, docRaw) == nil {
		t.Fatal("a public key was accepted before its keyring")
	}
	setSysFile(t, fx.server, fx.groupName, idpkg.GroupKeysDoc, string(raw))
	if err := fx.writeRule(t, idpkg.GroupPublicKeyDoc, docRaw); err != nil {
		t.Fatalf("matching public key refused: %v", err)
	}
	other, _ := fx.keyring(t, 1, everyone...)
	otherDoc := other.PublicDocument()
	_ = otherDoc.Sign(fx.group.priv)
	otherRaw, _ := json.Marshal(otherDoc)
	if fx.writeRule(t, idpkg.GroupPublicKeyDoc, otherRaw) == nil {
		t.Fatal("a public key that is not the keyring's was accepted")
	}
}

func TestGroupKeysReadableByMembersOnly(t *testing.T) {
	fx := newGroupFixture(t)
	_, raw := fx.keyring(t, 1, fx.groupName, fx.dana.name, fx.alice.name, fx.bob.name, fx.carol.name)
	setSysFile(t, fx.server, fx.groupName, idpkg.GroupKeysDoc, string(raw))
	read := func(id hostedID) (int, string) {
		challenge, sig := challengeFor(t, fx.ts, id)
		req, _ := http.NewRequest(http.MethodGet, fx.ts.URL+"/groups/"+fx.groupName+"/keys", nil)
		req.Header.Set("X-Poweur-Identity", id.name)
		req.Header.Set("X-Poweur-Challenge", challenge)
		req.Header.Set("X-Poweur-Signature", sig)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var body strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(buf)
			body.Write(buf[:n])
			if err != nil {
				break
			}
		}
		return resp.StatusCode, body.String()
	}
	for _, id := range []hostedID{fx.bob, fx.dana, fx.group} {
		if code, body := read(id); code != http.StatusOK || body != string(raw) {
			t.Fatalf("%s: %d", id.name, code)
		}
	}
	if code, _ := read(fx.stranger); code != http.StatusNotFound {
		t.Fatalf("stranger: %d", code)
	}
}

func TestGroupPublicKeyRoute(t *testing.T) {
	fx := newGroupFixture(t)
	resp, err := http.Get(fx.ts.URL + "/groups/" + fx.groupName + "/public-key")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("before any key: %d", resp.StatusCode)
	}
	ring, raw := fx.keyring(t, 1, fx.groupName, fx.dana.name, fx.alice.name, fx.bob.name, fx.carol.name)
	setSysFile(t, fx.server, fx.groupName, idpkg.GroupKeysDoc, string(raw))
	doc := ring.PublicDocument()
	_ = doc.Sign(fx.group.priv)
	docRaw, _ := json.Marshal(doc)
	setSysFile(t, fx.server, fx.groupName, idpkg.GroupPublicKeyDoc, string(docRaw))
	resp, err = http.Get(fx.ts.URL + "/groups/" + fx.groupName + "/public-key")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got idpkg.GroupPublicKey
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&got) != nil || got.Public != ring.Public {
		t.Fatalf("public key route: %d %+v", resp.StatusCode, got)
	}
}
