package integration_test

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
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

	relaypkg "github.com/poweur/api/pkg/relay"
	clipkg "github.com/poweur/cli/pkg/cli"
	idpkg "github.com/poweur/identity"

	"github.com/poweur/integration/fakedns"
)

// Background push (EPIC-009 E09-T11), end to end: a real relay with a VAPID
// key, real CLIs sending real encrypted mail, and a fake push service that
// decrypts what it is handed with the subscription's private key — exactly
// what a phone would do.

type fakePushService struct {
	srv  *httptest.Server
	priv *ecdh.PrivateKey
	auth []byte

	mu       sync.Mutex
	status   int
	bodies   [][]byte
	payloads []map[string]any
}

func newFakePushService(t *testing.T) *fakePushService {
	t.Helper()
	priv, _ := ecdh.P256().GenerateKey(rand.Reader)
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	f := &fakePushService{priv: priv, auth: auth, status: http.StatusCreated}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.HasPrefix(r.Header.Get("Authorization"), "vapid t=") {
			t.Errorf("push without VAPID: %q", r.Header.Get("Authorization"))
		}
		plain, err := relaypkg.DecryptWebPush(body, f.priv, f.auth)
		if err != nil {
			t.Errorf("push does not decrypt with the subscription key: %v", err)
		}
		var payload map[string]any
		_ = json.Unmarshal(plain, &payload)
		f.mu.Lock()
		f.bodies = append(f.bodies, body)
		f.payloads = append(f.payloads, payload)
		status := f.status
		f.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakePushService) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.payloads)
}

func (f *fakePushService) waitFor(t *testing.T, n int) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for f.count() < n && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.payloads) < n {
		t.Fatalf("want %d pushes, got %d", n, len(f.payloads))
	}
	return append([]map[string]any(nil), f.payloads...)
}

