// Integration tests for the WhatsApp-style two-tick delivery model.
//
// Tick 1 (delivered_recipient_relay) is recorded when the recipient relay
// returns 202 to POST /messages. Tick 2 (delivered_client) is recorded
// when the recipient's client decrypts the message and POSTs a signed ack
// back to the original sender's home relay; the sender learns of tick 2
// the next time it polls its own /messages endpoint and drains the acks
// array. Failures, offline-recipient stalls, and forged acks each pin the
// state to a different point on the same machine.
package integration_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// extractFirstMessageID reads the per-identity pending journal directly
// from the home tempdir (we already know its layout: $HOME/.eurything/
// pending/<identity>.jsonl). It returns the first non-empty message id
// found, which is enough for tests that only send a single message.
func extractFirstMessageID(t *testing.T, home, identity string) string {
	t.Helper()
	t.Setenv("HOME", home)
	stdout, _ := runCLI(t, home, "messages", "status", "--json")
	var statuses []map[string]any
	if err := json.Unmarshal([]byte(stdout), &statuses); err != nil {
		t.Fatalf("decode messages status json: %v\n%s", err, stdout)
	}
	if len(statuses) == 0 {
		t.Fatalf("no pending messages in journal for %s", identity)
	}
	id, _ := statuses[0]["message_id"].(string)
	if id == "" {
		t.Fatalf("first journal entry has no message_id: %+v", statuses[0])
	}
	return id
}

// statusForID returns the per-message status (latest state, etc) by
// running `eurything messages status --json --id <id>` and parsing the
// single-element output.
func statusForID(t *testing.T, home, id string) map[string]any {
	t.Helper()
	stdout, _ := runCLI(t, home, "messages", "status", "--json", "--id", id)
	var statuses []map[string]any
	if err := json.Unmarshal([]byte(stdout), &statuses); err != nil {
		t.Fatalf("decode status json for %s: %v\n%s", id, err, stdout)
	}
	if len(statuses) != 1 {
		t.Fatalf("expected exactly one status entry for %s, got %d:\n%s", id, len(statuses), stdout)
	}
	return statuses[0]
}

// TestINT_DLV_01_Tick1OnSuccessfulSend: after a default direct send Alice's
// pending journal records tick 1 (delivered_recipient_relay) for the
// outbound message. No tick 2 yet because Bob hasn't polled.
func TestINT_DLV_01_Tick1OnSuccessfulSend(t *testing.T) {
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

	runCLI(t, aliceHome, "send", "bob.example.org", "tick-test")

	id := extractFirstMessageID(t, aliceHome, "alice.example.com")
	status := statusForID(t, aliceHome, id)
	if got, _ := status["state"].(string); got != "delivered_recipient_relay" {
		t.Fatalf("expected tick 1 state delivered_recipient_relay, got %q (%+v)", got, status)
	}
}

// TestINT_DLV_02_Tick2AfterRecipientPolls: once Bob runs `inbox` he
// decrypts the message and posts an ack to Alice's home relay. The next
// time Alice runs `inbox` she drains the acks array and her journal
// advances to delivered_client (tick 2).
func TestINT_DLV_02_Tick2AfterRecipientPolls(t *testing.T) {
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

	runCLI(t, aliceHome, "send", "bob.example.org", "two-tick test")
	id := extractFirstMessageID(t, aliceHome, "alice.example.com")

	// Bob polls — this decrypts and emits the delivered_client ack.
	inbox, _ := runCLI(t, bobHome, "inbox")
	assertDecryptedInbox(t, inbox, "alice.example.com", "two-tick test")

	// Alice polls — this drains the acks array and advances her journal.
	runCLI(t, aliceHome, "inbox")

	status := statusForID(t, aliceHome, id)
	if got, _ := status["state"].(string); got != "delivered_client" {
		t.Fatalf("expected tick 2 state delivered_client, got %q (%+v)", got, status)
	}
}

// TestINT_DLV_03_OfflineRecipientStaysAtTick1: if Bob never polls his
// inbox, no ack is generated, and Alice's pending journal stays pinned at
// delivered_recipient_relay no matter how many times she polls her own
// inbox. This is the "single tick" case in the WhatsApp metaphor.
func TestINT_DLV_03_OfflineRecipientStaysAtTick1(t *testing.T) {
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

	runCLI(t, aliceHome, "send", "bob.example.org", "still offline")
	id := extractFirstMessageID(t, aliceHome, "alice.example.com")

	for i := 0; i < 3; i++ {
		runCLI(t, aliceHome, "inbox")
	}

	status := statusForID(t, aliceHome, id)
	if got, _ := status["state"].(string); got != "delivered_recipient_relay" {
		t.Fatalf("expected stuck-at-tick-1 state delivered_recipient_relay, got %q (%+v)", got, status)
	}
}

// TestINT_DLV_04_ForgedAckRejected posts a structurally-valid ack envelope
// (correct fields, plausible state) but signed by a key nobody in the
// system holds. Alice's home relay must reject with 401 unauthorized and
// Alice's journal must stay at tick 1 — a forged ack cannot fast-forward
// Alice's view of the world.
func TestINT_DLV_04_ForgedAckRejected(t *testing.T) {
	zone := newZone(t)
	_, relayA := newRelay(t, zone)
	_, relayB := newRelay(t, zone)
	relayURL := "http://" + relayA

	aliceHome := t.TempDir()
	bobHome := t.TempDir()
	runCLI(t, aliceHome, "identity", "create", "alice",
		"--parent-domain", "example.com", "--relay", relayURL,
		"--dns-provider", "mock", "--dns-token", "integration")
	runCLI(t, bobHome, "identity", "create", "bob",
		"--parent-domain", "example.org", "--relay", "http://"+relayB,
		"--dns-provider", "mock", "--dns-token", "integration")

	runCLI(t, aliceHome, "send", "bob.example.org", "ack me if real")
	id := extractFirstMessageID(t, aliceHome, "alice.example.com")

	forged := map[string]any{
		"type":       "ack",
		"id":         "ack_forged",
		"message_id": id,
		"state":      "delivered_client",
		"sender":     "bob.example.org",
		"recipient":  "alice.example.com",
		"timestamp":  time.Now().UTC().Format(time.RFC3339),
		"signature":  "AAAA",
	}
	body, _ := json.Marshal(forged)
	resp, err := http.Post(relayURL+"/acks", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post forged ack: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 401 on forged ack, got %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	runCLI(t, aliceHome, "inbox")
	status := statusForID(t, aliceHome, id)
	if got, _ := status["state"].(string); got == "delivered_client" {
		t.Fatalf("forged ack should not advance journal; got state %q", got)
	}
}
