// Integration coverage for `poweur identity lookup` reading an identity's
// public self-description (EPIC-006 E06-T2): profile.json and
// capabilities.json, written by the owner over DAV and read back by a
// stranger over `/.well-known/poweur/` — no new relay endpoint, and no
// credentials on the reading side.
package integration_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	clipkg "github.com/poweur/cli/pkg/cli"
)

func TestINT_PROFILE_01_LookupShowsProfileAndCapabilities(t *testing.T) {
	zone := newZone(t)
	ts, addr := newHostedRelay(t, zone, t.TempDir())
	defer ts.Close()
	relayURL := ts.URL
	zone.SetHost("pralice.poweur.net", addr)
	zone.SetHost("prbob.poweur.net", addr)

	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	aliceHome := t.TempDir()
	bobHome := t.TempDir()
	runCLI(t, aliceHome, "identity", "create", "pralice.poweur.net", "--hosted", "--relay", relayURL, "--json")
	runCLI(t, bobHome, "identity", "create", "prbob.poweur.net", "--hosted", "--relay", relayURL, "--json")

	// Before alice publishes anything, the lookup still works and simply
	// says nothing about her — an absent profile is not an error.
	stdout, stderr := runCLI(t, bobHome, "identity", "lookup", "pralice.poweur.net")
	if !strings.Contains(stdout, "pralice.poweur.net") {
		t.Fatalf("lookup output: %s", stdout)
	}
	if strings.Contains(stdout, "display name:") {
		t.Fatalf("no profile is published yet: %s", stdout)
	}
	if strings.Contains(stderr, "could not read") {
		t.Fatalf("a 404 must not be reported as a failure: %s", stderr)
	}

	// Alice writes her public self-description into her own tree.
	tok := mintTokenViaCLI(t, aliceHome, "--use-identity", "pralice.poweur.net")
	base := relayURL + "/dav/pralice.poweur.net/poweur-sys/public/"
	putSysFile(t, base+"profile.json", tok, map[string]any{
		"version":      1,
		"display_name": "Alice Example",
		"bio":          "builds things",
		"locale":       "en-GB",
		"avatar":       "public/avatar.png",
		"links":        []map[string]string{{"label": "site", "url": "https://example.test"}},
	})
	putSysFile(t, base+"capabilities.json", tok, map[string]any{
		"version":   1,
		"features":  map[string]string{"messaging": "v1", "files": "webdav"},
		"endpoints": map[string]string{"dav": "https://pralice.poweur.net/dav"},
	})

	// Bob — who has no credentials on alice's tree — now sees who she is.
	stdout, _ = runCLI(t, bobHome, "identity", "lookup", "pralice.poweur.net")
	for _, want := range []string{
		"display name: Alice Example",
		"bio: builds things",
		"locale: en-GB",
		"avatar: http://pralice.poweur.net/pub/avatar.png",
		"link: site — https://example.test",
		"capability: files=webdav",
		"capability: messaging=v1",
		"endpoint: dav=https://pralice.poweur.net/dav",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("lookup missing %q:\n%s", want, stdout)
		}
	}

	// The JSON form carries the documents themselves, and keeps the identity
	// document's own capability list under its established key.
	stdout, _ = runCLI(t, bobHome, "identity", "lookup", "pralice.poweur.net", "--json")
	var out struct {
		Identity string `json:"identity"`
		Profile  *struct {
			DisplayName string `json:"display_name"`
		} `json:"profile"`
		CapabilitiesDocument *struct {
			Features map[string]string `json:"features"`
		} `json:"capabilities_document"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("lookup --json: %v\n%s", err, stdout)
	}
	if out.Profile == nil || out.Profile.DisplayName != "Alice Example" {
		t.Fatalf("profile in json: %s", stdout)
	}
	if out.CapabilitiesDocument == nil || out.CapabilitiesDocument.Features["messaging"] != "v1" {
		t.Fatalf("capabilities in json: %s", stdout)
	}
}

// A profile the relay would refuse to store cannot arrive through the
// well-known route either: the write is validated (E06-T1), so the reader
// never has to decide what a malformed document means.
func TestINT_PROFILE_02_MalformedProfileRejectedOnWrite(t *testing.T) {
	zone := newZone(t)
	ts, addr := newHostedRelay(t, zone, t.TempDir())
	defer ts.Close()
	relayURL := ts.URL
	zone.SetHost("mpalice.poweur.net", addr)

	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	home := t.TempDir()
	runCLI(t, home, "identity", "create", "mpalice.poweur.net", "--hosted", "--relay", relayURL, "--json")

	tok := mintTokenViaCLI(t, home, "--use-identity", "mpalice.poweur.net")
	// avatar must be a path into the identity's own /public tree, never a
	// URL: rendering a profile must not become a request to a host the
	// profile's author chose.
	raw, _ := json.Marshal(map[string]any{"version": 1, "avatar": "https://evil.test/pixel.png"})
	resp := davDo(t, http.MethodPut, relayURL+"/dav/mpalice.poweur.net/poweur-sys/public/profile.json", tok, raw, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("external avatar URL must be rejected on write, got %d", resp.StatusCode)
	}
}

func putSysFile(t *testing.T, url, token string, document any) {
	t.Helper()
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	resp := davDo(t, http.MethodPut, url, token, raw, nil)
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("PUT %s: %d", url, resp.StatusCode)
	}
}
