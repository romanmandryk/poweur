package relay

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/poweur/api/internal/config"
	"github.com/poweur/api/internal/crypto"
	"github.com/poweur/api/internal/dns"
	"github.com/poweur/api/internal/storage"
)

func TestGETRoot(t *testing.T) {
	s, _ := newTestServer(t)
	ts := httptest.NewServer(s.Router())
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestGETIdentitiesGet(t *testing.T) {
	s, _ := newTestServer(t)
	pub, _, _ := ed25519.GenerateKey(nil)
	s.identities.Add(storage.Identity{
		Identity:       "known.poweur.net",
		PublicKey:      base64.RawURLEncoding.EncodeToString(pub),
		PublicKeyBytes: pub,
		CreatedAt:      time.Now().UTC(),
	})
	ts := httptest.NewServer(s.Router())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/identities/known.poweur.net")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got %d", resp.StatusCode)
	}
	resp2, _ := http.Get(ts.URL + "/identities/missing.poweur.net")
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("missing: %d", resp2.StatusCode)
	}
}

// TestForwardMessageCrossRelay exercises sender-local + recipient-remote:
// home relay A forwards POST /messages to peer relay B.
func TestForwardMessageCrossRelay(t *testing.T) {
	davePub, davePriv, _ := ed25519.GenerateKey(nil)
	bobPub, _, _ := ed25519.GenerateKey(nil)
	daveTxt := "poweur-pubkey=ed25519:" + base64.RawURLEncoding.EncodeToString(davePub)

	cfgB := config.Config{
		ListenAddr:   ":0",
		RelayAddress: "relay-b.test",
		RelayScheme:  "http",
		DNSTTL:       time.Minute,
		ChallengeTTL: time.Minute,
		Version:      "test",
		RateLimits:   config.RateLimits{PerMinute: 1000, PerHour: 10000, PerDay: 100000},
	}
	resolverB := &fakeResolver{
		txt: map[string][]string{
			"_poweur.dave.poweur.net": {daveTxt},
		},
		hosts: map[string][]string{},
	}
	serverB := NewServer(cfgB, resolverB, dns.NewProviderFactory(cfgB))
	serverB.identities.Add(storage.Identity{
		Identity:       "bob.poweur.net",
		PublicKey:      base64.RawURLEncoding.EncodeToString(bobPub),
		PublicKeyBytes: bobPub,
		CreatedAt:      time.Now().UTC(),
	})
	tsB := httptest.NewServer(serverB.Router())
	defer tsB.Close()
	uB, err := url.Parse(tsB.URL)
	if err != nil {
		t.Fatal(err)
	}
	peerHost := uB.Host

	cfgA := config.Config{
		ListenAddr:   ":0",
		RelayAddress: "relay-a.test",
		RelayScheme:  "http",
		DNSTTL:       time.Minute,
		ChallengeTTL: time.Minute,
		Version:      "test",
		RateLimits:   config.RateLimits{PerMinute: 1000, PerHour: 10000, PerDay: 100000},
	}
	resolverA := &fakeResolver{
		txt: map[string][]string{},
		hosts: map[string][]string{
			"bob.poweur.net": {peerHost},
		},
	}
	serverA := NewServer(cfgA, resolverA, dns.NewProviderFactory(cfgA))
	serverA.identities.Add(storage.Identity{
		Identity:       "dave.poweur.net",
		PublicKey:      base64.RawURLEncoding.EncodeToString(davePub),
		PublicKeyBytes: davePub,
		CreatedAt:      time.Now().UTC(),
	})
	tsA := httptest.NewServer(serverA.Router())
	defer tsA.Close()

	msg := Message{
		ID:        "msg_fwd_cross_1",
		Sender:    "dave.poweur.net",
		Recipient: "bob.poweur.net",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Payload:   "cipher",
		Encryption: &EncryptionMeta{
			Alg:                "x25519-chacha20-poly1305",
			EphemeralPublicKey: "e",
			Nonce:              "n",
		},
	}
	canonical := crypto.CanonicalMessageFull(msg.Sender, msg.Recipient, msg.Timestamp, msg.Payload, msg.ID, msg.SessionID, &crypto.EncryptionMeta{
		Alg:                msg.Encryption.Alg,
		EphemeralPublicKey: msg.Encryption.EphemeralPublicKey,
		Nonce:              msg.Encryption.Nonce,
	})
	msg.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(davePriv, []byte(canonical)))
	body, _ := json.Marshal(msg)
	resp, err := http.Post(tsA.URL+"/messages", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 from forward path, got %d", resp.StatusCode)
	}
}

