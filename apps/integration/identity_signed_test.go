// Integration tests for the `eurything send --sign-with=identity` path.
//
// Under this mode the CLI skips session registration entirely and signs the
// outbound message with the long-lived identity Ed25519 key. Encryption is
// unchanged — the recipient's X25519 static key still receives an ephemeral
// ECDH handshake per message — only the envelope's signing key changes.
//
// The relay already supported identity-signed envelopes via
// `resolveSigningKey` when `session_id` is empty; these tests confirm the
// end-to-end flow works both on a single relay and across two relays.
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

	clipkg "github.com/eurything/cli/pkg/cli"
)

// TestINT_IDSIGN_01_SameRelayIdentitySignedSend: Alice and Bob are on the
// same relay. Alice sends with `--sign-with=identity`. The relay accepts
// the identity-signed envelope, Bob decrypts. Alice's local session file
// MUST NOT be created by this send (the whole point is to skip sessions).
func TestINT_IDSIGN_01_SameRelayIdentitySignedSend(t *testing.T) {
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

	secret := "identity-signed hello"
	runCLI(t, aliceHome, "send", "--sign-with", "identity", "bob.example.com", secret)

	// No local session file should exist for Alice because we never went
	// through ensureSession on this path.
	aliceSess := filepath.Join(aliceHome, ".eurything", "sessions", "alice.example.com.toml")
	if _, err := os.Stat(aliceSess); !os.IsNotExist(err) {
		t.Fatalf("expected no session file after --sign-with=identity, got err=%v", err)
	}

	inbox, _ := runCLI(t, bobHome, "inbox")
	assertDecryptedInbox(t, inbox, "alice.example.com", secret)
}

// TestINT_IDSIGN_02_CrossRelayIdentitySignedSend: Alice on relay-A, Bob on
// relay-B. Alice sends with `--sign-with=identity`. Relay-A verifies using
// its local identity cache; it then forwards to relay-B which has no
// knowledge of Alice and resolves her identity key via the shared DNS
// zone (or via a peer `GET /identities/<sender>`). Bob decrypts.
func TestINT_IDSIGN_02_CrossRelayIdentitySignedSend(t *testing.T) {
	zone := newZone(t)
	_, relayA := newRelay(t, zone)
	_, relayB := newRelay(t, zone)
	if relayA == relayB {
		t.Fatalf("both relays bound to the same address: %s", relayA)
	}

	aliceHome := t.TempDir()
	bobHome := t.TempDir()

	runCLI(t, aliceHome, "identity", "create", "alice",
		"--parent-domain", "example.com", "--relay", "http://"+relayA,
		"--dns-provider", "mock", "--dns-token", "integration")
	runCLI(t, bobHome, "identity", "create", "bob",
		"--parent-domain", "example.org", "--relay", "http://"+relayB,
		"--dns-provider", "mock", "--dns-token", "integration")

	secret := "cross-relay identity-signed"
	runCLI(t, aliceHome, "send", "--sign-with", "identity", "bob.example.org", secret)

	inbox, _ := runCLI(t, bobHome, "inbox")
	assertDecryptedInbox(t, inbox, "alice.example.com", secret)
}

// TestINT_IDSIGN_03_ForgedIdentitySignatureRejected: posting a well-formed
// identity-signed envelope (empty session_id) with a signature produced by
// a key nobody in the system holds must be rejected with 401 and must not
// reach Bob's inbox.
func TestINT_IDSIGN_03_ForgedIdentitySignatureRejected(t *testing.T) {
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

	_, forgedPriv, _ := ed25519.GenerateKey(nil)
	timestamp := time.Now().UTC().Format(time.RFC3339)
	forged := map[string]any{
		"id":        "msg_forged_idsign03",
		"sender":    "alice.example.com",
		"recipient": "bob.example.com",
		"timestamp": timestamp,
		"payload":   "forged identity-signed attempt",
		"signature": base64.StdEncoding.EncodeToString(ed25519.Sign(forgedPriv, []byte("not-the-canonical-message"))),
		// session_id intentionally omitted so the relay takes the identity
		// verification path (same as --sign-with=identity).
		"encryption": map[string]string{
			"alg":                  "x25519-chacha20-poly1305",
			"ephemeral_public_key": "ephemeral-pub",
			"nonce":                "nonce",
		},
	}
	body, _ := json.Marshal(forged)
	resp, err := http.Post(relayURL+"/messages", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post forged identity-signed message: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for forged identity signature, got %d", resp.StatusCode)
	}

	// Drive Bob's inbox directly so a clean run has no leftover state from
	// earlier sends. We rely on runCLI's non-zero-exit guard.
	t.Setenv("HOME", bobHome)
	var stdout, stderr bytes.Buffer
	if code := clipkg.Run([]string{"inbox"}, &stdout, &stderr); code != 0 {
		t.Fatalf("inbox: exit %d stderr=%s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "forged identity-signed attempt") {
		t.Fatalf("forged identity-signed message leaked to bob's inbox:\n%s", stdout.String())
	}
}
