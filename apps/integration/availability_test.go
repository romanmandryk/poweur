package integration_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"testing"
	"time"

	relaypkg "github.com/poweur/api/pkg/relay"
	clipkg "github.com/poweur/cli/pkg/cli"
	idpkg "github.com/poweur/identity"
	"github.com/poweur/integration/fakedns"
)

// EPIC-018 E18-T2. The endpoint exists so a user learns "taken" before they
// spend a WebAuthn ceremony, so the case that matters is an identity that was
// really registered through the real path — not one poked into a store.
func TestINT_NAME_01_AvailabilityReasons(t *testing.T) {
	zone := newZone(t)
	ts, addr := newPolicyHostedRelay(t, zone, t.TempDir(), idpkg.NamePolicy{
		MinLen: 6, MaxLen: 24, Reserved: []string{"acme"},
	})
	defer ts.Close()
	relayURL := "http://" + addr

	zone.SetHost("robert.poweur.net", addr)
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	// Free before, taken after — through `poweur identity create`.
	if got := availabilityReason(t, relayURL, "robert", ""); got != idpkg.ReasonAvailable {
		t.Fatalf("before registration: reason = %q, want available", got)
	}
	runCLI(t, t.TempDir(), "identity", "create", "robert.poweur.net",
		"--hosted", "--relay", relayURL, "--json")
	if got := availabilityReason(t, relayURL, "robert", ""); got != idpkg.ReasonTaken {
		t.Fatalf("after registration: reason = %q, want taken", got)
	}

	cases := []struct {
		handle, domain string
		want           idpkg.NameReason
	}{
		{"admin", "", idpkg.ReasonReserved},
		{"acme", "", idpkg.ReasonReserved},
		{"bob", "", idpkg.ReasonTooShort},
		{"аdmin", "", idpkg.ReasonCharset}, // Cyrillic а: the bypass this epic closes
		{"melissa", "elsewhere.example", idpkg.ReasonDomainNotHosted},
		{"melissa", "", idpkg.ReasonAvailable},
	}
	for _, tc := range cases {
		if got := availabilityReason(t, relayURL, tc.handle, tc.domain); got != tc.want {
			t.Errorf("%q@%q: reason = %q, want %q", tc.handle, tc.domain, got, tc.want)
		}
	}
}

// The endpoint's verdict and the registration path must agree: a name the
// endpoint calls reserved has to be refused by POST /identities too, or the
// check is decoration.
func TestINT_NAME_02_PolicyIsEnforcedAtRegistration(t *testing.T) {
	zone := newZone(t)
	ts, addr := newPolicyHostedRelay(t, zone, t.TempDir(), idpkg.NamePolicy{MinLen: 6})
	defer ts.Close()
	relayURL := "http://" + addr

	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	for _, handle := range []string{"bob", "admin", "аdmin"} {
		zone.SetHost(handle+".poweur.net", addr)
		if code := runCLICode(t, t.TempDir(), "identity", "create", handle+".poweur.net",
			"--hosted", "--relay", relayURL, "--json"); code == 0 {
			t.Fatalf("%q was registered despite the policy", handle)
		}
	}

	zone.SetHost("melissa.poweur.net", addr)
	runCLI(t, t.TempDir(), "identity", "create", "melissa.poweur.net",
		"--hosted", "--relay", relayURL, "--json")
}

func availabilityReason(t *testing.T, relayURL, handle, domain string) idpkg.NameReason {
	t.Helper()
	query := neturl.Values{"handle": {handle}}
	if domain != "" {
		query.Set("domain", domain)
	}
	resp, err := http.Get(relayURL + "/hosted/availability?" + query.Encode())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("availability(%q): status %d", handle, resp.StatusCode)
	}
	var body struct {
		Available bool           `json:"available"`
		Reason    string         `json:"reason"`
		Policy    map[string]any `json:"policy"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Policy["min_len"] == nil {
		t.Fatalf("availability(%q): policy not echoed", handle)
	}
	if body.Available != (body.Reason == string(idpkg.ReasonAvailable)) {
		t.Fatalf("availability(%q): available=%v with reason %q", handle, body.Available, body.Reason)
	}
	return idpkg.NameReason(body.Reason)
}

// newPolicyHostedRelay is newHostedRelay with an operator name policy.
func newPolicyHostedRelay(t *testing.T, zone *fakedns.Zone, dataDir string, policy idpkg.NamePolicy) (*httptest.Server, string) {
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
		NamePolicy:           policy,
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
