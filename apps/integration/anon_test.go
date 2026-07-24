// Integration tests for anonymous messaging & PoW challenges (EPIC-014):
// CLI-driven anon send with proof-of-work, queue isolation, and the
// REGISTRATION_GATE=pow hosted-registration flow.
package integration_test

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	relaypkg "github.com/poweur/api/pkg/relay"
	clipkg "github.com/poweur/cli/pkg/cli"

	"github.com/poweur/integration/fakedns"
)

// TestINT_ANON_01: alice opts into anonymous messages behind an 8-bit PoW;
// bob's signed message bounces off contacts_only but his anonymous send
// solves the challenge and lands in her anon queue — never the inbox.
func TestINT_ANON_01_PowSendAndRead(t *testing.T) {
	zone := newZone(t)
	ts, addr := newHostedRelay(t, zone, t.TempDir())
	defer ts.Close()
	relayURL := ts.URL
	zone.SetHost("anonalice.poweur.net", addr)
	zone.SetHost("anonbob.poweur.net", addr)

	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	aliceHome := t.TempDir()
	bobHome := t.TempDir()
	runCLI(t, aliceHome, "identity", "create", "anonalice.poweur.net", "--hosted", "--relay", relayURL, "--json")
	runCLI(t, bobHome, "identity", "create", "anonbob.poweur.net", "--hosted", "--relay", relayURL, "--json")

	// Alice: contacts only, but anonymous allowed behind PoW.
	stdout, _ := runCLI(t, aliceHome, "policy", "set", "contacts_only",
		"--anon-allow", "--anon-challenge", "pow", "--anon-bits", "8")
	if !strings.Contains(stdout, "anonymous allowed, challenge=pow") {
		t.Fatalf("policy set output: %s", stdout)
	}

	// Bob's signed message is rejected by contacts_only…
	if code := runCLICode(t, bobHome, "send", "anonalice.poweur.net", "signed hello"); code == 0 {
		t.Fatal("signed stranger message must be rejected under contacts_only")
	}

	// …but his anonymous send solves the challenge and goes through.
	stdout, cliErr := runCLI(t, bobHome, "send", "anonalice.poweur.net", "secret tip: check the logs", "--anon")
	if !strings.Contains(stdout, "anonymous") {
		t.Fatalf("anon send output: %s", stdout)
	}
	if !strings.Contains(cliErr, "proof-of-work") || !strings.Contains(cliErr, "solved in") {
		t.Fatalf("anon send must report solving: %s", cliErr)
	}

	// Alice reads it decrypted from the anon queue.
	stdout, _ = runCLI(t, aliceHome, "anon")
	if !strings.Contains(stdout, "secret tip: check the logs") || !strings.Contains(stdout, "ANONYMOUS") {
		t.Fatalf("anon queue output: %s", stdout)
	}
	if !strings.Contains(stdout, "unauthenticated") {
		t.Fatalf("anon output must carry the trust warning: %s", stdout)
	}

	// The signed inbox stays clean.
	stdout, _ = runCLI(t, aliceHome, "inbox")
	if strings.Contains(stdout, "secret tip") {
		t.Fatalf("anon message leaked into the signed inbox: %s", stdout)
	}
}

// newPowGatedRelay is newHostedRelay with REGISTRATION_GATE=pow.
func newPowGatedRelay(t *testing.T, zone *fakedns.Zone, dataDir string) (*httptest.Server, string) {
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
		RegistrationGate:     "pow",
		RegistrationPowBits:  8,
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

// TestINT_ANON_02: hosted registration behind REGISTRATION_GATE=pow — the
// CLI fetches, solves and retries automatically (closes the EPIC-002
// deferral end to end).
func TestINT_ANON_02_PowRegistrationGate(t *testing.T) {
	zone := newZone(t)
	ts, addr := newPowGatedRelay(t, zone, t.TempDir())
	defer ts.Close()

	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	home := t.TempDir()
	stdout, cliErr := runCLI(t, home, "identity", "create", "powreg.poweur.net", "--hosted", "--relay", ts.URL, "--json")
	if !strings.Contains(cliErr, "proof-of-work for registration") || !strings.Contains(cliErr, "solved in") {
		t.Fatalf("registration must report solving:\nstderr: %s", cliErr)
	}
	if !strings.Contains(stdout, `"registered": true`) && !strings.Contains(stdout, `"registered":true`) {
		t.Fatalf("registration output: %s", stdout)
	}
}
