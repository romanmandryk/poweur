package relay

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/poweur/api/internal/crypto"
	"github.com/poweur/api/internal/storage"
	idpkg "github.com/poweur/identity"
)

// Device registry (EPIC-004 E04-T6). What matters here is not that a row
// appears — it is that revoking a row actually stops the credentials it
// named, and that nobody but the owner can read or revoke.

const testFingerprint = "test-laptop-fingerprint"

// mintDeviceDAVToken is mintDAVToken with device headers attached, so the
// resulting token is bound to a registry row.
func mintDeviceDAVToken(t *testing.T, ts *httptest.Server, id davTestIdentity, fingerprint, name, kind string) (string, int) {
	t.Helper()
	issued := time.Now().UTC().Format(time.RFC3339)
	nonce := "tok-" + time.Now().Format("150405.000000000")
	canon := crypto.CanonicalDAVToken(id.name, id.name, "dav:full", issued, nonce)
	sig := base64.RawURLEncoding.EncodeToString(ed25519.Sign(id.priv, []byte(canon)))
	body, _ := json.Marshal(DAVTokenRequest{
		Identity: id.name, Audience: id.name, Scope: "dav:full",
		IssuedAt: issued, Nonce: nonce, Signature: sig,
	})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/auth/dav-token", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if fingerprint != "" {
		req.Header.Set("X-Poweur-Device", fingerprint)
	}
	if name != "" {
		req.Header.Set("X-Poweur-Device-Name", name)
	}
	if kind != "" {
		req.Header.Set("X-Poweur-Device-Kind", kind)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return "", resp.StatusCode
	}
	var out DAVTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.Token, resp.StatusCode
}