// TestForwardAckCrossRelay: ack sender is local on B, original message sender
// (recipient of ack) is on A; B forwards the ack to A.
func TestForwardAckCrossRelay(t *testing.T) {
	carolPub, _, _ := ed25519.GenerateKey(nil)
	bobPub, bobPriv, _ := ed25519.GenerateKey(nil)
	bobTxt := "poweur-pubkey=ed25519:" + base64.RawURLEncoding.EncodeToString(bobPub)

	cfgA := config.Config{
		ListenAddr:   ":0",
		RelayAddress: "relay-a.test",
		RelayScheme:  "http",
		DNSTTL:       time.Minute,
		ChallengeTTL: time.Minute,
		Version:      "test",
		RateLimits:   config.RateLimits{PerMinute: 1000, PerHour: 10000, PerDay: 100000},
	}
	resolverA := &fakeResolver{
		txt: map[string][]string{
			// So A can verify Bob's signature when the forwarded ack lands.
			"_poweur.bob.poweur.net": {bobTxt},
		},
		hosts: map[string][]string{},
	}
	serverA := NewServer(cfgA, resolverA, dns.NewProviderFactory(cfgA))
	serverA.identities.Add(storage.Identity{
		Identity:       "carol.poweur.net",
		PublicKey:      base64.RawURLEncoding.EncodeToString(carolPub),
		PublicKeyBytes: carolPub,
		CreatedAt:      time.Now().UTC(),
	})
	tsA := httptest.NewServer(serverA.Router())
	defer tsA.Close()
	uA, _ := url.Parse(tsA.URL)

	cfgB := config.Config{
		ListenAddr:   ":0",
		RelayAddress: "relay-b.test",
		RelayScheme:  "http",
		DNSTTL:       time.Minute,
		ChallengeTTL: time.Minute,
		Version:      "test",
		RateLimits:   config.RateLimits{PerMinute: 1000, PerHour: 10000, PerDay: 100000},
	}
	resolverB := &fakeResolver{
		txt: map[string][]string{
			"_poweur.bob.poweur.net": {bobTxt},
		},
		hosts: map[string][]string{
			"carol.poweur.net": {uA.Host},
		},
	}
	serverB := NewServer(cfgB, resolverB, dns.NewProviderFactory(cfgB))
	serverB.identities.Add(storage.Identity{
		Identity:       "bob.poweur.net",
		PublicKey:      base64.RawURLEncoding.EncodeToString(bobPub),
		PublicKeyBytes: bobPub,
		CreatedAt:      time.Now().UTC(),
	})
	tsB := httptest.NewServer(serverB.Router())
	defer tsB.Close()

	ack := Ack{
		Type:      AckTypeDeliveryAck,
		ID:        "ack_fwd_1",
		MessageID: "msg_x",
		State:     AckStateDeliveredClient,
		Sender:    "bob.poweur.net",
		Recipient: "carol.poweur.net",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	canonical := crypto.CanonicalAck(ack.ID, ack.MessageID, ack.State, ack.Sender, ack.Recipient, ack.Timestamp, ack.SessionID)
	ack.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(bobPriv, []byte(canonical)))
	body, _ := json.Marshal(ack)
	resp, err := http.Post(tsB.URL+"/acks", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", resp.StatusCode)
	}
}

