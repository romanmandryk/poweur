package integration_test

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	relaypkg "github.com/poweur/api/pkg/relay"
	clipkg "github.com/poweur/cli/pkg/cli"
)

const integrationOperatorToken = "integration-operator-token-0123456789"

// TestINT_OPERATOR_01: the operator creates support.poweur.net — a reserved
// name nobody else can claim — from the CLI with the relay's OPERATOR_TOKEN.
// It comes back with a recovery kit, and it is then an ordinary ID: other
// users message it and it answers.
func TestINT_OPERATOR_01_ReservedNameCreatedAndMessaged(t *testing.T) {
	zone := newZone(t)
	ts := httptest.NewUnstartedServer(nil)
	addr := ts.Listener.Addr().String()
	cfg := relaypkg.Config{
		ListenAddr:           addr,
		RelayAddress:         addr,
		RelayScheme:          "http",
		DNSTTL:               time.Minute,
		ChallengeTTL:         time.Minute,
		Version:              "integration-test",
		DataDir:              t.TempDir(),
		HostedDomains:        []string{"poweur.net"},
		ResolverAllowPrivate: true,
		OperatorToken:        integrationOperatorToken,
		RateLimits:           relaypkg.RateLimits{PerMinute: 1000, PerHour: 10000, PerDay: 100000},
	}
	server := relaypkg.NewServer(cfg, zone, relaypkg.NewProviderFactory(cfg))
	ts.Config.Handler = relaypkg.Router(server)
	ts.Start()
	t.Cleanup(ts.Close)
	relayURL := "http://" + addr

	zone.SetHost("support.poweur.net", addr)
	zone.SetHost("aliceop.poweur.net", addr)
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	// Nobody else may claim it.
	stranger := t.TempDir()
	if code := runCLICode(t, stranger, "identity", "create", "support.poweur.net", "--hosted", "--relay", relayURL, "--json"); code == 0 {
		t.Fatal("a reserved name was claimed without the operator token")
	}

	support := t.TempDir()
	out, _ := runCLI(t, support,
		"identity", "create", "support.poweur.net",
		"--hosted", "--operator-token", integrationOperatorToken,
		"--relay", relayURL, "--json",
	)
	var created map[string]any
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatalf("create output: %v\n%s", err, out)
	}
	if created["registered"] != true || created["mnemonic"] == nil || created["seed"] == nil {
		t.Fatalf("operator create must register and return a recovery kit: %v", created)
	}

	alice := t.TempDir()
	runCLI(t, alice, "identity", "create", "aliceop.poweur.net", "--hosted", "--relay", relayURL, "--json")

	runCLI(t, alice, "send", "support.poweur.net", "I need more storage for my photos")
	supportInbox, _ := runCLI(t, support, "inbox")
	assertDecryptedInbox(t, supportInbox, "aliceop.poweur.net", "I need more storage for my photos")

	runCLI(t, support, "send", "aliceop.poweur.net", "Done: you have 2 GB now")
	aliceInbox, _ := runCLI(t, alice, "inbox")
	assertDecryptedInbox(t, aliceInbox, "support.poweur.net", "Done: you have 2 GB now")
}
