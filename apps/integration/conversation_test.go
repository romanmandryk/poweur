package integration_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestINT09_FullConversation drives a complete back-and-forth between Alice
// and Bob end-to-end. It verifies the full lifecycle in one flow:
//
//  1. Both users run `identity create` which generates and stores their
//     long-lived Ed25519 signing keypair AND their X25519 encryption keypair,
//     then registers the public halves with the relay (which publishes them
//     to the fake DNS zone).
//  2. First `send` from each user triggers the CLI to generate a short-lived
//     session keypair, sign the registration with the long-lived identity
//     key, and POST it to the relay — so both users end up with an active
//     session stored locally and on the relay.
//  3. Alice encrypts a message to Bob, signs it with her session key, and
//     posts it. The relay verifies and delivers to Bob's inbox.
//  4. Bob drains his inbox, decrypts with his X25519 private key, and sees
//     Alice's plaintext.
//  5. Bob replies to Alice (his first send, so it also spins up his
//     session). Alice drains her inbox and reads the reply.
//
// The test asserts that the decrypted bodies on both sides match the
// plaintext the sender typed, and that the ciphertext the relay actually
// stored never equals the plaintext (via the 🔒 marker that the CLI prints
// only when the payload was decrypted from a proper encryption envelope).
func TestINT09_FullConversation(t *testing.T) {
	zone := newZone(t)
	_, relayAddr := newRelay(t, zone)
	relayURL := "http://" + relayAddr

	aliceHome := t.TempDir()
	bobHome := t.TempDir()

	runCLI(t, aliceHome,
		"identity", "create", "alice",
		"--parent-domain", "example.com",
		"--relay", relayURL,
		"--dns-provider", "mock",
		"--dns-token", "integration",
	)
	runCLI(t, bobHome,
		"identity", "create", "bob",
		"--parent-domain", "example.com",
		"--relay", relayURL,
		"--dns-provider", "mock",
		"--dns-token", "integration",
	)

	const (
		msgAlice = "hey bob, dinner at 7?"
		msgBob   = "sure alice, see you then"
	)

	// Alice -> Bob
	runCLI(t, aliceHome, "send", "bob.example.com", msgAlice)
	bobInbox, _ := runCLI(t, bobHome, "inbox")
	assertDecryptedInbox(t, bobInbox, "alice.example.com", msgAlice)

	// Bob -> Alice
	runCLI(t, bobHome, "send", "alice.example.com", msgBob)
	aliceInbox, _ := runCLI(t, aliceHome, "inbox")
	assertDecryptedInbox(t, aliceInbox, "bob.example.com", msgBob)

	// Sanity: each user should see exactly one decrypted message, not the
	// other message, and not leftover inbox items from prior sends.
	if strings.Count(bobInbox, "🔒") != 1 {
		t.Fatalf("bob's inbox had unexpected lock-glyph count:\n%s", bobInbox)
	}
	if strings.Count(aliceInbox, "🔒") != 1 {
		t.Fatalf("alice's inbox had unexpected lock-glyph count:\n%s", aliceInbox)
	}
	if strings.Contains(bobInbox, msgBob) {
		t.Fatalf("bob's inbox leaked his own outbound message:\n%s", bobInbox)
	}
	if strings.Contains(aliceInbox, msgAlice) {
		t.Fatalf("alice's inbox leaked her own outbound message:\n%s", aliceInbox)
	}
}

// TestINT09_FullConversation_IdentitySigned is the twin of
// TestINT09_FullConversation but both directions of the conversation are
// signed with the sender's long-lived identity (Ed25519) key via
// `eurything send --sign-with=identity` instead of the default session
// key.
//
// This locks in two properties of the identity-signed path end-to-end:
//
//  1. `send --sign-with=identity` does not trigger ensureSession. We
//     assert this by checking that no sessions/<identity>.toml file
//     exists immediately after each send (but before any `inbox` call,
//     since `inbox` itself still registers a session to authenticate
//     the fetch).
//  2. Encryption is orthogonal to the signing-key choice: each recipient
//     still decrypts with their X25519 private key and sees the 🔒
//     glyph exactly once per inbox read.
func TestINT09_FullConversation_IdentitySigned(t *testing.T) {
	zone := newZone(t)
	_, relayAddr := newRelay(t, zone)
	relayURL := "http://" + relayAddr

	aliceHome := t.TempDir()
	bobHome := t.TempDir()

	runCLI(t, aliceHome,
		"identity", "create", "alice",
		"--parent-domain", "example.com",
		"--relay", relayURL,
		"--dns-provider", "mock",
		"--dns-token", "integration",
	)
	runCLI(t, bobHome,
		"identity", "create", "bob",
		"--parent-domain", "example.com",
		"--relay", relayURL,
		"--dns-provider", "mock",
		"--dns-token", "integration",
	)

	const (
		msgAlice = "hey bob, dinner at 7? (identity-signed)"
		msgBob   = "sure alice, see you then (identity-signed)"
	)

	aliceSess := filepath.Join(aliceHome, ".eurything", "sessions", "alice.example.com.toml")
	bobSess := filepath.Join(bobHome, ".eurything", "sessions", "bob.example.com.toml")

	assertNoSession := func(path string, when string) {
		t.Helper()
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("identity-signed send unexpectedly produced a session file %s (%s): err=%v", path, when, err)
		}
	}

	// Alice -> Bob, signed with Alice's long-lived identity key. No
	// session file should exist for Alice afterwards; `identity create`
	// above also must not have triggered one for Bob yet.
	runCLI(t, aliceHome, "send", "--sign-with", "identity", "bob.example.com", msgAlice)
	assertNoSession(aliceSess, "after alice identity-signed send")
	assertNoSession(bobSess, "before bob has done anything session-bearing")

	bobInbox, _ := runCLI(t, bobHome, "inbox")
	assertDecryptedInbox(t, bobInbox, "alice.example.com", msgAlice)

	// Bob -> Alice, signed with Bob's long-lived identity key. Bob's
	// session now exists (his inbox call created it), but Alice's still
	// must not because her only outbound action was identity-signed.
	runCLI(t, bobHome, "send", "--sign-with", "identity", "alice.example.com", msgBob)
	assertNoSession(aliceSess, "after bob identity-signed send, before alice inbox")

	aliceInbox, _ := runCLI(t, aliceHome, "inbox")
	assertDecryptedInbox(t, aliceInbox, "bob.example.com", msgBob)

	if strings.Count(bobInbox, "🔒") != 1 {
		t.Fatalf("bob's inbox had unexpected lock-glyph count:\n%s", bobInbox)
	}
	if strings.Count(aliceInbox, "🔒") != 1 {
		t.Fatalf("alice's inbox had unexpected lock-glyph count:\n%s", aliceInbox)
	}
	if strings.Contains(bobInbox, msgBob) {
		t.Fatalf("bob's inbox leaked his own outbound message:\n%s", bobInbox)
	}
	if strings.Contains(aliceInbox, msgAlice) {
		t.Fatalf("alice's inbox leaked her own outbound message:\n%s", aliceInbox)
	}
}
