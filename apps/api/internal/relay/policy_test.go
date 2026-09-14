package relay

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/poweur/api/internal/config"
	"github.com/poweur/api/internal/crypto"
	"github.com/poweur/api/internal/dns"
	"github.com/poweur/api/internal/storage"
)

// EPIC-007 E07-T2: inbox policy enforcement scenario tests over real HTTP —
// hosted identities, policy/contacts written via DAV, typed messages.

var policyMsgSeq int

func postTypedMessage(t *testing.T, ts *httptest.Server, from davTestIdentity, to, msgType, payload string) (*http.Response, string) {
	t.Helper()
	policyMsgSeq++
	msg := Message{
		ID:        "msg_pol_" + time.Now().Format("150405.000000000") + "_" + string(rune('a'+policyMsgSeq%26)),
		Sender:    from.name,
		Recipient: to,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Payload:   payload,
		Type:      msgType,
		Encryption: &EncryptionMeta{
			Alg:                "x25519-chacha20-poly1305",
			EphemeralPublicKey: "ephemeral-pub",
			Nonce:              "nonce",
		},
	}
	canonical := crypto.CanonicalMessageTyped(msg.Sender, msg.Recipient, msg.Timestamp, msg.Payload, msg.ID, "", msg.Type, &crypto.EncryptionMeta{
		Alg: msg.Encryption.Alg, EphemeralPublicKey: msg.Encryption.EphemeralPublicKey, Nonce: msg.Encryption.Nonce,
	})
	msg.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(from.priv, []byte(canonical)))
	body, _ := json.Marshal(msg)
	resp, err := http.Post(ts.URL+"/messages", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return resp, msg.ID
}

func mustStatus(t *testing.T, resp *http.Response, want int, context string) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		t.Fatalf("%s: %d want %d (%s)", context, resp.StatusCode, want, raw)
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

func putOwnerFile(t *testing.T, ts *httptest.Server, owner davTestIdentity, token, path, body string) {
	t.Helper()
	resp := davReq(t, ts, http.MethodPut, "/dav/"+owner.name+path, token, []byte(body), nil)
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("put %s: %d", path, resp.StatusCode)
	}
}

// challengeSigned performs a challenge-signed GET (inbox/requests pattern).
func challengeSigned(t *testing.T, ts *httptest.Server, id davTestIdentity, path string) map[string]any {
	t.Helper()
	chResp, err := http.Get(ts.URL + "/auth/challenge?identity=" + id.name)
	if err != nil {
		t.Fatal(err)
	}
	var ch ChallengeResponse
	if err := json.NewDecoder(chResp.Body).Decode(&ch); err != nil {
		t.Fatal(err)
	}
	chResp.Body.Close()
	req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
	req.Header.Set("X-Poweur-Identity", id.name)
	req.Header.Set("X-Poweur-Signature", base64.StdEncoding.EncodeToString(ed25519.Sign(id.priv, []byte(ch.Challenge))))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return mustStatus(t, resp, http.StatusOK, "GET "+path)
}

func TestPolicyDefaultOpenAndBlocked(t *testing.T) {
	server, ts := newDAVTestServer(t, 0, 0)
	alice := registerDAVIdentity(t, server, ts, "alice.poweur.net")
	bob := registerDAVIdentity(t, server, ts, "bob.poweur.net")
	aliceTok := mintDAVToken(t, ts, alice, "", "")

	// No policy file → open: a stranger's message is delivered.
	resp, _ := postTypedMessage(t, ts, bob, alice.name, "", "hello")
	mustStatus(t, resp, http.StatusAccepted, "stranger under default open")

	// A blocked sender is rejected even under open mode…
	putOwnerFile(t, ts, alice, aliceTok, "/poweur-sys/relay/contacts.json",
		`{"version":1,"contacts":[{"identity":"bob.poweur.net","state":"blocked"}]}`)
	resp, _ = postTypedMessage(t, ts, bob, alice.name, "", "hello again")
	out := mustStatus(t, resp, http.StatusForbidden, "blocked sender")
	if out["error"] != "policy_rejected" {
		t.Fatalf("blocked sender error: %v", out)
	}
}

