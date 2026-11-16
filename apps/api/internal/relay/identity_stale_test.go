package relay

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/poweur/api/internal/config"
	"github.com/poweur/api/internal/crypto"
	"github.com/poweur/api/internal/dns"
	"github.com/poweur/api/internal/storage"
	idpkg "github.com/poweur/identity"
)

// staleFixture is a relay hosting bob, and a separate web host serving the
// signed identity document of a remote sender, alice (EPIC-029 E29-T2).
type staleFixture struct {
	t          *testing.T
	server     *Server
	relay      *httptest.Server
	senderHost *httptest.Server
	senderPriv ed25519.PrivateKey
	dataDir    string
	// status is what alice's host answers with; 200 serves her document.
	status atomic.Int32
	seq    int
}

const staleSender = "alice.example.net"

func newStaleFixture(t *testing.T, maxStale time.Duration) *staleFixture {
	t.Helper()
	f := &staleFixture{t: t, dataDir: t.TempDir()}
	f.status.Store(http.StatusOK)

	senderPub, senderPriv, _ := ed25519.GenerateKey(nil)
	f.senderPriv = senderPriv
	doc := idpkg.NewDocument(staleSender, idpkg.FormatEd25519PublicKey(senderPub), "", "relay.example.net", nil)
	if err := doc.Sign(senderPriv); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(doc)
	f.senderHost = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status := int(f.status.Load()); status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		_, _ = w.Write(raw)
	}))
	t.Cleanup(f.senderHost.Close)

	cfg := config.Config{
		ListenAddr:           ":0",
		RelayAddress:         "relay.test",
		RelayScheme:          "http",
		DNSTTL:               time.Minute,
		ChallengeTTL:         time.Minute,
		Version:              "test",
		DataDir:              f.dataDir,
		ResolverAllowPrivate: true,
		StaleIdentityMax:     maxStale,
		IdentityStoreMax:     100,
		IdentityCacheTTL:     -1, // every message resolves, so the store is what's tested
		RateLimits:           config.RateLimits{PerMinute: 1000, PerHour: 10000, PerDay: 100000},
	}
	resolver := &fakeResolver{
		txt: map[string][]string{},
		hosts: map[string][]string{
			"bob.example.org": {"relay.test"},
			staleSender:       {f.senderHost.Listener.Addr().String()},
		},
	}
	f.server = NewServer(cfg, resolver, dns.NewProviderFactory(cfg))
	recipientPub, _, _ := ed25519.GenerateKey(nil)
	f.server.identities.Add(storage.Identity{
		Identity:       "bob.example.org",
		PublicKey:      base64.RawURLEncoding.EncodeToString(recipientPub),
		PublicKeyBytes: recipientPub,
		CreatedAt:      time.Now().UTC(),
	})
	f.relay = httptest.NewServer(f.server.Router())
	t.Cleanup(f.relay.Close)
	return f
}

