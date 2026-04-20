// Package integration runs end-to-end tests that span the Eurything CLI,
// the relay HTTP server, and an in-memory DNS zone inside a single process.
//
// The tests follow the same design every time:
//
//  1. Build a fakedns.Zone that acts as both the CLI's DNS resolver and the
//     relay's DNS resolver/provider. Writes from the relay are immediately
//     visible to the CLI.
//  2. Spin up one or more relay servers on httptest listeners. Each relay's
//     RelayAddress is pinned to its listener address (host:port) so that
//     cross-relay forwarding over HTTP actually reaches the right process.
//  3. Install the Zone as the CLI's DNS resolver.
//  4. Give each "user" its own HOME directory (via t.Setenv) and drive the
//     CLI end-to-end (identity create, send, inbox).
//
// These tests exercise real crypto, real HTTP, real config files, real
// canonical signing and envelope parsing — only DNS is mocked.
package integration_test

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	clipkg "github.com/eurything/cli/pkg/cli"

	relaypkg "github.com/eurything/api/pkg/relay"

	"github.com/eurything/integration/fakedns"
)

// newRelay builds an in-process relay whose DNS reads/writes flow through
// the supplied zone, and returns the running httptest server and its
// RelayAddress (host:port form). The server is closed automatically.
func newRelay(t *testing.T, zone *fakedns.Zone) (*httptest.Server, string) {
	t.Helper()

	ts := httptest.NewUnstartedServer(nil)
	addr := ts.Listener.Addr().String()

	cfg := relaypkg.Config{
		ListenAddr:   addr,
		RelayAddress: addr,
		RelayScheme:  "http",
		DNSTTL:       time.Minute,
		ChallengeTTL: time.Minute,
		Version:      "integration-test",
		RateLimits: relaypkg.RateLimits{
			PerMinute: 1000,
			PerHour:   10000,
			PerDay:    100000,
		},
	}

	providers := relaypkg.NewProviderFactory(cfg)
	relaypkg.RegisterProvider(providers, "mock", zone.Provider())

	server := relaypkg.NewServer(cfg, zone, providers)
	ts.Config.Handler = relaypkg.Router(server)
	ts.Start()
	t.Cleanup(ts.Close)
	return ts, addr
}

// installZone wires the zone as the CLI's DNS resolver and restores the
// default on cleanup.
func installZone(t *testing.T, zone *fakedns.Zone) {
	t.Helper()
	clipkg.SetDNSResolver(zone)
	t.Cleanup(clipkg.ResetDNSResolver)
}

// newZone is a convenience that creates a fresh zone and immediately wires
// it as the CLI's DNS resolver.
func newZone(t *testing.T) *fakedns.Zone {
	t.Helper()
	zone := fakedns.NewZone()
	installZone(t, zone)
	return zone
}

// runCLI runs the CLI once with the given HOME and returns stdout/stderr.
// Non-zero exit aborts the test.
func runCLI(t *testing.T, home string, args ...string) (string, string) {
	t.Helper()
	t.Setenv("HOME", home)
	var stdout, stderr bytes.Buffer
	code := clipkg.Run(args, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("cli %s exited %d\nstdout: %s\nstderr: %s",
			strings.Join(args, " "), code, stdout.String(), stderr.String())
	}
	return stdout.String(), stderr.String()
}

