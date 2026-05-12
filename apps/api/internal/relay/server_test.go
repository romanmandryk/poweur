package relay

import (
	"bytes"
	"context"
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

type fakeResolver struct {
	txt   map[string][]string
	hosts map[string][]string
}

func (r *fakeResolver) LookupTXT(ctx context.Context, name string) ([]string, error) {
	if value, ok := r.txt[name]; ok {
		return value, nil
	}
	return nil, context.DeadlineExceeded
}

func (r *fakeResolver) LookupHost(ctx context.Context, name string) ([]string, error) {
	if value, ok := r.hosts[name]; ok {
		return value, nil
	}
	return nil, context.DeadlineExceeded
}

func TestMessageInboxFlow(t *testing.T) {
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
	recipientPub, recipientPriv, _ := ed25519.GenerateKey(nil)

	senderTxt := "poweur-pubkey=ed25519:" + base64.RawURLEncoding.EncodeToString(senderPub)
	resolver := &fakeResolver{
		txt: map[string][]string{
			"_poweur.alice.poweur.net": {senderTxt},
		},
		hosts: map[string][]string{
			"bob.example.org": {"relay.test"},
		},
	}

	providers := dns.NewProviderFactory(cfg)
	server := NewServer(cfg, resolver, providers)
	server.identities.Add(storage.Identity{
		Identity:       "bob.example.org",
		PublicKey:      base64.RawURLEncoding.EncodeToString(recipientPub),
		PublicKeyBytes: recipientPub,
		CreatedAt:      time.Now().UTC(),
	})

	ts := httptest.NewServer(server.Router())
	defer ts.Close()

	// The relay now enforces encrypt-only, so every message must carry
	// encryption metadata. The payload itself is opaque from the relay's
	// perspective — the values below are placeholders that only need to
	// round-trip through the canonical signing input.
	msg := Message{
		ID:        "msg_test_basic_001",
		Sender:    "alice.poweur.net",
		Recipient: "bob.example.org",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Payload:   "Hello Bob",
		Encryption: &EncryptionMeta{
			Alg:                "x25519-chacha20-poly1305",
			EphemeralPublicKey: "ephemeral-pub",
			Nonce:              "nonce",
		},
	}
	canonical := crypto.CanonicalMessageFull(msg.Sender, msg.Recipient, msg.Timestamp, msg.Payload, msg.ID, msg.SessionID, &crypto.EncryptionMeta{
		Alg:                msg.Encryption.Alg,
		EphemeralPublicKey: msg.Encryption.EphemeralPublicKey,
		Nonce:              msg.Encryption.Nonce,
	})
	signature := ed25519.Sign(senderPriv, []byte(canonical))
	msg.Signature = base64.StdEncoding.EncodeToString(signature)

	body, _ := json.Marshal(msg)
	resp, err := http.Post(ts.URL+"/messages", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post message: %v", err)
	}
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", resp.StatusCode)
	}

	challengeResp, err := http.Get(ts.URL + "/auth/challenge?identity=bob.example.org")
	if err != nil {
		t.Fatalf("challenge: %v", err)
	}
	var challenge ChallengeResponse
	if err := json.NewDecoder(challengeResp.Body).Decode(&challenge); err != nil {
		t.Fatalf("decode challenge: %v", err)
	}

	challengeSig := ed25519.Sign(recipientPriv, []byte(challenge.Challenge))
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/messages/bob.example.org", nil)
	req.Header.Set("X-Poweur-Identity", "bob.example.org")
	req.Header.Set("X-Poweur-Signature", base64.StdEncoding.EncodeToString(challengeSig))
	inboxResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("inbox: %v", err)
	}
	if inboxResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", inboxResp.StatusCode)
	}

	var inbox struct {
		Messages []storage.StoredMessage `json:"messages"`
	}
	if err := json.NewDecoder(inboxResp.Body).Decode(&inbox); err != nil {
		t.Fatalf("decode inbox: %v", err)
	}
	if len(inbox.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(inbox.Messages))
	}
	if inbox.Messages[0].Payload != "Hello Bob" {
		t.Fatalf("unexpected payload: %s", inbox.Messages[0].Payload)
	}
}

