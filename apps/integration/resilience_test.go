// Integration tests for EPIC-029 (relay and DNS outage resilience).
package integration_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	clipkg "github.com/poweur/cli/pkg/cli"

	relaypkg "github.com/poweur/api/pkg/relay"

	"github.com/poweur/integration/fakedns"
)

// newStaleRelay is newHostedRelay with RELAY_STALE_IDENTITY_MAX set.
func newStaleRelay(t *testing.T, zone *fakedns.Zone, dataDir string, maxStale time.Duration) (*httptest.Server, string) {
	t.Helper()
	ts := httptest.NewUnstartedServer(nil)
	addr := ts.Listener.Addr().String()
	cfg := relaypkg.Config{
		ListenAddr:           addr,
		RelayAddress:         addr,
		RelayScheme:          "http",
		DNSTTL:               time.Minute,
		ChallengeTTL:         time.Minute,
		Version:              "integration-test",
		DataDir:              dataDir,
		HostedDomains:        []string{"poweur.net"},
		ResolverAllowPrivate: true,
		StaleIdentityMax:     maxStale,
		// No in-memory cache: the second message must be verified against
		// the stored document, not a five-minute-old cached answer.
		IdentityCacheTTL: -1,
		IdentityStoreMax: 100,
		RateLimits:       relaypkg.RateLimits{PerMinute: 1000, PerHour: 10000, PerDay: 100000},
	}
	providers := relaypkg.NewProviderFactory(cfg)
	relaypkg.RegisterProvider(providers, "mock", zone.Provider())
	server := relaypkg.NewServer(cfg, zone, providers)
	ts.Config.Handler = relaypkg.Router(server)
	ts.Start()
	t.Cleanup(ts.Close)
	return ts, addr
}

// senderRelayDown registers alice on one relay and bob on another, delivers
// one identity-signed message, then takes alice's relay (and with it her
// identity document) away and sends a second one. It returns the CLI exit
// code of that second send and bob's inbox afterwards.
func senderRelayDown(t *testing.T, maxStale time.Duration) (int, string) {
	t.Helper()
	zone := newZone(t)
	aliceRelay, aliceAddr := newHostedRelay(t, zone, t.TempDir())
	_, bobAddr := newStaleRelay(t, zone, t.TempDir(), maxStale)

	zone.SetHost("alice29.poweur.net", aliceAddr)
	zone.SetHost("bob29.poweur.net", bobAddr)
	clipkg.ConfigureIdentityResolverHosts("http", true, func(host string) string {
		if hosts, err := zone.LookupHost(context.Background(), host); err == nil && len(hosts) > 0 {
			return hosts[0]
		}
		return ""
	})
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	aliceHome, bobHome := t.TempDir(), t.TempDir()
	runCLI(t, aliceHome, "identity", "create", "alice29.poweur.net",
		"--hosted", "--relay", "http://"+aliceAddr, "--json")
	runCLI(t, bobHome, "identity", "create", "bob29.poweur.net",
		"--hosted", "--relay", "http://"+bobAddr, "--json")

	// Identity-signed, so bob's relay resolves alice's key for each message
	// instead of trusting a session it already knows.
	runCLI(t, aliceHome, "send", "--sign-with", "identity", "bob29.poweur.net", "before the outage")

	// Alice's relay goes away. Her name still points at it, so fetching her
	// document now fails at the connection: an outage, not a "not found".
	aliceRelay.Close()

	code := runCLICode(t, aliceHome, "send", "--sign-with", "identity", "bob29.poweur.net", "during the outage")
	inbox, _ := runCLI(t, bobHome, "inbox")
	return code, inbox
}

// TestINT_E29_T2_SenderRelayDown: within the stale window, bob's relay still
// accepts a message from a sender whose identity host is down, verifying it
// against the document it stored after her first message.
func TestINT_E29_T2_SenderRelayDown(t *testing.T) {
	code, inbox := senderRelayDown(t, 7*24*time.Hour)
	if code != 0 {
		t.Fatalf("send during the outage exited %d", code)
	}
	for _, want := range []string{"before the outage", "during the outage"} {
		if !strings.Contains(inbox, want) {
			t.Fatalf("bob's inbox is missing %q:\n%s", want, inbox)
		}
	}
}

