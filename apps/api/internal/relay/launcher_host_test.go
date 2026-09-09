package relay

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/poweur/api/internal/config"
	"github.com/poweur/api/internal/dns"
)

func launcherTestServer(t *testing.T, staticDir string) *Server {
	t.Helper()
	cfg := config.Config{
		ListenAddr:    ":0",
		RelayAddress:  "relay.test",
		RelayScheme:   "http",
		DNSTTL:        time.Minute,
		ChallengeTTL:  time.Minute,
		Version:       "test",
		WebStaticDir:  staticDir,
		HostedDomains: []string{"poweur.net"},
		LauncherHost:  "id.poweur.net",
		LauncherHosts: []string{"id.poweur.net", "poweur.net"},
		RateLimits:    config.RateLimits{PerMinute: 100, PerHour: 1000, PerDay: 10000},
	}
	return NewServer(cfg, &fakeResolver{}, dns.NewProviderFactory(cfg))
}

// A human typing the product's own domain used to get the JSON service banner
// (EPIC-015 E15-T7); every launcher host now redirects into the app.
func TestRootRedirectsOnEveryLauncherHost(t *testing.T) {
	ts := httptest.NewServer(launcherTestServer(t, t.TempDir()).Router())
	defer ts.Close()

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	for _, host := range []string{"id.poweur.net", "poweur.net", "POWEUR.NET"} {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/", nil)
		req.Host = host
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", host, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusFound {
			t.Errorf("%s: status = %d, want 302", host, resp.StatusCode)
		}
		if loc := resp.Header.Get("Location"); loc != "/app/" {
			t.Errorf("%s: Location = %q, want /app/", host, loc)
		}
	}
}

// The redirect must not eat the root document. `GET /` is where every client
// learns the relay's address and which hosts are launchers, so a client that
// asks for JSON gets JSON even on a launcher host — without this the web app
// followed the redirect into `/app/`, failed to parse HTML, and resolved to no
// mode at all on the one host whose whole job is claiming a name.
func TestRootServesDocumentToJSONClientsOnLauncherHost(t *testing.T) {
	ts := httptest.NewServer(launcherTestServer(t, t.TempDir()).Router())
	defer ts.Close()

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/", nil)
	req.Host = "id.poweur.net"
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body struct {
		Service       string   `json:"service"`
		LauncherHosts []string `json:"launcher_hosts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Service != "poweur-relay" {
		t.Errorf("service = %q", body.Service)
	}
	if len(body.LauncherHosts) != 2 {
		t.Errorf("launcher_hosts = %v, want both", body.LauncherHosts)
	}
}

// An identity host is not a launcher: it serves its own front door, and the
// service banner is still what a non-browser client asks for.
func TestRootServesBannerOnIdentityHost(t *testing.T) {
	ts := httptest.NewServer(launcherTestServer(t, t.TempDir()).Router())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/", nil)
	req.Host = "bob.poweur.net"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["service"] != "poweur-relay" {
		t.Fatalf("service = %v", body["service"])
	}
	// The set is what a client resolves its mode against; the canonical one
	// stays for the hand-off target.
	hosts, _ := body["launcher_hosts"].([]any)
	if len(hosts) != 2 || hosts[0] != "id.poweur.net" || hosts[1] != "poweur.net" {
		t.Fatalf("launcher_hosts = %v", body["launcher_hosts"])
	}
	if body["launcher_host"] != "id.poweur.net" {
		t.Fatalf("launcher_host = %v", body["launcher_host"])
	}
}

// Without a static tree there is no app to redirect to, so the banner is the
// only honest answer even on the launcher host.
func TestRootDoesNotRedirectWithoutStaticDir(t *testing.T) {
	ts := httptest.NewServer(launcherTestServer(t, "").Router())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/", nil)
	req.Host = "id.poweur.net"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}
