// Integration tests for the relay's at-least-one-local forwarding rule.
//
// The rule, enforced uniformly by POST /messages and POST /acks:
//
//	senderLocal    = identityStore.Exists(sender)    || dns(sender)    points here
//	recipientLocal = identityStore.Exists(recipient) || dns(recipient) points here
//	accept iff senderLocal || recipientLocal
//
// This file covers the four shape-of-traffic cases: note-to-self (both
// local), default direct send (recipient-local), --via-home-relay (sender-
// local + recipient-remote), and the "neither local" case which must be
// rejected with 403 not_authorized so the relay never serves as an open
// forwarder for the world.
package integration_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestINT_FWD_01_NoteToSelf: Alice sends to herself on her home relay.
// Both sender and recipient are local on the same relay, so the message
// is stored in Alice's inbox and she decrypts it on the next poll.
func TestINT_FWD_01_NoteToSelf(t *testing.T) {
	zone := newZone(t)
	_, relayAddr := newRelay(t, zone)

	aliceHome := t.TempDir()
	runCLI(t, aliceHome, "identity", "create", "alice",
		"--parent-domain", "example.com", "--relay", "http://"+relayAddr,
		"--dns-provider", "mock", "--dns-token", "integration")

	secret := "note-to-self memo"
	runCLI(t, aliceHome, "send", "alice.example.com", secret)

	stdout, _ := runCLI(t, aliceHome, "inbox")
	assertDecryptedInbox(t, stdout, "alice.example.com", secret)
}

// TestINT_FWD_02_NeitherLocalRejected posts a structurally-valid encrypted
// message envelope directly to a third relay where neither the sender nor
// the recipient is hosted. The relay must reject with 403 not_authorized
// before signature verification — the at-least-one-local rule is a cheap
// reject that prevents the relay from ever functioning as an open
// forwarder.
//
// We bypass the CLI here on purpose: the CLI always resolves the recipient
// relay via DNS, so it never produces this third-relay traffic. A real
// attacker would though, and that's exactly what the rule is for.
func TestINT_FWD_02_NeitherLocalRejected(t *testing.T) {
	zone := newZone(t)
	_, relayA := newRelay(t, zone)
	_, relayB := newRelay(t, zone)
	_, relayC := newRelay(t, zone)
	if relayA == relayC || relayB == relayC {
		t.Fatalf("relay address collision: A=%s B=%s C=%s", relayA, relayB, relayC)
	}

	aliceHome := t.TempDir()
	bobHome := t.TempDir()
	runCLI(t, aliceHome, "identity", "create", "alice",
		"--parent-domain", "example.com", "--relay", "http://"+relayA,
		"--dns-provider", "mock", "--dns-token", "integration")
	runCLI(t, bobHome, "identity", "create", "bob",
		"--parent-domain", "example.org", "--relay", "http://"+relayB,
		"--dns-provider", "mock", "--dns-token", "integration")

	envelope := map[string]any{
		"id":        "msg_neither_local",
		"sender":    "alice.example.com",
		"recipient": "bob.example.org",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"payload":   "should-be-rejected ciphertext",
		"signature": "AAAA",
		"encryption": map[string]string{
			"alg":                  "x25519-chacha20-poly1305",
			"ephemeral_public_key": "ephemeral-pub",
			"nonce":                "nonce",
		},
	}
	body, _ := json.Marshal(envelope)
	resp, err := http.Post("http://"+relayC+"/messages", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post to third relay: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 not_authorized when neither party is local, got %d", resp.StatusCode)
	}

	var errBody struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&errBody); err == nil {
		if !strings.Contains(errBody.Error, "not_authorized") {
			t.Fatalf("expected error code not_authorized, got %q", errBody.Error)
		}
	}
}

// TestINT_FWD_03_AcksAtLeastOneLocal posts a structurally-valid ack to a
// third relay where neither the ack's sender (Bob) nor the ack's recipient
// (Alice) is hosted. The ack endpoint shares the at-least-one-local rule
// with /messages, so the response must be 403 not_authorized.
func TestINT_FWD_03_AcksAtLeastOneLocal(t *testing.T) {
	zone := newZone(t)
	_, relayA := newRelay(t, zone)
	_, relayB := newRelay(t, zone)
	_, relayC := newRelay(t, zone)

	aliceHome := t.TempDir()
	bobHome := t.TempDir()
	runCLI(t, aliceHome, "identity", "create", "alice",
		"--parent-domain", "example.com", "--relay", "http://"+relayA,
		"--dns-provider", "mock", "--dns-token", "integration")
	runCLI(t, bobHome, "identity", "create", "bob",
		"--parent-domain", "example.org", "--relay", "http://"+relayB,
		"--dns-provider", "mock", "--dns-token", "integration")

	ack := map[string]any{
		"type":       "ack",
		"id":         "ack_neither_local",
		"message_id": "msg_irrelevant",
		"state":      "delivered_client",
		"sender":     "bob.example.org",
		"recipient":  "alice.example.com",
		"timestamp":  time.Now().UTC().Format(time.RFC3339),
		"signature":  "AAAA",
	}
	body, _ := json.Marshal(ack)
	resp, err := http.Post("http://"+relayC+"/acks", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post ack to third relay: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 not_authorized for neither-local ack, got %d", resp.StatusCode)
	}
}