// TestINT_E29_T2_StaleFallbackOff: with RELAY_STALE_IDENTITY_MAX=0 the relay
// behaves as before and the second message does not arrive.
func TestINT_E29_T2_StaleFallbackOff(t *testing.T) {
	_, inbox := senderRelayDown(t, 0)
	if !strings.Contains(inbox, "before the outage") {
		t.Fatalf("bob's inbox is missing the first message:\n%s", inbox)
	}
	if strings.Contains(inbox, "during the outage") {
		t.Fatalf("message accepted with the stale fallback off:\n%s", inbox)
	}
}

// TestINT_E29_T3_SendFromCachedIdentity: the recipient's relay (and with it
// their identity document) is unreachable. A contact resolved before is still
// encrypted to, from the CLI's identity cache, and the message waits in the
// outbox; when the relay is back, `outbox retry` delivers it.
func TestINT_E29_T3_SendFromCachedIdentity(t *testing.T) {
	zone := newZone(t)
	_, aliceAddr := newHostedRelay(t, zone, t.TempDir())
	bobData := t.TempDir()
	bobRelay, bobAddr := newHostedRelay(t, zone, bobData)

	zone.SetHost("alice30.poweur.net", aliceAddr)
	zone.SetHost("bob30.poweur.net", bobAddr)
	clipkg.ConfigureIdentityResolverHosts("http", true, func(host string) string {
		if hosts, err := zone.LookupHost(context.Background(), host); err == nil && len(hosts) > 0 {
			return hosts[0]
		}
		return ""
	})
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	aliceHome, bobHome := t.TempDir(), t.TempDir()
	runCLI(t, aliceHome, "identity", "create", "alice30.poweur.net",
		"--hosted", "--relay", "http://"+aliceAddr, "--json")
	runCLI(t, bobHome, "identity", "create", "bob30.poweur.net",
		"--hosted", "--relay", "http://"+bobAddr, "--json")

	// The first send resolves bob and leaves his document in alice's cache.
	_, stderr := runCLI(t, aliceHome, "send", "bob30.poweur.net", "while everything is up")
	if strings.Contains(stderr, "using cached identity") {
		t.Fatalf("cache used while bob resolves:\n%s", stderr)
	}

	bobRelay.Close()

	// With the fallback off there is nothing to encrypt to: the send fails
	// and nothing is queued in plaintext.
	code, _, stderr := runCLIFull(t, aliceHome, "send", "--max-stale", "0", "bob30.poweur.net", "must not be sent")
	if code == 0 || !strings.Contains(stderr, "no cached identity for bob30.poweur.net") {
		t.Fatalf("send with --max-stale 0: code=%d stderr=%s", code, stderr)
	}

	code, stdout, stderr := runCLIFull(t, aliceHome, "send", "bob30.poweur.net", "written during the outage")
	if code != 0 {
		t.Fatalf("send during the outage exited %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "using cached identity for bob30.poweur.net (fetched ") {
		t.Fatalf("no cached-identity notice on stderr:\n%s", stderr)
	}
	if !strings.Contains(stdout, "queued encrypted message to bob30.poweur.net") {
		t.Fatalf("message was not queued:\n%s", stdout)
	}

	lookup, _ := runCLI(t, aliceHome, "identity", "lookup", "bob30.poweur.net")
	if !strings.Contains(lookup, "source: stale (fetched ") {
		t.Fatalf("lookup does not say the identity is cached:\n%s", lookup)
	}

	// Bob's relay comes back on a new address with the same data.
	_, bobAddr2 := newHostedRelay(t, zone, bobData)
	zone.SetHost("bob30.poweur.net", bobAddr2)
	rewriteRelayURL(t, bobHome, "http://"+bobAddr2)

	retry, _ := runCLI(t, aliceHome, "outbox", "retry")
	if !strings.Contains(retry, "sent queued message to bob30.poweur.net") {
		t.Fatalf("outbox retry did not deliver:\n%s", retry)
	}
	inbox, _ := runCLI(t, bobHome, "inbox")
	for _, want := range []string{"while everything is up", "written during the outage"} {
		if !strings.Contains(inbox, want) {
			t.Fatalf("bob's inbox is missing %q:\n%s", want, inbox)
		}
	}
	if strings.Contains(inbox, "must not be sent") {
		t.Fatalf("a message refused with --max-stale 0 was delivered:\n%s", inbox)
	}
}
