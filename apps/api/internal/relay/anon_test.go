package relay

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/poweur/api/internal/config"
	"github.com/poweur/api/internal/crypto"
	"github.com/poweur/api/internal/dns"
	idpkg "github.com/poweur/identity"
)

// EPIC-014 E14-T3: anonymous ingress scenario tests — default deny,
// challenge dance, caps, queue isolation.

var anonMsgSeq int

func postAnon(t *testing.T, ts *httptest.Server, recipient, payload, token, solution string) *http.Response {
	t.Helper()
	anonMsgSeq++
	msg := map[string]any{
		"id":        "msg_anon_" + time.Now().Format("150405.000000000") + string(rune('a'+anonMsgSeq%26)),
		"recipient": recipient,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"payload":   payload,
		"encryption": map[string]string{
			"alg": "x25519-chacha20-poly1305", "ephemeral_public_key": "eph", "nonce": "n",
		},
	}
	if token != "" {
		msg["challenge_token"] = token
		msg["challenge_solution"] = solution
	}
	body, _ := json.Marshal(msg)
	resp, err := http.Post(ts.URL+"/messages", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

type challengeEnvelope struct {
	Error     string `json:"error"`
	Challenge struct {
		Type      string `json:"type"`
		Algo      string `json:"algo"`
		Token     string `json:"token"`
		Bits      int    `json:"bits"`
		ExpiresAt string `json:"expires_at"`
		Detail    string `json:"detail"`
	} `json:"challenge"`
}

func decodeChallenge(t *testing.T, resp *http.Response) challengeEnvelope {
	t.Helper()
	defer resp.Body.Close()
	var out challengeEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAnonDefaultDeny(t *testing.T) {
	server, ts := newTestRelay(t)
	registerTestIdentity(t, server, ts, "alice.poweur.net")

	resp := postAnon(t, ts, "alice.poweur.net", "hi", "", "")
	mustStatus(t, resp, http.StatusForbidden, "anon under default policy")

	// Explicit policy without the anonymous block also denies.
	alice := registerTestIdentity(t, server, ts, "bob.poweur.net")
	setSysFile(t, server, alice.name, ".poweur/relay/inbox-policy.json", `{"version":1,"mode":"open"}`)
	resp = postAnon(t, ts, "bob.poweur.net", "hi", "", "")
	mustStatus(t, resp, http.StatusForbidden, "anon with policy but no anonymous block")
}

func TestAnonNoChallengeFlow(t *testing.T) {
	server, ts := newTestRelay(t)
	alice := registerTestIdentity(t, server, ts, "alice.poweur.net")
	setSysFile(t, server, alice.name, ".poweur/relay/inbox-policy.json",
		`{"version":1,"mode":"contacts_only","anonymous":{"allow":true,"challenge":"none"}}`)

	resp := postAnon(t, ts, "alice.poweur.net", "anon says hi", "", "")
	out := mustStatus(t, resp, http.StatusAccepted, "anon with challenge none")
	if out["status"] != "anon_accepted" {
		t.Fatalf("outcome: %v", out)
	}

	// The anon queue holds it; the signed inbox does not.
	inbox := challengeSigned(t, ts, alice, "/messages/"+alice.name)
	if msgs, _ := inbox["messages"].([]any); len(msgs) != 0 {
		t.Fatalf("anon message leaked into signed inbox: %v", msgs)
	}
	anon := challengeSigned(t, ts, alice, "/anon/"+alice.name)
	msgs, _ := anon["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("anon queue: %v", anon)
	}
	first, _ := msgs[0].(map[string]any)
	if first["sender"] != nil && first["sender"] != "" {
		t.Fatalf("anon message must have no sender: %v", first)
	}
	if first["payload"] != "anon says hi" {
		t.Fatalf("payload: %v", first)
	}
}

// An accepted anonymous message must wake a listening client, and wake it to
// the right queue. Told "message", a client fetches the inbox — which by
// design never holds an anonymous message — and the anon tray stays silent
// until something unrelated makes the client look again.
func TestAnonDeliveryNotifiesItsOwnQueue(t *testing.T) {
	server, ts := newTestRelay(t)
	alice := registerTestIdentity(t, server, ts, "alice.poweur.net")
	setSysFile(t, server, alice.name, ".poweur/relay/inbox-policy.json",
		`{"version":1,"mode":"contacts_only","anonymous":{"allow":true,"challenge":"none"}}`)

	id, events, ok := server.hub.subscribe(alice.name, 0)
	if !ok {
		t.Fatal("subscribe refused")
	}
	defer server.hub.unsubscribe(alice.name, id)

	resp := postAnon(t, ts, "alice.poweur.net", "anon says hi", "", "")
	mustStatus(t, resp, http.StatusAccepted, "anon with challenge none")

	select {
	case event := <-events:
		if event.Type != "anon" {
			t.Fatalf("event kind = %q, want anon", event.Type)
		}
		if event.Identity != alice.name || event.MessageID == "" {
			t.Fatalf("event: %+v", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("an accepted anonymous message published no event")
	}
}

func TestAnonPowChallengeFlow(t *testing.T) {
	server, ts := newTestRelay(t)
	alice := registerTestIdentity(t, server, ts, "alice.poweur.net")
	setSysFile(t, server, alice.name, ".poweur/relay/inbox-policy.json",
		`{"version":1,"mode":"contacts_only","anonymous":{"allow":true,"challenge":"pow","pow_bits":8}}`)

	// First post: 428 with a pow challenge envelope.
	resp := postAnon(t, ts, "alice.poweur.net", "knock knock", "", "")
	if resp.StatusCode != http.StatusPreconditionRequired {
		t.Fatalf("want 428, got %d", resp.StatusCode)
	}
	env := decodeChallenge(t, resp)
	if env.Challenge.Type != "pow" || env.Challenge.Algo != idpkg.PowAlgo || env.Challenge.Token == "" || env.Challenge.Bits != 8 {
		t.Fatalf("challenge envelope: %+v", env)
	}

	// Wrong solution fails.
	resp = postAnon(t, ts, "alice.poweur.net", "knock knock", env.Challenge.Token, "wrong")
	mustStatus(t, resp, http.StatusForbidden, "wrong solution")

	// Correct solution is accepted.
	solution, err := idpkg.SolvePow(context.Background(), env.Challenge.Token, env.Challenge.Bits)
	if err != nil {
		t.Fatal(err)
	}
	resp = postAnon(t, ts, "alice.poweur.net", "knock knock", env.Challenge.Token, solution)
	mustStatus(t, resp, http.StatusAccepted, "solved challenge")

	// Replaying the same solution is rejected (single-use).
	resp = postAnon(t, ts, "alice.poweur.net", "again", env.Challenge.Token, solution)
	out := mustStatus(t, resp, http.StatusForbidden, "replayed solution")
	if out["error"] != "challenge_replayed" {
		t.Fatalf("replay outcome: %v", out)
	}

	// A token minted for another recipient must not verify (purpose-bound).
	bob := registerTestIdentity(t, server, ts, "bob.poweur.net")
	setSysFile(t, server, bob.name, ".poweur/relay/inbox-policy.json",
		`{"version":1,"mode":"open","anonymous":{"allow":true,"challenge":"pow","pow_bits":8}}`)
	resp = postAnon(t, ts, "bob.poweur.net", "cross", "", "")
	bobEnv := decodeChallenge(t, resp)
	bobSolution, err := idpkg.SolvePow(context.Background(), bobEnv.Challenge.Token, bobEnv.Challenge.Bits)
	if err != nil {
		t.Fatal(err)
	}
	resp = postAnon(t, ts, "alice.poweur.net", "cross", bobEnv.Challenge.Token, bobSolution)
	mustStatus(t, resp, http.StatusForbidden, "cross-recipient token")
}

func TestAnonCaps(t *testing.T) {
	server, ts := newTestRelay(t)
	alice := registerTestIdentity(t, server, ts, "alice.poweur.net")
	setSysFile(t, server, alice.name, ".poweur/relay/inbox-policy.json",
		`{"version":1,"mode":"open","anonymous":{"allow":true,"challenge":"none","max_bytes":64,"max_per_day":2}}`)

	// Size cap.
	resp := postAnon(t, ts, "alice.poweur.net", strings.Repeat("x", 100), "", "")
	mustStatus(t, resp, http.StatusRequestEntityTooLarge, "oversized anon message")

	// Daily cap.
	for i := 0; i < 2; i++ {
		resp = postAnon(t, ts, "alice.poweur.net", "ok", "", "")
		mustStatus(t, resp, http.StatusAccepted, "under daily cap")
	}
	resp = postAnon(t, ts, "alice.poweur.net", "one too many", "", "")
	out := mustStatus(t, resp, http.StatusTooManyRequests, "daily cap")
	if out["error"] != "anon_daily_cap" {
		t.Fatalf("cap outcome: %v", out)
	}
}

func TestAnonVerifiedAndPaymentSlots(t *testing.T) {
	server, ts := newTestRelay(t)
	alice := registerTestIdentity(t, server, ts, "alice.poweur.net")
	setSysFile(t, server, alice.name, ".poweur/relay/inbox-policy.json",
		`{"version":1,"mode":"open","anonymous":{"allow":true,"challenge":"verified"}}`)

	// The typed envelope names the unimplemented challenge.
	resp := postAnon(t, ts, "alice.poweur.net", "hi", "", "")
	if resp.StatusCode != http.StatusPreconditionRequired {
		t.Fatalf("want 428, got %d", resp.StatusCode)
	}
	env := decodeChallenge(t, resp)
	if env.Challenge.Type != "verified" || env.Challenge.Detail == "" {
		t.Fatalf("verified envelope: %+v", env)
	}
	// Attempting a solution anyway gets 501.
	resp = postAnon(t, ts, "alice.poweur.net", "hi", "sometoken", "sometry")
	mustStatus(t, resp, http.StatusNotImplemented, "verified solution attempt")
}

func TestAnonRequiresEncryptionAndValidPolicy(t *testing.T) {
	server, ts := newTestRelay(t)
	alice := registerTestIdentity(t, server, ts, "alice.poweur.net")

	setSysFile(t, server, alice.name, ".poweur/relay/inbox-policy.json",
		`{"version":1,"mode":"open","anonymous":{"allow":true,"challenge":"none"}}`)
	// Encrypt-only applies to anonymous senders too.
	body, _ := json.Marshal(map[string]any{
		"id": "msg_anon_plain", "recipient": "alice.poweur.net",
		"timestamp": time.Now().UTC().Format(time.RFC3339), "payload": "plaintext",
	})
	plain, err := http.Post(ts.URL+"/messages", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	mustStatus(t, plain, http.StatusBadRequest, "plaintext anon message")
}

// E14-T5: REGISTRATION_GATE=pow — hosted registration requires a solved,
// single-use proof-of-work challenge.
func TestRegistrationPowGate(t *testing.T) {
	cfg := config.Config{
		ListenAddr: ":0", RelayAddress: "relay.test", RelayScheme: "http",
		DNSTTL: time.Minute, ChallengeTTL: time.Minute, Version: "test",
		DataDir: t.TempDir(), HostedDomains: []string{"poweur.net"},
		ResolverAllowPrivate: true,
		RegistrationGate:     "pow", RegistrationPowBits: 8,
		RateLimits: config.RateLimits{PerMinute: 100000, PerHour: 100000, PerDay: 100000},
	}
	server := NewServer(cfg, dns.NewNetResolver(), dns.NewProviderFactory(cfg))
	ts := httptest.NewServer(server.Router())
	defer ts.Close()
	server.cfg.RelayAddress = strings.TrimPrefix(ts.URL, "http://")

	register := func(name, powToken, powSolution string) *http.Response {
		t.Helper()
		pub, priv, _ := ed25519.GenerateKey(nil)
		pubB64 := base64.RawURLEncoding.EncodeToString(pub)
		issued := time.Now().UTC().Format(time.RFC3339)
		nonce := "n-" + name
		canon := crypto.CanonicalIdentityRegistration(name, pubB64, "", server.cfg.RelayAddress, issued, nonce)
		sig := base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, []byte(canon)))
		doc := idpkg.NewDocument(name, idpkg.FormatEd25519PublicKey(pub), "", server.cfg.RelayAddress, nil)
		doc.UpdatedAt = issued
		if err := doc.Sign(priv); err != nil {
			t.Fatal(err)
		}
		docRaw, _ := json.Marshal(doc)
		body, _ := json.Marshal(IdentityRequest{
			Identity: name, PublicKey: pubB64, IssuedAt: issued, Nonce: nonce,
			IdentitySignature: sig, IdentityDocument: docRaw,
			PowToken: powToken, PowSolution: powSolution,
		})
		resp, err := http.Post(ts.URL+"/identities", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	// Without a solution: 401 pow_required.
	resp := register("nopow.poweur.net", "", "")
	out := mustStatus(t, resp, http.StatusUnauthorized, "registration without pow")
	if out["error"] != "pow_required" {
		t.Fatalf("outcome: %v", out)
	}

	// Fetch + solve + register succeeds.
	powResp, err := http.Get(ts.URL + "/auth/pow?purpose=registration")
	if err != nil {
		t.Fatal(err)
	}
	var ch struct {
		Token string `json:"token"`
		Bits  int    `json:"bits"`
	}
	if err := json.NewDecoder(powResp.Body).Decode(&ch); err != nil {
		t.Fatal(err)
	}
	powResp.Body.Close()
	if ch.Bits != 8 || ch.Token == "" {
		t.Fatalf("challenge: %+v", ch)
	}
	solution, err := idpkg.SolvePow(context.Background(), ch.Token, ch.Bits)
	if err != nil {
		t.Fatal(err)
	}
	resp = register("withpow.poweur.net", ch.Token, solution)
	mustStatus(t, resp, http.StatusCreated, "registration with solved pow")

	// The same solution cannot register a second identity (single-use).
	resp = register("replay.poweur.net", ch.Token, solution)
	out = mustStatus(t, resp, http.StatusForbidden, "replayed pow")
	if out["error"] != "pow_replayed" {
		t.Fatalf("replay outcome: %v", out)
	}

	// An anon-message token cannot be spent on registration (purpose-bound)
	// — covered structurally by VerifyPowSolution purpose checks; assert
	// the endpoint refuses other purposes outright.
	badPurpose, err := http.Get(ts.URL + "/auth/pow?purpose=anything-else")
	if err != nil {
		t.Fatal(err)
	}
	badPurpose.Body.Close()
	if badPurpose.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad purpose: %d", badPurpose.StatusCode)
	}
}

func TestAnonLoadRaisesDifficultyFloor(t *testing.T) {
	server, ts := newTestRelay(t)
	alice := registerTestIdentity(t, server, ts, "alice.poweur.net")
	setSysFile(t, server, alice.name, ".poweur/relay/inbox-policy.json",
		`{"version":1,"mode":"open","anonymous":{"allow":true,"challenge":"pow","pow_bits":8}}`)

	// Saturate the challenge-issuance window; the floor must rise.
	var lastBits int
	for i := 0; i < anonLoadPerMinute+2; i++ {
		resp := postAnon(t, ts, "alice.poweur.net", "flood", "", "")
		if resp.StatusCode != http.StatusPreconditionRequired {
			t.Fatalf("want 428, got %d", resp.StatusCode)
		}
		env := decodeChallenge(t, resp)
		lastBits = env.Challenge.Bits
	}
	if lastBits != 8+anonLoadRaiseBits {
		t.Fatalf("difficulty floor under load: %d want %d", lastBits, 8+anonLoadRaiseBits)
	}
}
