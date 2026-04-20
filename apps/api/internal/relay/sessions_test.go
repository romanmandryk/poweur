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

	"github.com/eurything/api/internal/config"
	"github.com/eurything/api/internal/crypto"
	"github.com/eurything/api/internal/dns"
	"github.com/eurything/api/internal/storage"
)

func newTestServer(t *testing.T) (*Server, *fakeResolver) {
	t.Helper()
	cfg := config.Config{
		ListenAddr:   ":0",
		RelayAddress: "relay.test",
		RelayScheme:  "http",
		DNSTTL:       time.Minute,
		ChallengeTTL: time.Minute,
		Version:      "test",
		RateLimits: config.RateLimits{
			PerMinute: 1000,
			PerHour:   10000,
			PerDay:    100000,
		},
	}
	resolver := &fakeResolver{
		txt:   map[string][]string{},
		hosts: map[string][]string{},
	}
	server := NewServer(cfg, resolver, dns.NewProviderFactory(cfg))
	return server, resolver
}

func TestSessionCreateAndMessageFlow(t *testing.T) {
	server, resolver := newTestServer(t)

	senderPub, senderPriv, _ := ed25519.GenerateKey(nil)
	recipientPub, _, _ := ed25519.GenerateKey(nil)

	senderTxt := "eurything-pubkey=ed25519:" + base64.RawURLEncoding.EncodeToString(senderPub)
	resolver.txt["_eurything.alice.example.com"] = []string{senderTxt}
	resolver.hosts["bob.example.org"] = []string{"relay.test"}

	server.identities.Add(storage.Identity{
		Identity:       "bob.example.org",
		PublicKey:      base64.RawURLEncoding.EncodeToString(recipientPub),
		PublicKeyBytes: recipientPub,
		CreatedAt:      time.Now().UTC(),
	})

	ts := httptest.NewServer(server.Router())
	defer ts.Close()

	sessionPub, sessionPriv, _ := ed25519.GenerateKey(nil)
	sessionPubB64 := base64.RawURLEncoding.EncodeToString(sessionPub)
	issuedAt := time.Now().UTC().Format(time.RFC3339)
	expiresAt := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339)
	nonce := "nonce-abc"

	canonical := crypto.CanonicalSessionRegistration(
		"alice.example.com", sessionPubB64, issuedAt, expiresAt, nonce,
	)
	identitySig := base64.StdEncoding.EncodeToString(ed25519.Sign(senderPriv, []byte(canonical)))

	sessionReq := SessionCreateRequest{
		Identity:          "alice.example.com",
		SessionPublicKey:  sessionPubB64,
		IssuedAt:          issuedAt,
		ExpiresAt:         expiresAt,
		Nonce:             nonce,
		IdentitySignature: identitySig,
	}
	body, _ := json.Marshal(sessionReq)
	resp, err := http.Post(ts.URL+"/sessions", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post session: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		b, _ := readBody(resp)
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, b)
	}
	var sessionResp SessionResponse
	if err := json.NewDecoder(resp.Body).Decode(&sessionResp); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	if sessionResp.SessionID == "" {
		t.Fatal("missing session id")
	}

	msg := Message{
		Sender:    "alice.example.com",
		Recipient: "bob.example.org",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Payload:   "ciphertext-placeholder",
		SessionID: sessionResp.SessionID,
		Encryption: &EncryptionMeta{
			Alg:                "x25519-chacha20-poly1305",
			EphemeralPublicKey: "eph-pubkey",
			Nonce:              "msg-nonce",
		},
	}
	encMeta := &crypto.EncryptionMeta{
		Alg:                msg.Encryption.Alg,
		EphemeralPublicKey: msg.Encryption.EphemeralPublicKey,
		Nonce:              msg.Encryption.Nonce,
	}
	canonicalMsg := crypto.CanonicalMessageFull(msg.Sender, msg.Recipient, msg.Timestamp, msg.Payload, msg.SessionID, encMeta)
	sig := ed25519.Sign(sessionPriv, []byte(canonicalMsg))
	msg.Signature = base64.StdEncoding.EncodeToString(sig)

	body, _ = json.Marshal(msg)
	resp, err = http.Post(ts.URL+"/messages", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post message: %v", err)
	}
	if resp.StatusCode != http.StatusAccepted {
		b, _ := readBody(resp)
		t.Fatalf("expected 202, got %d: %s", resp.StatusCode, b)
	}
}

