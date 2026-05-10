// Integration tests for the default "direct to recipient relay" send path
// and its --via-home-relay opt-in privacy proxy variant.
//
// In v1 of the protocol the sender's home relay is no longer a mandatory
// hop on the outbound path. Alice's CLI looks up Bob's home relay via DNS
// and POSTs the message there directly, so Alice's home relay never sees
// the outbound traffic. The opt-in --via-home-relay flag restores the old
// behaviour for users who would rather hide their IP from Bob's relay than
// from their own.
package integration_test

import (
	"testing"
)

// TestINT_SEND_01_DirectSendBypassesHomeRelay drives the default send path
// (no --via-home-relay) across two relays and asserts that:
//
//   - Alice's home relay (relayA) sees zero POST /messages traffic during
//     the send. (It still sees POST /sessions while Alice registers her
//     short-lived session key — that's expected.)
//   - Bob's home relay (relayB) accepts the message because Bob is local
//     to it (recipient-local arm of the at-least-one-local rule).
//   - Bob's inbox decrypts the ciphertext.
func TestINT_SEND_01_DirectSendBypassesHomeRelay(t *testing.T) {
	zone := newZone(t)
	relayA, counterA := newRelayWithCounter(t, zone)
	relayB, counterB := newRelayWithCounter(t, zone)
	if relayA == relayB {
		t.Fatalf("both relays bound to the same address: %s", relayA)
	}

	aliceHome := t.TempDir()
	bobHome := t.TempDir()

	runCLI(t, aliceHome, "identity", "create", "alice",
		"--parent-domain", "poweur.net", "--relay", "http://"+relayA,
		"--dns-provider", "mock", "--dns-token", "integration")
	runCLI(t, bobHome, "identity", "create", "bob",
		"--parent-domain", "example.org", "--relay", "http://"+relayB,
		"--dns-provider", "mock", "--dns-token", "integration")

	baselineA := counterA.PostMessages()
	baselineB := counterB.PostMessages()

	secret := "direct-send hello"
	runCLI(t, aliceHome, "send", "bob.example.org", secret)

	if got := counterA.PostMessages() - baselineA; got != 0 {
		t.Fatalf("expected zero POST /messages on Alice's home relay in default send mode, got %d", got)
	}
	if got := counterB.PostMessages() - baselineB; got != 1 {
		t.Fatalf("expected exactly one POST /messages on Bob's home relay, got %d", got)
	}

	stdout, _ := runCLI(t, bobHome, "inbox")
	assertDecryptedInbox(t, stdout, "alice.poweur.net", secret)
}

// TestINT_SEND_02_ViaHomeRelayRoutesThroughHome flips the routing knob:
// with --via-home-relay set, Alice's CLI posts to her home relay, which
// accepts because the sender is local and forwards to Bob's relay.
//
// We expect:
//   - Alice's home relay sees exactly one POST /messages (the client → home
//     hop).
//   - Bob's home relay also sees one POST /messages (the home → recipient
//     forward).
//   - Bob's inbox decrypts the ciphertext.
func TestINT_SEND_02_ViaHomeRelayRoutesThroughHome(t *testing.T) {
	zone := newZone(t)
	relayA, counterA := newRelayWithCounter(t, zone)
	relayB, counterB := newRelayWithCounter(t, zone)
	if relayA == relayB {
		t.Fatalf("both relays bound to the same address: %s", relayA)
	}

	aliceHome := t.TempDir()
	bobHome := t.TempDir()

	runCLI(t, aliceHome, "identity", "create", "alice",
		"--parent-domain", "poweur.net", "--relay", "http://"+relayA,
		"--dns-provider", "mock", "--dns-token", "integration")
	runCLI(t, bobHome, "identity", "create", "bob",
		"--parent-domain", "example.org", "--relay", "http://"+relayB,
		"--dns-provider", "mock", "--dns-token", "integration")

	baselineA := counterA.PostMessages()
	baselineB := counterB.PostMessages()

	secret := "via-home-relay hello"
	runCLI(t, aliceHome, "send", "--via-home-relay", "bob.example.org", secret)

	if got := counterA.PostMessages() - baselineA; got != 1 {
		t.Fatalf("expected exactly one POST /messages on Alice's home relay in --via-home-relay mode, got %d", got)
	}
	if got := counterB.PostMessages() - baselineB; got != 1 {
		t.Fatalf("expected exactly one forwarded POST /messages on Bob's home relay, got %d", got)
	}

	stdout, _ := runCLI(t, bobHome, "inbox")
	assertDecryptedInbox(t, stdout, "alice.poweur.net", secret)
}