// TestInboxDNSFallback verifies that GET /messages/{identity} works when the
// identity is absent from the in-memory store (simulating a server restart)
// but its pubkey is discoverable via DNS TXT and its hostname resolves to this
// relay's address. This is the "DNS as source of truth, store as cache" model.
func TestInboxDNSFallback(t *testing.T) {
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
	recipientPub, recipientPriv, _ := ed25519.GenerateKey(nil)

	resolver := &fakeResolver{
		txt: map[string][]string{
			"_poweur.alice.poweur.net": {"poweur-pubkey=ed25519:" + base64.RawURLEncoding.EncodeToString(senderPub)},
			"_poweur.bob.example.org":  {"poweur-pubkey=ed25519:" + base64.RawURLEncoding.EncodeToString(recipientPub)},
		},
		hosts: map[string][]string{
			"alice.poweur.net": {"relay.test"},
			"bob.example.org":  {"relay.test"},
		},
	}

	// bob.example.org intentionally absent from server.identities — cold store.
	server := NewServer(cfg, resolver, dns.NewProviderFactory(cfg))
	ts := httptest.NewServer(server.Router())
	defer ts.Close()

	msg := Message{
		ID:        "msg_dns_fallback_001",
		Sender:    "alice.poweur.net",
		Recipient: "bob.example.org",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Payload:   "DNS fallback payload",
		Encryption: &EncryptionMeta{
			Alg:                "x25519-chacha20-poly1305",
			EphemeralPublicKey: "ephemeral-pub",
			Nonce:              "nonce",
		},
	}
	canonical := crypto.CanonicalMessageFull(msg.Sender, msg.Recipient, msg.Timestamp, msg.Payload, msg.ID, "", &crypto.EncryptionMeta{
		Alg: msg.Encryption.Alg, EphemeralPublicKey: msg.Encryption.EphemeralPublicKey, Nonce: msg.Encryption.Nonce,
	})
	msg.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(senderPriv, []byte(canonical)))

	body, _ := json.Marshal(msg)
	if resp, err := http.Post(ts.URL+"/messages", "application/json", bytes.NewReader(body)); err != nil || resp.StatusCode != http.StatusAccepted {
		t.Fatalf("post message: err=%v status=%v", err, resp.StatusCode)
	}

	challengeResp, err := http.Get(ts.URL + "/auth/challenge?identity=bob.example.org")
	if err != nil {
		t.Fatalf("challenge: %v", err)
	}
	var challenge ChallengeResponse
	if err := json.NewDecoder(challengeResp.Body).Decode(&challenge); err != nil {
		t.Fatalf("decode challenge: %v", err)
	}

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/messages/bob.example.org", nil)
	req.Header.Set("X-Poweur-Identity", "bob.example.org")
	req.Header.Set("X-Poweur-Signature", base64.StdEncoding.EncodeToString(ed25519.Sign(recipientPriv, []byte(challenge.Challenge))))
	inboxResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("inbox: %v", err)
	}
	if inboxResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", inboxResp.StatusCode)
	}
	var inbox struct {
		Messages []storage.StoredMessage `json:"messages"`
	}
	if err := json.NewDecoder(inboxResp.Body).Decode(&inbox); err != nil {
		t.Fatalf("decode inbox: %v", err)
	}
	if len(inbox.Messages) != 1 || inbox.Messages[0].Payload != "DNS fallback payload" {
		t.Fatalf("unexpected inbox: %+v", inbox.Messages)
	}

	// Second drain: store should now be warm (no DNS needed).
	challengeResp2, _ := http.Get(ts.URL + "/auth/challenge?identity=bob.example.org")
	var challenge2 ChallengeResponse
	json.NewDecoder(challengeResp2.Body).Decode(&challenge2)
	req2, _ := http.NewRequest(http.MethodGet, ts.URL+"/messages/bob.example.org", nil)
	req2.Header.Set("X-Poweur-Identity", "bob.example.org")
	req2.Header.Set("X-Poweur-Signature", base64.StdEncoding.EncodeToString(ed25519.Sign(recipientPriv, []byte(challenge2.Challenge))))
	if resp2, err := http.DefaultClient.Do(req2); err != nil || resp2.StatusCode != http.StatusOK {
		t.Fatalf("second inbox (warm cache): err=%v status=%v", err, resp2.StatusCode)
	}
}

