package integration_test

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	relaypkg "github.com/poweur/api/pkg/relay"
	clipkg "github.com/poweur/cli/pkg/cli"
	idpkg "github.com/poweur/identity"
	"github.com/poweur/identity/drive"
)

// INT_DRIVE_05 (E20-T7 remote groups): alice, on relay A, shares a folder
// with a group identity hosted on relay B. Its members reach the folder by
// presenting the group's signed roster; once the group drops carol, her old
// roster is refused, the new one revokes her and the folder must rotate.
func TestINT_DRIVE_05_RemoteGroupMembers(t *testing.T) {
	zone := newZone(t)
	tsA, addrA := newHostedRelay(t, zone, t.TempDir())
	addrB, _ := newCountingRelay(t, zone, nil, relaypkg.RateLimits{})
	zone.SetHost("rgalice.poweur.net", addrA)
	clipkg.ConfigureIdentityResolver("http", true, addrA)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })
	aliceHome := t.TempDir()
	runCLI(t, aliceHome, "identity", "create", "rgalice.poweur.net", "--hosted", "--relay", tsA.URL, "--json")
	alice := driveClient{t: t, relay: tsA.URL, identity: "rgalice.poweur.net", key: loadIdentityKey(t, aliceHome, "rgalice.poweur.net")}
	member := func(name string) driveClient {
		home, full := newDNSIdentity(t, name, "remote.test", addrB)
		return driveClient{t: t, relay: tsA.URL, identity: full, key: loadIdentityKey(t, home, full)}
	}
	crew, bob, carol := member("rgcrew"), member("rgbob"), member("rgcarol")
	crewOnB := crew
	crewOnB.relay = "http://" + addrB

	setRoster := func(epoch int, members ...string) string {
		t.Helper()
		gr := idpkg.ShareGroup{Group: crew.identity, Owner: crew.identity, Members: members, Admins: []string{crew.identity},
			Epoch: epoch, UpdatedAt: time.Now().UTC().Format(time.RFC3339)}
		if err := gr.Sign(crew.key); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(gr)
		if resp, body := crewOnB.do(http.MethodPut, "/identities/"+crew.identity+"/system/.poweur/relay/group.json", raw); resp.StatusCode != http.StatusOK {
			t.Fatalf("roster on B: %d %s", resp.StatusCode, body)
		}
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	rosterV1 := setRoster(1, bob.identity, carol.identity)

	pub, _, _ := idpkg.GenerateX25519Keypair()
	root, folder := randomHex(16), randomHex(16)
	for _, m := range []drive.Manifest{
		{Format: 1, Drive: alice.identity, Node: root, Version: randomHex(16), Operation: drive.OpCreate, Author: alice.identity, Generation: 1,
			Kind: drive.KindFolder, NodeKey: sealedFor(t, pub, "node-key", make([]byte, 32)), Pages: []string{}},
		{Format: 1, Drive: alice.identity, Node: folder, Version: randomHex(16), Operation: drive.OpCreate, Author: alice.identity, Generation: 1,
			Kind: drive.KindFolder, Folder: root, Name: sealedName(t, pub, "crew notes"), NameHash: randomHex(32),
			NodeKey: sealedFor(t, pub, "node-key", make([]byte, 32)), Pages: []string{}},
	} {
		if status, out := alice.commit(map[string]any{"manifest": alice.signed(m)}); status != http.StatusOK {
			t.Fatalf("setup: %d %v", status, out)
		}
	}
	id, _ := drive.NewShareID()
	s := drive.Share{Format: 1, Drive: alice.identity, ID: id, Node: folder, Member: crew.identity, Role: drive.RoleRead, Generation: 1,
		NodeKey: sealedFor(t, pub, "node-key", make([]byte, 32)), NodePublic: base64.RawURLEncoding.EncodeToString(pub),
		Issuer: alice.identity, Issued: time.Now().UTC().Format(time.RFC3339)}
	if err := s.Sign(alice.key); err != nil {
		t.Fatal(err)
	}
	if status, out := alice.commit(map[string]any{"share": s}); status != http.StatusOK {
		t.Fatalf("share with the remote group: %d %v", status, out)
	}

	read := func(c driveClient, roster string) int {
		t.Helper()
		resp, _ := http.Get(c.relay + "/auth/challenge?identity=" + c.identity)
		var challenge struct {
			Challenge string `json:"challenge"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&challenge)
		resp.Body.Close()
		req, _ := http.NewRequest(http.MethodGet, c.relay+"/drive/"+alice.identity+"/nodes/"+folder, nil)
		req.Header.Set("X-Poweur-Identity", c.identity)
		req.Header.Set("X-Poweur-Challenge", challenge.Challenge)
		req.Header.Set("X-Poweur-Signature", c.sign(challenge.Challenge))
		if roster != "" {
			req.Header.Set("X-Poweur-Group-Roster", roster)
		}
		out, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		out.Body.Close()
		return out.StatusCode
	}
	if status := read(bob, ""); status != http.StatusForbidden {
		t.Fatalf("before presenting the roster: %d", status)
	}
	if status := read(bob, rosterV1); status != http.StatusOK {
		t.Fatalf("bob with the roster: %d", status)
	}
	if status := read(carol, ""); status != http.StatusOK {
		t.Fatalf("carol, roster already verified: %d", status)
	}

	// The group drops carol. Her copy is now stale; bob's new one revokes her.
	rosterV2 := setRoster(2, bob.identity)
	if status := read(carol, rosterV1); status != http.StatusForbidden {
		t.Fatalf("stale roster: %d", status)
	}
	if status := read(bob, rosterV2); status != http.StatusOK {
		t.Fatalf("bob with the new roster: %d", status)
	}
	if status := read(carol, ""); status != http.StatusForbidden {
		t.Fatalf("carol after removal: %d", status)
	}
	if status, out := alice.json(http.MethodGet, "/drive/"+alice.identity+"/nodes/"+folder, nil); status != http.StatusOK || out["rotate_required"] != true {
		t.Fatalf("shared folder after the group dropped a member: %d %v", status, out)
	}
	// A forged roster (signed by bob, not the group) is refused.
	forged := idpkg.ShareGroup{Group: crew.identity, Owner: crew.identity, Members: []string{carol.identity}, Admins: []string{crew.identity},
		Epoch: 2, UpdatedAt: time.Now().UTC().Format(time.RFC3339)}
	_ = forged.Sign(bob.key)
	raw, _ := json.Marshal(forged)
	if status := read(carol, base64.RawURLEncoding.EncodeToString(raw)); status != http.StatusForbidden {
		t.Fatalf("forged roster: %d", status)
	}
}
