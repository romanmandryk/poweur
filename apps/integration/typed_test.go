// Integration tests for typed messages & threads (EPIC-009 E09-T3).
//
// The epic's acceptance criterion is wire compatibility in both directions:
// an old client must interoperate with a new relay, and a new client with an
// old relay. There is no old binary to run, so "old" is reconstructed from
// the thing that actually defines it — the **pre-E09-T3 canonical signing
// string**, spelled out by hand in `legacyCanonical` below rather than
// borrowed from the code under test. A test that called the new function to
// describe the old protocol would pass no matter what the new function did.
package integration_test

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// legacyCanonical is the canonical signing string exactly as it stood before
// E09-T3: four header lines, then `id:`, `session:` and `enc:` when present,
// and nothing else. This is the string an old client signs and an old relay
// verifies. It is written out longhand on purpose — it is the fixed point the
// compatibility claim rests on.
func legacyCanonical(sender, recipient, timestamp, payload, id, sessionID, alg, eph, nonce string) string {
	parts := []string{sender, recipient, timestamp, payload}
	if id != "" {
		parts = append(parts, "id:"+id)
	}
	if sessionID != "" {
		parts = append(parts, "session:"+sessionID)
	}
	if alg != "" {
		parts = append(parts, "enc:"+alg+":"+eph+":"+nonce)
	}
	return strings.Join(parts, "\n")
}

// sentEnvelope is the shape `poweur send --json` reports back: the message as
// it went on the wire, signature included.
type sentEnvelope struct {
	ID      string `json:"id"`
	Message struct {
		ID         string            `json:"id"`
		Sender     string            `json:"sender"`
		Recipient  string            `json:"recipient"`
		Timestamp  string            `json:"timestamp"`
		Payload    string            `json:"payload"`
		Signature  string            `json:"signature"`
		Type       string            `json:"type"`
		ThreadID   string            `json:"thread_id"`
		ExpiresAt  string            `json:"expires_at"`
		Metadata   map[string]string `json:"metadata"`
		SessionID  string            `json:"session_id"`
		Encryption struct {
			Alg                string `json:"alg"`
			EphemeralPublicKey string `json:"ephemeral_public_key"`
			Nonce              string `json:"nonce"`
		} `json:"encryption"`
	} `json:"message"`
}

func sendJSON(t *testing.T, home string, args ...string) sentEnvelope {
	t.Helper()
	stdout, _ := runCLI(t, home, append([]string{"send"}, append(args, "--json")...)...)
	var out sentEnvelope
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("send --json: %v\n%s", err, stdout)
	}
	return out
}

// inboxJSON drains an identity's inbox as the raw relay response.
func inboxJSON(t *testing.T, home string) map[string]any {
	t.Helper()
	stdout, _ := runCLI(t, home, "inbox", "--json")
	var out map[string]any
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("inbox --json: %v\n%s", err, stdout)
	}
	return out
}

