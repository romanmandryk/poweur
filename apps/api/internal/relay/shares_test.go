package relay

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	idpkg "github.com/poweur/identity"
)

// HTTP-level sharing scenarios (EPIC-005): grants and groups written into
// the owner's tree over DAV, enforced end-to-end on DAV methods, listings,
// the sync changes feed / manifest, and chunked uploads.

// putShareGrant signs a grant as the owner and PUTs it at
// poweur-sys/relay/shares/<id>.json via DAV.
func putShareGrant(t *testing.T, ts *httptest.Server, owner davTestIdentity, ownerTok string, g idpkg.ShareGrant) {
	t.Helper()
	g.Owner = owner.name
	if g.CreatedAt == "" {
		g.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if err := g.Sign(owner.priv); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(g)
	resp := davReq(t, ts, http.MethodPut,
		"/dav/"+owner.name+"/poweur-sys/relay/shares/"+g.ShareID+".json", ownerTok, raw, nil)
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("put grant %s: %d", g.ShareID, resp.StatusCode)
	}
}

// putShareGroup signs a group document and PUTs it via DAV.
func putShareGroup(t *testing.T, ts *httptest.Server, owner davTestIdentity, ownerTok, name string, members ...string) {
	t.Helper()
	gr := idpkg.ShareGroup{
		Group: name, Owner: owner.name, Members: members,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := gr.Sign(owner.priv); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(gr)
	resp := davReq(t, ts, http.MethodPut,
		"/dav/"+owner.name+"/poweur-sys/relay/groups/"+name+".json", ownerTok, raw, nil)
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("put group %s: %d", name, resp.StatusCode)
	}
}

func audienceIDs(names ...string) []idpkg.ShareAudience {
	var out []idpkg.ShareAudience
	for _, n := range names {
		out = append(out, idpkg.ShareAudience{ID: n})
	}
	return out
}

// shareFixture: alice owns a tree with content under /shared, bob/carol/
// dave are other hosted identities.
type shareFixture struct {
	server *Server
	ts     *httptest.Server
	alice  davTestIdentity
	bob    davTestIdentity
	carol  davTestIdentity
	dave   davTestIdentity
	aliceTok string
}

func newShareFixture(t *testing.T) *shareFixture {
	t.Helper()
	server, ts := newDAVTestServer(t, 0, 0)
	fx := &shareFixture{
		server: server, ts: ts,
		alice: registerDAVIdentity(t, server, ts, "alice.poweur.net"),
		bob:   registerDAVIdentity(t, server, ts, "bob.poweur.net"),
		carol: registerDAVIdentity(t, server, ts, "carol.poweur.net"),
		dave:  registerDAVIdentity(t, server, ts, "dave.poweur.net"),
	}
	fx.aliceTok = mintDAVToken(t, ts, fx.alice, "", "")
	// Seed alice's tree: /shared/project/{readme.txt,docs/design.md},
	// /shared/private-project/secret.txt, /private/diary.txt.
	steps := []struct{ method, path, body string }{
		{"MKCOL", "/shared/project", ""},
		{"PUT", "/shared/project/readme.txt", "readme v1"},
		{"MKCOL", "/shared/project/docs", ""},
		{"PUT", "/shared/project/docs/design.md", "design v1"},
		{"MKCOL", "/shared/private-project", ""},
		{"PUT", "/shared/private-project/secret.txt", "top secret"},
		{"PUT", "/private/diary.txt", "dear diary"},
	}
	for _, s := range steps {
		var body []byte
		if s.body != "" {
			body = []byte(s.body)
		}
		resp := davReq(t, fx.ts, s.method, "/dav/alice.poweur.net"+s.path, fx.aliceTok, body, nil)
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			t.Fatalf("seed %s %s: %d", s.method, s.path, resp.StatusCode)
		}
	}
	return fx
}

// visitorTok mints a token for someone accessing alice's tree.
func (fx *shareFixture) visitorTok(t *testing.T, visitor davTestIdentity, scope string) string {
	t.Helper()
	return mintDAVToken(t, fx.ts, visitor, fx.alice.name, scope)
}

func (fx *shareFixture) dav(t *testing.T, method, path, token, body string, hdr map[string]string) int {
	t.Helper()
	var b []byte
	if body != "" {
		b = []byte(body)
	}
	resp := davReq(t, fx.ts, method, "/dav/alice.poweur.net"+path, token, b, hdr)
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	return resp.StatusCode
}

