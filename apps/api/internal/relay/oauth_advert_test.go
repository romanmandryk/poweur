package relay

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/poweur/api/internal/config"
)

func getWellKnown(t *testing.T, url, host string) (*http.Response, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Host = host
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

// EPIC-022: hosted identities advertise the web signer beside them and the
// operator's bridge, without the user writing either into their tree.
func TestCapabilitiesAdvertiseSignerAndBridge(t *testing.T) {
	server, ts := newDAVTestServer(t, 0, 0)
	alice := registerDAVIdentity(t, server, ts, "alice.poweur.net")
	capsURL := ts.URL + "/.well-known/poweur/capabilities.json"

	// Nothing configured, nothing written: nothing to serve.
	if resp, _ := getWellKnown(t, capsURL, "alice.poweur.net"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("empty capabilities = %d", resp.StatusCode)
	}

	server.cfg.WebStaticDir = t.TempDir()
	server.cfg.OAuthBridgeURL = "https://oauth.poweur.org"
	resp, caps := getWellKnown(t, capsURL, "alice.poweur.net")
	endpoints, _ := caps["endpoints"].(map[string]any)
	if resp.StatusCode != 200 || endpoints["web_signer"] != "http://alice.poweur.net/app/" || endpoints["oauth_bridge"] != "https://oauth.poweur.org" {
		t.Fatalf("default capabilities = %d %v", resp.StatusCode, caps)
	}

	// The user's own document wins where it speaks, and keeps its features.
	tok := mintDAVToken(t, ts, alice, "", "")
	put := davReq(t, ts, http.MethodPut, "/dav/alice.poweur.net/poweur-sys/public/capabilities.json", tok,
		[]byte(`{"version":1,"features":{"messaging":"v1"},"endpoints":{"web_signer":"https://signer.example/app/"}}`), nil)
	put.Body.Close()
	if put.StatusCode >= 300 {
		t.Fatalf("PUT capabilities = %d", put.StatusCode)
	}
	_, caps = getWellKnown(t, capsURL, "alice.poweur.net")
	endpoints, _ = caps["endpoints"].(map[string]any)
	features, _ := caps["features"].(map[string]any)
	if endpoints["web_signer"] != "https://signer.example/app/" || endpoints["oauth_bridge"] != "https://oauth.poweur.org" || features["messaging"] != "v1" {
		t.Fatalf("merged capabilities = %v", caps)
	}

	// Unknown identities get nothing.
	if resp, _ := getWellKnown(t, capsURL, "nobody.poweur.net"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown identity = %d", resp.StatusCode)
	}
}

func TestIdentityRootAdvertisesIndieAuthBridge(t *testing.T) {
	server, ts := newDAVTestServer(t, 0, 0)
	registerDAVIdentity(t, server, ts, "alice.poweur.net")

	resp, _ := getWellKnown(t, ts.URL+"/", "alice.poweur.net")
	if resp.Header.Get("Link") != "" {
		t.Fatal("Link advertised with no bridge configured")
	}
	server.cfg.OAuthBridgeURL = "https://oauth.poweur.org"
	resp, _ = getWellKnown(t, ts.URL+"/", "alice.poweur.net")
	link := resp.Header.Get("Link")
	if !strings.Contains(link, "<https://oauth.poweur.org/.well-known/oauth-authorization-server>") || !strings.Contains(link, `rel="indieauth-metadata"`) {
		t.Fatalf("Link = %q", link)
	}
	// Only identity hosts: the relay's own host and unknown names get none.
	for _, host := range []string{"relay.test", "nobody.poweur.net"} {
		if resp, _ := getWellKnown(t, ts.URL+"/", host); resp.Header.Get("Link") != "" {
			t.Fatalf("Link advertised on %s", host)
		}
	}
}

func TestOAuthBridgeURLValidated(t *testing.T) {
	base := config.Config{RelayAddress: "relay.test", ListenAddr: ":0"}
	for _, bad := range []string{"oauth.poweur.org", "https://oauth.poweur.org/path", "ftp://x"} {
		c := base
		c.OAuthBridgeURL = bad
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "OAUTH_BRIDGE_URL") {
			t.Errorf("OAUTH_BRIDGE_URL=%q: %v", bad, err)
		}
	}
	c := base
	c.OAuthBridgeURL = "https://oauth.poweur.org"
	if err := c.Validate(); err != nil {
		t.Fatalf("valid URL refused: %v", err)
	}
}
