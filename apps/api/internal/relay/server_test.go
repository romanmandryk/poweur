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

	"github.com/eurything/api/internal/config"
	"github.com/eurything/api/internal/crypto"
	"github.com/eurything/api/internal/dns"
	"github.com/eurything/api/internal/storage"
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

	senderTxt := "eurything-pubkey=ed25519:" + base64.RawURLEncoding.EncodeToString(senderPub)
	resolver := &fakeResolver{
		txt: map[string][]string{
			"_eurything.alice.example.com": {senderTxt},
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

	msg := Message{
		Sender:    "alice.example.com",
		Recipient: "bob.example.org",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Payload:   "Hello Bob",
	}
	canonical := crypto.CanonicalMessage(msg.Sender, msg.Recipient, msg.Timestamp, msg.Payload)
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
	req.Header.Set("X-Eurything-Identity", "bob.example.org")
	req.Header.Set("X-Eurything-Signature", base64.StdEncoding.EncodeToString(challengeSig))
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

	senderTxt := "eurything-pubkey=ed25519:" + base64.RawURLEncoding.EncodeToString(senderPub)
	resolver := &fakeResolver{
		txt: map[string][]string{
			"_eurything.alice.example.com": {senderTxt},
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
		Sender:    "alice.example.com",
		Recipient: "bob.example.org",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Payload:   "Hello Bob",
	}
	canonical := crypto.CanonicalMessage(msg.Sender, msg.Recipient, msg.Timestamp, msg.Payload)
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