func (fx *shareFixture) davGet(t *testing.T, path, token string) (int, string) {
	t.Helper()
	resp := davReq(t, fx.ts, http.MethodGet, "/dav/alice.poweur.net"+path, token, nil, nil)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// TestShareMethodMatrix: the full read/write DAV method matrix for a
// read-only and a read-write grant on a folder.
func TestShareMethodMatrix(t *testing.T) {
	fx := newShareFixture(t)
	putShareGrant(t, fx.ts, fx.alice, fx.aliceTok, idpkg.ShareGrant{
		ShareID: "shr_rw", Path: "shared/project",
		Audience: audienceIDs(fx.bob.name), Permissions: []string{"read", "write"},
	})
	putShareGrant(t, fx.ts, fx.alice, fx.aliceTok, idpkg.ShareGrant{
		ShareID: "shr_ro", Path: "shared/project",
		Audience: audienceIDs(fx.carol.name), Permissions: []string{"read"},
	})
	bobTok := fx.visitorTok(t, fx.bob, "dav:full")
	carolTok := fx.visitorTok(t, fx.carol, "dav:full")

	// Reads work for both.
	for name, tok := range map[string]string{"bob": bobTok, "carol": carolTok} {
		if code, body := fx.davGet(t, "/shared/project/readme.txt", tok); code != 200 || body != "readme v1" {
			t.Fatalf("%s GET: %d %q", name, code, body)
		}
		if code := fx.dav(t, "PROPFIND", "/shared/project", tok, "", map[string]string{"Depth": "1"}); code != 207 {
			t.Fatalf("%s PROPFIND: %d", name, code)
		}
	}

	// Write matrix: bob (rw) succeeds, carol (read) gets 403.
	writes := []struct {
		method, path, body string
		hdr                map[string]string
		okCode             int
	}{
		{"PUT", "/shared/project/new.txt", "from visitor", nil, 201},
		{"MKCOL", "/shared/project/subdir", "", nil, 201},
		{"PUT", "/shared/project/subdir/deep.txt", "deep", nil, 201},
		{"MOVE", "/shared/project/new.txt", "", map[string]string{
			"Destination": fx.ts.URL + "/dav/alice.poweur.net/shared/project/renamed.txt"}, 201},
		{"DELETE", "/shared/project/renamed.txt", "", nil, 204},
	}
	for _, wr := range writes {
		if code := fx.dav(t, wr.method, wr.path, carolTok, wr.body, wr.hdr); code != http.StatusForbidden {
			t.Fatalf("carol %s %s: %d want 403", wr.method, wr.path, code)
		}
	}
	for _, wr := range writes {
		if code := fx.dav(t, wr.method, wr.path, bobTok, wr.body, wr.hdr); code != wr.okCode {
			t.Fatalf("bob %s %s: %d want %d", wr.method, wr.path, code, wr.okCode)
		}
	}

	// Dave (no grant) is denied everything, even reads.
	daveTok := fx.visitorTok(t, fx.dave, "dav:full")
	if code, _ := fx.davGet(t, "/shared/project/readme.txt", daveTok); code != http.StatusForbidden {
		t.Fatalf("dave GET: %d want 403", code)
	}
	if code := fx.dav(t, "PUT", "/shared/project/dave.txt", daveTok, "x", nil); code != http.StatusForbidden {
		t.Fatalf("dave PUT: %d want 403", code)
	}
}

// TestShareParentToChildPropagation: a folder grant covers existing nested
// content and children created later by either side.
func TestShareParentToChildPropagation(t *testing.T) {
	fx := newShareFixture(t)
	putShareGrant(t, fx.ts, fx.alice, fx.aliceTok, idpkg.ShareGrant{
		ShareID: "shr_p", Path: "shared/project",
		Audience: audienceIDs(fx.bob.name), Permissions: []string{"read", "write"},
	})
	bobTok := fx.visitorTok(t, fx.bob, "dav:full")

	// Existing nested file.
	if code, body := fx.davGet(t, "/shared/project/docs/design.md", bobTok); code != 200 || body != "design v1" {
		t.Fatalf("nested read: %d %q", code, body)
	}
	// Owner adds a new child after the grant: bob sees it.
	resp := davReq(t, fx.ts, "PUT", "/dav/alice.poweur.net/shared/project/docs/later.md", fx.aliceTok, []byte("added later"), nil)
	resp.Body.Close()
	if code, body := fx.davGet(t, "/shared/project/docs/later.md", bobTok); code != 200 || body != "added later" {
		t.Fatalf("later child read: %d %q", code, body)
	}
	// Bob builds a deep new subtree.
	if code := fx.dav(t, "MKCOL", "/shared/project/bob-dir", bobTok, "", nil); code != 201 {
		t.Fatalf("bob mkcol: %d", code)
	}
	if code := fx.dav(t, "PUT", "/shared/project/bob-dir/notes.txt", bobTok, "bob notes", nil); code != 201 {
		t.Fatalf("bob deep put: %d", code)
	}
	// The owner sees bob's write.
	if code, body := fx.davGet(t, "/shared/project/bob-dir/notes.txt", fx.aliceTok); code != 200 || body != "bob notes" {
		t.Fatalf("owner read of visitor write: %d %q", code, body)
	}
}

// TestShareListingFiltering: PROPFIND of /shared shows a grant-holder only
// the subtree they can reach; siblings stay invisible.
func TestShareListingFiltering(t *testing.T) {
	fx := newShareFixture(t)
	putShareGrant(t, fx.ts, fx.alice, fx.aliceTok, idpkg.ShareGrant{
		ShareID: "shr_l", Path: "shared/project",
		Audience: audienceIDs(fx.bob.name), Permissions: []string{"read"},
	})
	bobTok := fx.visitorTok(t, fx.bob, "dav:read")

	resp := davReq(t, fx.ts, "PROPFIND", "/dav/alice.poweur.net/shared", bobTok, nil, map[string]string{"Depth": "1"})
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 207 {
		t.Fatalf("PROPFIND /shared: %d", resp.StatusCode)
	}
	listing := string(raw)
	if !strings.Contains(listing, "project") {
		t.Fatal("granted folder must appear in the listing")
	}
	if strings.Contains(listing, "private-project") {
		t.Fatal("ungranted sibling must not appear in the listing")
	}
	// Direct access to the sibling is denied outright.
	if code, _ := fx.davGet(t, "/shared/private-project/secret.txt", bobTok); code != http.StatusForbidden {
		t.Fatalf("sibling read: %d want 403", code)
	}
}

// TestShareSingleFile: sharing one file grants exactly that file.
func TestShareSingleFile(t *testing.T) {
	fx := newShareFixture(t)
	putShareGrant(t, fx.ts, fx.alice, fx.aliceTok, idpkg.ShareGrant{
		ShareID: "shr_f", Path: "shared/project/readme.txt",
		Audience: audienceIDs(fx.bob.name), Permissions: []string{"read", "write"},
	})
	bobTok := fx.visitorTok(t, fx.bob, "dav:full")

	if code, body := fx.davGet(t, "/shared/project/readme.txt", bobTok); code != 200 || body != "readme v1" {
		t.Fatalf("file read: %d %q", code, body)
	}
	if code := fx.dav(t, "PUT", "/shared/project/readme.txt", bobTok, "readme v2 by bob", nil); code >= 300 {
		t.Fatalf("file write: %d", code)
	}
	// The sibling and the parent dir's other children stay closed.
	if code, _ := fx.davGet(t, "/shared/project/docs/design.md", bobTok); code != http.StatusForbidden {
		t.Fatalf("sibling read: %d want 403", code)
	}
	if code := fx.dav(t, "PUT", "/shared/project/other.txt", bobTok, "x", nil); code != http.StatusForbidden {
		t.Fatalf("sibling write: %d want 403", code)
	}
	// Traversal: listing the parent shows only the granted file.
	resp := davReq(t, fx.ts, "PROPFIND", "/dav/alice.poweur.net/shared/project", bobTok, nil, map[string]string{"Depth": "1"})
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(raw), "readme.txt") || strings.Contains(string(raw), "docs") {
		t.Fatalf("parent listing must show only the granted file:\n%s", raw)
	}
}

