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
	"strings"
	"testing"
	"time"

	"github.com/poweur/api/internal/crypto"
	"github.com/poweur/api/internal/storage"
	idpkg "github.com/poweur/identity"
)

// Device registry (EPIC-004 E04-T6, now in .poweur/state/devices.json). What
// matters here is not that a row appears — it is that revoking a row actually
// stops the sessions it named, and that nobody but the owner can read or
// revoke.

const testFingerprint = "test-laptop-fingerprint"

// ownerAuth returns the challenge-signed headers an owner endpoint needs.
func ownerAuth(t *testing.T, ts *httptest.Server, id hostedID) map[string]string {
	t.Helper()
	challenge, sig := challengeFor(t, ts, id)
	return map[string]string{
		"X-Poweur-Identity":  id.name,
		"X-Poweur-Challenge": challenge,
		"X-Poweur-Signature": sig,
	}
}

// openSession registers a session for id from the given device, returning
// the HTTP status.
func openSession(t *testing.T, ts *httptest.Server, id hostedID, fingerprint, name, kind string) int {
	t.Helper()
	sessionPub, _, _ := ed25519.GenerateKey(nil)
	sessionPubB64 := base64.RawURLEncoding.EncodeToString(sessionPub)
	issued := time.Now().UTC().Format(time.RFC3339)
	expires := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	nonce := "sess-" + time.Now().Format("150405.000000000")
	canon := crypto.CanonicalSessionRegistration(id.name, sessionPubB64, issued, expires, nonce)
	body, _ := json.Marshal(SessionCreateRequest{
		Identity: id.name, SessionPublicKey: sessionPubB64, IssuedAt: issued, ExpiresAt: expires,
		Nonce: nonce, IdentitySignature: base64.StdEncoding.EncodeToString(ed25519.Sign(id.priv, []byte(canon))),
	})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/sessions", bytes.NewReader(body))
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
	resp.Body.Close()
	return resp.StatusCode
}

