package relay

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/poweur/api/internal/storage"
	"github.com/poweur/api/internal/webpush"
)

// Background push (EPIC-009 E09-T11). The fake push service below decrypts
// what it receives with the subscription's private key, so these tests
// assert what a phone would actually see — and that nothing else could.

type fakePushService struct {
	t      *testing.T
	srv    *httptest.Server
	priv   *ecdh.PrivateKey
	auth   []byte
	status int

	mu       sync.Mutex
	received []PushPayload
	headers  []http.Header
}

func newFakePushService(t *testing.T) *fakePushService {
	t.Helper()
	priv, _ := ecdh.P256().GenerateKey(rand.Reader)
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	f := &fakePushService{t: t, priv: priv, auth: auth, status: http.StatusCreated}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		plain, err := webpush.Decrypt(body, f.priv, f.auth)
		if err != nil {
			t.Errorf("push body does not decrypt: %v", err)
		}
		var payload PushPayload
		_ = json.Unmarshal(plain, &payload)
		f.mu.Lock()
		f.received = append(f.received, payload)
		f.headers = append(f.headers, r.Header.Clone())
		status := f.status
		f.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakePushService) subscription() map[string]any {
	return map[string]any{
		"endpoint": f.srv.URL + "/push/" + strings.Repeat("x", 20),
		"keys": map[string]string{
			"p256dh": base64.RawURLEncoding.EncodeToString(f.priv.PublicKey().Bytes()),
			"auth":   base64.RawURLEncoding.EncodeToString(f.auth),
		},
	}
}

func (f *fakePushService) got() []PushPayload {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]PushPayload(nil), f.received...)
}

func enablePush(t *testing.T, server *Server) {
	t.Helper()
	key, err := webpush.GenerateVAPIDKey()
	if err != nil {
		t.Fatal(err)
	}
	server.push = newPusher(key, "mailto:ops@relay.test", []string{"127.0.0.1"}, 20*time.Millisecond, true)
	if server.push == nil {
		t.Fatal("push not enabled")
	}
}

func pushCall(t *testing.T, ts *httptest.Server, id hostedID, action, fingerprint string, body any) (int, map[string]any) {
	t.Helper()
	hdr := ownerAuth(t, ts, id)
	hdr["Content-Type"] = "application/json"
	if fingerprint != "" {
		hdr["X-Poweur-Device"] = fingerprint
	}
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	} else {
		raw = []byte("{}")
	}
	resp := httpReq(t, ts, http.MethodPost, "/push/"+id.name+"/"+action, "", raw, hdr)
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestPushConfig(t *testing.T) {
	server, ts := newTestRelay(t)
	resp := httpReq(t, ts, http.MethodGet, "/push/config", "", nil, nil)
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if out["enabled"] != false || out["vapid_public_key"] != nil {
		t.Fatalf("disabled relay advertised push: %v", out)
	}
	enablePush(t, server)
	resp = httpReq(t, ts, http.MethodGet, "/push/config", "", nil, nil)
	out = nil
	_ = json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if out["enabled"] != true || out["vapid_public_key"] != server.push.sender.VAPID.PublicKey() {
		t.Fatalf("config %v", out)
	}
}

func TestPushEndpointsDisabledWithoutVAPID(t *testing.T) {
	server, ts := newTestRelay(t)
	alice := registerTestIdentity(t, server, ts, "alice.poweur.net")
	if status, _ := pushCall(t, ts, alice, "subscribe", testFingerprint, map[string]any{}); status != http.StatusNotFound {
		t.Fatalf("subscribe on a push-less relay: %d", status)
	}
	server.notify(alice.name, "message", "m1") // must not panic
}

