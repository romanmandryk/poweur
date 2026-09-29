package integration_test

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	clipkg "github.com/poweur/cli/pkg/cli"
)

func TestINT_IDENTITY_PAGE_01_HostRoutingNegotiationAndOptOuts(t *testing.T) {
	zone := newZone(t)
	staticDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(staticDir, "index.html"), []byte("web-app-shell"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, addr := newHostedRelayWithWeb(t, zone, t.TempDir(), staticDir)
	relayURL := "http://" + addr
	for _, identity := range []string{"pagealice.poweur.net", "pagebob.poweur.net"} {
		zone.SetHost(identity, addr)
	}
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	aliceHome := t.TempDir()
	bobHome := t.TempDir()
	runCLI(t, aliceHome, "identity", "create", "pagealice.poweur.net", "--hosted", "--relay", relayURL, "--json")
	runCLI(t, bobHome, "identity", "create", "pagebob.poweur.net", "--hosted", "--relay", relayURL, "--json")
	// Search indexing is opt-in (off by default); alice opted in.
	putPublicDoc(t, relayURL, aliceHome, "pagealice.poweur.net", "profile.json", map[string]any{
		"version": 1, "display_name": "Alice Page", "identity_page": map[string]any{"indexable": true},
	})
	putPublicDoc(t, relayURL, bobHome, "pagebob.poweur.net", "profile.json", map[string]any{
		"version": 1, "display_name": "Bob Page",
		"identity_page": map[string]any{"indexable": false, "advertise_anonymous_messages": true},
	})

	alice := identityPageRequest(t, relayURL, "pagealice.poweur.net", "text/html", "")
	aliceBody := readIdentityPage(t, alice)
	if alice.StatusCode != http.StatusOK || !strings.Contains(aliceBody, "Alice Page") {
		t.Fatal("alice identity page was not host-routed")
	}
	if strings.Contains(aliceBody, "Write anonymously") {
		t.Fatal("anonymous messaging was advertised without the public setting")
	}
	if alice.Header.Get("X-Robots-Tag") != "" {
		t.Fatalf("alice page unexpectedly noindexed: %q", alice.Header.Get("X-Robots-Tag"))
	}
	etag := alice.Header.Get("ETag")
	conditional := identityPageRequest(t, relayURL, "pagealice.poweur.net", "text/html", etag)
	if conditional.StatusCode != http.StatusNotModified {
		t.Fatalf("conditional status = %d", conditional.StatusCode)
	}
	conditional.Body.Close()

	bob := identityPageRequest(t, relayURL, "pagebob.poweur.net", "text/html", "")
	if body := readIdentityPage(t, bob); bob.StatusCode != http.StatusOK || !strings.Contains(body, "Bob Page") || strings.Contains(body, "Alice Page") || !strings.Contains(body, `href="/app/?anonymous=1"`) {
		t.Fatalf("bob identity page: status=%d body=%s", bob.StatusCode, body)
	}
	if bob.Header.Get("X-Robots-Tag") != "noindex, nofollow" || bob.Header.Get("ETag") == etag {
		t.Fatalf("bob page headers: %v", bob.Header)
	}

	jsonResponse := identityPageRequest(t, relayURL, "pagealice.poweur.net", "application/json", "")
	if body := readIdentityPage(t, jsonResponse); jsonResponse.StatusCode != http.StatusOK || !strings.Contains(body, `"service":"poweur-relay"`) {
		t.Fatalf("JSON root changed: status=%d body=%s", jsonResponse.StatusCode, body)
	}

	putPublicDoc(t, relayURL, aliceHome, "pagealice.poweur.net", "profile.json", map[string]any{
		"version": 1, "display_name": "Do not leak",
		"identity_page": map[string]any{"enabled": false},
	})
	disabled := identityPageRequest(t, relayURL, "pagealice.poweur.net", "text/html", "")
	if body := readIdentityPage(t, disabled); disabled.StatusCode != http.StatusNotFound || strings.Contains(body, "Do not leak") {
		t.Fatalf("disabled page: status=%d body=%s", disabled.StatusCode, body)
	}

	wellKnownRequest, err := http.NewRequest(http.MethodGet, relayURL+"/.well-known/poweur/id.json", nil)
	if err != nil {
		t.Fatal(err)
	}
	wellKnownRequest.Host = "pagealice.poweur.net"
	wellKnown, err := http.DefaultClient.Do(wellKnownRequest)
	if err != nil {
		t.Fatal(err)
	}
	wellKnown.Body.Close()
	if wellKnown.StatusCode != http.StatusOK || strings.HasPrefix(wellKnown.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("well-known route changed: status=%d type=%q", wellKnown.StatusCode, wellKnown.Header.Get("Content-Type"))
	}

	app := identityPagePathRequest(t, relayURL, "pagealice.poweur.net", "/app/", "text/html")
	if body := readIdentityPage(t, app); app.StatusCode != http.StatusOK || !strings.Contains(body, "web-app-shell") || strings.Contains(body, "Copy ID") {
		t.Fatalf("/app/ was replaced by the identity page: status=%d body=%s", app.StatusCode, body)
	}
	unknown := identityPagePathRequest(t, relayURL, "pagebob.poweur.net", "/not-a-page", "text/html")
	if body := readIdentityPage(t, unknown); strings.Contains(body, "Bob Page") || !strings.Contains(body, `"service":"poweur-relay"`) {
		t.Fatalf("unknown path rendered an identity page: status=%d body=%s", unknown.StatusCode, body)
	}
}

func identityPageRequest(t *testing.T, relayURL, host, accept, etag string) *http.Response {
	t.Helper()
	return identityPagePathRequest(t, relayURL, host, "/", accept, etag)
}

func identityPagePathRequest(t *testing.T, relayURL, host, path, accept string, etag ...string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, relayURL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	req.Header.Set("Accept", accept)
	if len(etag) > 0 && etag[0] != "" {
		req.Header.Set("If-None-Match", etag[0])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func readIdentityPage(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