// TestShareMoveOutOfShareDenied: MOVE with a destination outside the
// granted subtree is rejected (no exfiltration into other tree areas).
func TestShareMoveOutOfShareDenied(t *testing.T) {
	fx := newShareFixture(t)
	putShareGrant(t, fx.ts, fx.alice, fx.aliceTok, idpkg.ShareGrant{
		ShareID: "shr_m", Path: "shared/project",
		Audience: audienceIDs(fx.bob.name), Permissions: []string{"read", "write"},
	})
	bobTok := fx.visitorTok(t, fx.bob, "dav:full")

	code := fx.dav(t, "MOVE", "/shared/project/readme.txt", bobTok, "", map[string]string{
		"Destination": fx.ts.URL + "/dav/alice.poweur.net/shared/private-project/stolen.txt"})
	if code != http.StatusForbidden {
		t.Fatalf("move out of share: %d want 403", code)
	}
	code = fx.dav(t, "MOVE", "/shared/project/readme.txt", bobTok, "", map[string]string{
		"Destination": fx.ts.URL + "/dav/alice.poweur.net/shared/project/docs/moved.txt"})
	if code != 201 {
		t.Fatalf("move within share: %d want 201", code)
	}
}

// TestShareGroupsLifecycle: group grants, membership add/remove, over HTTP.
func TestShareGroupsLifecycle(t *testing.T) {
	fx := newShareFixture(t)
	putShareGroup(t, fx.ts, fx.alice, fx.aliceTok, "team", fx.bob.name, fx.carol.name)
	putShareGrant(t, fx.ts, fx.alice, fx.aliceTok, idpkg.ShareGrant{
		ShareID: "shr_g", Path: "shared/project",
		Audience: []idpkg.ShareAudience{{Group: "team"}}, Permissions: []string{"read", "write"},
	})
	bobTok := fx.visitorTok(t, fx.bob, "dav:full")
	carolTok := fx.visitorTok(t, fx.carol, "dav:full")
	daveTok := fx.visitorTok(t, fx.dave, "dav:full")

	for name, tok := range map[string]string{"bob": bobTok, "carol": carolTok} {
		if code, _ := fx.davGet(t, "/shared/project/readme.txt", tok); code != 200 {
			t.Fatalf("group member %s read: %d", name, code)
		}
	}
	if code, _ := fx.davGet(t, "/shared/project/readme.txt", daveTok); code != http.StatusForbidden {
		t.Fatalf("non-member read: %d want 403", code)
	}

	// One signed member-list update: dave in, bob out — no new grant.
	putShareGroup(t, fx.ts, fx.alice, fx.aliceTok, "team", fx.carol.name, fx.dave.name)
	if code, _ := fx.davGet(t, "/shared/project/readme.txt", daveTok); code != 200 {
		t.Fatalf("added member read: %d", code)
	}
	if code, _ := fx.davGet(t, "/shared/project/readme.txt", bobTok); code != http.StatusForbidden {
		t.Fatalf("removed member read: %d want 403", code)
	}
}