func listDevices(t *testing.T, ts *httptest.Server, id hostedID) []idpkg.Device {
	t.Helper()
	resp := httpReq(t, ts, http.MethodGet, "/devices/"+id.name, "", nil, ownerAuth(t, ts, id))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET /devices/%s: %d %s", id.name, resp.StatusCode, raw)
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

func revokeDeviceReq(t *testing.T, ts *httptest.Server, owner string, headers map[string]string, deviceID string) (deviceRevocation, int) {
	t.Helper()
	body, _ := json.Marshal(DeviceRevokeRequest{DeviceID: deviceID})
	hdr := map[string]string{"Content-Type": "application/json"}
	for k, v := range headers {
		hdr[k] = v
	}
	resp := httpReq(t, ts, http.MethodPost, "/devices/"+owner+"/revoke", "", body, hdr)
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
	server, ts := newTestRelay(t)
	alice := registerTestIdentity(t, server, ts, "devtouch.poweur.net")

	if code := openSession(t, ts, alice, testFingerprint, "Roman's laptop", "laptop"); code != http.StatusCreated {
		t.Fatalf("session: %d", code)
	}
	devices := listDevices(t, ts, alice)
	if len(devices) != 1 {
		t.Fatalf("want one device, got %d: %+v", len(devices), devices)
	}
	d := devices[0]
	if d.ID != idpkg.DeviceIDFromFingerprint(testFingerprint) {
		t.Fatalf("device id %q", d.ID)
	}
	if d.Name != "Roman's laptop" || d.Kind != idpkg.DeviceKindLaptop {
		t.Fatalf("label/kind not recorded: %+v", d)
	}
	if d.AddedAt == "" || d.LastSeen == "" {
		t.Fatalf("timestamps missing: %+v", d)
	}
	// A second connection from the same device does not create a second row.
	openSession(t, ts, alice, testFingerprint, "Roman's laptop", "laptop")
	if got := listDevices(t, ts, alice); len(got) != 1 {
		t.Fatalf("reconnect forked the registry: %+v", got)
	}
	// A different device does.
	openSession(t, ts, alice, "phone-fingerprint", "phone", "phone")
	if got := listDevices(t, ts, alice); len(got) != 2 {
		t.Fatalf("second device not recorded: %+v", got)
	}
}

// A client that says nothing stays out of the registry entirely.
func TestDeviceRegistryAnonymousClient(t *testing.T) {
	server, ts := newTestRelay(t)
	alice := registerTestIdentity(t, server, ts, "devanon.poweur.net")
	openSession(t, ts, alice, "", "", "")
	if got := listDevices(t, ts, alice); len(got) != 0 {
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
	server, ts := newTestRelay(t)
	alice := registerTestIdentity(t, server, ts, "devsanitize.poweur.net")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Poweur-Device", testFingerprint)
	req.Header["X-Poweur-Device-Name"] = []string{"lap\x00top\x1b[31m\nX-Injected: yes"}
	req.Header.Set("X-Poweur-Device-Kind", "toaster")
	obs := deviceFromRequest(req)
	if obs.Fingerprint != testFingerprint {
		t.Fatalf("fingerprint not read: %q", obs.Fingerprint)
	}
	server.touchDevice(context.Background(), alice.name, obs)

	devices := listDevices(t, ts, alice)
	if len(devices) != 1 {
		t.Fatalf("want one device: %+v", devices)
	}
	if strings.ContainsAny(devices[0].Name, "\x00\x1b\n") {
		t.Fatalf("control characters survived: %q", devices[0].Name)
	}
	if devices[0].Kind != idpkg.DeviceKindUnknown {
		t.Fatalf("kind %q, want unknown", devices[0].Kind)
	}
	if err := server.readDevices(context.Background(), alice.name).Validate(); err != nil {
		t.Fatalf("registry no longer validates: %v", err)
	}
}

// The registry is the owner's alone: an unauthenticated caller and another
// identity both get 401, and an unknown identity is a 404.
func TestDeviceRegistryOwnerOnly(t *testing.T) {
	server, ts := newTestRelay(t)
	alice := registerTestIdentity(t, server, ts, "devowner.poweur.net")
	bob := registerTestIdentity(t, server, ts, "devsnoop.poweur.net")

	resp := httpReq(t, ts, http.MethodGet, "/devices/"+alice.name, "", nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous read: %d want 401", resp.StatusCode)
	}
	resp = httpReq(t, ts, http.MethodGet, "/devices/"+alice.name, "", nil, ownerAuth(t, ts, bob))
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("visitor read: %d want 401", resp.StatusCode)
	}
	if _, code := revokeDeviceReq(t, ts, alice.name, ownerAuth(t, ts, bob), idpkg.DeviceIDFromFingerprint("x")); code != http.StatusUnauthorized {
		t.Fatalf("visitor revoke: %d want 401", code)
	}
	resp = httpReq(t, ts, http.MethodGet, "/devices/nobody.poweur.net", "", nil, ownerAuth(t, ts, alice))
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown identity: %d want 404", resp.StatusCode)
	}
}

// The revoke endpoint refuses anything that is not a device id, rather than
// letting a path-shaped string reach the registry.
func TestDeviceRevokeRejectsBadIDs(t *testing.T) {
	server, ts := newTestRelay(t)
	alice := registerTestIdentity(t, server, ts, "devbadid.poweur.net")
	for _, bad := range []string{"", "nope", "dev_../../etc/passwd", "dev_TOOSHORT", "sess_abcdefghijklmnop"} {
		if _, code := revokeDeviceReq(t, ts, alice.name, ownerAuth(t, ts, alice), bad); code != http.StatusBadRequest {
			t.Fatalf("revoke %q: %d want 400", bad, code)
		}
	}
	if _, code := revokeDeviceReq(t, ts, alice.name, ownerAuth(t, ts, alice),
		idpkg.DeviceIDFromFingerprint("never-seen")); code != http.StatusNotFound {
		t.Fatalf("absent device: %d want 404", code)
	}
}