func listDevices(t *testing.T, ts *httptest.Server, identity, token string) []idpkg.Device {
	t.Helper()
	resp := davReq(t, ts, http.MethodGet, "/devices/"+identity, token, nil, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET /devices/%s: %d %s", identity, resp.StatusCode, raw)
	}
	var out struct {
		Identity string         `json:"identity"`
		Devices  []idpkg.Device `json:"devices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.Devices
}

func revokeDeviceReq(t *testing.T, ts *httptest.Server, identity, token, deviceID string) (deviceRevocation, int) {
	t.Helper()
	body, _ := json.Marshal(DeviceRevokeRequest{DeviceID: deviceID})
	resp := davReq(t, ts, http.MethodPost, "/devices/"+identity+"/revoke", token, body,
		map[string]string{"Content-Type": "application/json"})
	defer resp.Body.Close()
	var out deviceRevocation
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
	}
	return out, resp.StatusCode
}

// A device that names itself gets a row; the row carries the label and kind
// it sent, and the id is derived so a reconnect lands on the same row.
func TestDeviceRegistryTouch(t *testing.T) {
	server, ts := newDAVTestServer(t, 0, 0)
	alice := registerDAVIdentity(t, server, ts, "devtouch.poweur.net")

	tok, code := mintDeviceDAVToken(t, ts, alice, testFingerprint, "Roman's laptop", "laptop")
	if code != http.StatusCreated {
		t.Fatalf("mint: %d", code)
	}
	devices := listDevices(t, ts, alice.name, tok)
	if len(devices) != 1 {
		t.Fatalf("want one device, got %d: %+v", len(devices), devices)
	}
	want := idpkg.DeviceIDFromFingerprint(testFingerprint)
	d := devices[0]
	if d.ID != want {
		t.Fatalf("device id %q, want %q", d.ID, want)
	}
	if d.Name != "Roman's laptop" || d.Kind != idpkg.DeviceKindLaptop {
		t.Fatalf("label/kind not recorded: %+v", d)
	}
	if d.AddedAt == "" || d.LastSeen == "" {
		t.Fatalf("timestamps missing: %+v", d)
	}

	// A second connection from the same device does not create a second row.
	tok2, _ := mintDeviceDAVToken(t, ts, alice, testFingerprint, "Roman's laptop", "laptop")
	if got := listDevices(t, ts, alice.name, tok2); len(got) != 1 {
		t.Fatalf("reconnect forked the registry: %+v", got)
	}
	// A different device does.
	tok3, _ := mintDeviceDAVToken(t, ts, alice, "phone-fingerprint", "phone", "phone")
	if got := listDevices(t, ts, alice.name, tok3); len(got) != 2 {
		t.Fatalf("second device not recorded: %+v", got)
	}
}

// A client that says nothing stays out of the registry entirely — the
// pre-E04-T6 behaviour, and the one third-party DAV clients get.
func TestDeviceRegistryAnonymousClient(t *testing.T) {
	server, ts := newDAVTestServer(t, 0, 0)
	alice := registerDAVIdentity(t, server, ts, "devanon.poweur.net")
	tok := mintDAVToken(t, ts, alice, "", "")
	if got := listDevices(t, ts, alice.name, tok); len(got) != 0 {
		t.Fatalf("anonymous client was recorded: %+v", got)
	}
}

// A client-supplied name is sanitized before it lands, so a header cannot
// inject control characters into whatever renders the list.
//
// The hostile header goes in through deviceFromRequest directly: Go's own
// http.Client refuses to *send* a header containing NUL, and the attacker
// this guards against is not using Go's client.
func TestDeviceRegistrySanitizesClientLabels(t *testing.T) {
	server, ts := newDAVTestServer(t, 0, 0)
	alice := registerDAVIdentity(t, server, ts, "devsanitize.poweur.net")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Poweur-Device", testFingerprint)
	req.Header["X-Poweur-Device-Name"] = []string{"lap\x00top\x1b[31m\nX-Injected: yes"}
	req.Header.Set("X-Poweur-Device-Kind", "toaster")
	obs := deviceFromRequest(req)
	if obs.Fingerprint != testFingerprint {
		t.Fatalf("fingerprint not read: %q", obs.Fingerprint)
	}
	server.touchDevice(context.Background(), alice.name, obs)

	tok := mintDAVToken(t, ts, alice, "", "")
	devices := listDevices(t, ts, alice.name, tok)
	if len(devices) != 1 {
		t.Fatalf("want one device: %+v", devices)
	}
	if strings.ContainsAny(devices[0].Name, "\x00\x1b\n") {
		t.Fatalf("control characters survived: %q", devices[0].Name)
	}
	// An unrecognized kind degrades to unknown rather than costing the row.
	if devices[0].Kind != idpkg.DeviceKindUnknown {
		t.Fatalf("kind %q, want unknown", devices[0].Kind)
	}
	// The stored document must still validate — sanitizing on the way in is
	// what keeps the relay's own writes legal.
	if err := server.readDevices(context.Background(), alice.name).Validate(); err != nil {
		t.Fatalf("registry no longer validates: %v", err)
	}
}

// The registry is the owner's alone: no visitor may read it, and an
// unauthenticated caller gets 401.
func TestDeviceRegistryOwnerOnly(t *testing.T) {
	server, ts := newDAVTestServer(t, 0, 0)
	alice := registerDAVIdentity(t, server, ts, "devowner.poweur.net")
	bob := registerDAVIdentity(t, server, ts, "devsnoop.poweur.net")

	resp := davReq(t, ts, http.MethodGet, "/devices/"+alice.name, "", nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous read: %d want 401", resp.StatusCode)
	}

	// Bob's own token names Bob as the subject, so it is not an owner
	// credential for Alice's tree.
	bobTok := mintDAVToken(t, ts, bob, "", "")
	resp = davReq(t, ts, http.MethodGet, "/devices/"+alice.name, bobTok, nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("visitor read: %d want 401", resp.StatusCode)
	}
	if _, code := revokeDeviceReq(t, ts, alice.name, bobTok, idpkg.DeviceIDFromFingerprint("x")); code != http.StatusUnauthorized {
		t.Fatalf("visitor revoke: %d want 401", code)
	}

	// An unknown identity is a 404, not a registry.
	aliceTok := mintDAVToken(t, ts, alice, "", "")
	resp = davReq(t, ts, http.MethodGet, "/devices/nobody.poweur.net", aliceTok, nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown identity: %d want 404", resp.StatusCode)
	}
}

// A read-only credential may list devices but not revoke one: revocation is
// a write to the registry, and scope has to mean something here too.
func TestDeviceRegistryScopeEnforced(t *testing.T) {
	server, ts := newDAVTestServer(t, 0, 0)
	alice := registerDAVIdentity(t, server, ts, "devscope.poweur.net")

	readOnly := mintDAVToken(t, ts, alice, alice.name, "dav:read")
	resp := davReq(t, ts, http.MethodGet, "/devices/"+alice.name, readOnly, nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("read-only list: %d want 200", resp.StatusCode)
	}
	if _, code := revokeDeviceReq(t, ts, alice.name, readOnly,
		idpkg.DeviceIDFromFingerprint(testFingerprint)); code != http.StatusForbidden {
		t.Fatalf("read-only revoke: %d want 403", code)
	}
}

// The revoke endpoint refuses anything that is not a device id, rather than
// letting a path-shaped string reach the registry.
func TestDeviceRevokeRejectsBadIDs(t *testing.T) {
	server, ts := newDAVTestServer(t, 0, 0)
	alice := registerDAVIdentity(t, server, ts, "devbadid.poweur.net")
	tok := mintDAVToken(t, ts, alice, "", "")

	for _, bad := range []string{"", "nope", "dev_../../etc/passwd", "dev_TOOSHORT", "sess_abcdefghijklmnop"} {
		if _, code := revokeDeviceReq(t, ts, alice.name, tok, bad); code != http.StatusBadRequest {
			t.Fatalf("revoke %q: %d want 400", bad, code)
		}
	}
	// Well-formed but absent is a 404.
	if _, code := revokeDeviceReq(t, ts, alice.name, tok,
		idpkg.DeviceIDFromFingerprint("never-seen")); code != http.StatusNotFound {
		t.Fatalf("absent device: %d want 404", code)
	}
}

// The acceptance criterion: revoking a device kills its DAV tokens, its
// sessions and its app passwords, and it cannot re-arm itself afterwards.
func TestDeviceRevokeKillsCredentials(t *testing.T) {
	server, ts := newDAVTestServer(t, 0, 0)
	alice := registerDAVIdentity(t, server, ts, "devrevoke.poweur.net")
	deviceID := idpkg.DeviceIDFromFingerprint(testFingerprint)

	// A token bound to the device, and one that is not.
	deviceTok, _ := mintDeviceDAVToken(t, ts, alice, testFingerprint, "laptop", "laptop")
	otherTok, _ := mintDeviceDAVToken(t, ts, alice, "other-device", "desktop", "desktop")

	// An app password linked to the device, and one that is not.
	pw, err := idpkg.GenerateAppPassword()
	if err != nil {
		t.Fatal(err)
	}
	hash, err := idpkg.HashAppPassword(pw)
	if err != nil {
		t.Fatal(err)
	}
	fileRaw, _ := json.Marshal(idpkg.AppPasswordsFile{Passwords: []idpkg.AppPassword{
		{Name: "laptop-dav", Hash: hash, Scope: "dav:full", DeviceID: deviceID},
		{Name: "server-dav", Hash: hash, Scope: "dav:full"},
	}})
	resp := davReq(t, ts, http.MethodPut, "/dav/"+alice.name+"/poweur-sys/relay/app-passwords.json",
		deviceTok, fileRaw, nil)
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("write app-passwords.json: %d", resp.StatusCode)
	}

	// A session belonging to the device.
	server.sessions.Put(storage.Session{
		ID: "sess_device", Identity: alice.name, DeviceFingerprint: testFingerprint,
		IssuedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour),
	})
	server.sessions.Put(storage.Session{
		ID: "sess_other", Identity: alice.name, DeviceFingerprint: "other-device",
		IssuedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour),
	})

	// The listing shows the credential link before revocation.
	before := listDevices(t, ts, alice.name, deviceTok)
	var found bool
	for _, d := range before {
		if d.ID == deviceID {
			found = true
			if len(d.AppPasswords) != 1 || d.AppPasswords[0] != "laptop-dav" {
				t.Fatalf("app password link not reported: %+v", d.AppPasswords)
			}
		}
	}
	if !found {
		t.Fatalf("device missing from listing: %+v", before)
	}

	result, code := revokeDeviceReq(t, ts, alice.name, otherTok, deviceID)
	if code != http.StatusOK {
		t.Fatalf("revoke: %d want 200", code)
	}
	if result.Sessions != 1 || result.DAVTokens != 1 || result.AppPasswords != 1 {
		t.Fatalf("revocation reported %+v, want one of each", result)
	}

	// The device's DAV token is dead.
	resp = davReq(t, ts, "PROPFIND", "/dav/"+alice.name+"/private/", deviceTok, nil,
		map[string]string{"Depth": "1"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked token still works: %d want 401", resp.StatusCode)
	}
	// The device's session is gone; the other identity's session is not.
	if _, ok := server.sessions.Get("sess_device"); ok {
		t.Fatal("revoked device's session survived")
	}
	if _, ok := server.sessions.Get("sess_other"); !ok {
		t.Fatal("revocation took an unrelated session")
	}
	// The device's app password is dead; the unlinked one still works.
	basic := base64.StdEncoding.EncodeToString([]byte(alice.name + ":" + pw))
	req, _ := http.NewRequest("PROPFIND", ts.URL+"/dav/"+alice.name+"/private/", nil)
	req.Header.Set("Authorization", "Basic "+basic)
	req.Header.Set("Depth", "1")
	bresp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	bresp.Body.Close()
	// Both entries share a hash in this fixture, so the surviving
	// "server-dav" entry is what keeps this authenticating — the point being
	// that revocation removed exactly one entry, not the file.
	if bresp.StatusCode != 207 {
		t.Fatalf("unlinked app password died with the device: %d", bresp.StatusCode)
	}
	remaining := server.readAppPasswords(context.Background(), alice.name)
	if len(remaining.Passwords) != 1 || remaining.Passwords[0].Name != "server-dav" {
		t.Fatalf("app-passwords.json after revoke: %+v", remaining.Passwords)
	}

	// The row survives as an audit trail, marked revoked and naming no
	// credentials.
	after := listDevices(t, ts, alice.name, otherTok)
	for _, d := range after {
		if d.ID != deviceID {
			continue
		}
		if !d.Revoked || d.RevokedAt == "" {
			t.Fatalf("row not marked revoked: %+v", d)
		}
		if len(d.AppPasswords) != 0 {
			t.Fatalf("revoked row still names credentials: %+v", d.AppPasswords)
		}
	}

	// And the device cannot quietly re-arm itself with the same fingerprint.
	if _, code := mintDeviceDAVToken(t, ts, alice, testFingerprint, "laptop", "laptop"); code != http.StatusForbidden {
		t.Fatalf("revoked device minted a fresh token: %d want 403", code)
	}
	// Revoking twice is still a 200 — the post-condition is what matters.
	if _, code := revokeDeviceReq(t, ts, alice.name, otherTok, deviceID); code != http.StatusOK {
		t.Fatalf("second revoke: %d want 200", code)
	}
}

// Per-device sync cursors: reading the changes feed records where the device
// got to, which is what makes "phone last synced 3 days ago" answerable.
func TestDeviceSyncCursorRecorded(t *testing.T) {
	server, ts := newDAVTestServer(t, 0, 0)
	alice := registerDAVIdentity(t, server, ts, "devcursor.poweur.net")
	tok, _ := mintDeviceDAVToken(t, ts, alice, testFingerprint, "laptop", "laptop")

	resp := davReq(t, ts, http.MethodPut, "/dav/"+alice.name+"/private/note.txt", tok, []byte("hi"), nil)
	resp.Body.Close()

	resp = davReq(t, ts, http.MethodGet, "/sync/"+alice.name+"/changes?paths=private", tok, nil,
		map[string]string{"X-Poweur-Device": testFingerprint})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("changes: %d", resp.StatusCode)
	}

	devices := listDevices(t, ts, alice.name, tok)
	if len(devices) != 1 {
		t.Fatalf("want one device: %+v", devices)
	}
	d := devices[0]
	if d.SyncCursor == "" || d.SyncedAt == "" {
		t.Fatalf("cursor not recorded: %+v", d)
	}
	if len(d.SyncScopes) != 1 || d.SyncScopes[0] != "private" {
		t.Fatalf("sync scopes not recorded: %+v", d.SyncScopes)
	}
	age, ok := d.Stale(time.Now().UTC())
	if !ok || age > time.Minute {
		t.Fatalf("staleness unreadable: age=%v ok=%v", age, ok)
	}
}

// devices.json is the relay's, not the owner's: an owner DAV write is
// refused outright, which is what makes "pull-only for sync clients"
// enforcement rather than etiquette.
func TestDevicesDocIsRelayManaged(t *testing.T) {
	server, ts := newDAVTestServer(t, 0, 0)
	alice := registerDAVIdentity(t, server, ts, "devmanaged.poweur.net")
	tok, _ := mintDeviceDAVToken(t, ts, alice, testFingerprint, "laptop", "laptop")

	valid, _ := json.Marshal(idpkg.DevicesFile{Version: 1})
	resp := davReq(t, ts, http.MethodPut, "/dav/"+alice.name+"/poweur-sys/relay/devices.json", tok, valid, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("owner PUT of devices.json: %d want 403", resp.StatusCode)
	}
	// Reading it is normal.
	resp = davReq(t, ts, http.MethodGet, "/dav/"+alice.name+"/poweur-sys/relay/devices.json", tok, nil, nil)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("owner GET of devices.json: %d want 200", resp.StatusCode)
	}
	if _, err := idpkg.ParseDevicesFile(body); err != nil {
		t.Fatalf("relay wrote a document its own validator rejects: %v", err)
	}
	// DELETE is a write too.
	resp = davReq(t, ts, http.MethodDelete, "/dav/"+alice.name+"/poweur-sys/relay/devices.json", tok, nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("owner DELETE of devices.json: %d want 403", resp.StatusCode)
	}
}

// A registry too damaged for the relay to read must not freeze revocation:
// the reader starts over rather than propagating the error.
func TestDeviceRegistryRecoversFromCorruption(t *testing.T) {
	server, ts := newDAVTestServer(t, 0, 0)
	alice := registerDAVIdentity(t, server, ts, "devcorrupt.poweur.net")
	ctx := context.Background()

	if err := server.writeDevices(ctx, alice.name, idpkg.DevicesFile{Version: 1}); err == nil {
		// Overwrite it with junk behind the DAV layer's back.
		if err := writeRawSysFile(t, server, alice.name, "not json at all"); err != nil {
			t.Fatal(err)
		}
	}
	doc := server.readDevices(ctx, alice.name)
	if doc.Version != 1 || len(doc.Devices) != 0 {
		t.Fatalf("corrupt registry did not reset: %+v", doc)
	}
	// And a fresh observation still lands.
	if id := server.touchDevice(ctx, alice.name, deviceObservation{
		Fingerprint: testFingerprint, Name: "laptop", Kind: "laptop",
	}); id != idpkg.DeviceIDFromFingerprint(testFingerprint) {
		t.Fatalf("touch after corruption returned %q", id)
	}
	tok, _ := mintDeviceDAVToken(t, ts, alice, testFingerprint, "laptop", "laptop")
	if got := listDevices(t, ts, alice.name, tok); len(got) != 1 {
		t.Fatalf("registry did not recover: %+v", got)
	}
}

func writeRawSysFile(t *testing.T, server *Server, owner, body string) error {
	t.Helper()
	f, err := server.filesProvider.OpenFile(context.Background(), owner, devicesDocPath,
		os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write([]byte(body))
	return err
}

func TestDeviceLocksArePerIdentity(t *testing.T) {
	locks := newDeviceLocks()
	a := locks.get("alice.poweur.net")
	// Case-insensitive: the same identity spelled differently must not get a
	// second lock, or two requests would race on one document.
	if b := locks.get("ALICE.poweur.net"); a != b {
		t.Fatal("identity casing forked the lock")
	}
	if c := locks.get("bob.poweur.net"); a == c {
		t.Fatal("distinct identities share a lock")
	}
}

func TestSameStringsAndContainsFold(t *testing.T) {
	cases := []struct {
		name string
		a, b []string
		want bool
	}{
		{"equal", []string{"a", "b"}, []string{"a", "b"}, true},
		{"empty", nil, nil, true},
		{"different length", []string{"a"}, []string{"a", "b"}, false},
		{"different order", []string{"a", "b"}, []string{"b", "a"}, false},
		{"different content", []string{"a"}, []string{"b"}, false},
	}
	for _, tc := range cases {
		if got := sameStrings(tc.a, tc.b); got != tc.want {
			t.Fatalf("%s: sameStrings = %v, want %v", tc.name, got, tc.want)
		}
	}
	if !containsFold([]string{"Finder", "phone"}, "finder") {
		t.Fatal("containsFold missed a case-insensitive match")
	}
	if containsFold([]string{"finder"}, "phone") {
		t.Fatal("containsFold matched the wrong entry")
	}
	if containsFold(nil, "finder") {
		t.Fatal("containsFold matched in an empty list")
	}
}

func TestAppPasswordsForDevice(t *testing.T) {
	deviceID := idpkg.DeviceIDFromFingerprint("laptop")
	file := idpkg.AppPasswordsFile{Passwords: []idpkg.AppPassword{
		{Name: "linked", DeviceID: deviceID},
		{Name: "linked-upper", DeviceID: strings.ToUpper(deviceID)},
		{Name: "unlinked"},
		{Name: "other", DeviceID: idpkg.DeviceIDFromFingerprint("phone")},
	}}
	got := appPasswordsForDevice(file, deviceID)
	if len(got) != 2 || got[0] != "linked" || got[1] != "linked-upper" {
		t.Fatalf("appPasswordsForDevice = %+v", got)
	}
	// An empty device id must not sweep up every unlinked password.
	if got := appPasswordsForDevice(file, ""); len(got) != 0 {
		t.Fatalf("empty device id matched: %+v", got)
	}
}
