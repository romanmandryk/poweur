package integration_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	clipkg "github.com/poweur/cli/pkg/cli"
	idpkg "github.com/poweur/identity"
	"github.com/poweur/identity/drive"
)

// INT_DRIVE_04 (E20-T7 acceptance): alice's board, shared with bob and
// published by link, moves to the "crew" Space alice administers. Bob keeps
// his access in the Space, the old link redirects to the Space's copy, and
// alice's drive no longer serves the board.
func TestINT_DRIVE_04_TransferBoardToSpace(t *testing.T) {
	zone := newZone(t)
	ts, addr := newHostedRelay(t, zone, t.TempDir())
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })
	client := func(name string) driveClient {
		zone.SetHost(name, addr)
		home := t.TempDir()
		runCLI(t, home, "identity", "create", name, "--hosted", "--relay", ts.URL, "--json")
		return driveClient{t: t, relay: ts.URL, identity: name, key: loadIdentityKey(t, home, name)}
	}
	alice, bob, crew := client("tralice.poweur.net"), client("trbob.poweur.net"), client("trcrew.poweur.net")

	// crew is a group identity: alice administers it, bob is a member.
	roster := idpkg.ShareGroup{Group: crew.identity, Owner: crew.identity, Members: []string{bob.identity}, Admins: []string{alice.identity},
		Epoch: 1, UpdatedAt: time.Now().UTC().Format(time.RFC3339)}
	if err := roster.Sign(crew.key); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(roster)
	if resp, body := crew.do(http.MethodPut, "/identities/"+crew.identity+"/system/.poweur/relay/group.json", raw); resp.StatusCode != http.StatusOK {
		t.Fatalf("roster: %d %s", resp.StatusCode, body)
	}

	pub, _, _ := idpkg.GenerateX25519Keypair()
	folder := func(c driveClient, driveID, node, parent string) drive.Manifest {
		m := drive.Manifest{Format: 1, Drive: driveID, Node: node, Version: randomHex(16), Operation: drive.OpCreate, Author: c.identity,
			Generation: 1, Kind: drive.KindFolder, NodeKey: sealedFor(t, pub, "node-key", bytes.Repeat([]byte{1}, 32)), Pages: []string{}}
		if parent != "" {
			m.Folder, m.Name, m.NameHash = parent, sealedName(t, pub, "board"), randomHex(32)
		}
		return c.signed(m)
	}
	card := func(c driveClient, driveID, node, parent string) (drive.Manifest, []drive.ChunkPage, []byte) {
		ctx, _ := drive.Context(driveID, node, drive.PurposeContent, 1)
		data, _ := drive.EncryptChunk(bytes.Repeat([]byte{9}, 32), []byte("ship it on friday"), ctx)
		ref := drive.ChunkRef{ID: drive.ChunkID(data), Size: uint64(len(data))}
		if resp, body := c.do(http.MethodPut, "/drive/"+driveID+"/chunks/"+ref.ID, data); resp.StatusCode != http.StatusOK {
			t.Fatalf("upload to %s: %d %s", driveID, resp.StatusCode, body)
		}
		pages, hashes, _ := drive.SplitPages(driveID, node, []drive.ChunkRef{ref})
		return c.signed(drive.Manifest{Format: 1, Drive: driveID, Node: node, Version: randomHex(16), Operation: drive.OpCreate, Author: c.identity,
			Generation: 1, Kind: drive.KindFile, Mode: drive.ModeReplace, Folder: parent, Name: sealedName(t, pub, "card"), NameHash: randomHex(32),
			NodeKey: sealedFor(t, pub, "node-key", bytes.Repeat([]byte{2}, 32)), ContentKey: sealedFor(t, pub, "content-key", bytes.Repeat([]byte{9}, 32)),
			Count: 1, Pages: hashes}), pages, data
	}
	commit := func(c driveClient, driveID string, body map[string]any) map[string]any {
		t.Helper()
		status, out := c.commitTo(driveID, body)
		if status != http.StatusOK {
			t.Fatalf("%s commit to %s: %d %v", c.identity, driveID, status, out)
		}
		return out
	}
	share := func(issuer driveClient, driveID, node, member, link, role string) {
		id, _ := drive.NewShareID()
		s := drive.Share{Format: 1, Drive: driveID, ID: id, Node: node, Member: member, Link: link, Role: role, Generation: 1,
			NodeKey: sealedFor(t, pub, "node-key", bytes.Repeat([]byte{3}, 32)), NodePublic: base64.RawURLEncoding.EncodeToString(pub),
			Issuer: issuer.identity, Issued: time.Now().UTC().Format(time.RFC3339)}
		if err := s.Sign(issuer.key); err != nil {
			t.Fatal(err)
		}
		commit(issuer, driveID, map[string]any{"share": s})
	}

	// Alice's board: a card, bob as a writer, a public read link.
	aRoot, aBoard, aCard := randomHex(16), randomHex(16), randomHex(16)
	commit(alice, alice.identity, map[string]any{"manifest": folder(alice, alice.identity, aRoot, "")})
	commit(alice, alice.identity, map[string]any{"manifest": folder(alice, alice.identity, aBoard, aRoot)})
	m, pages, _ := card(alice, alice.identity, aCard, aBoard)
	commit(alice, alice.identity, map[string]any{"manifest": m, "pages": pages})
	linkID, _ := drive.NewShareID()
	share(alice, alice.identity, aBoard, bob.identity, "", drive.RoleWrite)
	share(alice, alice.identity, aBoard, "", linkID, drive.RoleRead)

	// The Space has a root of its own (created with the group's key).
	sRoot, sBoard, sCard := randomHex(16), randomHex(16), randomHex(16)
	commit(crew, crew.identity, map[string]any{"manifest": folder(crew, crew.identity, sRoot, "")})

	// Alice, as the Space's admin, re-creates the board there (content
	// re-encrypted for the Space's drive) and re-issues its shares, keeping
	// the link ID; then retires her copy.
	commit(alice, crew.identity, map[string]any{"manifest": folder(alice, crew.identity, sBoard, sRoot)})
	m, pages, data := card(alice, crew.identity, sCard, sBoard)
	commit(alice, crew.identity, map[string]any{"manifest": m, "pages": pages})
	share(alice, crew.identity, sBoard, bob.identity, "", drive.RoleWrite)
	share(alice, crew.identity, sBoard, "", linkID, drive.RoleRead)
	commit(alice, alice.identity, map[string]any{"transfer": map[string]string{"node": aBoard, "to": crew.identity, "to_node": sBoard}})

	// Bob keeps his access, in the Space.
	status, out := bob.json(http.MethodGet, "/drive/"+crew.identity+"/nodes/"+sCard, nil)
	if status != http.StatusOK {
		t.Fatalf("bob in the Space: %d %v", status, out)
	}
	pageResp, pageRaw := bob.do(http.MethodGet, "/drive/"+crew.identity+"/nodes/"+sCard+"/versions/"+m.Version+"/pages/"+m.Pages[0], nil)
	if pageResp.StatusCode != http.StatusOK {
		t.Fatalf("bob reads the page: %d %s", pageResp.StatusCode, pageRaw)
	}
	chunkResp, chunk := bob.do(http.MethodGet, "/drive/"+crew.identity+"/nodes/"+sCard+"/versions/"+m.Version+"/chunks/"+drive.ChunkID(data), nil)
	if chunkResp.StatusCode != http.StatusOK || !bytes.Equal(chunk, data) {
		t.Fatalf("bob reads the card: %d", chunkResp.StatusCode)
	}
	// The old copy is gone for him, and says where it went.
	if status, out := bob.json(http.MethodGet, "/drive/"+alice.identity+"/nodes/"+aBoard, nil); status != http.StatusGone || out["moved_to"] == nil {
		t.Fatalf("old board: %d %v", status, out)
	}
	if status, _ := bob.json(http.MethodGet, "/drive/"+alice.identity+"/nodes/"+aCard, nil); status != http.StatusForbidden {
		t.Fatalf("old card: %d", status)
	}
	// The link, published with alice's drive in it, still opens: it follows.
	status, info := anonymous(t, ts.URL, http.MethodGet, "/drive/"+alice.identity+"/links/"+linkID, nil, nil)
	if status != http.StatusOK || info["drive"] != crew.identity || info["role"] != "read" {
		t.Fatalf("old link: %d %v", status, info)
	}
	if status, _ := anonymous(t, ts.URL, http.MethodGet, "/drive/"+crew.identity+"/nodes/"+sCard, map[string]string{"X-Poweur-Link": linkID}, nil); status != http.StatusOK {
		t.Fatalf("link reads the Space's copy: %d", status)
	}
}