// TestINT01_IdentityCreatePublishesDNS verifies that running `identity create`
// against a running relay causes the relay to publish the expected DNS
// records (signing key + encryption key + relay host) into the zone.
func TestINT01_IdentityCreatePublishesDNS(t *testing.T) {
	zone := fakedns.NewZone()
	installZone(t, zone)
	_, relayAddr := newRelay(t, zone)

	aliceHome := t.TempDir()
	runCLI(t, aliceHome,
		"identity", "create", "alice",
		"--parent-domain", "example.com",
		"--relay", "http://"+relayAddr,
		"--dns-provider", "mock",
		"--dns-token", "integration",
	)

	snapshot := zone.Snapshot()
	pubTXT, ok := snapshot["TXT:_eurything.alice.example.com"]
	if !ok || len(pubTXT) == 0 {
		t.Fatalf("missing signing-key TXT record\nzone=%v", snapshot)
	}
	if !strings.HasPrefix(pubTXT[0], "eurything-pubkey=ed25519:") {
		t.Fatalf("unexpected pubkey TXT: %s", pubTXT[0])
	}

	encTXT, ok := snapshot["TXT:_eurything-enc.alice.example.com"]
	if !ok || len(encTXT) == 0 {
		t.Fatalf("missing encryption-key TXT record\nzone=%v", snapshot)
	}
	if !strings.HasPrefix(encTXT[0], "eurything-enckey=x25519:") {
		t.Fatalf("unexpected enc TXT: %s", encTXT[0])
	}

	hosts, ok := snapshot["HOST:alice.example.com"]
	if !ok || len(hosts) == 0 {
		t.Fatalf("missing relay host record\nzone=%v", snapshot)
	}
	if hosts[0] != relayAddr {
		t.Fatalf("host record %q != relay addr %q", hosts[0], relayAddr)
	}
}

// TestINT02_EncryptedMessageSameRelay drives two users on the same relay
// through identity creation, message send with end-to-end encryption, and
// inbox drain + decrypt. The received plaintext must match.
func TestINT02_EncryptedMessageSameRelay(t *testing.T) {
	zone := fakedns.NewZone()
	installZone(t, zone)
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

	secret := "meet me at the fountain at noon"
	runCLI(t, aliceHome, "send", "bob.example.com", secret)

	stdout, _ := runCLI(t, bobHome, "inbox")
	assertDecryptedInbox(t, stdout, "alice.example.com", secret)
}

// TestINT03_EncryptedMessageCrossRelay places Alice and Bob on different
// relays so the sending relay must forward the message over HTTP to Bob's
// relay, which verifies the session proof (without prior knowledge of
// Alice's session), stores the ciphertext, and later serves it to Bob for
// decryption.
func TestINT03_EncryptedMessageCrossRelay(t *testing.T) {
	zone := fakedns.NewZone()
	installZone(t, zone)
	_, relayA := newRelay(t, zone)
	_, relayB := newRelay(t, zone)

	if relayA == relayB {
		t.Fatalf("both relays bound to the same address: %s", relayA)
	}

	aliceHome := t.TempDir()
	bobHome := t.TempDir()

	runCLI(t, aliceHome,
		"identity", "create", "alice",
		"--parent-domain", "example.com",
		"--relay", "http://"+relayA,
		"--dns-provider", "mock",
		"--dns-token", "integration",
	)
	runCLI(t, bobHome,
		"identity", "create", "bob",
		"--parent-domain", "example.org",
		"--relay", "http://"+relayB,
		"--dns-provider", "mock",
		"--dns-token", "integration",
	)

	secret := "bananas have potassium"
	runCLI(t, aliceHome, "send", "bob.example.org", secret)

	stdout, _ := runCLI(t, bobHome, "inbox")
	assertDecryptedInbox(t, stdout, "alice.example.com", secret)
}

// assertDecryptedInbox parses the text-mode inbox output and confirms it
// contains a line whose sender matches and whose decrypted body equals the
// expected plaintext. The text format printed by the CLI is:
//
//	<prefix> [<timestamp>] <sender>: <decrypted-plaintext>
//
// where <prefix> is either two spaces (plaintext) or a lock glyph (decrypted
// ciphertext). Callers that sent with default encryption should see the
// lock glyph, which is asserted here to make sure we really exercised E2E.
func assertDecryptedInbox(t *testing.T, stdout, wantSender, wantPlaintext string) {
	t.Helper()
	if strings.TrimSpace(stdout) == "" || strings.Contains(stdout, "no messages") {
		t.Fatalf("empty inbox output:\n%s", stdout)
	}
	if !strings.Contains(stdout, wantSender) {
		t.Fatalf("sender %q not found in inbox:\n%s", wantSender, stdout)
	}
	if !strings.Contains(stdout, wantPlaintext) {
		t.Fatalf("plaintext %q not found in decrypted inbox (decrypt likely failed):\n%s",
			wantPlaintext, stdout)
	}
	if !strings.Contains(stdout, "🔒") {
		t.Fatalf("expected lock glyph indicating decrypted ciphertext, got:\n%s", stdout)
	}
}