func inboxMessages(t *testing.T, home string) []map[string]any {
	t.Helper()
	raw, _ := inboxJSON(t, home)["messages"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		if m, ok := entry.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// identityPublicKey fetches a sender's long-lived Ed25519 key from the relay,
// which is what a verifier (relay or recipient) has to work with.
func identityPublicKey(t *testing.T, relayURL, identity string) ed25519.PublicKey {
	t.Helper()
	resp, err := http.Get(relayURL + "/identities/" + identity)
	if err != nil {
		t.Fatalf("GET /identities/%s: %v", identity, err)
	}
	defer resp.Body.Close()
	var record struct {
		PublicKey string `json:"public_key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&record); err != nil {
		t.Fatalf("decode identity record: %v", err)
	}
	raw := strings.TrimPrefix(record.PublicKey, "ed25519:")
	key, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		if key, err = base64.StdEncoding.DecodeString(raw); err != nil {
			t.Fatalf("decode public key %q: %v", record.PublicKey, err)
		}
	}
	if len(key) != ed25519.PublicKeySize {
		t.Fatalf("public key for %s is %d bytes", identity, len(key))
	}
	return ed25519.PublicKey(key)
}

// loadIdentityKey reads a CLI identity's private key. The plaintext form is
// bare base64; these tests never set a passphrase.
func loadIdentityKey(t *testing.T, home, identity string) ed25519.PrivateKey {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(home, ".poweur", "keys", identity+".key"))
	if err != nil {
		t.Fatalf("read key for %s: %v", identity, err)
	}
	// Same detection order as the CLI's own decodeKeyBytes.
	text := strings.TrimSpace(string(raw))
	decoded, err := base64.RawStdEncoding.DecodeString(text)
	if err != nil {
		if decoded, err = base64.StdEncoding.DecodeString(text); err != nil {
			t.Fatalf("decode key for %s: %v", identity, err)
		}
	}
	if len(decoded) != ed25519.PrivateKeySize {
		t.Fatalf("key for %s is %d bytes (encrypted key file?)", identity, len(decoded))
	}
	return ed25519.PrivateKey(decoded)
}

// twoIdentities spins up one relay with alice and bob on it.
func twoIdentities(t *testing.T) (relayURL, aliceHome, bobHome string) {
	t.Helper()
	zone := newZone(t)
	_, addr := newRelay(t, zone)
	relayURL = "http://" + addr
	aliceHome, bobHome = t.TempDir(), t.TempDir()
	runCLI(t, aliceHome, "identity", "create", "alice",
		"--parent-domain", "poweur.net", "--relay", relayURL,
		"--dns-provider", "mock", "--dns-token", "integration")
	runCLI(t, bobHome, "identity", "create", "bob",
		"--parent-domain", "poweur.net", "--relay", relayURL,
		"--dns-provider", "mock", "--dns-token", "integration")
	return relayURL, aliceHome, bobHome
}

// TestINT_TYPED_01_FullEnvelopeRoundTrip: a new client sends every E09-T3
// field, the relay accepts and stores them, and the recipient gets them back
// verbatim alongside a payload that still decrypts.
//
// "Verbatim" is the load-bearing word: the recipient recomputes the canonical
// string to check the signature, so a relay that dropped `thread_id` would
// turn every threaded message into a downstream signature failure.
func TestINT_TYPED_01_FullEnvelopeRoundTrip(t *testing.T) {
	_, aliceHome, bobHome := twoIdentities(t)

	secret := "the logo, v3"
	expires := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339)
	sendJSON(t, aliceHome, "bob.poweur.net", secret,
		"--type", "chat.attachment",
		"--thread", "thr_rebrand",
		"--expires", expires,
		"--meta", "mime=image/png",
		"--meta", "bytes=20480")

	msgs := inboxMessages(t, bobHome)
	if len(msgs) != 1 {
		t.Fatalf("expected one message, got %d", len(msgs))
	}
	got := msgs[0]
	for field, want := range map[string]string{
		"type":       "chat.attachment",
		"thread_id":  "thr_rebrand",
		"expires_at": expires,
	} {
		if got[field] != want {
			t.Fatalf("%s not preserved: got %v, want %q", field, got[field], want)
		}
	}
	meta, _ := got["metadata"].(map[string]any)
	if meta["mime"] != "image/png" || meta["bytes"] != "20480" {
		t.Fatalf("metadata not preserved: %v", got["metadata"])
	}
}

// TestINT_TYPED_02_OldClientNewRelay is half of the epic's acceptance
// criterion. A hand-built envelope signed over the **pre-E09-T3** canonical
// string — no type, no thread, no expiry, no metadata, and no knowledge that
// such fields exist — is accepted by a relay running this revision and
// delivered readable.
//
// The ciphertext is borrowed from a real send (encryption binds to the
// recipient's key, not to the message id), so this is a genuine old-shaped
// envelope end to end rather than an undecryptable stub.
func TestINT_TYPED_02_OldClientNewRelay(t *testing.T) {
	relayURL, aliceHome, bobHome := twoIdentities(t)

	secret := "hello from a client that predates typing"
	seed := sendJSON(t, aliceHome, "--sign-with", "identity", "bob.poweur.net", secret)
	// Drain the seeded copy so the assertion below sees only the replay.
	if len(inboxMessages(t, bobHome)) != 1 {
		t.Fatal("seeding send did not arrive")
	}

	alicePriv := loadIdentityKey(t, aliceHome, "alice.poweur.net")
	id := "msg_oldclient_typed02"
	timestamp := time.Now().UTC().Format(time.RFC3339)
	canonical := legacyCanonical(
		"alice.poweur.net", "bob.poweur.net", timestamp, seed.Message.Payload, id, "",
		seed.Message.Encryption.Alg, seed.Message.Encryption.EphemeralPublicKey,
		seed.Message.Encryption.Nonce)

	// Note the envelope: exactly the keys an old client knows about.
	envelope := map[string]any{
		"id":        id,
		"sender":    "alice.poweur.net",
		"recipient": "bob.poweur.net",
		"timestamp": timestamp,
		"payload":   seed.Message.Payload,
		"signature": base64.StdEncoding.EncodeToString(ed25519.Sign(alicePriv, []byte(canonical))),
		"encryption": map[string]string{
			"alg":                  seed.Message.Encryption.Alg,
			"ephemeral_public_key": seed.Message.Encryption.EphemeralPublicKey,
			"nonce":                seed.Message.Encryption.Nonce,
		},
	}
	body, _ := json.Marshal(envelope)
	resp, err := http.Post(relayURL+"/messages", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post old-shaped envelope: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		raw, _ := json.Marshal(envelope)
		t.Fatalf("a new relay rejected an old client's envelope: %d\n%s", resp.StatusCode, raw)
	}

	inbox, _ := runCLI(t, bobHome, "inbox")
	if !strings.Contains(inbox, secret) {
		t.Fatalf("old client's message did not arrive readable:\n%s", inbox)
	}
	// The relay must not have invented a type for it either — the envelope is
	// what the sender signed, and `chat.text` is the meaning of its absence,
	// not a value to write in.
	if strings.Contains(inbox, "app message from") {
		t.Fatalf("an untyped message must render as chat text:\n%s", inbox)
	}
}

// TestINT_TYPED_03_NewClientOldRelay is the other half. An old relay is
// reconstructed as its verifier: it rebuilds `legacyCanonical` and checks the
// signature. A new client that sets none of the new fields must pass it —
// that is what lets this revision ship without a wire version number.
func TestINT_TYPED_03_NewClientOldRelay(t *testing.T) {
	relayURL, aliceHome, _ := twoIdentities(t)
	alicePub := identityPublicKey(t, relayURL, "alice.poweur.net")

	sent := sendJSON(t, aliceHome, "--sign-with", "identity", "bob.poweur.net", "ordinary chat")
	m := sent.Message
	if m.Type != "" || m.ThreadID != "" || m.ExpiresAt != "" || len(m.Metadata) != 0 {
		t.Fatalf("a plain send must set none of the E09-T3 fields, got %+v", m)
	}
	canonical := legacyCanonical(m.Sender, m.Recipient, m.Timestamp, m.Payload, m.ID, m.SessionID,
		m.Encryption.Alg, m.Encryption.EphemeralPublicKey, m.Encryption.Nonce)
	sig, err := base64.StdEncoding.DecodeString(m.Signature)
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	if !ed25519.Verify(alicePub, []byte(canonical), sig) {
		t.Fatalf("an old relay would reject a new client's plain message.\ncanonical:\n%s", canonical)
	}
}

// TestINT_TYPED_04_OldRelayFailsClosedOnNewFields is the safety half of the
// compatibility story, and the reason the new fields are *signed* rather than
// left as unsigned hints.
//
// An old relay presented with a threaded envelope cannot verify it: the
// signature covers lines that relay does not know how to rebuild. It fails
// closed — rejecting the message — rather than accepting it and silently
// dropping the thread. Silent field loss is the failure mode that would let a
// relay on the path rewrite routing metadata unnoticed.
func TestINT_TYPED_04_OldRelayFailsClosedOnNewFields(t *testing.T) {
	relayURL, aliceHome, _ := twoIdentities(t)
	alicePub := identityPublicKey(t, relayURL, "alice.poweur.net")

	sent := sendJSON(t, aliceHome, "--sign-with", "identity", "bob.poweur.net", "in a thread",
		"--thread", "thr_x", "--meta", "mime=text/plain")
	m := sent.Message
	if m.ThreadID != "thr_x" {
		t.Fatalf("expected the send to carry the thread, got %+v", m)
	}
	canonical := legacyCanonical(m.Sender, m.Recipient, m.Timestamp, m.Payload, m.ID, m.SessionID,
		m.Encryption.Alg, m.Encryption.EphemeralPublicKey, m.Encryption.Nonce)
	sig, _ := base64.StdEncoding.DecodeString(m.Signature)
	if ed25519.Verify(alicePub, []byte(canonical), sig) {
		t.Fatal("an old relay must NOT verify a threaded envelope: the thread would be silently droppable")
	}
}

// TestINT_TYPED_05_SysNamespaceIsReserved: an application cannot mint a
// `sys.*` type. The CLI refuses locally (before anything is encrypted or
// journalled) and the relay refuses at ingress, so neither side is the only
// thing standing between an app and the relay's routing authority.
func TestINT_TYPED_05_SysNamespaceIsReserved(t *testing.T) {
	relayURL, aliceHome, bobHome := twoIdentities(t)

	if code := runCLICode(t, aliceHome, "send", "bob.poweur.net", "pretending", "--type", "sys.made.up"); code == 0 {
		t.Fatal("the CLI must refuse an unregistered sys.* type")
	}
	// Nothing was sent, so nothing arrives.
	if msgs := inboxMessages(t, bobHome); len(msgs) != 0 {
		t.Fatalf("a refused send must not reach the recipient: %v", msgs)
	}

	// The relay refuses it too, for a client that skipped the local check.
	seed := sendJSON(t, aliceHome, "--sign-with", "identity", "bob.poweur.net", "seed")
	inboxMessages(t, bobHome) // drain
	alicePriv := loadIdentityKey(t, aliceHome, "alice.poweur.net")
	id := "msg_sysreserved_typed05"
	timestamp := time.Now().UTC().Format(time.RFC3339)
	canonical := legacyCanonical(
		"alice.poweur.net", "bob.poweur.net", timestamp, seed.Message.Payload, id, "",
		seed.Message.Encryption.Alg, seed.Message.Encryption.EphemeralPublicKey,
		seed.Message.Encryption.Nonce) + "\ntype:sys.made.up"
	envelope := map[string]any{
		"id": id, "sender": "alice.poweur.net", "recipient": "bob.poweur.net",
		"timestamp": timestamp, "payload": seed.Message.Payload, "type": "sys.made.up",
		"signature": base64.StdEncoding.EncodeToString(ed25519.Sign(alicePriv, []byte(canonical))),
		"encryption": map[string]string{
			"alg":                  seed.Message.Encryption.Alg,
			"ephemeral_public_key": seed.Message.Encryption.EphemeralPublicKey,
			"nonce":                seed.Message.Encryption.Nonce,
		},
	}
	body, _ := json.Marshal(envelope)
	resp, err := http.Post(relayURL+"/messages", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post sys.* envelope: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unregistered sys.* type, got %d", resp.StatusCode)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out["error"] != "unsupported_type" {
		t.Fatalf("expected unsupported_type, got %v", out)
	}
}

// TestINT_TYPED_06_UnknownTypesAreOpaque: a type the relay has never heard of
// (outside `sys.*`) is stored and served unchanged, and the CLI shows it as a
// generic app message rather than dumping the payload as chat text. Together
// those are what let an application ship a message type without a relay
// release and without every other client misrendering it.
func TestINT_TYPED_06_UnknownTypesAreOpaque(t *testing.T) {
	_, aliceHome, bobHome := twoIdentities(t)

	sendJSON(t, aliceHome, "bob.poweur.net", `{"widget":1,"secret":"do-not-show"}`,
		"--type", "net.example.widget.poked")

	inbox, _ := runCLI(t, bobHome, "inbox")
	if !strings.Contains(inbox, "app message from alice.poweur.net (net.example.widget.poked)") {
		t.Fatalf("unknown type must render generically:\n%s", inbox)
	}
	if strings.Contains(inbox, "do-not-show") {
		t.Fatalf("an unknown type's payload must not be rendered as chat text:\n%s", inbox)
	}
}

// TestINT_TYPED_07_ThreadSurvivesHistory: threads have to outlive the drain.
// The relay hands a message over exactly once, so if the archive did not keep
// `thread_id` a reload would regroup the conversation differently from what
// the user just saw.
func TestINT_TYPED_07_ThreadSurvivesHistory(t *testing.T) {
	const alice = "thralice.poweur.net"
	const bob = "thrbob.poweur.net"
	// The archive lives in the DAV tree, so this one needs a relay with
	// POWEUR_DATA behind it rather than the memory-only fixture.
	relayURL, _ := journeyRelay(t, alice, bob)
	aliceHome, bobHome := t.TempDir(), t.TempDir()
	createIdentity(t, aliceHome, alice, relayURL)
	createIdentity(t, bobHome, bob, relayURL)

	runCLI(t, aliceHome, "send", bob, "in the rebrand thread", "--thread", "thr_rebrand")
	inbox, _ := runCLI(t, bobHome, "inbox")
	if !strings.Contains(inbox, "[thread thr_rebrand]") {
		t.Fatalf("inbox should mark the thread:\n%s", inbox)
	}

	stdout, _ := runCLI(t, bobHome, "history", "--json", "--use-identity", bob, "--keep-unread")
	if !strings.Contains(stdout, "thr_rebrand") {
		t.Fatalf("the archive must keep the thread so a reload regroups the same way:\n%s", stdout)
	}
	// And the sender's own copy is threaded too — the relay never hands a
	// sender their message back, so the archive is the only record.
	stdout, _ = runCLI(t, aliceHome, "history", "--json", "--use-identity", alice, "--keep-unread")
	if !strings.Contains(stdout, "thr_rebrand") {
		t.Fatalf("sender's archived copy lost the thread:\n%s", stdout)
	}
}

// TestINT_TYPED_08_MalformedExtensionsRefused: shape errors are caught before
// any signature work, and the client is told which field is wrong.
func TestINT_TYPED_08_MalformedExtensionsRefused(t *testing.T) {
	_, aliceHome, bobHome := twoIdentities(t)

	for name, args := range map[string][]string{
		"unnamespaced type":   {"--type", "widget"},
		"uppercase type":      {"--type", "Chat.Text"},
		"thread with space":   {"--thread", "thr one"},
		"expires not rfc3339": {"--expires", "next tuesday"},
		"uppercase meta key":  {"--meta", "Mime=image/png"},
		"meta without value":  {"--meta", "mime"},
	} {
		t.Run(name, func(t *testing.T) {
			args := append([]string{"send", "bob.poweur.net", "nope"}, args...)
			if code := runCLICode(t, aliceHome, args...); code == 0 {
				t.Fatalf("%s must be refused", name)
			}
		})
	}
	if msgs := inboxMessages(t, bobHome); len(msgs) != 0 {
		t.Fatalf("no malformed send may reach the recipient: %v", msgs)
	}
}

// TestINT_TYPED_09_AnonRefusesEnvelopeExtensions: an anonymous envelope
// carries no signature, so nothing binds these fields to a sender. Shipping
// them would be shipping routing metadata any relay on the path could rewrite.
func TestINT_TYPED_09_AnonRefusesEnvelopeExtensions(t *testing.T) {
	_, aliceHome, _ := twoIdentities(t)
	for _, args := range [][]string{
		{"--thread", "thr_x"},
		{"--type", "chat.attachment"},
		{"--meta", "mime=image/png"},
	} {
		full := append([]string{"send", "bob.poweur.net", "anonymously", "--anon"}, args...)
		if code := runCLICode(t, aliceHome, full...); code == 0 {
			t.Fatalf("--anon with %v must be refused", args)
		}
	}
}