func TestNeitherLocalReturns403(t *testing.T) {
	cfg := config.Config{
		ListenAddr:   ":0",
		RelayAddress: "relay.test",
		RelayScheme:  "http",
		DNSTTL:       time.Minute,
		ChallengeTTL: time.Minute,
		Version:      "test",
		RateLimits:   config.RateLimits{PerMinute: 100, PerHour: 1000, PerDay: 10000},
	}
	senderPub, senderPriv, _ := ed25519.GenerateKey(nil)
	senderTxt := "poweur-pubkey=ed25519:" + base64.RawURLEncoding.EncodeToString(senderPub)
	res := &fakeResolver{
		txt: map[string][]string{
			"_poweur.alice.poweur.net": {senderTxt},
		},
		hosts: map[string][]string{
			"alice.poweur.net": {"192.0.2.1"},
			"bob.poweur.net":   {"192.0.2.1"},
		},
	}
	s := NewServer(cfg, res, dns.NewProviderFactory(cfg))
	ts := httptest.NewServer(s.Router())
	defer ts.Close()
	msg := Message{
		ID:        "msg_403_1",
		Sender:    "alice.poweur.net",
		Recipient: "bob.poweur.net",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Payload:   "x",
		Encryption: &EncryptionMeta{
			Alg: "x25519-chacha20-poly1305", EphemeralPublicKey: "e", Nonce: "n",
		},
	}
	can := crypto.CanonicalMessageFull(msg.Sender, msg.Recipient, msg.Timestamp, msg.Payload, msg.ID, msg.SessionID, &crypto.EncryptionMeta{
		Alg: msg.Encryption.Alg, EphemeralPublicKey: msg.Encryption.EphemeralPublicKey, Nonce: msg.Encryption.Nonce,
	})
	msg.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(senderPriv, []byte(can)))
	b, _ := json.Marshal(msg)
	r, _ := http.Post(ts.URL+"/messages", "application/json", bytes.NewReader(b))
	defer r.Body.Close()
	if r.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", r.StatusCode)
	}
}

// TestIsLocalIdentitySharedIP: identity and relay address resolve to the same
// IP so the recipient is treated as local without being in the identity store.
func TestMessageAcceptedWhenRecipientLocalViaSharedIP(t *testing.T) {
	senderPub, senderPriv, _ := ed25519.GenerateKey(nil)
	senderTxt := "poweur-pubkey=ed25519:" + base64.RawURLEncoding.EncodeToString(senderPub)
	shared := "10.11.12.13"
	res := &fakeResolver{
		txt: map[string][]string{
			"_poweur.from.poweur.net": {senderTxt},
		},
		hosts: map[string][]string{
			"bob.edge.poweur.net": {shared},
			"relay.edge":          {shared},
		},
	}
	cfg := config.Config{
		ListenAddr:   ":0",
		RelayAddress: "relay.edge",
		RelayScheme:  "http",
		DNSTTL:       time.Minute,
		ChallengeTTL: time.Minute,
		Version:      "test",
		RateLimits:   config.RateLimits{PerMinute: 100, PerHour: 1000, PerDay: 10000},
	}
	s := NewServer(cfg, res, dns.NewProviderFactory(cfg))
	// No bob in identity store — local only via IP overlap
	ts := httptest.NewServer(s.Router())
	defer ts.Close()
	msg := Message{
		ID:        "msg_ipoverlap_1",
		Sender:    "from.poweur.net",
		Recipient: "bob.edge.poweur.net",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Payload:   "c",
		Encryption: &EncryptionMeta{
			Alg: "x25519-chacha20-poly1305", EphemeralPublicKey: "e", Nonce: "n",
		},
	}
	can := crypto.CanonicalMessageFull(msg.Sender, msg.Recipient, msg.Timestamp, msg.Payload, msg.ID, msg.SessionID, &crypto.EncryptionMeta{
		Alg: msg.Encryption.Alg, EphemeralPublicKey: msg.Encryption.EphemeralPublicKey, Nonce: msg.Encryption.Nonce,
	})
	msg.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(senderPriv, []byte(can)))
	b, _ := json.Marshal(msg)
	r, _ := http.Post(ts.URL+"/messages", "application/json", bytes.NewReader(b))
	defer r.Body.Close()
	if r.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", r.StatusCode)
	}
}

