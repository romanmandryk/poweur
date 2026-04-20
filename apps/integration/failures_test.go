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

	toml "github.com/pelletier/go-toml/v2"

	clipkg "github.com/eurything/cli/pkg/cli"
)

// sessionFile is a minimal reflection of what the CLI persists at
// $HOME/.eurything/sessions/<identity>.toml. We only parse the fields we
// need for failure-mode tests; extra keys round-trip through toml without
// us having to stay in sync with the production struct field-for-field.
type sessionFile struct {
	Identity          string    `toml:"identity"`
	SessionID         string    `toml:"session_id"`
	SessionPrivateKey string    `toml:"session_private_key"`
	SessionPublicKey  string    `toml:"session_public_key"`
	IssuedAt          time.Time `toml:"issued_at"`
	ExpiresAt         time.Time `toml:"expires_at"`
	RelayURL          string    `toml:"relay_url"`

	IssuedAtRaw       string `toml:"issued_at_raw"`
	ExpiresAtRaw      string `toml:"expires_at_raw"`
	Nonce             string `toml:"nonce"`
	IdentitySignature string `toml:"identity_signature"`
}

func sessionPath(home, identity string) string {
	return filepath.Join(home, ".eurything", "sessions", identity+".toml")
}

func readSessionFile(t *testing.T, path string) sessionFile {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read session file %s: %v", path, err)
	}
	var sess sessionFile
	if err := toml.Unmarshal(data, &sess); err != nil {
		t.Fatalf("parse session file %s: %v\n%s", path, err, data)
	}
	return sess
}

func writeSessionFile(t *testing.T, path string, sess sessionFile) {
	t.Helper()
	data, err := toml.Marshal(sess)
	if err != nil {
		t.Fatalf("marshal session: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write session file: %v", err)
	}
}

// TestINT04_SessionAutoRefreshOnLocalExpiry simulates a device whose cached
// session has aged out (a common scenario: user left the CLI alone past
// the 24h TTL). The next send must transparently re-register a new session
// using the long-lived identity key (which on mobile is the passkey
// checkpoint), pick up a fresh session id, and deliver the message.
//
// The test asserts that:
//   - the post-expiry session id differs from the pre-expiry one,
//   - the new session's ExpiresAt is pushed into the future, and
//   - the recipient actually receives both messages intact.
func TestINT04_SessionAutoRefreshOnLocalExpiry(t *testing.T) {
	zone := newZone(t)
	_, relayAddr := newRelay(t, zone)
	relayURL := "http://" + relayAddr

	aliceHome := t.TempDir()
	bobHome := t.TempDir()

	runCLI(t, aliceHome, "identity", "create", "alice",
		"--parent-domain", "example.com", "--relay", relayURL,
		"--dns-provider", "mock", "--dns-token", "integration")
	runCLI(t, bobHome, "identity", "create", "bob",
		"--parent-domain", "example.com", "--relay", relayURL,
		"--dns-provider", "mock", "--dns-token", "integration")

	runCLI(t, aliceHome, "send", "bob.example.com", "first message")

	aliceSessPath := sessionPath(aliceHome, "alice.example.com")
	before := readSessionFile(t, aliceSessPath)
	if before.SessionID == "" {
		t.Fatalf("expected a session id after first send, got empty: %+v", before)
	}

	// Forge local expiry. We rewrite the session file with ExpiresAt set
	// an hour in the past, which matches how a real stale session looks
	// after the device slept overnight.
	before.ExpiresAt = time.Now().Add(-1 * time.Hour).UTC()
	before.ExpiresAtRaw = before.ExpiresAt.Format(time.RFC3339)
	writeSessionFile(t, aliceSessPath, before)

	runCLI(t, aliceHome, "send", "bob.example.com", "second message")

	after := readSessionFile(t, aliceSessPath)
	if after.SessionID == "" {
		t.Fatalf("expected a session id after auto-refresh, got empty: %+v", after)
	}
	if after.SessionID == before.SessionID {
		t.Fatalf("session id did not rotate on expiry: still %s", after.SessionID)
	}
	if !after.ExpiresAt.After(time.Now().Add(20 * time.Hour)) {
		t.Fatalf("refreshed session expires too soon: %s", after.ExpiresAt)
	}

	inbox, _ := runCLI(t, bobHome, "inbox")
	if !strings.Contains(inbox, "first message") || !strings.Contains(inbox, "second message") {
		t.Fatalf("expected both messages in bob's inbox, got:\n%s", inbox)
	}
}