// Contact requests ride the requests queue even under the default `open`
// policy. Chat from anyone still lands in the inbox; the handshake does not.
func TestPolicyOpenQueuesContactRequest(t *testing.T) {
	server, ts := newDAVTestServer(t, 0, 0)
	alice := registerDAVIdentity(t, server, ts, "alice.poweur.net")
	bob := registerDAVIdentity(t, server, ts, "bob.poweur.net")

	resp, reqID := postTypedMessage(t, ts, bob, alice.name, "sys.contact.request", "hi, it's bob")
	out := mustStatus(t, resp, http.StatusAccepted, "request under default open")
	if out["status"] != "request_queued" {
		t.Fatalf("request outcome: %v", out)
	}

	inbox := challengeSigned(t, ts, alice, "/messages/"+alice.name)
	if msgs, _ := inbox["messages"].([]any); len(msgs) != 0 {
		t.Fatalf("request leaked into an open inbox: %v", msgs)
	}
	reqs := challengeSigned(t, ts, alice, "/requests/"+alice.name)
	list, _ := reqs["requests"].([]any)
	if len(list) != 1 {
		t.Fatalf("requests queue: %v", reqs)
	}
	first, _ := list[0].(map[string]any)
	if first["sender"] != bob.name || first["type"] != "sys.contact.request" || first["id"] != reqID {
		t.Fatalf("queued request: %v", first)
	}

	// Ordinary chat from the same stranger still reaches the open inbox.
	resp, _ = postTypedMessage(t, ts, bob, alice.name, "", "hello anyway")
	mustStatus(t, resp, http.StatusAccepted, "stranger chat under open")
	inbox = challengeSigned(t, ts, alice, "/messages/"+alice.name)
	if msgs, _ := inbox["messages"].([]any); len(msgs) != 1 {
		t.Fatalf("open inbox after chat: %v", inbox)
	}
}

// An accepted contact can still knock. John already listing Bob as a contact
// must not swallow Bob's sys.contact.request into chat — that is the only
// envelope that lets John answer and finish Bob's handshake.
func TestPolicyAcceptedContactRequestStillQueued(t *testing.T) {
	server, ts := newDAVTestServer(t, 0, 0)
	alice := registerDAVIdentity(t, server, ts, "alice.poweur.net")
	bob := registerDAVIdentity(t, server, ts, "bob.poweur.net")
	aliceTok := mintDAVToken(t, ts, alice, "", "")

	putOwnerFile(t, ts, alice, aliceTok, "/poweur-sys/relay/contacts.json",
		`{"version":1,"contacts":[{"identity":"bob.poweur.net","state":"accepted"}]}`)

	resp, reqID := postTypedMessage(t, ts, bob, alice.name, "sys.contact.request", "please add me back")
	out := mustStatus(t, resp, http.StatusAccepted, "request from accepted contact")
	if out["status"] != "request_queued" {
		t.Fatalf("request outcome: %v", out)
	}

	inbox := challengeSigned(t, ts, alice, "/messages/"+alice.name)
	if msgs, _ := inbox["messages"].([]any); len(msgs) != 0 {
		t.Fatalf("accepted-contact request leaked into inbox: %v", msgs)
	}
	reqs := challengeSigned(t, ts, alice, "/requests/"+alice.name)
	list, _ := reqs["requests"].([]any)
	if len(list) != 1 {
		t.Fatalf("requests queue: %v", reqs)
	}
	first, _ := list[0].(map[string]any)
	if first["id"] != reqID || first["type"] != "sys.contact.request" {
		t.Fatalf("queued request: %v", first)
	}

	// Chat from the same contact still delivers.
	resp, _ = postTypedMessage(t, ts, bob, alice.name, "", "still friends")
	mustStatus(t, resp, http.StatusAccepted, "chat from accepted contact")
}