// TestShareExpiryAndRevocation: expiry honored; deleting the grant file
// revokes on the very next request.
func TestShareExpiryAndRevocation(t *testing.T) {
	fx := newShareFixture(t)
	putShareGrant(t, fx.ts, fx.alice, fx.aliceTok, idpkg.ShareGrant{
		ShareID: "shr_exp", Path: "shared/project",
		Audience: audienceIDs(fx.bob.name), Permissions: []string{"read"},
		ExpiresAt: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
	})
	bobTok := fx.visitorTok(t, fx.bob, "dav:read")
	if code, _ := fx.davGet(t, "/shared/project/readme.txt", bobTok); code != http.StatusForbidden {
		t.Fatalf("expired grant read: %d want 403", code)
	}

	putShareGrant(t, fx.ts, fx.alice, fx.aliceTok, idpkg.ShareGrant{
		ShareID: "shr_live", Path: "shared/project",
		Audience: audienceIDs(fx.bob.name), Permissions: []string{"read"},
	})
	if code, _ := fx.davGet(t, "/shared/project/readme.txt", bobTok); code != 200 {
		t.Fatalf("live grant read: %d", code)
	}
	// Revoke = owner deletes the grant file.
	resp := davReq(t, fx.ts, http.MethodDelete, "/dav/alice.poweur.net/poweur-sys/relay/shares/shr_live.json", fx.aliceTok, nil, nil)
	resp.Body.Close()
	if code, _ := fx.davGet(t, "/shared/project/readme.txt", bobTok); code != http.StatusForbidden {
		t.Fatalf("revoked grant read: %d want 403", code)
	}
}