func TestRateLimit(t *testing.T) {
	cfg := config.Config{
		ListenAddr:   ":0",
		RelayAddress: "relay.test",
		RelayScheme:  "http",
		DNSTTL:       time.Minute,
		ChallengeTTL: time.Minute,
		Version:      "test",
		RateLimits: config.RateLimits{
			PerMinute: 1,
			PerHour:   10,
			PerDay:    100,
		},
	}
	senderPub, senderPriv, _ := ed25519.GenerateKey(nil)
	recipientPub, _, _ := ed25519.GenerateKey(nil)

	senderTxt := "poweur-pubkey=ed25519:" + base64.RawURLEncoding.EncodeToString(senderPub)
	resolver := &fakeResolver{
		txt: map[string][]string{
			"_poweur.alice.poweur.net": {senderTxt},
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

	msg := Message{
		ID:        "msg_test_ratelimit_001",
		Sender:    "alice.poweur.net",
		Recipient: "bob.example.org",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Payload:   "Hello Bob",
		Encryption: &EncryptionMeta{
			Alg:                "x25519-chacha20-poly1305",
			EphemeralPublicKey: "ephemeral-pub",
			Nonce:              "nonce",
		},
	}
	canonical := crypto.CanonicalMessageFull(msg.Sender, msg.Recipient, msg.Timestamp, msg.Payload, msg.ID, msg.SessionID, &crypto.EncryptionMeta{
		Alg:                msg.Encryption.Alg,
		EphemeralPublicKey: msg.Encryption.EphemeralPublicKey,
		Nonce:              msg.Encryption.Nonce,
	})
	signature := ed25519.Sign(senderPriv, []byte(canonical))
	msg.Signature = base64.StdEncoding.EncodeToString(signature)

	body, _ := json.Marshal(msg)
	first, _ := http.Post(ts.URL+"/messages", "application/json", bytes.NewReader(body))
	if first.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", first.StatusCode)
	}
	second, _ := http.Post(ts.URL+"/messages", "application/json", bytes.NewReader(body))
	if second.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", second.StatusCode)
	}
}

// TestIdentitySignedMessageAccepted covers the opt-out path: a message
// posted with no session_id, signed directly with the sender's long-lived
// identity Ed25519 key. The relay must resolve the verifying key via DNS
// (TXT record) and accept the envelope. This mirrors what the CLI produces
// when the operator runs `poweur send --sign-with=identity ...`.
func TestIdentitySignedMessageAccepted(t *testing.T) {
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

	senderTxt := "poweur-pubkey=ed25519:" + base64.RawURLEncoding.EncodeToString(senderPub)
	resolver := &fakeResolver{
		txt: map[string][]string{
			"_poweur.alice.poweur.net": {senderTxt},
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

	// Build an identity-signed message: empty session_id, canonical string
	// omits the "session:" line entirely, signed with the long-lived key.
	msg := Message{
		ID:        "msg_test_idsigned_001",
		Sender:    "alice.poweur.net",
		Recipient: "bob.example.org",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Payload:   "identity-signed ciphertext",
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
	msg.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(senderPriv, []byte(canonical)))

	body, _ := json.Marshal(msg)
	resp, err := http.Post(ts.URL+"/messages", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 for identity-signed message, got %d", resp.StatusCode)
	}

	// Now forge: sign a different canonical string and expect 401.
	_, forgedPriv, _ := ed25519.GenerateKey(nil)
	forged := msg
	forged.ID = "msg_test_idsigned_forged_001"
	forged.Timestamp = time.Now().UTC().Add(time.Second).Format(time.RFC3339)
	forged.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(forgedPriv, []byte("not-the-canonical-message")))
	body, _ = json.Marshal(forged)
	resp2, err := http.Post(ts.URL+"/messages", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post forged: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for forged identity-signed message, got %d", resp2.StatusCode)
	}
}

// TestPlaintextMessageRejected verifies the relay's encrypt-only policy: any
// POST /messages that lacks encryption metadata is rejected at the edge
// before we spend cycles on signature verification.
func TestPlaintextMessageRejected(t *testing.T) {
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
	server := NewServer(cfg, &fakeResolver{txt: map[string][]string{}, hosts: map[string][]string{}}, dns.NewProviderFactory(cfg))
	ts := httptest.NewServer(server.Router())
	defer ts.Close()

	msg := Message{
		ID:        "msg_test_plaintext_001",
		Sender:    "alice.poweur.net",
		Recipient: "bob.example.org",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Payload:   "plain text",
		Signature: "not-verified-before-encryption-check",
	}
	body, _ := json.Marshal(msg)
	resp, err := http.Post(ts.URL+"/messages", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	var payload ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Error != "encryption_required" {
		t.Fatalf("expected encryption_required, got %s / %s", payload.Error, payload.Detail)
	}
}
