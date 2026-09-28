package integration_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	clipkg "github.com/poweur/cli/pkg/cli"
	idpkg "github.com/poweur/identity"
	"github.com/poweur/identity/drive"
)

// anonymous calls the drive API with only a link — no identity, no key.
func anonymous(t *testing.T, relay, method, path string, headers map[string]string, body []byte) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, _ := http.NewRequest(method, relay+path, reader)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// INT_DRIVE_03 (E20-T7 file request): alice publishes a create-only link on
// her inbox folder with proof-of-work and a two-file cap. Someone with no
// identity uploads an encrypted file and submits it signed by a throwaway
// guest key; they cannot list or read the folder, a submission without
// proof-of-work is refused, and the cap holds.
func TestINT_DRIVE_03_AnonymousFileRequest(t *testing.T) {
	zone := newZone(t)
	ts, addr := newHostedRelay(t, zone, t.TempDir())
	zone.SetHost("reqalice.poweur.net", addr)
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })
	home := t.TempDir()
	runCLI(t, home, "identity", "create", "reqalice.poweur.net", "--hosted", "--relay", ts.URL, "--json")
	alice := driveClient{t: t, relay: ts.URL, identity: "reqalice.poweur.net", key: loadIdentityKey(t, home, "reqalice.poweur.net")}
	base := "/drive/" + alice.identity
	folderPub, _, _ := idpkg.GenerateX25519Keypair()

	root, inbox := randomHex(16), randomHex(16)
	for _, m := range []drive.Manifest{
		{Format: 1, Drive: alice.identity, Node: root, Version: randomHex(16), Operation: drive.OpCreate, Author: alice.identity, Generation: 1,
			Kind: drive.KindFolder, NodeKey: sealedFor(t, folderPub, "node-key", bytes.Repeat([]byte{1}, 32)), Pages: []string{}},
		{Format: 1, Drive: alice.identity, Node: inbox, Version: randomHex(16), Operation: drive.OpCreate, Author: alice.identity, Generation: 1,
			Kind: drive.KindFolder, Folder: root, Name: sealedName(t, folderPub, "Uploads"), NameHash: randomHex(32),
			NodeKey: sealedFor(t, folderPub, "node-key", bytes.Repeat([]byte{2}, 32)), Pages: []string{}},
	} {
		if status, out := alice.commit(map[string]any{"manifest": alice.signed(m)}); status != http.StatusOK {
			t.Fatalf("setup: %d %v", status, out)
		}
	}
	linkID, _ := drive.NewShareID()
	shareID, _ := drive.NewShareID()
	s := drive.Share{Format: 1, Drive: alice.identity, ID: shareID, Node: inbox, Link: linkID, Role: drive.RoleCreate, Generation: 1,
		NodePublic: base64.RawURLEncoding.EncodeToString(folderPub), Caps: drive.Caps{Files: 2, PerHour: 50}, PoW: 8,
		Issuer: alice.identity, Issued: time.Now().UTC().Format(time.RFC3339)}
	if err := s.Sign(alice.key); err != nil {
		t.Fatal(err)
	}
	if status, out := alice.commit(map[string]any{"share": s}); status != http.StatusOK {
		t.Fatalf("link: %d %v", status, out)
	}

	// The submitter: only the link ID (and the folder public key from the share).
	link := map[string]string{"X-Poweur-Link": linkID}
	status, info := anonymous(t, ts.URL, http.MethodGet, base+"/links/"+linkID, nil, nil)
	if status != http.StatusOK || info["role"] != "create" || info["pow"] != float64(8) {
		t.Fatalf("link info: %d %v", status, info)
	}
	status, out := anonymous(t, ts.URL, http.MethodGet, base+"/shares", link, nil)
	shares, _ := out["shares"].([]any)
	if status != http.StatusOK || len(shares) != 1 {
		t.Fatalf("open link: %d %v", status, out)
	}
	pub, _ := base64.RawURLEncoding.DecodeString(shares[0].(map[string]any)["node_public"].(string))

	_, guestPriv, _ := ed25519.GenerateKey(nil)
	guest := drive.GuestAuthor(guestPriv.Public().(ed25519.PublicKey))
	submit := func(pow bool) (int, map[string]any) {
		t.Helper()
		node, contentKey := randomHex(16), bytes.Repeat([]byte{7}, 32)
		ctx, _ := drive.Context(alice.identity, node, drive.PurposeContent, 1)
		data, _ := drive.EncryptChunk(contentKey, []byte("my tax documents"), ctx)
		ref := drive.ChunkRef{ID: drive.ChunkID(data), Size: uint64(len(data))}
		if status, out := anonymous(t, ts.URL, http.MethodPut, base+"/chunks/"+ref.ID, link, data); status != http.StatusOK {
			t.Fatalf("anonymous upload: %d %v", status, out)
		}
		pages, hashes, _ := drive.SplitPages(alice.identity, node, []drive.ChunkRef{ref})
		m := drive.Manifest{Format: 1, Drive: alice.identity, Node: node, Version: randomHex(16), Operation: drive.OpCreate, Author: guest,
			Generation: 1, Kind: drive.KindFile, Mode: drive.ModeReplace, Folder: inbox,
			// A create-only writer cannot compute the folder's name index: a random token.
			Name: sealedName(t, pub, "taxes.pdf"), NameHash: randomHex(32),
			NodeKey: sealedFor(t, pub, "node-key", bytes.Repeat([]byte{8}, 32)), ContentKey: sealedFor(t, pub, "content-key", contentKey),
			Count: 1, Pages: hashes}
		if err := m.Sign(guestPriv); err != nil {
			t.Fatal(err)
		}
		headers := map[string]string{"X-Poweur-Link": linkID}
		if pow {
			_, challenge := anonymous(t, ts.URL, http.MethodGet, "/auth/pow?purpose=drive-link&identity="+alice.identity+"&link="+linkID, nil, nil)
			token := challenge["token"].(string)
			solution, err := idpkg.SolvePow(context.Background(), token, int(challenge["bits"].(float64)))
			if err != nil {
				t.Fatal(err)
			}
			headers["X-Poweur-PoW-Token"], headers["X-Poweur-PoW-Solution"] = token, solution
		}
		body, _ := json.Marshal(map[string]any{"id": randomHex(16), "manifest": m, "pages": pages})
		return anonymous(t, ts.URL, http.MethodPost, base+"/commit", headers, body)
	}
	if status, out := submit(false); status != http.StatusForbidden || out["error"] != "pow_required" {
		t.Fatalf("without proof-of-work: %d %v", status, out)
	}
	if status, out := submit(true); status != http.StatusOK {
		t.Fatalf("submission: %d %v", status, out)
	}
	// The submitter sees nothing in the folder, not even their own file.
	if status, _ := anonymous(t, ts.URL, http.MethodGet, base+"/nodes/"+inbox+"/children", link, nil); status != http.StatusForbidden {
		t.Fatalf("anonymous listing: %d", status)
	}
	if status, _ := submit(true); status != http.StatusOK {
		t.Fatal("second submission")
	}
	if status, out := submit(true); status != http.StatusTooManyRequests {
		t.Fatalf("over the file cap: %d %v", status, out)
	}
	// Alice sees both, authored by the guest.
	status, out = alice.json(http.MethodGet, base+"/nodes/"+inbox+"/children", nil)
	if children, _ := out["children"].([]any); status != http.StatusOK || len(children) != 2 {
		t.Fatalf("alice's inbox: %d %v", status, out)
	}
	// A guest key cannot be used without a link.
	if status, _ := anonymous(t, ts.URL, http.MethodPost, base+"/commit", nil, []byte(`{}`)); status != http.StatusUnauthorized {
		t.Fatalf("no link, no identity: %d", status)
	}
}
