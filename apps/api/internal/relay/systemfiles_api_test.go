package relay

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	idpkg "github.com/poweur/identity"
)

func sysReq(t *testing.T, ts *httptest.Server, method string, as hostedID, owner, path string, body []byte, hdr map[string]string) *http.Response {
	t.Helper()
	headers := ownerAuth(t, ts, as)
	for k, v := range hdr {
		headers[k] = v
	}
	return httpReq(t, ts, method, "/identities/"+owner+"/system/"+path, "", body, headers)
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return string(raw)
}

// The owner writes and reads a known document; the relay enforces what was
// written on the next message, and a malformed document is refused whole.
func TestSystemFileOwnerWriteReadAndEnforce(t *testing.T) {
	server, ts := newTestRelay(t)
	alice := registerTestIdentity(t, server, ts, "sysalice.poweur.net")

	policy := `{"version":1,"mode":"contacts_only"}`
	resp := sysReq(t, ts, http.MethodPut, alice, alice.name, inboxPolicyPath, []byte(policy), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT policy: %d %s", resp.StatusCode, readAll(t, resp))
	}
	etag := resp.Header.Get("ETag")
	resp.Body.Close()

	resp = sysReq(t, ts, http.MethodGet, alice, alice.name, inboxPolicyPath, nil, nil)
	if got := readAll(t, resp); resp.StatusCode != http.StatusOK || got != policy || resp.Header.Get("ETag") != etag {
		t.Fatalf("GET policy: %d %q etag %q", resp.StatusCode, got, resp.Header.Get("ETag"))
	}
	if p, _ := server.recipientPolicy(t.Context(), alice.name); p.Mode != idpkg.InboxContactsOnly {
		t.Fatalf("relay does not enforce the written policy: %+v", p)
	}

	resp = sysReq(t, ts, http.MethodPut, alice, alice.name, inboxPolicyPath, []byte(`{"version":1,"mode":"bogus"}`), nil)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("malformed policy: %d want 422", resp.StatusCode)
	}
	resp.Body.Close()
	if p, _ := server.recipientPolicy(t.Context(), alice.name); p.Mode != idpkg.InboxContactsOnly {
		t.Fatal("a refused write changed the stored policy")
	}
}

// Nobody but the owner reads or writes, and the relay's own zone and the
// identity document are not the owner's to write.
func TestSystemFileAccessRules(t *testing.T) {
	server, ts := newTestRelay(t)
	alice := registerTestIdentity(t, server, ts, "sysowner.poweur.net")
	bob := registerTestIdentity(t, server, ts, "syssnoop.poweur.net")
	setSysFile(t, server, alice.name, contactsPath, `{"version":1,"contacts":[]}`)

	resp := httpReq(t, ts, http.MethodGet, "/identities/"+alice.name+"/system/"+contactsPath, "", nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous read: %d want 401", resp.StatusCode)
	}
	resp = sysReq(t, ts, http.MethodGet, bob, alice.name, contactsPath, nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("other identity read: %d want 401", resp.StatusCode)
	}
	for _, path := range []string{devicesDocPath, sysPublicDir + "/id.json"} {
		resp = sysReq(t, ts, http.MethodPut, alice, alice.name, path, []byte(`{}`), nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("PUT %s: %d want 403", path, resp.StatusCode)
		}
	}
	for _, path := range []string{"poweur-sys/relay/contacts.json", ".poweur/private/x.json", ".poweur/relay/a/b.json", ".poweur/relay/.hidden", ".poweur/relay/UP.json"} {
		resp = sysReq(t, ts, http.MethodGet, alice, alice.name, path, nil, nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("GET %s: %d want 400", path, resp.StatusCode)
		}
	}
}