// send posts an identity-signed message from alice, signed with priv, and
// returns the status.
func (f *staleFixture) send(priv ed25519.PrivateKey) int {
	f.t.Helper()
	f.seq++
	msg := Message{
		ID:        "msg_stale_" + string(rune('a'+f.seq)),
		Sender:    staleSender,
		Recipient: "bob.example.org",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Payload:   "ciphertext",
		Encryption: &EncryptionMeta{
			Alg:                "x25519-chacha20-poly1305",
			EphemeralPublicKey: "ephemeral-pub",
			Nonce:              "nonce",
		},
	}
	canonical := crypto.CanonicalMessageFull(msg.Sender, msg.Recipient, msg.Timestamp, msg.Payload, msg.ID, "", &crypto.EncryptionMeta{
		Alg:                msg.Encryption.Alg,
		EphemeralPublicKey: msg.Encryption.EphemeralPublicKey,
		Nonce:              msg.Encryption.Nonce,
	})
	msg.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(canonical)))
	body, _ := json.Marshal(msg)
	resp, err := http.Post(f.relay.URL+"/messages", "application/json", bytes.NewReader(body))
	if err != nil {
		f.t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func (f *staleFixture) storedFile() string {
	return filepath.Join(f.dataDir, "cache", "identities", staleSender+".json")
}

func TestStaleIdentity_UsedWhenSenderHostIsDown(t *testing.T) {
	f := newStaleFixture(t, time.Hour)
	if got := f.send(f.senderPriv); got != http.StatusAccepted {
		t.Fatalf("first message: %d", got)
	}
	if _, err := os.Stat(f.storedFile()); err != nil {
		t.Fatalf("sender document not stored after a verified message: %v", err)
	}

	f.senderHost.Close()
	if got := f.send(f.senderPriv); got != http.StatusAccepted {
		t.Fatalf("message while the sender's host is down: %d, want 202", got)
	}
	// The stored key is still the only one accepted.
	_, otherPriv, _ := ed25519.GenerateKey(nil)
	if got := f.send(otherPriv); got != http.StatusUnauthorized {
		t.Fatalf("message signed with another key: %d, want 401", got)
	}
}

func TestStaleIdentity_UsedOn5xxButNotOn404(t *testing.T) {
	f := newStaleFixture(t, time.Hour)
	if got := f.send(f.senderPriv); got != http.StatusAccepted {
		t.Fatalf("first message: %d", got)
	}
	f.status.Store(http.StatusServiceUnavailable)
	if got := f.send(f.senderPriv); got != http.StatusAccepted {
		t.Fatalf("sender host answering 503: %d, want 202", got)
	}
	// "Not found" is an answer about the identity, not an outage.
	f.status.Store(http.StatusNotFound)
	if got := f.send(f.senderPriv); got != http.StatusUnauthorized {
		t.Fatalf("sender host answering 404: %d, want 401", got)
	}
}

func TestStaleIdentity_NotUsedPastMaxAge(t *testing.T) {
	f := newStaleFixture(t, time.Hour)
	if got := f.send(f.senderPriv); got != http.StatusAccepted {
		t.Fatalf("first message: %d", got)
	}
	stored, ok := f.server.idStore.Get(staleSender)
	if !ok {
		t.Fatal("sender document not stored")
	}
	if err := f.server.idStore.Put(staleSender, stored.Raw, time.Now().Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	f.senderHost.Close()
	if got := f.send(f.senderPriv); got != http.StatusUnauthorized {
		t.Fatalf("document older than the stale window: %d, want 401", got)
	}
}

func TestStaleIdentity_NotStoredBeforeSignatureVerifies(t *testing.T) {
	f := newStaleFixture(t, time.Hour)
	_, forgedPriv, _ := ed25519.GenerateKey(nil)
	if got := f.send(forgedPriv); got != http.StatusUnauthorized {
		t.Fatalf("forged message: %d, want 401", got)
	}
	if _, err := os.Stat(f.storedFile()); !os.IsNotExist(err) {
		t.Fatalf("document stored for a sender whose signature never verified (err=%v)", err)
	}
	// Nothing stored, so an outage now means alice can't be verified.
	f.senderHost.Close()
	if got := f.send(f.senderPriv); got != http.StatusUnauthorized {
		t.Fatalf("message with nothing stored and the host down: %d, want 401", got)
	}
}

func TestStaleIdentity_OffWhenMaxIsZero(t *testing.T) {
	f := newStaleFixture(t, 0)
	if got := f.send(f.senderPriv); got != http.StatusAccepted {
		t.Fatalf("first message: %d", got)
	}
	if f.server.idStore != nil {
		t.Fatal("identity store created with the fallback off")
	}
	f.senderHost.Close()
	if got := f.send(f.senderPriv); got != http.StatusUnauthorized {
		t.Fatalf("fallback off and the host down: %d, want 401", got)
	}
}

// Sessions, drive access and policy never act on a stored document.
func TestStaleIdentity_OnlyTheMessagePathFallsBack(t *testing.T) {
	f := newStaleFixture(t, time.Hour)
	if got := f.send(f.senderPriv); got != http.StatusAccepted {
		t.Fatalf("first message: %d", got)
	}
	f.senderHost.Close()
	if _, err := f.server.resolveIdentityPublicKey(t.Context(), staleSender); err == nil {
		t.Fatal("resolveIdentityPublicKey returned a stored document")
	}
	if _, err := f.server.resolveSenderPublicKey(t.Context(), staleSender); err != nil {
		t.Fatalf("resolveSenderPublicKey: %v", err)
	}
}