func TestPushSubscribeValidation(t *testing.T) {
	server, ts := newTestRelay(t)
	enablePush(t, server)
	alice := registerTestIdentity(t, server, ts, "alice.poweur.net")
	bob := registerTestIdentity(t, server, ts, "bob.poweur.net")
	fake := newFakePushService(t)

	if status, _ := pushCall(t, ts, alice, "subscribe", "", fake.subscription()); status != http.StatusBadRequest {
		t.Fatalf("no device header: %d", status)
	}
	bad := fake.subscription()
	bad["keys"] = map[string]string{"p256dh": "AAAA", "auth": "AAAA"}
	if status, _ := pushCall(t, ts, alice, "subscribe", testFingerprint, bad); status != http.StatusBadRequest {
		t.Fatalf("bad keys: %d", status)
	}
	elsewhere := fake.subscription()
	elsewhere["endpoint"] = "https://evil.example.com/push"
	if status, _ := pushCall(t, ts, alice, "subscribe", testFingerprint, elsewhere); status != http.StatusForbidden {
		t.Fatalf("endpoint outside the allow-list: %d", status)
	}
	// Bob cannot register on Alice's identity: his challenge signature does
	// not verify for her.
	hdr := ownerAuth(t, ts, bob)
	hdr["X-Poweur-Identity"] = alice.name
	hdr["X-Poweur-Device"] = testFingerprint
	raw, _ := json.Marshal(fake.subscription())
	resp := httpReq(t, ts, http.MethodPost, "/push/"+alice.name+"/subscribe", "", raw, hdr)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("cross-identity subscribe: %d", resp.StatusCode)
	}
	for _, d := range listDevices(t, ts, alice) {
		if d.Push != nil {
			t.Fatalf("a refused subscribe stored a registration: %+v", d)
		}
	}
}

func TestPushEndpointAllowed(t *testing.T) {
	p := &pusher{hosts: []string{"fcm.googleapis.com", "push.apple.com"}}
	for endpoint, want := range map[string]bool{
		"https://fcm.googleapis.com/fcm/send/x":    true,
		"https://web.push.apple.com/QOx":           true,
		"https://evilpush.apple.com.example.net/x": false,
		"https://notpush.apple.com/x":              false,
		"http://fcm.googleapis.com/x":              false,
		"https://user@fcm.googleapis.com/x":        false,
		"https://127.0.0.1/x":                      false,
	} {
		if got := p.endpointAllowed(endpoint); got != want {
			t.Errorf("%s: %v, want %v", endpoint, got, want)
		}
	}
}

func TestPushDeliversCoalescedCount(t *testing.T) {
	server, ts := newTestRelay(t)
	enablePush(t, server)
	alice := registerTestIdentity(t, server, ts, "alice.poweur.net")
	fake := newFakePushService(t)
	if status, out := pushCall(t, ts, alice, "subscribe", testFingerprint, fake.subscription()); status != http.StatusOK {
		t.Fatalf("subscribe: %d %v", status, out)
	}

	// Three messages and a contact request in one burst: one push, naming
	// the request (the most important thing) and counting all four.
	for _, id := range []string{"m1", "m2", "m3"} {
		server.inbox.Add(alice.name, storage.StoredMessage{ID: id, Sender: "bob.poweur.net", Recipient: alice.name}, 100)
		server.notify(alice.name, "message", id)
	}
	server.requests.Add(alice.name, "carol.poweur.net", storage.StoredMessage{ID: "r1", Sender: "carol.poweur.net", Recipient: alice.name}, time.Hour)
	server.notify(alice.name, "request", "r1")
	// Acks never push.
	server.notify(alice.name, "ack", "m0")
	server.WaitPush()

	got := fake.got()
	if len(got) != 1 {
		t.Fatalf("want one coalesced push, got %d: %+v", len(got), got)
	}
	if got[0].Identity != alice.name || got[0].Kind != "request" || got[0].Count != 4 || got[0].V != 1 {
		t.Fatalf("payload %+v", got[0])
	}
	h := fake.headers[0]
	if h.Get("Urgency") != "high" || h.Get("TTL") != "86400" || h.Get("Topic") != webpush.Topic(alice.name) {
		t.Fatalf("headers %v", h)
	}
	if raw, _ := json.Marshal(got[0]); strings.Contains(string(raw), "bob") || strings.Contains(string(raw), "carol") {
		t.Fatalf("payload names a sender: %s", raw)
	}

	// An ack alone sends nothing.
	server.notify(alice.name, "ack", "m1")
	server.WaitPush()
	if n := len(fake.got()); n != 1 {
		t.Fatalf("ack pushed: %d", n)
	}
}

