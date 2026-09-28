// Integration coverage for `poweur identity lookup` reading an identity's
// public self-description (EPIC-006 E06-T2): profile.json and
// capabilities.json in the owner's .poweur/public/, read back by a stranger
// over `/.well-known/poweur/` with no credentials on the reading side.
package integration_test

import (
	"encoding/json"
	"strings"
	"testing"

	clipkg "github.com/poweur/cli/pkg/cli"
)

func TestINT_PROFILE_01_LookupShowsProfileAndCapabilities(t *testing.T) {
	zone := newZone(t)
	dataDir := t.TempDir()
	ts, addr := newHostedRelay(t, zone, dataDir)
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

	// Alice's public self-description in her .poweur/public/ (written on the
	// relay's disk here; clients use the owner system-file API).
	putPublicDoc(t, dataDir, "pralice.poweur.net", "profile.json", map[string]any{
		"version":      1,
		"display_name": "Alice Example",
		"bio":          "builds things",
		"locale":       "en-GB",
		"avatar":       "avatar.png",
		"links":        []map[string]string{{"label": "site", "url": "https://example.test"}},
	})
	putPublicDoc(t, dataDir, "pralice.poweur.net", "capabilities.json", map[string]any{
		"version":   1,
		"features":  map[string]string{"messaging": "v1", "files": "drive"},
		"endpoints": map[string]string{"web_signer": "https://pralice.poweur.net/app/"},
	})

	// Bob — who has no credentials on alice's tree — now sees who she is.
	stdout, _ = runCLI(t, bobHome, "identity", "lookup", "pralice.poweur.net")
	for _, want := range []string{
		"display name: Alice Example",
		"bio: builds things",
		"locale: en-GB",
		"avatar: http://pralice.poweur.net/.well-known/poweur/avatar.png",
		"link: site — https://example.test",
		"capability: files=drive",
		"capability: messaging=v1",
		"endpoint: web_signer=https://pralice.poweur.net/app/",
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

// putPublicDoc stores a JSON document in identity's .poweur/public/.
func putPublicDoc(t *testing.T, dataDir, identity, name string, doc any) {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	writeRelaySysFile(t, dataDir, identity, ".poweur/public/"+name, raw)
}