// Revoking a device kills its sessions, keeps the row as an audit trail, and
// stops the device from re-arming itself with the same fingerprint.
func TestDeviceRevokeKillsSessions(t *testing.T) {
	server, ts := newTestRelay(t)
	alice := registerTestIdentity(t, server, ts, "devrevoke.poweur.net")
	deviceID := idpkg.DeviceIDFromFingerprint(testFingerprint)

	openSession(t, ts, alice, testFingerprint, "laptop", "laptop")
	server.sessions.Put(storage.Session{
		ID: "sess_device", Identity: alice.name, DeviceFingerprint: testFingerprint,
		IssuedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour),
	})
	server.sessions.Put(storage.Session{
		ID: "sess_other", Identity: alice.name, DeviceFingerprint: "other-device",
		IssuedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour),
	})

	result, code := revokeDeviceReq(t, ts, alice.name, ownerAuth(t, ts, alice), deviceID)
	if code != http.StatusOK {
		t.Fatalf("revoke: %d want 200", code)
	}
	if result.Sessions != 2 {
		t.Fatalf("revocation reported %+v, want both of the device's sessions", result)
	}
	if _, ok := server.sessions.Get("sess_device"); ok {
		t.Fatal("revoked device's session survived")
	}
	if _, ok := server.sessions.Get("sess_other"); !ok {
		t.Fatal("revocation took an unrelated session")
	}
	var found bool
	for _, d := range listDevices(t, ts, alice) {
		if d.ID == deviceID {
			found = true
			if !d.Revoked || d.RevokedAt == "" {
				t.Fatalf("row not marked revoked: %+v", d)
			}
		}
	}
	if !found {
		t.Fatal("revoked row disappeared")
	}
	if code := openSession(t, ts, alice, testFingerprint, "laptop", "laptop"); code != http.StatusForbidden {
		t.Fatalf("revoked device opened a fresh session: %d want 403", code)
	}
	if _, code := revokeDeviceReq(t, ts, alice.name, ownerAuth(t, ts, alice), deviceID); code != http.StatusOK {
		t.Fatalf("second revoke: %d want 200", code)
	}
}

// A registry the relay cannot parse is reset rather than freezing revocation.
func TestDeviceRegistryRecoversFromCorruption(t *testing.T) {
	server, ts := newTestRelay(t)
	alice := registerTestIdentity(t, server, ts, "devcorrupt.poweur.net")
	ctx := context.Background()

	setSysFile(t, server, alice.name, devicesDocPath, "not json at all")
	doc := server.readDevices(ctx, alice.name)
	if doc.Version != 1 || len(doc.Devices) != 0 {
		t.Fatalf("corrupt registry did not reset: %+v", doc)
	}
	if id := server.touchDevice(ctx, alice.name, deviceObservation{
		Fingerprint: testFingerprint, Name: "laptop", Kind: "laptop",
	}); id != idpkg.DeviceIDFromFingerprint(testFingerprint) {
		t.Fatalf("touch after corruption returned %q", id)
	}
	if got := listDevices(t, ts, alice); len(got) != 1 {
		t.Fatalf("registry did not recover: %+v", got)
	}
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

// Client, platform, browser and the keystore link arrive as optional headers
// and are recorded, sanitized, on the same row the name and kind land in. A
// later observation that omits them leaves them alone.
func TestDeviceRegistryClientMetadata(t *testing.T) {
	server, ts := newTestRelay(t)
	alice := registerTestIdentity(t, server, ts, "devmeta.poweur.net")

	observe := func(headers map[string]string) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-Poweur-Device", testFingerprint)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		server.touchDevice(context.Background(), alice.name, deviceFromRequest(req))
	}
	observe(map[string]string{
		"X-Poweur-Device-Name":       "Safari on Mac",
		"X-Poweur-Device-Client":     "WEB",
		"X-Poweur-Device-Platform":   "macOS",
		"X-Poweur-Device-Browser":    "Safari",
		"X-Poweur-Device-Enrollment": "abc_DEF-123",
	})
	observe(nil)
	devices := listDevices(t, ts, alice)
	if len(devices) != 1 {
		t.Fatalf("want one device: %+v", devices)
	}
	d := devices[0]
	if d.Client != idpkg.DeviceClientWeb || d.Platform != "macOS" || d.Browser != "Safari" || d.EnrollmentID != "abc_DEF-123" {
		t.Fatalf("metadata not recorded or lost on a bare observation: %+v", d)
	}

	// Junk is dropped rather than stored, and never fails the request.
	observe(map[string]string{
		"X-Poweur-Device-Client":     "toaster",
		"X-Poweur-Device-Enrollment": "not valid!",
		"X-Poweur-Device-Platform":   strings.Repeat("p", 500),
	})
	d = listDevices(t, ts, alice)[0]
	if d.Client != idpkg.DeviceClientWeb || d.EnrollmentID != "abc_DEF-123" || len(d.Platform) > idpkg.MaxDeviceFieldLen {
		t.Fatalf("junk overwrote or bypassed the caps: %+v", d)
	}
	if err := server.readDevices(context.Background(), alice.name).Validate(); err != nil {
		t.Fatalf("registry no longer validates: %v", err)
	}
}