func TestPolicyContactsOnly(t *testing.T) {
	server, ts := newDAVTestServer(t, 0, 0)
	alice := registerDAVIdentity(t, server, ts, "alice.poweur.net")
	bob := registerDAVIdentity(t, server, ts, "bob.poweur.net")
	carol := registerDAVIdentity(t, server, ts, "carol.poweur.net")
	aliceTok := mintDAVToken(t, ts, alice, "", "")

	putOwnerFile(t, ts, alice, aliceTok, "/poweur-sys/relay/inbox-policy.json", `{"version":1,"mode":"contacts_only"}`)
	putOwnerFile(t, ts, alice, aliceTok, "/poweur-sys/relay/contacts.json",
		`{"version":1,"contacts":[{"identity":"bob.poweur.net","state":"accepted"}]}`)

	// Accepted contact delivers; stranger is rejected; a contact request is
	// also rejected (contacts_only means no requests either).
	resp, _ := postTypedMessage(t, ts, bob, alice.name, "", "from contact")
	mustStatus(t, resp, http.StatusAccepted, "contact under contacts_only")
	resp, _ = postTypedMessage(t, ts, carol, alice.name, "", "from stranger")
	mustStatus(t, resp, http.StatusForbidden, "stranger under contacts_only")
	resp, _ = postTypedMessage(t, ts, carol, alice.name, "sys.contact.request", "let me in")
	mustStatus(t, resp, http.StatusForbidden, "request under contacts_only")

	// Self-messages (own devices) always pass.
	resp, _ = postTypedMessage(t, ts, alice, alice.name, "", "note to self")
	mustStatus(t, resp, http.StatusAccepted, "self message")
}

func TestPolicyContactsAndRequestsFlow(t *testing.T) {
	server, ts := newDAVTestServer(t, 0, 0)
	alice := registerDAVIdentity(t, server, ts, "alice.poweur.net")
	bob := registerDAVIdentity(t, server, ts, "bob.poweur.net")
	aliceTok := mintDAVToken(t, ts, alice, "", "")

	putOwnerFile(t, ts, alice, aliceTok, "/poweur-sys/relay/inbox-policy.json", `{"version":1,"mode":"contacts_and_requests"}`)

	// A normal message from a stranger is rejected with a hint…
	resp, _ := postTypedMessage(t, ts, bob, alice.name, "", "hi")
	mustStatus(t, resp, http.StatusForbidden, "stranger normal message")

	// …but a sys.contact.request is queued.
	resp, reqID := postTypedMessage(t, ts, bob, alice.name, "sys.contact.request", "hi, it's bob")
	out := mustStatus(t, resp, http.StatusAccepted, "contact request")
	if out["status"] != "request_queued" {
		t.Fatalf("request outcome: %v", out)
	}

	// One pending slot: a second request is 409 request_pending.
	resp, _ = postTypedMessage(t, ts, bob, alice.name, "sys.contact.request", "me again")
	out = mustStatus(t, resp, http.StatusConflict, "duplicate request")
	if out["error"] != "request_pending" {
		t.Fatalf("duplicate outcome: %v", out)
	}

	// The request is NOT in the normal inbox…
	inbox := challengeSigned(t, ts, alice, "/messages/"+alice.name)
	if msgs, _ := inbox["messages"].([]any); len(msgs) != 0 {
		t.Fatalf("request leaked into inbox: %v", msgs)
	}
	// …it is in the requests queue, with type and sender intact.
	reqs := challengeSigned(t, ts, alice, "/requests/"+alice.name)
	list, _ := reqs["requests"].([]any)
	if len(list) != 1 {
		t.Fatalf("requests queue: %v", reqs)
	}
	first, _ := list[0].(map[string]any)
	if first["sender"] != bob.name || first["type"] != "sys.contact.request" || first["id"] != reqID {
		t.Fatalf("queued request: %v", first)
	}

	// After drain (still unaccepted), an immediate re-request hits cooldown.
	resp, _ = postTypedMessage(t, ts, bob, alice.name, "sys.contact.request", "still me")
	out = mustStatus(t, resp, http.StatusConflict, "cooldown request")
	if out["error"] != "request_cooldown" {
		t.Fatalf("cooldown outcome: %v", out)
	}

	// Alice accepts: writes bob as accepted → normal messages now deliver.
	putOwnerFile(t, ts, alice, aliceTok, "/poweur-sys/relay/contacts.json",
		`{"version":1,"contacts":[{"identity":"bob.poweur.net","state":"accepted"}]}`)
	resp, _ = postTypedMessage(t, ts, bob, alice.name, "", "we're contacts now")
	mustStatus(t, resp, http.StatusAccepted, "post-accept message")
	inbox = challengeSigned(t, ts, alice, "/messages/"+alice.name)
	if msgs, _ := inbox["messages"].([]any); len(msgs) != 1 {
		t.Fatalf("post-accept inbox: %v", inbox)
	}
}

