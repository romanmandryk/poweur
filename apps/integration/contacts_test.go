// Integration tests for contacts, inbox policy & key pinning (EPIC-007):
// the CLI-driven request → accept → chat flow against a real relay, and
// the pinned-key refusal on key change.
package integration_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	clipkg "github.com/poweur/cli/pkg/cli"
)

// TestINT_CONTACTS_01: alice runs contacts_and_requests; bob's normal
// message bounces, his contact request lands in her queue, she accepts,
// then normal messaging flows — the consent loop end to end via CLI.
func TestINT_CONTACTS_01_RequestAcceptChat(t *testing.T) {
	zone := newZone(t)
	ts, addr := newHostedRelay(t, zone, t.TempDir())
	defer ts.Close()
	relayURL := ts.URL
	zone.SetHost("polalice.poweur.net", addr)
	zone.SetHost("polbob.poweur.net", addr)

	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	aliceHome := t.TempDir()
	bobHome := t.TempDir()
	runCLI(t, aliceHome, "identity", "create", "polalice.poweur.net", "--hosted", "--relay", relayURL, "--json")
	runCLI(t, bobHome, "identity", "create", "polbob.poweur.net", "--hosted", "--relay", relayURL, "--json")

	// Alice locks her inbox down to contacts + requests.
	stdout, _ := runCLI(t, aliceHome, "policy", "set", "contacts_and_requests")
	if !strings.Contains(stdout, "contacts_and_requests") {
		t.Fatalf("policy set: %s", stdout)
	}

	// Bob's normal message is rejected by alice's policy.
	if code := runCLICode(t, bobHome, "send", "polalice.poweur.net", "hello alice"); code == 0 {
		t.Fatal("stranger message must be rejected under contacts_and_requests")
	}

	// Bob sends a contact request instead.
	runCLI(t, bobHome, "contacts", "request", "polalice.poweur.net", "hi, this is bob")

	// Alice sees it in her requests queue…
	stdout, _ = runCLI(t, aliceHome, "requests")
	if !strings.Contains(stdout, "polbob.poweur.net") || !strings.Contains(stdout, "sys.contact.request") {
		t.Fatalf("requests output: %s", stdout)
	}

	// …and accepts. (The accept notification back to bob rides messaging.)
	runCLI(t, aliceHome, "contacts", "accept", "polbob.poweur.net")
	stdout, _ = runCLI(t, aliceHome, "contacts", "ls")
	if !strings.Contains(stdout, "polbob.poweur.net") || !strings.Contains(stdout, "accepted") {
		t.Fatalf("contacts ls: %s", stdout)
	}

	// Now bob's normal message is delivered and alice can read it.
	runCLI(t, bobHome, "send", "polalice.poweur.net", "we are contacts now")
	stdout, _ = runCLI(t, aliceHome, "inbox")
	if !strings.Contains(stdout, "we are contacts now") {
		t.Fatalf("alice inbox: %s", stdout)
	}
}

// TestINT_CONTACTS_02: key pinning — a contact whose resolved key no longer
// matches the pin is refused at send time; --accept-new-key re-pins.
func TestINT_CONTACTS_02_KeyPinning(t *testing.T) {
	zone := newZone(t)
	ts, addr := newHostedRelay(t, zone, t.TempDir())
	defer ts.Close()
	relayURL := ts.URL
	zone.SetHost("pinalice.poweur.net", addr)
	zone.SetHost("pinbob.poweur.net", addr)

	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	aliceHome := t.TempDir()
	bobHome := t.TempDir()
	runCLI(t, aliceHome, "identity", "create", "pinalice.poweur.net", "--hosted", "--relay", relayURL, "--json")
	runCLI(t, bobHome, "identity", "create", "pinbob.poweur.net", "--hosted", "--relay", relayURL, "--json")

	// Alice adds bob — pinning his current key — and messaging works.
	runCLI(t, aliceHome, "contacts", "add", "pinbob.poweur.net")
	runCLI(t, aliceHome, "send", "pinbob.poweur.net", "pinned and fine")

	// Simulate impersonation: overwrite the pin with a wrong key (as a
	// malicious relay swapping bob's published key would appear).
	tok := mintTokenViaCLI(t, aliceHome, "--use-identity", "pinalice.poweur.net")
	contactsURL := relayURL + "/dav/pinalice.poweur.net/poweur-sys/relay/contacts.json"
	resp := davDo(t, http.MethodGet, contactsURL, tok, nil, nil)
	var contacts struct {
		Version  int `json:"version"`
		Contacts []map[string]any `json:"contacts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&contacts); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(contacts.Contacts) != 1 {
		t.Fatalf("contacts: %+v", contacts)
	}
	// A valid ed25519 key that is not bob's (all-zeros is length-valid).
	contacts.Contacts[0]["pinned_key"] = "ed25519:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	raw, _ := json.Marshal(contacts)
	resp = davDo(t, http.MethodPut, contactsURL, tok, raw, nil)
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("tamper put: %d", resp.StatusCode)
	}

	// Send must now refuse: pin mismatch, no rotation statement.
	if code := runCLICode(t, aliceHome, "send", "pinbob.poweur.net", "should not go"); code == 0 {
		t.Fatal("send must refuse on pinned-key mismatch")
	}

	// Explicit override re-pins and sends.
	runCLI(t, aliceHome, "send", "pinbob.poweur.net", "trusted again", "--accept-new-key")
	stdout, _ := runCLI(t, aliceHome, "contacts", "ls", "--json")
	if strings.Contains(stdout, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA") {
		t.Fatalf("pin must have been replaced by bob's real key: %s", stdout)
	}
}
