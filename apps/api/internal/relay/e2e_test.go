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

// TestRelayTreatsEncryptedPayloadAsOpaque ensures the relay only verifies the
// signature over the ciphertext and routes the message without inspecting or
// decrypting the payload. The relay never sees plaintext under E2E encryption.
func TestRelayTreatsEncryptedPayloadAsOpaque(t *testing.T) {
	cfg := config.Config{
		ListenAddr:   ":0",
		RelayAddress: "relay.test",
		RelayScheme:  "http",
		DNSTTL:       time.Minute,
		ChallengeTTL: time.Minute,
		Version:      "test",
		RateLimits: config.RateLimits{
			PerMinute: 100,
			PerHour:   1000,
			PerDay:    10000,
		},
	}

	senderPub, senderPriv, _ := ed25519.GenerateKey(nil)
	recipientPub, _, _ := ed25519.GenerateKey(nil)

	resolver := &fakeResolver{
		txt: map[string][]string{
			"_eurything.alice.example.com": {"eurything-pubkey=ed25519:" + base64.RawURLEncoding.EncodeToString(senderPub)},
		},
		hosts: map[string][]string{
			"bob.example.org": {"relay.test"},
		},
	}
	server := NewServer(cfg, resolver, dns.NewProviderFactory(cfg))
	server.identities.Add(storage.Identity{
		Identity:       "bob.example.org",
		PublicKey:      base64.RawURLEncoding.EncodeToString(recipientPub),
		PublicKeyBytes: recipientPub,
		CreatedAt:      time.Now().UTC(),
	})

	ts := httptest.NewServer(server.Router())
	defer ts.Close()

	// Register a session for Alice.
	sessionPub, sessionPriv, _ := ed25519.GenerateKey(nil)
	sessionPubB64 := base64.RawURLEncoding.EncodeToString(sessionPub)
	issuedAt := time.Now().UTC().Format(time.RFC3339)
	expiresAt := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	nonce := "nonce-e2e"
	canonical := crypto.CanonicalSessionRegistration(
		"alice.example.com", sessionPubB64, issuedAt, expiresAt, nonce,
	)
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(senderPriv, []byte(canonical)))

	body, _ := json.Marshal(SessionCreateRequest{
		Identity:          "alice.example.com",
		SessionPublicKey:  sessionPubB64,
		IssuedAt:          issuedAt,
		ExpiresAt:         expiresAt,
		Nonce:             nonce,
		IdentitySignature: sig,
	})
	resp, err := http.Post(ts.URL+"/sessions", "application/json", bytes.NewReader(body))
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("session register failed: %v %v", err, resp.StatusCode)
	}
	var sessionResp SessionResponse
	_ = json.NewDecoder(resp.Body).Decode(&sessionResp)
	resp.Body.Close()

	// Build an "encrypted" message (payload is opaque from the relay's perspective).
	ciphertext := base64.RawURLEncoding.EncodeToString([]byte("opaque-ciphertext"))
	msg := Message{
		ID:        "msg_test_e2e_001",
		Sender:    "alice.example.com",
		Recipient: "bob.example.org",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Payload:   ciphertext,
		SessionID: sessionResp.SessionID,
		Encryption: &EncryptionMeta{
			Alg:                "x25519-chacha20-poly1305",
			EphemeralPublicKey: "ephemeral-pub",
			Nonce:              "nonce",
		},
	}
	enc := &crypto.EncryptionMeta{
		Alg:                msg.Encryption.Alg,
		EphemeralPublicKey: msg.Encryption.EphemeralPublicKey,
		Nonce:              msg.Encryption.Nonce,
	}
	canon := crypto.CanonicalMessageFull(msg.Sender, msg.Recipient, msg.Timestamp, msg.Payload, msg.ID, msg.SessionID, enc)
	msg.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(sessionPriv, []byte(canon)))

	body, _ = json.Marshal(msg)
	resp, err = http.Post(ts.URL+"/messages", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post message: %v", err)
	}
	if resp.StatusCode != http.StatusAccepted {
		b, _ := readBody(resp)
		t.Fatalf("expected 202, got %d: %s", resp.StatusCode, b)
	}

	// Inbox should contain the opaque ciphertext unchanged.
	messages := server.inbox.Drain("bob.example.org")
	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messages))
	}
	if messages[0].Payload != ciphertext {
		t.Fatalf("relay mutated ciphertext: got %s want %s", messages[0].Payload, ciphertext)
	}
}
