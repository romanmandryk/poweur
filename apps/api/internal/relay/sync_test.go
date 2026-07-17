package relay

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/poweur/api/internal/files"
)

type changesResponse struct {
	Identity   string                `json:"identity"`
	Since      string                `json:"since"`
	Next       string                `json:"next"`
	Latest     string                `json:"latest"`
	FullResync bool                  `json:"full_resync"`
	Changes    []files.JournalRecord `json:"changes"`
}

func getChanges(t *testing.T, ts *httptest.Server, identity, token, query string) changesResponse {
	t.Helper()
	resp := davReq(t, ts, http.MethodGet, "/sync/"+identity+"/changes"+query, token, nil, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("changes: %d %s", resp.StatusCode, raw)
	}
	var out changesResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSyncChangesFeed(t *testing.T) {
	server, ts := newDAVTestServer(t, 0, 0)
	alice := registerDAVIdentity(t, server, ts, "alice.poweur.net")
	token := mintDAVToken(t, ts, alice, "", "")

	// Mutations through DAV: mkdir, two puts, a move, a delete.
	for _, step := range []struct{ method, path, body string }{
		{"MKCOL", "/dav/alice.poweur.net/private/docs", ""},
		{"PUT", "/dav/alice.poweur.net/private/docs/a.txt", "hello"},
		{"PUT", "/dav/alice.poweur.net/public/b.txt", "world"},
	} {
		var body []byte
		if step.body != "" {
			body = []byte(step.body)
		}
		resp := davReq(t, ts, step.method, step.path, token, body, nil)
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			t.Fatalf("%s %s: %d", step.method, step.path, resp.StatusCode)
		}
	}

	out := getChanges(t, ts, "alice.poweur.net", token, "")
	if len(out.Changes) != 3 || out.FullResync {
		t.Fatalf("want 3 changes, got %+v", out)
	}
	if out.Changes[0].Op != files.OpMkdir || out.Changes[1].Op != files.OpPut {
		t.Fatalf("unexpected ops: %+v", out.Changes)
	}
	if out.Next != out.Latest {
		t.Fatalf("drained feed must have next == latest: %+v", out)
	}

	// Incremental: a new PUT shows up after the cursor.
	resp := davReq(t, ts, "PUT", "/dav/alice.poweur.net/private/docs/c.txt", token, []byte("x"), nil)
	resp.Body.Close()
	inc := getChanges(t, ts, "alice.poweur.net", token, "?since="+out.Next)
	if len(inc.Changes) != 1 || inc.Changes[0].Path != "private/docs/c.txt" {
		t.Fatalf("incremental fetch: %+v", inc)
	}
	if inc.Changes[0].Actor != "alice.poweur.net" {
		t.Fatalf("journal must carry the actor: %+v", inc.Changes[0])
	}

	// paths= filter narrows the feed.
	filtered := getChanges(t, ts, "alice.poweur.net", token, "?paths=/public/")
	for _, rec := range filtered.Changes {
		if !strings.HasPrefix(rec.Path, "public") {
			t.Fatalf("paths filter leaked %q", rec.Path)
		}
	}

	// Invalid cursor is a 400.
	bad := davReq(t, ts, http.MethodGet, "/sync/alice.poweur.net/changes?since=nope", token, nil, nil)
	bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid cursor: %d", bad.StatusCode)
	}
}

func TestSyncChangesVisitorVisibility(t *testing.T) {
	server, ts := newDAVTestServer(t, 0, 0)
	alice := registerDAVIdentity(t, server, ts, "alice.poweur.net")
	bob := registerDAVIdentity(t, server, ts, "bob.poweur.net")
	aliceTok := mintDAVToken(t, ts, alice, "", "")

	for _, p := range []string{"/dav/alice.poweur.net/private/secret.txt", "/dav/alice.poweur.net/public/open.txt"} {
		resp := davReq(t, ts, "PUT", p, aliceTok, []byte("data"), nil)
		resp.Body.Close()
	}

	// Bob (visitor) sees only /public in alice's changes feed.
	bobTok := mintDAVToken(t, ts, bob, "alice.poweur.net", "dav:read")
	out := getChanges(t, ts, "alice.poweur.net", bobTok, "")
	if len(out.Changes) != 1 || out.Changes[0].Path != "public/open.txt" {
		t.Fatalf("visitor feed must only contain /public: %+v", out.Changes)
	}

	// Anonymous gets nothing beyond poweur-sys/public.
	anon := getChanges(t, ts, "alice.poweur.net", "", "")
	for _, rec := range anon.Changes {
		if !strings.HasPrefix(rec.Path, "poweur-sys/public") {
			t.Fatalf("anonymous feed leaked %q", rec.Path)
		}
	}

	// Scoped owner token only sees its slice.
	scoped := mintDAVToken(t, ts, alice, "", "dav:rw:/public/")
	scopedOut := getChanges(t, ts, "alice.poweur.net", scoped, "")
	for _, rec := range scopedOut.Changes {
		if !strings.HasPrefix(rec.Path, "public") {
			t.Fatalf("scoped feed leaked %q", rec.Path)
		}
	}
}