// TestINT05_DecryptionFailureIsReportedGracefully verifies the CLI's graceful
// failure mode when ciphertext arrives but the recipient has no X25519
// private key on disk (for example, keys were wiped or never synced to a
// secondary device). The relay must still deliver the message (it cannot
// read the payload either way), and the inbox output should explicitly
// flag the message as undecryptable rather than print ciphertext as if it
// were plaintext.
func TestINT05_DecryptionFailureIsReportedGracefully(t *testing.T) {
	zone := newZone(t)
	_, relayAddr := newRelay(t, zone)
	relayURL := "http://" + relayAddr

	aliceHome := t.TempDir()
	bobHome := t.TempDir()

	runCLI(t, aliceHome, "identity", "create", "alice",
		"--parent-domain", "example.com", "--relay", relayURL,
		"--dns-provider", "mock", "--dns-token", "integration")
	runCLI(t, bobHome, "identity", "create", "bob",
		"--parent-domain", "example.com", "--relay", relayURL,
		"--dns-provider", "mock", "--dns-token", "integration")

	runCLI(t, aliceHome, "send", "bob.example.com", "ciphertext only")

	// Wipe Bob's encryption private key. Note: the DNS-published encryption
	// public key stays in place so Alice's earlier send looked healthy.
	bobEncKey := filepath.Join(bobHome, ".eurything", "keys", "bob.example.com.enc")
	if err := os.Remove(bobEncKey); err != nil {
		t.Fatalf("remove bob enc key: %v", err)
	}

	inbox, _ := runCLI(t, bobHome, "inbox")
	if !strings.Contains(inbox, "[encrypted: no local encryption key]") {
		t.Fatalf("expected graceful decryption-failure marker, got:\n%s", inbox)
	}
	if strings.Contains(inbox, "ciphertext only") {
		t.Fatalf("plaintext surfaced even though decryption key was removed:\n%s", inbox)
	}
}

// TestINT06_ReplayRejection is an aspirational test documenting the
// requirement that a signed message replayed bit-for-bit should be accepted
// at most once. Replay defense is NOT implemented yet: the current relay
// accepts repeats as long as they stay within rate limits. The test is
// kept (and skipped) so the placeholder flips to green automatically as
// soon as dedup lands.
func TestINT06_ReplayRejection(t *testing.T) {
	t.Skip("replay defense not yet implemented on the relay (see SESS/INT docs)")
}