func newPushRelay(t *testing.T, zone *fakedns.Zone) string {
	t.Helper()
	key, err := relaypkg.GenerateVAPIDKey()
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(nil)
	addr := ts.Listener.Addr().String()
	cfg := relaypkg.Config{
		ListenAddr:           addr,
		RelayAddress:         addr,
		RelayScheme:          "http",
		DNSTTL:               time.Minute,
		ChallengeTTL:         time.Minute,
		Version:              "integration-test",
		DataDir:              t.TempDir(),
		HostedDomains:        []string{"poweur.net"},
		ResolverAllowPrivate: true,
		PushVAPIDPrivateKey:  key,
		PushVAPIDSubject:     "mailto:ops@poweur.test",
		PushEndpointHosts:    []string{"127.0.0.1"},
		PushCoalesce:         50 * time.Millisecond,
		RateLimits:           relaypkg.RateLimits{PerMinute: 1000, PerHour: 10000, PerDay: 100000},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	providers := relaypkg.NewProviderFactory(cfg)
	relaypkg.RegisterProvider(providers, "mock", zone.Provider())
	server := relaypkg.NewServer(cfg, zone, providers)
	ts.Config.Handler = relaypkg.Router(server)
	ts.Start()
	t.Cleanup(ts.Close)
	return addr
}

// ownerCall is an owner-authenticated request from a named device, signed
// with the identity key in home — the web client's path, minus the browser.
func ownerCall(t *testing.T, relayURL, home, identity, device, method, path string, body any) (int, []byte) {
	t.Helper()
	key := loadIdentityKey(t, home, identity)
	resp, err := http.Get(relayURL + "/auth/challenge?identity=" + identity)
	if err != nil {
		t.Fatal(err)
	}
	var challenge struct {
		Challenge string `json:"challenge"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&challenge)
	resp.Body.Close()
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, relayURL+path, reader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Poweur-Identity", identity)
	req.Header.Set("X-Poweur-Challenge", challenge.Challenge)
	req.Header.Set("X-Poweur-Signature", base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, []byte(challenge.Challenge))))
	req.Header.Set("X-Poweur-Device", device)
	req.Header.Set("X-Poweur-Device-Kind", "phone")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// INT_WEBPUSH_01: with no stream open, mail to an identity produces one
// encrypted push per registered device, carrying the pending count and no
// content; a push service that says the subscription is gone gets it removed.
func TestINT_WEBPUSH_01_BackgroundPush(t *testing.T) {
	zone := newZone(t)
	addr := newPushRelay(t, zone)
	relayURL := "http://" + addr
	const alice, bob = "pushalice.poweur.net", "pushbob.poweur.net"
	zone.SetHost(alice, addr)
	zone.SetHost(bob, addr)
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	aliceHome, bobHome := t.TempDir(), t.TempDir()
	runCLI(t, aliceHome, "identity", "create", alice, "--hosted", "--relay", relayURL, "--json")
	runCLI(t, bobHome, "identity", "create", bob, "--hosted", "--relay", relayURL, "--json")

	// The relay advertises its key.
	resp, err := http.Get(relayURL + "/push/config")
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Enabled bool   `json:"enabled"`
		Key     string `json:"vapid_public_key"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&config)
	resp.Body.Close()
	if !config.Enabled || len(config.Key) != 87 {
		t.Fatalf("push config %+v", config)
	}

	phone := newFakePushService(t)
	sub := map[string]any{
		"endpoint": phone.srv.URL + "/send/alice-phone",
		"keys": map[string]string{
			"p256dh": base64.RawURLEncoding.EncodeToString(phone.priv.PublicKey().Bytes()),
			"auth":   base64.RawURLEncoding.EncodeToString(phone.auth),
		},
	}
	if status, raw := ownerCall(t, relayURL, aliceHome, alice, "alice-phone", http.MethodPost, "/push/"+alice+"/subscribe", sub); status != http.StatusOK {
		t.Fatalf("subscribe: %d %s", status, raw)
	}

	const secret = "the eagle lands at midnight"
	runCLI(t, bobHome, "send", alice, secret)
	got := phone.waitFor(t, 1)
	if got[0]["identity"] != alice || got[0]["kind"] != "message" || got[0]["count"] != float64(1) {
		t.Fatalf("payload %+v", got[0])
	}
	phone.mu.Lock()
	for _, body := range phone.bodies {
		if bytes.Contains(body, []byte(secret)) || bytes.Contains(body, []byte(bob)) {
			t.Fatal("push body carries message content or the sender in the clear")
		}
	}
	phone.mu.Unlock()
	if raw, _ := json.Marshal(got[0]); strings.Contains(string(raw), "eagle") || strings.Contains(string(raw), bob) {
		t.Fatalf("decrypted payload leaks content or sender: %s", raw)
	}

	// The test button reaches the same device.
	if status, raw := ownerCall(t, relayURL, aliceHome, alice, "alice-phone", http.MethodPost, "/push/"+alice+"/test", nil); status != http.StatusOK {
		t.Fatalf("test push: %d %s", status, raw)
	}
	if got := phone.waitFor(t, 2); got[1]["kind"] != "test" {
		t.Fatalf("test payload %+v", got[1])
	}

	// The browser dropped the subscription: the relay forgets it.
	phone.mu.Lock()
	phone.status = http.StatusGone
	phone.mu.Unlock()
	runCLI(t, bobHome, "send", alice, "second")
	phone.waitFor(t, 3)
	deadline := time.Now().Add(5 * time.Second)
	for {
		status, raw := ownerCall(t, relayURL, aliceHome, alice, "alice-phone", http.MethodGet, "/devices/"+alice, nil)
		if status != http.StatusOK {
			t.Fatalf("devices: %d %s", status, raw)
		}
		var list struct {
			Devices []idpkg.Device `json:"devices"`
		}
		_ = json.Unmarshal(raw, &list)
		still := false
		for _, d := range list.Devices {
			still = still || d.Push != nil
		}
		if !still {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a 410 left the push registration in place")
		}
		time.Sleep(20 * time.Millisecond)
	}
	runCLI(t, bobHome, "send", alice, "third")
	time.Sleep(300 * time.Millisecond)
	if n := phone.count(); n != 3 {
		t.Fatalf("pushed to a gone subscription: %d pushes", n)
	}

	// Alice still gets all three messages the normal way.
	inbox, _ := runCLI(t, aliceHome, "inbox")
	for _, want := range []string{secret, "second", "third"} {
		if !strings.Contains(inbox, want) {
			t.Fatalf("inbox missing %q:\n%s", want, inbox)
		}
	}
}
