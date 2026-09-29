package relay

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/poweur/identity/drive"
)

// Public folders are served at https://<identity>/pub/..., decrypted with the
// content key their manifests publish; nothing private resolves there.
func TestPublicFolders(t *testing.T) {
	server, ts := newTestRelay(t)
	alice := registerTestIdentity(t, server, ts, "pubalice.poweur.net")
	base := "/drive/" + alice.name
	commit := func(m drive.Manifest, pages []drive.ChunkPage) (int, map[string]any) {
		t.Helper()
		if err := m.Sign(alice.priv); err != nil {
			t.Fatal(err)
		}
		status, out, _ := driveReq(t, ts, http.MethodPost, alice, base+"/commit", map[string]any{"id": randHex(16), "manifest": m, "pages": pages})
		return status, out
	}
	folder := func(node, parent, name string, public bool) drive.Manifest {
		m := drive.Manifest{Format: 1, Drive: alice.name, Node: node, Version: randHex(16), Operation: drive.OpCreate, Author: alice.name,
			Generation: 1, Kind: drive.KindFolder, Folder: parent, Pages: []string{}}
		if public {
			m.Public, m.PlainName, m.NameHash = true, name, drive.PublicNameHash(parent, name)
		} else {
			m.NodeKey = testSealed(1)
			if parent != "" {
				m.Name, m.NameHash = testSealed(2), randHex(32)
			}
		}
		return m
	}
	root, site, private := randHex(16), randHex(16), randHex(16)
	for _, m := range []drive.Manifest{folder(root, "", "", false), folder(site, root, "site", true), folder(private, root, "", false)} {
		if status, out := commit(m, nil); status != http.StatusOK {
			t.Fatalf("create %s: %d %v", m.Node, status, out)
		}
	}

	// A public file: encrypted like any other, key published in the manifest.
	key := bytes.Repeat([]byte{7}, 32)
	page := randHex(16)
	m := drive.Manifest{Format: 1, Drive: alice.name, Node: page, Version: randHex(16), Operation: drive.OpCreate, Author: alice.name,
		Generation: 1, Kind: drive.KindFile, Mode: drive.ModeReplace, Folder: site, Public: true, PlainName: "index.html",
		NameHash: drive.PublicNameHash(site, "index.html"), PlainKey: base64.RawURLEncoding.EncodeToString(key)}
	ctx, _ := drive.Context(alice.name, page, drive.PurposeContent, 1)
	blob, err := drive.EncryptChunk(key, []byte("<h1>hello public</h1>"), ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status, out, _ := driveReq(t, ts, http.MethodPut, alice, base+"/chunks/"+drive.ChunkID(blob), blob); status != http.StatusOK {
		t.Fatalf("upload: %d %v", status, out)
	}
	pages, hashes, _ := drive.SplitPages(alice.name, page, []drive.ChunkRef{{ID: drive.ChunkID(blob), Size: uint64(len(blob))}})
	m.Count, m.Pages = 1, hashes
	if status, out := commit(m, pages); status != http.StatusOK {
		t.Fatalf("create public file: %d %v", status, out)
	}

	get := func(path, accept string) (*http.Response, string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		req.Host = alice.name
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp, string(raw)
	}
	resp, body := get("/pub/site/index.html", "")
	if resp.StatusCode != http.StatusOK || body != "<h1>hello public</h1>" || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("public file: %d %q %v", resp.StatusCode, body, resp.Header)
	}
	if !strings.Contains(resp.Header.Get("Content-Security-Policy"), "sandbox") || resp.Header.Get("ETag") == "" {
		t.Fatalf("public headers: %v", resp.Header)
	}
	if resp, _ := get("/pub/site", ""); resp.StatusCode != http.StatusMovedPermanently {
		t.Fatalf("folder without slash: %d", resp.StatusCode)
	}
	resp, body = get("/pub/", "")
	var listing struct {
		Entries []struct{ Name, Kind string } `json:"entries"`
	}
	if err := json.Unmarshal([]byte(body), &listing); err != nil || resp.StatusCode != http.StatusOK || len(listing.Entries) != 1 || listing.Entries[0].Name != "site" {
		t.Fatalf("public roots: %d %s", resp.StatusCode, body)
	}
	if resp, body := get("/pub/site/", "text/html"); resp.StatusCode != http.StatusOK || !strings.Contains(body, `href="index.html"`) {
		t.Fatalf("html listing: %d %s", resp.StatusCode, body)
	}
	// Nothing private resolves, and unknown hosts serve nothing.
	for _, path := range []string{"/pub/site/missing.txt", "/pub/" + private} {
		if resp, _ := get(path, ""); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s: %d", path, resp.StatusCode)
		}
	}
	// Dot segments never reach the handler: the router redirects to the
	// cleaned path, outside /pub.
	if resp, _ := get("/pub/../drive", ""); resp.StatusCode == http.StatusOK {
		t.Fatal("dot segments served content")
	}

	// The engine keeps public trees public and rooted at the top.
	if status, _ := commit(folder(randHex(16), site, "", false), nil); status != http.StatusUnprocessableEntity && status != http.StatusBadRequest {
		t.Fatalf("private node in a public folder: %d", status)
	}
	if status, _ := commit(folder(randHex(16), private, "deep", true), nil); status != http.StatusUnprocessableEntity && status != http.StatusBadRequest {
		t.Fatalf("public tree below the top: %d", status)
	}
	// A public node cannot turn private (or back): a move keeps its kind.
	status, info, _ := driveReq(t, ts, http.MethodGet, alice, base+"/nodes/"+site, nil)
	if status != http.StatusOK || info["public"] != true {
		t.Fatalf("node info: %d %v", status, info)
	}
	move := drive.Manifest{Format: 1, Drive: alice.name, Node: site, Version: randHex(16), Parent: info["head"].(string), Operation: drive.OpMove,
		Author: alice.name, Generation: 1, Kind: drive.KindFolder, Folder: root, Name: testSealed(5), NameHash: randHex(32), NodeKey: testSealed(6), Pages: []string{}}
	if status, _ := commit(move, nil); status != http.StatusUnprocessableEntity && status != http.StatusBadRequest {
		t.Fatalf("public folder turned private: %d", status)
	}
}
