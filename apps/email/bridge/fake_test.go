package bridge

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/poweur/identity"
)

// fakeNet stands in for the Poweur network in unit tests: one HTTP server
// that serves every identity's well-known documents (chosen by Host) and
// plays the bridge's home relay (POST /messages, pickup, consume, system
// files). The integration suite runs the real relay.
type fakeNet struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	ids    map[string]*fakeID
	posted []Envelope
	inbox  []Envelope // the bridge's own inbox
	sys    map[string][]byte
	refuse func(env Envelope) int
}

type fakeID struct {
	name    string
	seed    []byte
	priv    ed25519.PrivateKey
	encPub  []byte
	encPriv []byte
	doc     []byte
	optIn   []byte
}

func newFakeNet(t *testing.T) *fakeNet {
	t.Helper()
	n := &fakeNet{t: t, ids: map[string]*fakeID{}, sys: map[string][]byte{}}
	n.srv = httptest.NewServer(http.HandlerFunc(n.serve))
	t.Cleanup(n.srv.Close)
	return n
}

func (n *fakeNet) resolve() identity.ResolveOptions {
	addr := strings.TrimPrefix(n.srv.URL, "http://")
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}, Timeout: 5 * time.Second}
	return identity.ResolveOptions{Scheme: "http", AllowPrivate: true, HTTPClient: client}
}

// add creates an identity, optionally opted into bridge.
func (n *fakeNet) add(name, bridge string) *fakeID {
	n.t.Helper()
	seed := make([]byte, 32)
	copy(seed, name)
	_, priv, _ := identity.DeriveSigningKey(seed)
	encPub, encPriv, _ := identity.DeriveEncryptionKey(seed)
	pub := priv.Public().(ed25519.PublicKey)
	doc := identity.NewDocument(name, "ed25519:"+base64.RawURLEncoding.EncodeToString(pub),
		"x25519:"+base64.RawURLEncoding.EncodeToString(encPub), "relay.test", nil)
	doc.UpdatedAt = "2026-10-13T00:00:00Z"
	if err := doc.Sign(priv); err != nil {
		n.t.Fatal(err)
	}
	raw, _ := json.Marshal(doc)
	id := &fakeID{name: name, seed: seed, priv: priv, encPub: encPub, encPriv: encPriv, doc: raw}
	if bridge != "" {
		o, err := identity.NewEmailOptIn(name, bridge, time.Now())
		if err != nil {
			n.t.Fatal(err)
		}
		_ = o.Sign(priv)
		id.optIn, _ = json.Marshal(o)
	}
	n.mu.Lock()
	n.ids[name] = id
	n.mu.Unlock()
	return id
}