func TestGlobalRateLimitReturns429(t *testing.T) {
	senderPub, senderPriv, _ := ed25519.GenerateKey(nil)
	recipientPub, _, _ := ed25519.GenerateKey(nil)
	senderTxt := "poweur-pubkey=ed25519:" + base64.RawURLEncoding.EncodeToString(senderPub)
	res := &fakeResolver{
		txt: map[string][]string{
			"_poweur.alice.poweur.net": {senderTxt},
		},
		hosts: map[string][]string{
			"bob.poweur.net": {"relay.test"},
		},
	}
	cfg := config.Config{
		ListenAddr:   ":0",
		RelayAddress: "relay.test",
		RelayScheme:  "http",
		DNSTTL:       time.Minute,
		ChallengeTTL: time.Minute,
		Version:      "test",
		RateLimits:   config.RateLimits{PerMinute: 100, PerHour: 1000, PerDay: 10000},
		GlobalRateLimits: config.GlobalRateLimits{
			PerMinute: 2,
			PerHour:   0,
			PerDay:    0,
		},
	}
	s := NewServer(cfg, res, dns.NewProviderFactory(cfg))
	s.identities.Add(storage.Identity{
		Identity:       "bob.poweur.net",
		PublicKey:      base64.RawURLEncoding.EncodeToString(recipientPub),
		PublicKeyBytes: recipientPub,
		CreatedAt:      time.Now().UTC(),
	})
	ts := httptest.NewServer(s.Router())
	defer ts.Close()
	charliePub, charliePriv, _ := ed25519.GenerateKey(nil)
	s.identities.Add(storage.Identity{
		Identity:       "charlie.poweur.net",
		PublicKey:      base64.RawURLEncoding.EncodeToString(charliePub),
		PublicKeyBytes: charliePub,
		CreatedAt:      time.Now().UTC(),
	})
	cTxt := "poweur-pubkey=ed25519:" + base64.RawURLEncoding.EncodeToString(charliePub)
	res.txt["_poweur.charlie.poweur.net"] = []string{cTxt}

	post := func(t *testing.T, id, sender string, signPriv ed25519.PrivateKey) *http.Response {
		t.Helper()
		msg := Message{
			ID: id, Sender: sender, Recipient: "bob.poweur.net",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Payload:   "p",
			Encryption: &EncryptionMeta{
				Alg: "x25519-chacha20-poly1305", EphemeralPublicKey: "e", Nonce: "n",
			},
		}
		can := crypto.CanonicalMessageFull(msg.Sender, msg.Recipient, msg.Timestamp, msg.Payload, msg.ID, msg.SessionID, &crypto.EncryptionMeta{
			Alg: msg.Encryption.Alg, EphemeralPublicKey: msg.Encryption.EphemeralPublicKey, Nonce: msg.Encryption.Nonce,
		})
		msg.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(signPriv, []byte(can)))
		b, _ := json.Marshal(msg)
		r, e := http.Post(ts.URL+"/messages", "application/json", bytes.NewReader(b))
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	// first two (any sender) consume global=2; third hits global, not per-sender
	if post(t, "m1", "alice.poweur.net", senderPriv).StatusCode != http.StatusAccepted {
		t.Fatal("1")
	}
	if post(t, "m2", "alice.poweur.net", senderPriv).StatusCode != http.StatusAccepted {
		t.Fatal("2")
	}
	r3 := post(t, "m3", "charlie.poweur.net", charliePriv)
	defer r3.Body.Close()
	if r3.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", r3.StatusCode)
	}
}