func TestPushSkipsDeviceWithOpenStream(t *testing.T) {
	server, ts := newTestRelay(t)
	enablePush(t, server)
	alice := registerTestIdentity(t, server, ts, "alice.poweur.net")
	fake := newFakePushService(t)
	pushCall(t, ts, alice, "subscribe", testFingerprint, fake.subscription())
	deviceID := listDevices(t, ts, alice)[0].ID

	detach := server.hub.attachDevice(alice.name, deviceID)
	server.notify(alice.name, "message", "m1")
	server.WaitPush()
	if n := len(fake.got()); n != 0 {
		t.Fatalf("pushed to a device that is listening: %d", n)
	}
	detach()
	server.notify(alice.name, "message", "m2")
	server.WaitPush()
	if n := len(fake.got()); n != 1 {
		t.Fatalf("after the stream closed: %d pushes", n)
	}
}

func TestPushGoneRemovesRegistration(t *testing.T) {
	server, ts := newTestRelay(t)
	enablePush(t, server)
	alice := registerTestIdentity(t, server, ts, "alice.poweur.net")
	fake := newFakePushService(t)
	fake.status = http.StatusGone
	pushCall(t, ts, alice, "subscribe", testFingerprint, fake.subscription())

	server.notify(alice.name, "message", "m1")
	server.WaitPush()
	for _, d := range listDevices(t, ts, alice) {
		if d.Push != nil {
			t.Fatalf("410 left the registration in place: %+v", d.Push)
		}
	}
}

func TestPushTestAndUnsubscribe(t *testing.T) {
	server, ts := newTestRelay(t)
	enablePush(t, server)
	alice := registerTestIdentity(t, server, ts, "alice.poweur.net")
	fake := newFakePushService(t)

	if status, _ := pushCall(t, ts, alice, "test", testFingerprint, nil); status != http.StatusNotFound {
		t.Fatalf("test before subscribing: %d", status)
	}
	pushCall(t, ts, alice, "subscribe", testFingerprint, fake.subscription())
	if status, out := pushCall(t, ts, alice, "test", testFingerprint, nil); status != http.StatusOK || out["sent"] != true {
		t.Fatalf("test push: %d %v", status, out)
	}
	if got := fake.got(); len(got) != 1 || got[0].Kind != "test" {
		t.Fatalf("test payload %+v", got)
	}

	fake.mu.Lock()
	fake.status = http.StatusBadRequest
	fake.mu.Unlock()
	if status, _ := pushCall(t, ts, alice, "test", testFingerprint, nil); status != http.StatusBadGateway {
		t.Fatalf("rejected test push: %d", status)
	}

	if status, _ := pushCall(t, ts, alice, "unsubscribe", testFingerprint, nil); status != http.StatusOK {
		t.Fatalf("unsubscribe: %d", status)
	}
	server.notify(alice.name, "message", "m1")
	server.WaitPush()
	if n := len(fake.got()); n != 2 {
		t.Fatalf("pushed after unsubscribe: %d", n)
	}
}

func TestPushRevokedDeviceStopsReceiving(t *testing.T) {
	server, ts := newTestRelay(t)
	enablePush(t, server)
	alice := registerTestIdentity(t, server, ts, "alice.poweur.net")
	fake := newFakePushService(t)
	pushCall(t, ts, alice, "subscribe", testFingerprint, fake.subscription())
	deviceID := listDevices(t, ts, alice)[0].ID

	raw, _ := json.Marshal(DeviceRevokeRequest{DeviceID: deviceID})
	hdr := ownerAuth(t, ts, alice)
	hdr["Content-Type"] = "application/json"
	resp := httpReq(t, ts, http.MethodPost, "/devices/"+alice.name+"/revoke", "", raw, hdr)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("revoke: %d", resp.StatusCode)
	}
	server.notify(alice.name, "message", "m1")
	server.WaitPush()
	if n := len(fake.got()); n != 0 {
		t.Fatalf("revoked device was pushed: %d", n)
	}
	if status, _ := pushCall(t, ts, alice, "subscribe", testFingerprint, fake.subscription()); status != http.StatusForbidden {
		t.Fatalf("revoked device re-subscribed: %d", status)
	}
}
