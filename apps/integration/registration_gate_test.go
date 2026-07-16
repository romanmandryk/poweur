package integration_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	clipkg "github.com/poweur/cli/pkg/cli"
	relaypkg "github.com/poweur/api/pkg/relay"
	"github.com/poweur/integration/fakedns"
)

func TestINT_REG_01_InviteRequired(t *testing.T) {
	zone := newZone(t)
	dataDir := t.TempDir()
	ts, addr := newGatedHostedRelay(t, zone, dataDir, "invite", []string{"good-invite"})
	defer ts.Close()
	relayURL := "http://" + addr

	zone.SetHost("invited.poweur.net", addr)
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	home := t.TempDir()
	if code := runCLICode(t, home,
		"identity", "create", "invited.poweur.net",
		"--hosted", "--relay", relayURL, "--json",
	); code == 0 {
		t.Fatal("expected registration without invite to fail")
	}

	if code := runCLICode(t, home,
		"identity", "create", "invited.poweur.net",
		"--hosted", "--relay", relayURL, "--invite-code", "wrong", "--json",
	); code == 0 {
		t.Fatal("expected wrong invite to fail")
	}

	home2 := t.TempDir()
	runCLI(t, home2,
		"identity", "create", "invited.poweur.net",
		"--hosted", "--relay", relayURL, "--invite-code", "good-invite", "--json",
	)
}

func TestINT_REG_02_RegistrationFloodRateLimit(t *testing.T) {
	zone := newZone(t)
	dataDir := t.TempDir()
	ts, addr := newLowLimitHostedRelay(t, zone, dataDir)
	defer ts.Close()
	relayURL := "http://" + addr
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	sawLimit := false
	for i := 0; i < 20; i++ {
		name := "flood" + time.Now().Format("150405.000") + string(rune('a'+i%26)) + ".poweur.net"
		// ensure unique names
		name = "flood" + string(rune('a'+i)) + time.Now().Format("150405") + ".poweur.net"
		zone.SetHost(name, addr)
		home := t.TempDir()
		if code := runCLICode(t, home,
			"identity", "create", name,
			"--hosted", "--relay", relayURL, "--json",
		); code != 0 {
			sawLimit = true
			break
		}
	}
	if !sawLimit {
		t.Fatal("expected registration flood to hit rate limit")
	}
}

func TestINT_OPS_01_RestoreDataDir(t *testing.T) {
	zone := newZone(t)
	dataDir := t.TempDir()
	ts, addr := newHostedRelay(t, zone, dataDir)
	relayURL := "http://" + addr
	zone.SetHost("restoreme.poweur.net", addr)
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	home := t.TempDir()
	runCLI(t, home,
		"identity", "create", "restoreme.poweur.net",
		"--hosted", "--relay", relayURL, "--json",
	)
	ts.Close()

	ts2, addr2 := newHostedRelay(t, zone, dataDir)
	defer ts2.Close()
	zone.SetHost("restoreme.poweur.net", addr2)

	resp, err := http.Get("http://" + addr2 + "/identities/restoreme.poweur.net")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected identity after restore, got %d", resp.StatusCode)
	}
}

func runCLICode(t *testing.T, home string, args ...string) int {
	t.Helper()
	t.Setenv("HOME", home)
	var stdout, stderr bytes.Buffer
	return clipkg.Run(args, &stdout, &stderr)
}

func newGatedHostedRelay(t *testing.T, zone *fakedns.Zone, dataDir, gate string, codes []string) (*httptest.Server, string) {
	t.Helper()
	ts := httptest.NewUnstartedServer(nil)
	addr := ts.Listener.Addr().String()
	cfg := relaypkg.Config{
		ListenAddr:              addr,
		RelayAddress:            addr,
		RelayScheme:             "http",
		DNSTTL:                  time.Minute,
		ChallengeTTL:            time.Minute,
		Version:                 "integration-test",
		DataDir:                 dataDir,
		HostedDomains:           []string{"poweur.net"},
		ResolverAllowPrivate:    true,
		RegistrationGate:        gate,
		RegistrationInviteCodes: codes,
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

func newLowLimitHostedRelay(t *testing.T, zone *fakedns.Zone, dataDir string) (*httptest.Server, string) {
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
		RegistrationGate:     "open",
		RateLimits: relaypkg.RateLimits{
			PerMinute: 3,
			PerHour:   3,
			PerDay:    3,
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