func TestPolicyContactAcceptRouting(t *testing.T) {
	server, ts := newDAVTestServer(t, 0, 0)
	alice := registerDAVIdentity(t, server, ts, "alice.poweur.net")
	bob := registerDAVIdentity(t, server, ts, "bob.poweur.net")
	carol := registerDAVIdentity(t, server, ts, "carol.poweur.net")
	aliceTok := mintDAVToken(t, ts, alice, "", "")

	putOwnerFile(t, ts, alice, aliceTok, "/poweur-sys/relay/inbox-policy.json", `{"version":1,"mode":"contacts_and_requests"}`)
	// Alice sent bob a request (state=requested in her contacts).
	putOwnerFile(t, ts, alice, aliceTok, "/poweur-sys/relay/contacts.json",
		`{"version":1,"contacts":[{"identity":"bob.poweur.net","state":"requested"}]}`)

	// Bob's accept rides the requests queue.
	resp, _ := postTypedMessage(t, ts, bob, alice.name, "sys.contact.accept", "accepted!")
	out := mustStatus(t, resp, http.StatusAccepted, "accept from requested contact")
	if out["status"] != "request_queued" {
		t.Fatalf("accept outcome: %v", out)
	}
	// An accept from someone alice never requested is rejected.
	resp, _ = postTypedMessage(t, ts, carol, alice.name, "sys.contact.accept", "gotcha")
	mustStatus(t, resp, http.StatusForbidden, "unsolicited accept")
	// A `requested` peer still cannot send normal messages.
	resp, _ = postTypedMessage(t, ts, bob, alice.name, "", "sneaky normal message")
	mustStatus(t, resp, http.StatusForbidden, "normal message while requested")
}

// A `contacts_only` inbox may still start a handshake, so it has to be able
// to hear the answer. Without this rule the accept bounces, the requester's
// contacts stay `requested`, and their own policy then bounces every message
// the person who accepted them sends — a stalemate neither side can see.
func TestPolicyContactAcceptReachesContactsOnlyRequester(t *testing.T) {
	server, ts := newDAVTestServer(t, 0, 0)
	alice := registerDAVIdentity(t, server, ts, "alice.poweur.net")
	bob := registerDAVIdentity(t, server, ts, "bob.poweur.net")
	carol := registerDAVIdentity(t, server, ts, "carol.poweur.net")
	aliceTok := mintDAVToken(t, ts, alice, "", "")

	putOwnerFile(t, ts, alice, aliceTok, "/poweur-sys/relay/inbox-policy.json", `{"version":1,"mode":"contacts_only"}`)
	putOwnerFile(t, ts, alice, aliceTok, "/poweur-sys/relay/contacts.json",
		`{"version":1,"contacts":[{"identity":"bob.poweur.net","state":"requested"}]}`)

	// Bob answers the request alice sent him: queued, not rejected.
	resp, acceptID := postTypedMessage(t, ts, bob, alice.name, "sys.contact.accept", "yes")
	out := mustStatus(t, resp, http.StatusAccepted, "accept answering our own request")
	if out["status"] != "request_queued" {
		t.Fatalf("accept outcome: %v", out)
	}

	// It rides the requests queue, not the message stream — a closed inbox
	// stays closed to the message stream even for this.
	inbox := challengeSigned(t, ts, alice, "/messages/"+alice.name)
	if msgs, _ := inbox["messages"].([]any); len(msgs) != 0 {
		t.Fatalf("accept leaked into a contacts_only inbox: %v", msgs)
	}
	reqs := challengeSigned(t, ts, alice, "/requests/"+alice.name)
	list, _ := reqs["requests"].([]any)
	if len(list) != 1 {
		t.Fatalf("requests queue: %v", reqs)
	}
	if first, _ := list[0].(map[string]any); first["id"] != acceptID || first["type"] != "sys.contact.accept" {
		t.Fatalf("queued accept: %v", list[0])
	}

	// The rule is narrow: only an accept, and only from someone we asked.
	resp, _ = postTypedMessage(t, ts, carol, alice.name, "sys.contact.accept", "gotcha")
	mustStatus(t, resp, http.StatusForbidden, "unsolicited accept under contacts_only")
	resp, _ = postTypedMessage(t, ts, bob, alice.name, "sys.contact.request", "let me in")
	mustStatus(t, resp, http.StatusForbidden, "request under contacts_only")
	resp, _ = postTypedMessage(t, ts, bob, alice.name, "", "sneaky normal message")
	mustStatus(t, resp, http.StatusForbidden, "normal message while requested")
}

