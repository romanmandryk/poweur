// Integration coverage for `poweur listen` (EPIC-009 E09-T2): the Go CLI's
// push client against a real relay's SSE endpoint.
package integration_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	clipkg "github.com/poweur/cli/pkg/cli"
)

// TestINT_LISTEN_01: a message waiting at the cursor is picked up the moment
// the stream connects, and the pickup is the same one `poweur inbox`
// performs — decrypted, and archived to history.
//
// The catch-up path is what this pins, deliberately. The epic's rule is "the
// socket is notification, the cursor is truth": if catching up on connect
// did not deliver, then every reconnect would lose whatever arrived while
// the stream was down, and the transport would silently become the delivery
// guarantee it is explicitly not.
func TestINT_LISTEN_01_CatchesUpOnConnect(t *testing.T) {
	zone := newZone(t)
	_, relayAddr := newRelay(t, zone)
	relayURL := "http://" + relayAddr

	aliceHome := t.TempDir()
	bobHome := t.TempDir()
	for home, name := range map[string]string{aliceHome: "alice", bobHome: "bob"} {
		runCLI(t, home, "identity", "create", name,
			"--parent-domain", "poweur.net", "--relay", relayURL,
			"--dns-provider", "mock", "--dns-token", "integration")
	}

	const msg = "listen picks this up on connect"
	runCLI(t, aliceHome, "send", "bob.poweur.net", msg)

	stdout, stderr := runListenOnce(t, bobHome, 30*time.Second)
	if !strings.Contains(stdout, msg) {
		t.Fatalf("listen did not deliver the waiting message.\nstdout: %s\nstderr: %s", stdout, stderr)
	}
	// The lock glyph is how the CLI says it decrypted rather than printed
	// ciphertext, so its presence is the proof the pickup was a real one.
	if !strings.Contains(stdout, "🔒") {
		t.Fatalf("listen printed an undecrypted payload:\n%s", stdout)
	}
	if !strings.Contains(stderr, "listening as bob.poweur.net") {
		t.Fatalf("listen never announced itself: %s", stderr)
	}

	// The message was consumed, exactly as a poll would have consumed it —
	// the drain runs the same renderInboxPayload as `poweur inbox`, so the
	// tick-2 ack and the history archive are the same code path too.
	inbox, _ := runCLI(t, bobHome, "inbox")
	if strings.Contains(inbox, msg) {
		t.Fatalf("listen left the message in the inbox:\n%s", inbox)
	}
	if !strings.Contains(inbox, "no messages") {
		t.Fatalf("listen left something behind:\n%s", inbox)
	}
}

// TestINT_LISTEN_02: --json hands over the relay's own wire shape, so a
// script consuming `poweur listen --json` sees exactly what `poweur inbox
// --json` would have.
func TestINT_LISTEN_02_JSONOutput(t *testing.T) {
	zone := newZone(t)
	_, relayAddr := newRelay(t, zone)
	relayURL := "http://" + relayAddr

	aliceHome := t.TempDir()
	bobHome := t.TempDir()
	for home, name := range map[string]string{aliceHome: "alice", bobHome: "bob"} {
		runCLI(t, home, "identity", "create", name,
			"--parent-domain", "poweur.net", "--relay", relayURL,
			"--dns-provider", "mock", "--dns-token", "integration")
	}
	runCLI(t, aliceHome, "send", "bob.poweur.net", "json please")

	stdout, stderr := runListenOnce(t, bobHome, 30*time.Second, "--json")
	if !strings.Contains(stdout, `"messages"`) || !strings.Contains(stdout, `"encryption"`) {
		t.Fatalf("listen --json did not emit the inbox wire shape.\nstdout: %s\nstderr: %s", stdout, stderr)
	}
	// The stream itself carries no payload — anything readable came from the
	// cursor read, never from an event frame.
	if strings.Contains(stdout, `"type":"ready"`) {
		t.Fatalf("listen leaked stream frames into its output:\n%s", stdout)
	}
}