func TestSessionRejectsForgedSignature(t *testing.T) {
	server, resolver := newTestServer(t)
	senderPub, _, _ := ed25519.GenerateKey(nil)
	senderTxt := "eurything-pubkey=ed25519:" + base64.RawURLEncoding.EncodeToString(senderPub)
	resolver.txt["_eurything.alice.example.com"] = []string{senderTxt}

	ts := httptest.NewServer(server.Router())
	defer ts.Close()

	sessionPub, _, _ := ed25519.GenerateKey(nil)
	sessionPubB64 := base64.RawURLEncoding.EncodeToString(sessionPub)

	_, wrongPriv, _ := ed25519.GenerateKey(nil)
	issuedAt := time.Now().UTC().Format(time.RFC3339)
	expiresAt := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	canonical := crypto.CanonicalSessionRegistration(
		"alice.example.com", sessionPubB64, issuedAt, expiresAt, "n",
	)
	identitySig := base64.StdEncoding.EncodeToString(ed25519.Sign(wrongPriv, []byte(canonical)))

	body, _ := json.Marshal(SessionCreateRequest{
		Identity:          "alice.example.com",
		SessionPublicKey:  sessionPubB64,
		IssuedAt:          issuedAt,
		ExpiresAt:         expiresAt,
		Nonce:             "n",
		IdentitySignature: identitySig,
	})
	resp, err := http.Post(ts.URL+"/sessions", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post session: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestSessionRejectsTTLOverLimit(t *testing.T) {
	server, resolver := newTestServer(t)
	senderPub, senderPriv, _ := ed25519.GenerateKey(nil)
	senderTxt := "eurything-pubkey=ed25519:" + base64.RawURLEncoding.EncodeToString(senderPub)
	resolver.txt["_eurything.alice.example.com"] = []string{senderTxt}

	ts := httptest.NewServer(server.Router())
	defer ts.Close()

	sessionPub, _, _ := ed25519.GenerateKey(nil)
	sessionPubB64 := base64.RawURLEncoding.EncodeToString(sessionPub)

	issuedAt := time.Now().UTC().Format(time.RFC3339)
	expiresAt := time.Now().UTC().Add(25 * time.Hour).Format(time.RFC3339)
	canonical := crypto.CanonicalSessionRegistration(
		"alice.example.com", sessionPubB64, issuedAt, expiresAt, "n",
	)
	identitySig := base64.StdEncoding.EncodeToString(ed25519.Sign(senderPriv, []byte(canonical)))

	body, _ := json.Marshal(SessionCreateRequest{
		Identity:          "alice.example.com",
		SessionPublicKey:  sessionPubB64,
		IssuedAt:          issuedAt,
		ExpiresAt:         expiresAt,
		Nonce:             "n",
		IdentitySignature: identitySig,
	})
	resp, err := http.Post(ts.URL+"/sessions", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post session: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 (ttl too long), got %d", resp.StatusCode)
	}
}

// TestSessionProofAcceptedByForeignRelay simulates a message signed by a
// session on one relay being delivered to a second relay that has no prior
// knowledge of the session. The foreign relay must verify the embedded
// SessionProof against the sender's long-lived identity key (resolved via
// DNS), accept the message, and cache the session for subsequent messages.
func TestSessionProofAcceptedByForeignRelay(t *testing.T) {
	peer, peerResolver := newTestServer(t)

	senderPub, senderPriv, _ := ed25519.GenerateKey(nil)
	senderTxt := "eurything-pubkey=ed25519:" + base64.RawURLEncoding.EncodeToString(senderPub)
	peerResolver.txt["_eurything.alice.example.com"] = []string{senderTxt}

	recipientPub, _, _ := ed25519.GenerateKey(nil)
	peer.identities.Add(storage.Identity{
		Identity:       "bob.example.org",
		PublicKey:      base64.RawURLEncoding.EncodeToString(recipientPub),
		PublicKeyBytes: recipientPub,
		CreatedAt:      time.Now().UTC(),
	})
	peerResolver.hosts["bob.example.org"] = []string{"relay.test"}

	ts := httptest.NewServer(peer.Router())
	defer ts.Close()

	sessionPub, sessionPriv, _ := ed25519.GenerateKey(nil)
	sessionPubB64 := base64.RawURLEncoding.EncodeToString(sessionPub)
	issuedAt := time.Now().UTC().Format(time.RFC3339)
	expiresAt := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	nonce := "n-proof"

	regCanonical := crypto.CanonicalSessionRegistration(
		"alice.example.com", sessionPubB64, issuedAt, expiresAt, nonce,
	)
	identitySig := base64.StdEncoding.EncodeToString(ed25519.Sign(senderPriv, []byte(regCanonical)))

	sessionID := "sess_" + base64.RawURLEncoding.EncodeToString([]byte("foreign"))

	msg := Message{
		Sender:    "alice.example.com",
		Recipient: "bob.example.org",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Payload:   "hello-cross-relay",
		SessionID: sessionID,
		SessionProof: &SessionProof{
			SessionPublicKey:  sessionPubB64,
			IssuedAt:          issuedAt,
			ExpiresAt:         expiresAt,
			Nonce:             nonce,
			IdentitySignature: identitySig,
		},
		Encryption: &EncryptionMeta{
			Alg:                "x25519-chacha20-poly1305",
			EphemeralPublicKey: "ephemeral-pub",
			Nonce:              "nonce",
		},
	}
	canonicalMsg := crypto.CanonicalMessageFull(msg.Sender, msg.Recipient, msg.Timestamp, msg.Payload, msg.SessionID, &crypto.EncryptionMeta{
		Alg:                msg.Encryption.Alg,
		EphemeralPublicKey: msg.Encryption.EphemeralPublicKey,
		Nonce:              msg.Encryption.Nonce,
	})
	msg.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(sessionPriv, []byte(canonicalMsg)))

	body, _ := json.Marshal(msg)
	resp, err := http.Post(ts.URL+"/messages", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post message: %v", err)
	}
	if resp.StatusCode != http.StatusAccepted {
		b, _ := readBody(resp)
		t.Fatalf("expected 202, got %d: %s", resp.StatusCode, b)
	}
	if _, ok := peer.sessions.Get(sessionID); !ok {
		t.Fatal("expected session to be cached after verifying proof")
	}
}

func readBody(resp *http.Response) (string, error) {
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	_, err := buf.ReadFrom(resp.Body)
	return buf.String(), err
}