// If-Match turns a lost update into a visible conflict.
func TestSystemFileIfMatch(t *testing.T) {
	server, ts := newTestRelay(t)
	alice := registerTestIdentity(t, server, ts, "sysmatch.poweur.net")

	resp := sysReq(t, ts, http.MethodPut, alice, alice.name, contactsPath, []byte(`{"version":1,"contacts":[]}`),
		map[string]string{"If-Match": "*"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("If-Match * on a missing file: %d want 412", resp.StatusCode)
	}
	resp = sysReq(t, ts, http.MethodPut, alice, alice.name, contactsPath, []byte(`{"version":1,"contacts":[]}`), nil)
	first := resp.Header.Get("ETag")
	resp.Body.Close()
	resp = sysReq(t, ts, http.MethodPut, alice, alice.name, contactsPath,
		[]byte(`{"version":1,"contacts":[{"identity":"x.poweur.net","state":"blocked"}]}`), map[string]string{"If-Match": first})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("matching If-Match: %d want 200", resp.StatusCode)
	}
	resp = sysReq(t, ts, http.MethodPut, alice, alice.name, contactsPath, []byte(`{"version":1,"contacts":[]}`),
		map[string]string{"If-Match": first})
	resp.Body.Close()
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("stale If-Match: %d want 412", resp.StatusCode)
	}
}

func pngBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// An avatar is a real image, served publicly; HTML named .png is refused.
func TestSystemFileAvatar(t *testing.T) {
	server, ts := newTestRelay(t)
	alice := registerTestIdentity(t, server, ts, "sysavatar.poweur.net")
	path := sysPublicDir + "/avatar.png"

	resp := sysReq(t, ts, http.MethodPut, alice, alice.name, path, []byte("<html><script>x</script></html>"), nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("HTML as avatar: %d want 422", resp.StatusCode)
	}
	img := pngBytes(t)
	resp = sysReq(t, ts, http.MethodPut, alice, alice.name, path, img, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT avatar: %d", resp.StatusCode)
	}
	resp, body := getWellKnownRaw(t, ts.URL+"/.well-known/poweur/avatar.png", alice.name)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "image/png" || !bytes.Equal(body, img) {
		t.Fatalf("public avatar: %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	resp = sysReq(t, ts, http.MethodDelete, alice, alice.name, path, nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE avatar: %d want 204", resp.StatusCode)
	}
	if resp, _ := getWellKnownRaw(t, ts.URL+"/.well-known/poweur/avatar.png", alice.name); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("deleted avatar still served: %d", resp.StatusCode)
	}
}

// A group roster is accepted only when the group signs itself.
func TestSystemFileGroupRosterMustBeSelfSigned(t *testing.T) {
	server, ts := newTestRelay(t)
	group := registerTestIdentity(t, server, ts, "sysgroup.poweur.net")
	stranger := registerTestIdentity(t, server, ts, "sysstranger.poweur.net")
	roster := func(signer hostedID) []byte {
		gr := idpkg.ShareGroup{Group: group.name, Owner: group.name, Members: []string{stranger.name},
			Admins: []string{stranger.name}, Epoch: 1, UpdatedAt: time.Now().UTC().Format(time.RFC3339)}
		if err := gr.Sign(signer.priv); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(gr)
		return raw
	}
	resp := sysReq(t, ts, http.MethodPut, group, group.name, groupRosterPath, roster(stranger), nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("roster signed by someone else: %d want 422", resp.StatusCode)
	}
	resp = sysReq(t, ts, http.MethodPut, group, group.name, groupRosterPath, roster(group), nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("self-signed roster: %d", resp.StatusCode)
	}
	if _, err := server.groupIdentity(t.Context(), group.name); err != nil {
		t.Fatalf("stored roster does not resolve: %v", err)
	}
}

// System files survive a relay restart: they are files, not memory.
func TestSystemFilesPersistAcrossRestart(t *testing.T) {
	server, ts := newTestRelay(t)
	alice := registerTestIdentity(t, server, ts, "syspersist.poweur.net")
	resp := sysReq(t, ts, http.MethodPut, alice, alice.name, inboxPolicyPath, []byte(`{"version":1,"mode":"contacts_only"}`), nil)
	resp.Body.Close()

	restarted := NewServer(server.cfg, server.resolver, nil)
	if p, _ := restarted.recipientPolicy(t.Context(), alice.name); p.Mode != idpkg.InboxContactsOnly {
		t.Fatalf("policy lost across restart: %+v", p)
	}
}

func getWellKnownRaw(t *testing.T, url, host string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Host = host
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, raw
}
