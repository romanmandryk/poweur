package integration_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	clipkg "github.com/poweur/cli/pkg/cli"
	relaypkg "github.com/poweur/api/pkg/relay"
	"github.com/poweur/integration/fakedns"
)

// TestINT_HOSTED_01 registers two hosted identities (no DNS tokens), serves
// well-known documents, exchanges an E2E message, and survives relay restart
// with the same POWEUR_DATA directory.
func TestINT_HOSTED_01_HostedRegisterAndMessage(t *testing.T) {
	zone := newZone(t)
	dataDir := t.TempDir()

	ts, addr := newHostedRelay(t, zone, dataDir)
	relayURL := "http://" + addr

	zone.SetHost("alicehost.poweur.net", addr)
	zone.SetHost("bobhost.poweur.net", addr)

	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() {
		clipkg.ConfigureIdentityResolver("https", false, "")
	})

	aliceHome := t.TempDir()
	bobHome := t.TempDir()

	runCLI(t, aliceHome,
		"identity", "create", "alicehost.poweur.net",
		"--hosted", "--relay", relayURL, "--json",
	)
	runCLI(t, bobHome,
		"identity", "create", "bobhost.poweur.net",
		"--hosted", "--relay", relayURL, "--json",
	)

	snap := zone.Snapshot()
	for k := range snap {
		if strings.Contains(k, "TXT:_poweur.alicehost") || strings.Contains(k, "TXT:_poweur.bobhost") {
			t.Fatalf("unexpected per-identity TXT write for hosted id: %s", k)
		}
	}

	req, err := http.NewRequest(http.MethodGet, relayURL+"/.well-known/poweur/id.json", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "alicehost.poweur.net"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("well-known status %d", resp.StatusCode)
	}

	const msg = "hello hosted world"
	runCLI(t, aliceHome, "send", "bobhost.poweur.net", msg)
	bobInbox, _ := runCLI(t, bobHome, "inbox")
	assertDecryptedInbox(t, bobInbox, "alicehost.poweur.net", msg)

	// Restart with same data dir
	ts.Close()
	_, addr2 := newHostedRelay(t, zone, dataDir)
	zone.SetHost("alicehost.poweur.net", addr2)
	zone.SetHost("bobhost.poweur.net", addr2)
	clipkg.ConfigureIdentityResolver("http", true, addr2)
	relayURL2 := "http://" + addr2

	// Point client configs at the restarted relay
	rewriteRelayURL(t, aliceHome, relayURL2)
	rewriteRelayURL(t, bobHome, relayURL2)

	resp2, err := http.Get(relayURL2 + "/identities/alicehost.poweur.net")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("identity missing after restart: %d", resp2.StatusCode)
	}
	var body map[string]any
	_ = json.NewDecoder(resp2.Body).Decode(&body)
	if body["identity"] == nil {
		t.Fatal("empty identity after restart")
	}

	const msg2 = "after restart"
	runCLI(t, aliceHome, "send", "bobhost.poweur.net", msg2)
	bobInbox2, _ := runCLI(t, bobHome, "inbox")
	assertDecryptedInbox(t, bobInbox2, "alicehost.poweur.net", msg2)
}

// A claimed name must not leave a leftover keypair in the failing client's
// HOME — those files would shadow a later enroll/recover of the real identity.
func TestINT_HOSTED_CreateTakenNameLeavesNoLocalKeys(t *testing.T) {
	zone := newZone(t)
	_, addr := newHostedRelay(t, zone, t.TempDir())
	relayURL := "http://" + addr
	name := "takenname.poweur.net"
	zone.SetHost(name, addr)

	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	owner := t.TempDir()
	runCLI(t, owner, "identity", "create", name, "--hosted", "--relay", relayURL, "--json")
	ownerKey := filepath.Join(owner, ".poweur", "keys", name+".key")
	original, err := os.ReadFile(ownerKey)
	if err != nil {
		t.Fatal(err)
	}

	other := t.TempDir()
	if code := runCLICode(t, other, "identity", "create", name, "--hosted", "--relay", relayURL, "--json"); code == 0 {
		t.Fatal("second create of a claimed name must fail")
	}
	for _, leaf := range []string{name + ".key", name + ".enc"} {
		path := filepath.Join(other, ".poweur", "keys", leaf)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("loser home must not keep leftover %s: %v", leaf, err)
		}
	}
	after, err := os.ReadFile(ownerKey)
	if err != nil {
		t.Fatal(err)
	}
	if string(original) != string(after) {
		t.Fatal("owner keys were mutated by the failed create")
	}
}

func rewriteRelayURL(t *testing.T, home, relayURL string) {
	t.Helper()
	cfgPath := filepath.Join(home, ".poweur", "config.toml")
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(b), "\n")
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "relay_url") {
			lines[i] = `relay_url = '` + relayURL + `'`
		}
	}
	if err := os.WriteFile(cfgPath, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
}

func newHostedRelay(t *testing.T, zone *fakedns.Zone, dataDir string) (*httptest.Server, string) {
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