// TestShareScopeCapsGrant: a visitor's read-only token cannot write even
// into a write-granted share.
func TestShareScopeCapsGrant(t *testing.T) {
	fx := newShareFixture(t)
	putShareGrant(t, fx.ts, fx.alice, fx.aliceTok, idpkg.ShareGrant{
		ShareID: "shr_s", Path: "shared/project",
		Audience: audienceIDs(fx.bob.name), Permissions: []string{"read", "write"},
	})
	roTok := fx.visitorTok(t, fx.bob, "dav:read")
	if code := fx.dav(t, "PUT", "/shared/project/x.txt", roTok, "x", nil); code != http.StatusForbidden {
		t.Fatalf("read-scoped token write: %d want 403", code)
	}
	if code, _ := fx.davGet(t, "/shared/project/readme.txt", roTok); code != 200 {
		t.Fatalf("read-scoped token read: %d", code)
	}
}

// TestShareSyncVisibilityAndActorAudit: grant holders see exactly their
// slice in the changes feed and manifest; their writes journal with actor.
func TestShareSyncVisibilityAndActorAudit(t *testing.T) {
	fx := newShareFixture(t)
	putShareGrant(t, fx.ts, fx.alice, fx.aliceTok, idpkg.ShareGrant{
		ShareID: "shr_v", Path: "shared/project",
		Audience: audienceIDs(fx.bob.name), Permissions: []string{"read", "write"},
	})
	bobTok := fx.visitorTok(t, fx.bob, "dav:full")

	// Bob writes through the share.
	if code := fx.dav(t, "PUT", "/shared/project/from-bob.txt", bobTok, "bob was here", nil); code >= 300 {
		t.Fatalf("bob write: %d", code)
	}

	// Bob's changes feed on alice's tree: /shared/project + ancestors only.
	out := getChanges(t, fx.ts, "alice.poweur.net", bobTok, "")
	var sawBobWrite bool
	for _, rec := range out.Changes {
		p := rec.Path
		ok := strings.HasPrefix(p, "shared/project") || p == "shared" ||
			strings.HasPrefix(p, "public") || strings.HasPrefix(p, "poweur-sys/public")
		if !ok {
			t.Fatalf("bob's feed leaked %q", p)
		}
		if p == "shared/project/from-bob.txt" {
			sawBobWrite = true
			if rec.Actor != fx.bob.name {
				t.Fatalf("share write must journal the visitor as actor: %+v", rec)
			}
		}
	}
	if !sawBobWrite {
		t.Fatal("bob's own write must appear in his feed")
	}
	// private/ and the ungranted sibling never leak.
	for _, rec := range out.Changes {
		if strings.HasPrefix(rec.Path, "private") || strings.HasPrefix(rec.Path, "shared/private-project") {
			t.Fatalf("feed leaked %q", rec.Path)
		}
	}

	// The owner sees bob as actor too (audit trail).
	ownerOut := getChanges(t, fx.ts, "alice.poweur.net", fx.aliceTok, "")
	var ownerSaw bool
	for _, rec := range ownerOut.Changes {
		if rec.Path == "shared/project/from-bob.txt" && rec.Actor == fx.bob.name {
			ownerSaw = true
		}
	}
	if !ownerSaw {
		t.Fatal("owner's feed must attribute the write to bob")
	}

	// Manifest: bob sees the granted subtree, not the siblings.
	resp := davReq(t, fx.ts, http.MethodGet, "/sync/alice.poweur.net/manifest", bobTok, nil, nil)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	manifest := string(raw)
	if !strings.Contains(manifest, "shared/project/readme.txt") {
		t.Fatal("manifest must include the granted subtree")
	}
	if strings.Contains(manifest, "private-project") || strings.Contains(manifest, "diary") {
		t.Fatalf("manifest leaked ungranted paths:\n%s", manifest)
	}
}