func TestTypedMessageSignatureBindsType(t *testing.T) {
	server, ts := newDAVTestServer(t, 0, 0)
	alice := registerDAVIdentity(t, server, ts, "alice.poweur.net")
	bob := registerDAVIdentity(t, server, ts, "bob.poweur.net")
	aliceTok := mintDAVToken(t, ts, alice, "", "")
	putOwnerFile(t, ts, alice, aliceTok, "/poweur-sys/relay/inbox-policy.json", `{"version":1,"mode":"contacts_and_requests"}`)

	// Sign an untyped message, then claim it is a contact request: the
	// signature must not verify (type is inside the canonical string).
	msg := Message{
		ID: "msg_forged_type", Sender: bob.name, Recipient: alice.name,
		Timestamp: time.Now().UTC().Format(time.RFC3339), Payload: "hi",
		Encryption: &EncryptionMeta{Alg: "x25519-chacha20-poly1305", EphemeralPublicKey: "e", Nonce: "n"},
	}
	canonical := crypto.CanonicalMessageTyped(msg.Sender, msg.Recipient, msg.Timestamp, msg.Payload, msg.ID, "", "", &crypto.EncryptionMeta{
		Alg: "x25519-chacha20-poly1305", EphemeralPublicKey: "e", Nonce: "n"})
	msg.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(bob.priv, []byte(canonical)))
	msg.Type = "sys.contact.request" // forged after signing
	body, _ := json.Marshal(msg)
	resp, err := http.Post(ts.URL+"/messages", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	mustStatus(t, resp, http.StatusUnauthorized, "type forged after signing")
}