func TestSyncManifest(t *testing.T) {
	server, ts := newDAVTestServer(t, 0, 0)
	alice := registerDAVIdentity(t, server, ts, "alice.poweur.net")
	token := mintDAVToken(t, ts, alice, "", "")

	resp := davReq(t, ts, "PUT", "/dav/alice.poweur.net/private/a.txt", token, []byte("aaa"), nil)
	resp.Body.Close()
	resp = davReq(t, ts, "MKCOL", "/dav/alice.poweur.net/private/dir", token, nil, nil)
	resp.Body.Close()

	mresp := davReq(t, ts, http.MethodGet, "/sync/alice.poweur.net/manifest", token, nil, nil)
	defer mresp.Body.Close()
	if mresp.StatusCode != http.StatusOK {
		t.Fatalf("manifest: %d", mresp.StatusCode)
	}
	if ct := mresp.Header.Get("Content-Type"); ct != "application/x-ndjson" {
		t.Fatalf("manifest content type: %s", ct)
	}
	scanner := bufio.NewScanner(mresp.Body)
	var lines []map[string]any
	for scanner.Scan() {
		var m map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, m)
	}
	if len(lines) != 3 {
		t.Fatalf("want header + 2 entries, got %d: %v", len(lines), lines)
	}
	if lines[0]["manifest"] != float64(1) || lines[0]["cursor"] == "" {
		t.Fatalf("manifest header missing cursor: %v", lines[0])
	}
	if lines[1]["path"] != "private/a.txt" || lines[1]["etag"] == "" {
		t.Fatalf("manifest entry: %v", lines[1])
	}
	if lines[2]["path"] != "private/dir" || lines[2]["dir"] != true {
		t.Fatalf("manifest dir entry: %v", lines[2])
	}
}

func TestSyncChunkedUploadResume(t *testing.T) {
	server, ts := newDAVTestServer(t, 0, 0)
	alice := registerDAVIdentity(t, server, ts, "alice.poweur.net")
	token := mintDAVToken(t, ts, alice, "", "")

	body := bytes.Repeat([]byte("payload!"), 1024) // 8 KiB
	resp := davReq(t, ts, http.MethodPost, "/sync/alice.poweur.net/upload?path=/private/big.bin", token, nil,
		map[string]string{"Upload-Length": strconv.Itoa(len(body))})
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("upload create: %d %s", resp.StatusCode, raw)
	}
	var created uploadResponse
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	if loc == "" {
		t.Fatal("upload create must return Location")
	}

	// First chunk (60%), then "crash": re-probe with HEAD and resume.
	cut := len(body) * 6 / 10
	resp = davReq(t, ts, http.MethodPatch, loc, token, body[:cut],
		map[string]string{"Upload-Offset": "0"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("first chunk: %d", resp.StatusCode)
	}

	head := davReq(t, ts, http.MethodHead, loc, token, nil, nil)
	head.Body.Close()
	if head.StatusCode != http.StatusOK {
		t.Fatalf("HEAD: %d", head.StatusCode)
	}
	off, _ := strconv.Atoi(head.Header.Get("Upload-Offset"))
	if off != cut {
		t.Fatalf("resume offset = %d, want %d", off, cut)
	}

	// A stale retry of the first chunk must 409, not corrupt the spool.
	resp = davReq(t, ts, http.MethodPatch, loc, token, body[:cut],
		map[string]string{"Upload-Offset": "0"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("stale chunk: %d", resp.StatusCode)
	}

	// Final chunk completes and assembles.
	resp = davReq(t, ts, http.MethodPatch, loc, token, body[cut:],
		map[string]string{"Upload-Offset": strconv.Itoa(off)})
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("final chunk: %d %s", resp.StatusCode, raw)
	}
	var done map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&done)
	resp.Body.Close()
	if done["path"] != "private/big.bin" {
		t.Fatalf("assembly response: %v", done)
	}

	// The body must be fetchable via DAV and match.
	get := davReq(t, ts, http.MethodGet, "/dav/alice.poweur.net/private/big.bin", token, nil, nil)
	got, _ := io.ReadAll(get.Body)
	get.Body.Close()
	if !bytes.Equal(got, body) {
		t.Fatalf("assembled file mismatch: %d bytes", len(got))
	}

	// And the changes journal saw exactly one put for it.
	out := getChanges(t, ts, "alice.poweur.net", token, "")
	var puts int
	for _, rec := range out.Changes {
		if rec.Path == "private/big.bin" && rec.Op == files.OpPut {
			puts++
		}
	}
	if puts != 1 {
		t.Fatalf("upload must journal exactly one put, got %d (%+v)", puts, out.Changes)
	}
}

func TestSyncUploadQuotaAtStart(t *testing.T) {
	server, ts := newDAVTestServer(t, 1024, 0) // 1 KiB quota
	alice := registerDAVIdentity(t, server, ts, "alice.poweur.net")
	token := mintDAVToken(t, ts, alice, "", "")

	resp := davReq(t, ts, http.MethodPost, "/sync/alice.poweur.net/upload?path=/private/big.bin", token, nil,
		map[string]string{"Upload-Length": "10240"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusInsufficientStorage {
		t.Fatalf("quota must be enforced at upload start: %d", resp.StatusCode)
	}
}

func TestSyncUploadVisitorForbidden(t *testing.T) {
	server, ts := newDAVTestServer(t, 0, 0)
	registerDAVIdentity(t, server, ts, "alice.poweur.net")
	bob := registerDAVIdentity(t, server, ts, "bob.poweur.net")
	bobTok := mintDAVToken(t, ts, bob, "alice.poweur.net", "dav:read")

	resp := davReq(t, ts, http.MethodPost, "/sync/alice.poweur.net/upload?path=/private/x.bin", bobTok, nil,
		map[string]string{"Upload-Length": "10"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("visitor upload must be forbidden: %d", resp.StatusCode)
	}
}