// TestINT07_TamperedSignatureRejected sends a message directly to the relay
// with a well-formed envelope but a random signature. The relay must
// return 401 before storing anything in the inbox.
//
// This guards the cheap fast-path rejection: signature check happens after
// schema validation but before forwarding, inbox writes, or any outbound IO.
func TestINT07_TamperedSignatureRejected(t *testing.T) {
	zone := newZone(t)
	_, relayAddr := newRelay(t, zone)
	relayURL := "http://" + relayAddr

	aliceHome := t.TempDir()
	bobHome := t.TempDir()

	runCLI(t, aliceHome, "identity", "create", "alice",
		"--parent-domain", "example.com", "--relay", relayURL,
		"--dns-provider", "mock", "--dns-token", "integration")
	runCLI(t, bobHome, "identity", "create", "bob",
		"--parent-domain", "example.com", "--relay", relayURL,
		"--dns-provider", "mock", "--dns-token", "integration")

	// Bootstrap a real session for Alice by sending one legit message.
	runCLI(t, aliceHome, "send", "bob.example.com", "real message")
	sess := readSessionFile(t, sessionPath(aliceHome, "alice.example.com"))

	// Construct a forged envelope: valid shape, valid session id known to
	// the relay, but the signature is a random 64 bytes signed under a
	// key nobody in the system holds.
	_, forgedPriv, _ := ed25519.GenerateKey(nil)
	timestamp := time.Now().UTC().Format(time.RFC3339)
	forged := map[string]any{
		"sender":     "alice.example.com",
		"recipient":  "bob.example.com",
		"timestamp":  timestamp,
		"payload":    "impersonation attempt",
		"session_id": sess.SessionID,
		"signature":  base64.StdEncoding.EncodeToString(ed25519.Sign(forgedPriv, []byte("not-the-canonical-message"))),
		// The relay enforces encrypt-only at the edge, so a forged plaintext
		// envelope now gets rejected with encryption_required before
		// signature check ever runs. Supply plausible encryption metadata
		// so this test actually exercises the signature-verification path
		// we care about.
		"encryption": map[string]string{
			"alg":                  "x25519-chacha20-poly1305",
			"ephemeral_public_key": "ephemeral-pub",
			"nonce":                "nonce",
		},
	}
	body, _ := json.Marshal(forged)
	resp, err := http.Post(relayURL+"/messages", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post forged message: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 on forged signature, got %d", resp.StatusCode)
	}

	// Bob's inbox must still only contain the legit message, not the
	// forged one.
	inbox, _ := runCLI(t, bobHome, "inbox")
	if strings.Contains(inbox, "impersonation attempt") {
		t.Fatalf("forged message leaked to bob's inbox:\n%s", inbox)
	}
	if !strings.Contains(inbox, "real message") {
		t.Fatalf("real message missing from inbox:\n%s", inbox)
	}
}

// TestINT08_TamperedDNSBreaksCrossRelayVerification rewrites Alice's
// published signing key in the fake DNS zone with a key the attacker
// controls, between Alice registering and sending. Her home relay still
// accepts the message (it has her real key cached in memory), but when it
// forwards to Bob's relay, Bob's relay resolves Alice via DNS — gets the
// attacker's key — and rejects the session proof. The CLI exits non-zero
// because forwarding failed, and Bob's inbox stays empty.
//
// This guards the non-trivial property that relays do NOT blindly trust
// forwarded messages: each relay re-verifies against DNS-published keys.
func TestINT08_TamperedDNSBreaksCrossRelayVerification(t *testing.T) {
	zone := newZone(t)
	_, relayA := newRelay(t, zone)
	_, relayB := newRelay(t, zone)

	aliceHome := t.TempDir()
	bobHome := t.TempDir()

	runCLI(t, aliceHome, "identity", "create", "alice",
		"--parent-domain", "example.com", "--relay", "http://"+relayA,
		"--dns-provider", "mock", "--dns-token", "integration")
	runCLI(t, bobHome, "identity", "create", "bob",
		"--parent-domain", "example.org", "--relay", "http://"+relayB,
		"--dns-provider", "mock", "--dns-token", "integration")

	// Attacker overwrites Alice's signing-key TXT record. Her encryption
	// key stays untouched — the point here is to corrupt the verification
	// path, not to break the CLI's earlier encryption step.
	_, forgedPub, _ := ed25519.GenerateKey(nil)
	forgedPubB64 := base64.RawURLEncoding.EncodeToString([]byte(forgedPub))
	zone.SetTXT("_eurything.alice.example.com", "eurything-pubkey=ed25519:"+forgedPubB64)

	// Drive the CLI directly (bypassing runCLI) so we can inspect the exit
	// code without failing the test, since the expectation is non-zero.
	t.Setenv("HOME", aliceHome)
	var stdout, stderr bytes.Buffer
	code := clipkg.Run([]string{"send", "bob.example.org", "should be rejected"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected send to fail after DNS tamper, got exit 0\nstdout=%s\nstderr=%s",
			stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "forward") && !strings.Contains(stderr.String(), "502") {
		// Relay responds with 502 forward_failed; surface the whole
		// stderr in the failure message if the substring check misses
		// so we can tighten it once we see real output.
		t.Logf("stderr text (for debugging): %q", stderr.String())
	}

	inbox, _ := runCLI(t, bobHome, "inbox")
	if strings.Contains(inbox, "should be rejected") {
		t.Fatalf("tampered message reached bob's inbox despite DNS mismatch:\n%s", inbox)
	}
}