// TestINT_LISTEN_03_JSONDecrypt: --json --decrypt is what a bot runs so it
// never holds the identity's message key. stdout is the inbox wire shape plus
// each message's plaintext as `body`, nothing else; the pickup is otherwise a
// full one — consumed from the inbox (history archiving shares the human path's code) — and `inbox --json --decrypt`
// speaks the same shape.
func TestINT_LISTEN_03_JSONDecrypt(t *testing.T) {
	zone := newZone(t)
	_, relayAddr := newRelay(t, zone)
	relayURL := "http://" + relayAddr

	aliceHome := t.TempDir()
	bobHome := t.TempDir()
	for home, name := range map[string]string{aliceHome: "alice", bobHome: "bob"} {
		runCLI(t, home, "identity", "create", name,
			"--parent-domain", "poweur.net", "--relay", relayURL,
			"--dns-provider", "mock", "--dns-token", "integration")
	}

	type pickup struct {
		Messages []struct {
			Sender    string `json:"sender"`
			Payload   string `json:"payload"`
			Body      string `json:"body"`
			Decrypted bool   `json:"decrypted"`
		} `json:"messages"`
	}
	decode := func(out string) pickup {
		t.Helper()
		var p pickup
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &p); err != nil {
			t.Fatalf("stdout is not a single JSON document: %v\n%s", err, out)
		}
		return p
	}

	const msg = "decrypted for the script, not for the eye"
	runCLI(t, aliceHome, "send", "bob.poweur.net", msg)

	stdout, stderr := runListenOnce(t, bobHome, 30*time.Second, "--json", "--decrypt")
	got := decode(stdout)
	if len(got.Messages) != 1 || got.Messages[0].Body != msg || !got.Messages[0].Decrypted || got.Messages[0].Sender != "alice.poweur.net" {
		t.Fatalf("listen --json --decrypt: %+v\nstdout: %s\nstderr: %s", got, stdout, stderr)
	}
	if got.Messages[0].Payload == msg || got.Messages[0].Payload == "" {
		t.Fatalf("the wire payload must stay ciphertext, got %q", got.Messages[0].Payload)
	}
	if strings.Contains(stdout, "🔒") {
		t.Fatalf("human rendering leaked into the JSON stream:\n%s", stdout)
	}

	inbox, _ := runCLI(t, bobHome, "inbox")
	if !strings.Contains(inbox, "no messages") {
		t.Fatalf("--decrypt left the message in the inbox:\n%s", inbox)
	}

	runCLI(t, aliceHome, "send", "bob.poweur.net", "second, by poll")
	polled, _ := runCLI(t, bobHome, "inbox", "--json", "--decrypt")
	if p := decode(polled); len(p.Messages) != 1 || p.Messages[0].Body != "second, by poll" {
		t.Fatalf("inbox --json --decrypt: %s", polled)
	}

	if code, _, stderr := runCLIFull(t, bobHome, "inbox", "--decrypt"); code == 0 || !strings.Contains(stderr, "--json") {
		t.Fatalf("--decrypt without --json should be refused, got %d: %s", code, stderr)
	}
}

// runListenOnce runs `poweur listen --once` and fails the test if it does
// not exit within the deadline. A hung listen is a real failure — it means
// the catch-up drain never fired — and it must not become a 15-minute
// package timeout with no explanation.
func runListenOnce(t *testing.T, home string, within time.Duration, extra ...string) (string, string) {
	t.Helper()
	t.Setenv("HOME", home)
	args := append([]string{"listen", "--once"}, extra...)

	var stdout, stderr bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- clipkg.Run(args, &stdout, &stderr) }()

	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("listen exited %d\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
		}
	case <-time.After(within):
		t.Fatalf("listen did not exit within %s\nstdout: %s\nstderr: %s",
			within, stdout.String(), stderr.String())
	}
	return stdout.String(), stderr.String()
}
