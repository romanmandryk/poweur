// Integration tests for contacts, inbox policy & key pinning (EPIC-007):
// the CLI-driven request → accept → chat flow against a real relay, and
// the pinned-key refusal on key change.
package integration_test

import (
	"encoding/json"
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

// TestINT_CONTACTS_05: a policy rejection converts itself into a contact
// request (E07-T3). The relay already knew a request would be accepted; the
// CLI stops making the user retype their message into a second command, and
// carries what they wrote across as the intro so the recipient can decide on
// evidence rather than on a bare identity name.
func TestINT_CONTACTS_05_SendAutoRequestOnPolicyRejection(t *testing.T) {
	zone := newZone(t)
	ts, addr := newHostedRelay(t, zone, t.TempDir())
	defer ts.Close()
	relayURL := ts.URL
	zone.SetHost("aralice.poweur.net", addr)
	zone.SetHost("arbob.poweur.net", addr)

	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	aliceHome := t.TempDir()
	bobHome := t.TempDir()
	runCLI(t, aliceHome, "identity", "create", "aralice.poweur.net", "--hosted", "--relay", relayURL, "--json")
	runCLI(t, bobHome, "identity", "create", "arbob.poweur.net", "--hosted", "--relay", relayURL, "--json")

	runCLI(t, aliceHome, "policy", "set", "contacts_and_requests")

	// Bob writes to a stranger. The send is refused — and becomes a request.
	const intro = "hi alice, bob here, we met at the conference"
	if code := runCLICode(t, bobHome, "send", "aralice.poweur.net", intro, "--request-on-reject"); code != 0 {
		t.Fatalf("send with --request-on-reject should succeed as a request, exited %d", code)
	}

	// Bob's own contacts now record the pending request, exactly as if he
	// had run `contacts request` himself.
	stdout, _ := runCLI(t, bobHome, "contacts", "ls")
	if !strings.Contains(stdout, "aralice.poweur.net") || !strings.Contains(stdout, "requested") {
		t.Fatalf("bob's contacts should hold a requested entry: %s", stdout)
	}

	// Alice sees the request *and* reads what he said before deciding.
	stdout, _ = runCLI(t, aliceHome, "requests")
	if !strings.Contains(stdout, "arbob.poweur.net") || !strings.Contains(stdout, "sys.contact.request") {
		t.Fatalf("requests output: %s", stdout)
	}
	if !strings.Contains(stdout, intro) {
		t.Fatalf("the request intro must be decrypted for the recipient: %s", stdout)
	}
}

// TestINT_CONTACTS_06: without the flag and without a TTY the send still
// fails, and nothing is written to either side. A rejected send must never
// silently pin a key and file a request on the user's behalf.
func TestINT_CONTACTS_06_NoAutoRequestWithoutConsent(t *testing.T) {
	zone := newZone(t)
	ts, addr := newHostedRelay(t, zone, t.TempDir())
	defer ts.Close()
	relayURL := ts.URL
	zone.SetHost("nralice.poweur.net", addr)
	zone.SetHost("nrbob.poweur.net", addr)

	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	aliceHome := t.TempDir()
	bobHome := t.TempDir()
	runCLI(t, aliceHome, "identity", "create", "nralice.poweur.net", "--hosted", "--relay", relayURL, "--json")
	runCLI(t, bobHome, "identity", "create", "nrbob.poweur.net", "--hosted", "--relay", relayURL, "--json")

	runCLI(t, aliceHome, "policy", "set", "contacts_and_requests")

	if code := runCLICode(t, bobHome, "send", "nralice.poweur.net", "unasked-for hello"); code == 0 {
		t.Fatal("a rejected send without --request-on-reject must fail")
	}
	stdout, _ := runCLI(t, bobHome, "contacts", "ls")
	if strings.Contains(stdout, "nralice.poweur.net") {
		t.Fatalf("no contact should have been written: %s", stdout)
	}
	stdout, _ = runCLI(t, aliceHome, "requests")
	if strings.Contains(stdout, "nrbob.poweur.net") {
		t.Fatalf("no request should have been filed: %s", stdout)
	}
}

// TestINT_CONTACTS_02: key pinning — a contact whose resolved key no longer
// matches the pin is refused at send time; --accept-new-key re-pins.
func TestINT_CONTACTS_02_KeyPinning(t *testing.T) {
	zone := newZone(t)
	dataDir := t.TempDir()
	ts, addr := newHostedRelay(t, zone, dataDir)
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

	// Simulate impersonation: overwrite the pin with a wrong key on the
	// relay's disk, as a malicious relay swapping bob's key would appear.
	var contacts struct {
		Version  int              `json:"version"`
		Contacts []map[string]any `json:"contacts"`
	}
	if err := json.Unmarshal(readRelaySysFile(t, relayURL, aliceHome, "pinalice.poweur.net", ".poweur/relay/contacts.json"), &contacts); err != nil {
		t.Fatal(err)
	}
	if len(contacts.Contacts) != 1 {
		t.Fatalf("contacts: %+v", contacts)
	}
	// A valid ed25519 key that is not bob's (all-zeros is length-valid).
	contacts.Contacts[0]["pinned_key"] = "ed25519:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	raw, _ := json.Marshal(contacts)
	writeRelaySysFile(t, relayURL, aliceHome, "pinalice.poweur.net", ".poweur/relay/contacts.json", raw)

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
