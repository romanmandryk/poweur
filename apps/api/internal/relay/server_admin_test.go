package relay

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/poweur/api/internal/config"
	"github.com/poweur/api/internal/crypto"
	"github.com/poweur/api/internal/dns"
	"github.com/poweur/api/internal/storage"
)

// TestIdentitiesPostSuccess runs the full owner-only registration path using
// the in-memory DNS provider and a real identity signature.
func TestIdentitiesPostSuccess(t *testing.T) {
	cfg := config.Config{
		ListenAddr:   ":0",
		RelayAddress: "relay.reg.test",
		RelayScheme:  "http",
		DNSTTL:       time.Minute,
		ChallengeTTL: time.Minute,
		Version:      "t",
		RateLimits:   config.RateLimits{PerMinute: 100, PerHour: 1000, PerDay: 10000},
	}
	pub, priv, _ := ed25519.GenerateKey(nil)
	enc32 := [32]byte{9: 1, 20: 2, 30: 3} // 32 bytes for X25519
	encB64 := base64.RawURLEncoding.EncodeToString(enc32[:])
	pubB64 := base64.RawURLEncoding.EncodeToString(pub)
	norm, _, err := crypto.NormalizePublicKey(pubB64)
	if err != nil {
		t.Fatal(err)
	}
	issued := time.Now().UTC().Format(time.RFC3339)
	nonce := "n-reg-1"
	canon := crypto.CanonicalIdentityRegistration("newid.reg.test", norm, encB64, cfg.RelayAddress, issued, nonce)
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(canon)))

	f := dns.NewProviderFactory(cfg)
	f.Register("mock", dns.NewMemoryProvider(cfg))
	s := NewServer(cfg, &fakeResolver{}, f)

	ts := httptest.NewServer(s.Router())
	defer ts.Close()
	req := IdentityRequest{
		Identity:            "newid.reg.test",
		PublicKey:           pubB64,
		EncryptionPublicKey: encB64,
		DNSProvider:         "mock",
		DNSToken:            "token",
		IssuedAt:            issued,
		Nonce:               nonce,
		IdentitySignature:   sig,
	}
	b, _ := json.Marshal(req)
	resp, err := http.Post(ts.URL+"/identities", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

// TestSessionDeleteWithIdentitySignature exercises DELETE /sessions/:id
// with a valid identity_signature on the long-lived key.
func TestSessionDeleteWithIdentitySignature(t *testing.T) {
	server, _ := newTestServer(t)
	pub, _, _ := ed25519.GenerateKey(nil)
	sid := "sess_to_delete"
	issuedS := time.Now().UTC()
	expiresS := issuedS.Add(time.Hour)
	spub := base64.RawURLEncoding.EncodeToString(pub)
	server.sessions.Put(storage.Session{
		ID:             sid,
		Identity:       "alice.sd.test",
		PublicKey:      spub,
		PublicKeyBytes: pub,
		IssuedAt:       issuedS,
		ExpiresAt:      expiresS,
		IssuedAtRaw:    issuedS.Format(time.RFC3339),
		ExpiresAtRaw:   expiresS.Format(time.RFC3339),
		Nonce:          "n",
	})
	idPub, idPriv, _ := ed25519.GenerateKey(nil)
	idB64 := base64.RawURLEncoding.EncodeToString(idPub)
	server.identities.Add(storage.Identity{
		Identity:       "alice.sd.test",
		PublicKey:      idB64,
		PublicKeyBytes: idPub,
		CreatedAt:      time.Now().UTC(),
	})
	// Session must belong to same identity; session was registered with "alice.sd.test" and pub (session key). Identity store has idPub. OK.

	issued := time.Now().UTC().Format(time.RFC3339)
	nonce := "r1"
	canon := crypto.CanonicalSessionRevocation("alice.sd.test", sid, issued, nonce)
	body := SessionRevokeRequest{
		Identity:          "alice.sd.test",
		IssuedAt:          issued,
		Nonce:             nonce,
		IdentitySignature: base64.StdEncoding.EncodeToString(ed25519.Sign(idPriv, []byte(canon))),
	}
	b, _ := json.Marshal(body)
	ts := httptest.NewServer(server.Router())
	defer ts.Close()
	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/sessions/"+sid, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status %d", resp.StatusCode)
	}
}