// TestShareChunkedUploadThroughGrant: rw grant-holders may use the
// resumable upload endpoint; read-only holders may not.
func TestShareChunkedUploadThroughGrant(t *testing.T) {
	fx := newShareFixture(t)
	putShareGrant(t, fx.ts, fx.alice, fx.aliceTok, idpkg.ShareGrant{
		ShareID: "shr_up", Path: "shared/project",
		Audience: audienceIDs(fx.bob.name), Permissions: []string{"read", "write"},
	})
	putShareGrant(t, fx.ts, fx.alice, fx.aliceTok, idpkg.ShareGrant{
		ShareID: "shr_up_ro", Path: "shared/project",
		Audience: audienceIDs(fx.carol.name), Permissions: []string{"read"},
	})
	bobTok := fx.visitorTok(t, fx.bob, "dav:full")
	carolTok := fx.visitorTok(t, fx.carol, "dav:full")

	body := strings.Repeat("upload", 512)
	resp := davReq(t, fx.ts, http.MethodPost,
		"/sync/alice.poweur.net/upload?path=/shared/project/big-from-bob.bin", bobTok, nil,
		map[string]string{"Upload-Length": strconv.Itoa(len(body))})
	var created uploadResponse
	_ = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("bob upload create: %d", resp.StatusCode)
	}
	patch := davReq(t, fx.ts, http.MethodPatch, "/sync/alice.poweur.net/upload/"+created.ID, bobTok,
		[]byte(body), map[string]string{"Upload-Offset": "0"})
	io.Copy(io.Discard, patch.Body) //nolint:errcheck
	patch.Body.Close()
	if patch.StatusCode != http.StatusOK {
		t.Fatalf("bob upload complete: %d", patch.StatusCode)
	}
	if code, got := fx.davGet(t, "/shared/project/big-from-bob.bin", fx.aliceTok); code != 200 || got != body {
		t.Fatalf("assembled shared upload: %d (%d bytes)", code, len(got))
	}

	// Read-only carol: 403 at create.
	resp = davReq(t, fx.ts, http.MethodPost,
		"/sync/alice.poweur.net/upload?path=/shared/project/nope.bin", carolTok, nil,
		map[string]string{"Upload-Length": "10"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("carol upload create: %d want 403", resp.StatusCode)
	}
}

// TestShareAppsSubtree: /apps paths are shareable the same way (the shared
// task-project pattern from EPIC-006).
func TestShareAppsSubtree(t *testing.T) {
	fx := newShareFixture(t)
	resp := davReq(t, fx.ts, "MKCOL", "/dav/alice.poweur.net/apps/net.poweur.tasks", fx.aliceTok, nil, nil)
	resp.Body.Close()
	resp = davReq(t, fx.ts, "PUT", "/dav/alice.poweur.net/apps/net.poweur.tasks/project.json", fx.aliceTok, []byte(`{"tasks":[]}`), nil)
	resp.Body.Close()

	putShareGrant(t, fx.ts, fx.alice, fx.aliceTok, idpkg.ShareGrant{
		ShareID: "shr_app", Path: "apps/net.poweur.tasks",
		Audience: audienceIDs(fx.bob.name), Permissions: []string{"read", "write"},
	})
	bobTok := fx.visitorTok(t, fx.bob, "dav:full")
	if code, _ := fx.davGet(t, "/apps/net.poweur.tasks/project.json", bobTok); code != 200 {
		t.Fatalf("apps share read: %d", code)
	}
	if code := fx.dav(t, "PUT", "/apps/net.poweur.tasks/project.json", bobTok, `{"tasks":["x"]}`, nil); code >= 300 {
		t.Fatalf("apps share write: %d", code)
	}
	// Other apps stay closed.
	if code, _ := fx.davGet(t, "/apps/other.app/data.json", bobTok); code != http.StatusForbidden {
		t.Fatalf("other app read: %d want 403", code)
	}
}

// TestShareForgedGrantIgnoredOverHTTP: a grant file signed with the wrong
// key sitting in the tree grants nothing.
func TestShareForgedGrantIgnoredOverHTTP(t *testing.T) {
	fx := newShareFixture(t)
	// bob signs a grant for himself and (as a thought experiment — the
	// relay wrote it, e.g. via a compromised path) it lands in alice's tree.
	forged := idpkg.ShareGrant{
		ShareID: "shr_forged", Owner: fx.alice.name, Path: "shared/private-project",
		Audience:    audienceIDs(fx.bob.name),
		Permissions: []string{"read", "write"},
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
	}
	if err := forged.Sign(fx.bob.priv); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(forged)
	resp := davReq(t, fx.ts, http.MethodPut,
		"/dav/alice.poweur.net/poweur-sys/relay/shares/shr_forged.json", fx.aliceTok, raw, nil)
	resp.Body.Close()

	bobTok := fx.visitorTok(t, fx.bob, "dav:full")
	if code, _ := fx.davGet(t, "/shared/private-project/secret.txt", bobTok); code != http.StatusForbidden {
		t.Fatalf("forged grant must not grant access: %d want 403", code)
	}
}
