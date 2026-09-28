package integration_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	relaypkg "github.com/poweur/api/pkg/relay"
	clipkg "github.com/poweur/cli/pkg/cli"
	idpkg "github.com/poweur/identity"
	"github.com/poweur/identity/drive"
)

// memberEvents opens c's drive.changed stream on another identity's drive.
func (c driveClient) memberEvents(driveID string) (<-chan map[string]any, <-chan struct{}) {
	c.t.Helper()
	resp, _ := http.Get(c.relay + "/auth/challenge?identity=" + c.identity)
	var challenge struct {
		Challenge string `json:"challenge"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&challenge)
	resp.Body.Close()
	req, _ := http.NewRequest(http.MethodGet, c.relay+"/drive/"+driveID+"/events", nil)
	req.Header.Set("X-Poweur-Identity", c.identity)
	req.Header.Set("X-Poweur-Challenge", challenge.Challenge)
	req.Header.Set("X-Poweur-Signature", c.sign(challenge.Challenge))
	stream, err := http.DefaultClient.Do(req)
	if err != nil || stream.StatusCode != http.StatusOK {
		c.t.Fatalf("member stream: %v %v", err, stream)
	}
	c.t.Cleanup(func() { stream.Body.Close() })
	events, done := make(chan map[string]any, 16), make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 64<<10)
		var pending string
		for {
			n, err := stream.Body.Read(buf)
			pending += string(buf[:n])
			for {
				line, rest, found := strings.Cut(pending, "\n")
				if !found {
					break
				}
				pending = rest
				var event map[string]any
				if strings.HasPrefix(line, "data: ") && json.Unmarshal([]byte(line[6:]), &event) == nil && event["type"] != "ready" {
					events <- event
				}
			}
			if err != nil {
				return
			}
		}
	}()
	return events, done
}

// INT_DRIVE_02 (E20-T7): alice shares a folder read-only with bob, who is
// homed on another relay, and a file append-only with carol. Bob reads and
// follows only what is shared; carol appends without reading; revoking bob
// closes his stream and forces a key rotation before new content.
func TestINT_DRIVE_02_SharesAcrossRelays(t *testing.T) {
	zone := newZone(t)
	tsA, addrA := newHostedRelay(t, zone, t.TempDir())
	addrB, _ := newCountingRelay(t, zone, nil, relaypkg.RateLimits{})
	zone.SetHost("sharealice.poweur.net", addrA)
	clipkg.ConfigureIdentityResolver("http", true, addrA)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })
	aliceHome := t.TempDir()
	runCLI(t, aliceHome, "identity", "create", "sharealice.poweur.net", "--hosted", "--relay", tsA.URL, "--json")
	alice := driveClient{t: t, relay: tsA.URL, identity: "sharealice.poweur.net", key: loadIdentityKey(t, aliceHome, "sharealice.poweur.net")}
	bobHome, bobName := newDNSIdentity(t, "sharebob", "members.test", addrB)
	bob := driveClient{t: t, relay: tsA.URL, identity: bobName, key: loadIdentityKey(t, bobHome, bobName)}
	carolHome, carolName := newDNSIdentity(t, "sharecarol", "members.test", addrB)
	carol := driveClient{t: t, relay: tsA.URL, identity: carolName, key: loadIdentityKey(t, carolHome, carolName)}
	base := "/drive/" + alice.identity
	x := func() []byte { pub, _, _ := idpkg.GenerateX25519Keypair(); return pub }

	create := func(node, folder, kind, mode string) {
		t.Helper()
		m := drive.Manifest{Format: 1, Drive: alice.identity, Node: node, Version: randomHex(16), Operation: drive.OpCreate, Author: alice.identity,
			Generation: 1, Kind: kind, Mode: mode, NodeKey: sealedFor(t, x(), "node-key", bytes.Repeat([]byte{1}, 32)), Pages: []string{}}
		if folder != "" {
			m.Folder, m.Name, m.NameHash = folder, sealedName(t, x(), node), randomHex(32)
		}
		if kind == drive.KindFile {
			m.ContentKey = sealedFor(t, x(), "content-key", bytes.Repeat([]byte{2}, 32))
		}
		if status, out := alice.commit(map[string]any{"manifest": alice.signed(m)}); status != http.StatusOK {
			t.Fatalf("create %s: %d %v", node, status, out)
		}
	}
	root, shared, private, log, notes := randomHex(16), randomHex(16), randomHex(16), randomHex(16), randomHex(16)
	create(root, "", drive.KindFolder, "")
	create(shared, root, drive.KindFolder, "")
	create(private, root, drive.KindFolder, "")
	create(notes, shared, drive.KindFile, drive.ModeReplace)
	create(log, root, drive.KindFile, drive.ModeAppend)

	share := func(node, member, role string) drive.Share {
		t.Helper()
		id, _ := drive.NewShareID()
		s := drive.Share{Format: 1, Drive: alice.identity, ID: id, Node: node, Member: member, Role: role, Generation: 1,
			NodePublic: base64.RawURLEncoding.EncodeToString(x()), Issuer: alice.identity, Issued: time.Now().UTC().Format(time.RFC3339)}
		if drive.KeyBearing(role) {
			s.NodeKey = sealedFor(t, x(), "node-key", bytes.Repeat([]byte{3}, 32))
		}
		if err := s.Sign(alice.key); err != nil {
			t.Fatal(err)
		}
		if status, out := alice.commit(map[string]any{"share": s}); status != http.StatusOK {
			t.Fatalf("share: %d %v", status, out)
		}
		return s
	}
	bobShare := share(shared, bob.identity, drive.RoleRead)
	share(log, carol.identity, drive.RoleAppend)

	// Bob finds his share, reads inside it, and nothing beside it.
	status, out := bob.json(http.MethodGet, base+"/shares", nil)
	if shares, _ := out["shares"].([]any); status != http.StatusOK || len(shares) != 1 || shares[0].(map[string]any)["node_key"] == nil {
		t.Fatalf("bob's shares: %d %v", status, out)
	}
	if status, _ := bob.json(http.MethodGet, base+"/nodes/"+notes, nil); status != http.StatusOK {
		t.Fatalf("bob reads a shared file: %d", status)
	}
	if status, out := bob.json(http.MethodGet, base+"/nodes/"+shared+"/children", nil); status != http.StatusOK || len(out["children"].([]any)) != 1 {
		t.Fatalf("bob lists the shared folder: %d %v", status, out)
	}
	for _, path := range []string{"/nodes/" + private, "/nodes/" + root + "/children", "", "/nodes/" + log + "/records"} {
		if status, _ := bob.json(http.MethodGet, base+path, nil); status != http.StatusForbidden {
			t.Fatalf("bob reads %q: %d", path, status)
		}
	}
	events, closed := bob.memberEvents(alice.identity)

	// Carol appends and cannot read what she appended to.
	record := drive.AppendRecord{Format: 1, Drive: alice.identity, Node: log, Author: carol.identity, Generation: 1, Sequence: 1, Chunks: []drive.ChunkRef{}}
	if err := record.SealContent(x(), []byte("carol's line")); err != nil {
		t.Fatal(err)
	}
	if err := record.Sign(carol.key); err != nil {
		t.Fatal(err)
	}
	if status, out := carol.commitTo(alice.identity, map[string]any{"records": []drive.AppendRecord{record}}); status != http.StatusOK {
		t.Fatalf("carol appends: %d %v", status, out)
	}
	if status, _ := carol.json(http.MethodGet, base+"/nodes/"+log+"/records", nil); status != http.StatusForbidden {
		t.Fatalf("carol reads the log: %d", status)
	}

	// Bob's stream carries the shared folder's changes only.
	create(randomHex(16), private, drive.KindFolder, "")
	sharedChild := randomHex(16)
	create(sharedChild, shared, drive.KindFolder, "")
	select {
	case event := <-events:
		if event["drive"].(map[string]any)["node"] != sharedChild {
			t.Fatalf("bob saw %v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bob's stream is silent")
	}

	// Revocation: access ends, the stream closes, new content waits for a rotation.
	if status, out := alice.commit(map[string]any{"unshare": map[string]string{"id": bobShare.ID}}); status != http.StatusOK {
		t.Fatalf("revoke: %d %v", status, out)
	}
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("revoked member's stream stayed open")
	}
	if status, _ := bob.json(http.MethodGet, base+"/nodes/"+notes, nil); status != http.StatusForbidden {
		t.Fatalf("revoked bob reads: %d", status)
	}
	m := drive.Manifest{Format: 1, Drive: alice.identity, Node: randomHex(16), Version: randomHex(16), Operation: drive.OpCreate, Author: alice.identity,
		Generation: 1, Kind: drive.KindFolder, Folder: shared, Name: sealedName(t, x(), "after"), NameHash: randomHex(32),
		NodeKey: sealedFor(t, x(), "node-key", bytes.Repeat([]byte{4}, 32)), Pages: []string{}}
	if status, out := alice.commit(map[string]any{"manifest": alice.signed(m)}); status != http.StatusConflict {
		t.Fatalf("write under a revoked key: %d %v", status, out)
	}
	if status, out := alice.json(http.MethodGet, base+"/nodes/"+shared, nil); status != http.StatusOK || out["rotate_required"] != true {
		t.Fatalf("node does not say it needs rotation: %v", out)
	}
}