// Cross-relay: dave (local on relay A) messages bob (hosted on relay B,
// contacts_only). A forwards to B; B — the recipient's relay — rejects, so
// policy holds for forwarded senders exactly like local ones.
func TestPolicyEnforcedOnForwardedCrossRelay(t *testing.T) {
	davePub, davePriv, _ := ed25519.GenerateKey(nil)
	bobPub, _, _ := ed25519.GenerateKey(nil)

	// Relay B: bob's home, durable storage, contacts_only policy on disk.
	dataDirB := t.TempDir()
	cfgB := config.Config{
		ListenAddr: ":0", RelayAddress: "relay-b.test", RelayScheme: "http",
		DNSTTL: time.Minute, ChallengeTTL: time.Minute, Version: "test",
		DataDir:    dataDirB,
		RateLimits: config.RateLimits{PerMinute: 1000, PerHour: 10000, PerDay: 100000},
	}
	daveTxt := "poweur-pubkey=ed25519:" + base64.RawURLEncoding.EncodeToString(davePub)
	resolverB := &fakeResolver{
		txt:   map[string][]string{"_poweur.dave.poweur.net": {daveTxt}},
		hosts: map[string][]string{},
	}
	serverB := NewServer(cfgB, resolverB, dns.NewProviderFactory(cfgB))
	serverB.identities.Add(storage.Identity{
		Identity: "bob.poweur.net", PublicKey: base64.RawURLEncoding.EncodeToString(bobPub),
		PublicKeyBytes: bobPub, CreatedAt: time.Now().UTC(),
	})
	bobHome, err := serverB.identities.IdentityHomeDir("bob.poweur.net")
	if err != nil || bobHome == "" {
		t.Fatalf("bob home dir: %q %v", bobHome, err)
	}
	policyDir := filepath.Join(bobHome, "poweur-sys", "relay")
	if err := os.MkdirAll(policyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(policyDir, "inbox-policy.json"),
		[]byte(`{"version":1,"mode":"contacts_only"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	tsB := httptest.NewServer(serverB.Router())
	defer tsB.Close()
	uB, _ := url.Parse(tsB.URL)

	// Relay A: dave's home; resolves bob to relay B.
	cfgA := config.Config{
		ListenAddr: ":0", RelayAddress: "relay-a.test", RelayScheme: "http",
		DNSTTL: time.Minute, ChallengeTTL: time.Minute, Version: "test",
		RateLimits: config.RateLimits{PerMinute: 1000, PerHour: 10000, PerDay: 100000},
	}
	resolverA := &fakeResolver{
		txt:   map[string][]string{},
		hosts: map[string][]string{"bob.poweur.net": {uB.Host}},
	}
	serverA := NewServer(cfgA, resolverA, dns.NewProviderFactory(cfgA))
	serverA.identities.Add(storage.Identity{
		Identity: "dave.poweur.net", PublicKey: base64.RawURLEncoding.EncodeToString(davePub),
		PublicKeyBytes: davePub, CreatedAt: time.Now().UTC(),
	})
	tsA := httptest.NewServer(serverA.Router())
	defer tsA.Close()

	msg := Message{
		ID: "msg_fwd_policy_1", Sender: "dave.poweur.net", Recipient: "bob.poweur.net",
		Timestamp: time.Now().UTC().Format(time.RFC3339), Payload: "cipher",
		Encryption: &EncryptionMeta{Alg: "x25519-chacha20-poly1305", EphemeralPublicKey: "e", Nonce: "n"},
	}
	canonical := crypto.CanonicalMessageTyped(msg.Sender, msg.Recipient, msg.Timestamp, msg.Payload, msg.ID, "", "", &crypto.EncryptionMeta{
		Alg: msg.Encryption.Alg, EphemeralPublicKey: msg.Encryption.EphemeralPublicKey, Nonce: msg.Encryption.Nonce})
	msg.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(davePriv, []byte(canonical)))
	body, _ := json.Marshal(msg)
	resp, err := http.Post(tsA.URL+"/messages", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 400 {
		t.Fatalf("forwarded message to contacts_only recipient must fail, got %d", resp.StatusCode)
	}
	if got := serverB.inbox.Drain("bob.poweur.net"); len(got) != 0 {
		t.Fatalf("message must not reach bob's inbox: %v", got)
	}
}

func TestRequestStoreCooldownUnit(t *testing.T) {
	st := storage.NewRequestStore()
	msg := storage.StoredMessage{ID: "1", Sender: "bob", Recipient: "alice"}
	if got := st.Add("alice", "bob", msg, time.Hour); got != storage.RequestQueued {
		t.Fatalf("first add: %s", got)
	}
	if got := st.Add("alice", "bob", msg, time.Hour); got != storage.RequestDup {
		t.Fatalf("dup add: %s", got)
	}
	if got := st.Drain("alice"); len(got) != 1 {
		t.Fatalf("drain: %v", got)
	}
	if got := st.Add("alice", "bob", msg, time.Hour); got != storage.RequestCooldown {
		t.Fatalf("cooldown add: %s", got)
	}
	st.ClearCooldown("alice", "bob")
	if got := st.Add("alice", "bob", msg, time.Hour); got != storage.RequestQueued {
		t.Fatalf("post-clear add: %s", got)
	}
	// Zero cooldown allows immediate re-request after drain.
	st2 := storage.NewRequestStore()
	st2.Add("a", "b", msg, 0)
	st2.Drain("a")
	if got := st2.Add("a", "b", msg, 0); got != storage.RequestQueued {
		t.Fatalf("zero-cooldown re-add: %s", got)
	}
}