func (n *fakeNet) serve(w http.ResponseWriter, r *http.Request) {
	host := strings.Split(r.Host, ":")[0]
	n.mu.Lock()
	defer n.mu.Unlock()
	switch {
	case r.URL.Path == "/.well-known/poweur/id.json":
		if id, ok := n.ids[host]; ok {
			_, _ = w.Write(id.doc)
			return
		}
		http.NotFound(w, r)
	case r.URL.Path == "/.well-known/poweur/email.json":
		if id, ok := n.ids[host]; ok && id.optIn != nil {
			_, _ = w.Write(id.optIn)
			return
		}
		http.NotFound(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/messages":
		var env Envelope
		_ = json.NewDecoder(r.Body).Decode(&env)
		if n.refuse != nil {
			if code := n.refuse(env); code != 0 {
				w.WriteHeader(code)
				_, _ = w.Write([]byte(`{"error":"refused","detail":"test"}`))
				return
			}
		}
		n.posted = append(n.posted, env)
		w.WriteHeader(http.StatusAccepted)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/messages/"):
		if r.Header.Get("X-Poweur-Signature") == "" {
			http.Error(w, "unsigned", http.StatusUnauthorized)
			return
		}
		cursor := r.URL.Query().Get("since")
		start := 0
		if cursor != "" {
			start = len(cursor)
		}
		var out []Envelope
		if start < len(n.inbox) {
			out = n.inbox[start:]
		}
		next := strings.Repeat("x", len(n.inbox))
		_ = json.NewEncoder(w).Encode(map[string]any{"messages": out, "cursor": next})
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/consume"):
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/system/"):
		body, _ := io.ReadAll(r.Body)
		n.sys[r.URL.Path] = body
		w.WriteHeader(http.StatusOK)
	default:
		http.NotFound(w, r)
	}
}

// sendToBridge puts an encrypted, signed message from `from` into the
// bridge's inbox, as the relay would.
func (n *fakeNet) sendToBridge(from *fakeID, bridge *fakeID, msgType string, payload any) {
	n.t.Helper()
	raw, _ := json.Marshal(payload)
	sealed, err := identity.Seal(bridge.encPub, raw, identity.MessageSealDomain, nil)
	if err != nil {
		n.t.Fatal(err)
	}
	env := Envelope{
		ID: newMessageID(), Sender: from.name, Recipient: bridge.name, Timestamp: time.Now().UTC().Format(time.RFC3339),
		Payload: sealed.Ciphertext, Type: msgType,
		Encryption: &identity.EnvelopeEncryption{Alg: identity.SealAlgorithm, EphemeralPublicKey: sealed.EphemeralPublicKey, Nonce: sealed.Nonce},
	}
	n.mu.Lock()
	n.inbox = append(n.inbox, env)
	n.mu.Unlock()
}

// delivered returns and opens what the bridge posted to recipient.
func (n *fakeNet) delivered(to *fakeID, msgType string) [][]byte {
	n.t.Helper()
	n.mu.Lock()
	defer n.mu.Unlock()
	var out [][]byte
	for _, env := range n.posted {
		if env.Recipient != to.name || env.Type != msgType {
			continue
		}
		plain, err := identity.OpenSeal(to.encPriv, identity.SealedPayload{
			Ciphertext: env.Payload, EphemeralPublicKey: env.Encryption.EphemeralPublicKey, Nonce: env.Encryption.Nonce,
		}, identity.MessageSealDomain, nil)
		if err != nil {
			n.t.Fatalf("delivered message does not open: %v", err)
		}
		out = append(out, plain)
	}
	return out
}

// testBridge builds a bridge on the fake network.
type testBridge struct {
	*Server
	net    *fakeNet
	self   *fakeID
	logDir string
	public *httptest.Server
}

func newTestBridge(t *testing.T, mutate ...func(*Config)) *testBridge {
	t.Helper()
	n := newFakeNet(t)
	self := n.add("email.poweur.test", "")
	logDir := t.TempDir()
	var handler http.Handler = http.NotFoundHandler()
	public := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
	t.Cleanup(public.Close)
	cfg := Config{
		PublicURL: public.URL,
		HTTPAddr:  "127.0.0.1:0", LMTPAddr: "127.0.0.1:0",
		BridgeID: self.name, Seed: self.seed, RelayURL: n.srv.URL,
		Domains:    []DomainMap{{Mail: "poweur.test", Identity: "poweur.test"}, {Mail: "alias.test", Identity: "poweur.test"}},
		HookSecret: "hook-secret-0123456789", LMTPUsername: "stalwart", LMTPPassword: "lmtp-password-0123456789",
		LMTPInsecure: true, AuthServID: "mail.poweur.test",
		Transport: "log", LogDir: logDir, DataDir: t.TempDir(),
		Footer: "first", SignupURL: "https://poweur.test",
		OutDailyLimit: 100, OutNewRecipients: 20, InDailyPerRecipient: 500,
		Resolve: n.resolve(),
	}
	for _, m := range mutate {
		m(&cfg)
	}
	srv, err := New(cfg, Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	handler = srv.Handler()
	t.Cleanup(func() { srv.Store.Close() })
	return &testBridge{Server: srv, net: n, self: self, logDir: logDir, public: public}
}
