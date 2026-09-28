// Safety numbers end to end (EPIC-007 E07-T4).
//
// The derivation is unit-tested in packages/identity and pinned by
// conformance vectors; what these tests are for is the plumbing — that the
// strings a *user* is asked to compare actually appear where the trust
// decision is made, against a real relay and a real resolver, and that the
// two identities in a conversation see the same number for the same key.
package integration_test

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	clipkg "github.com/poweur/cli/pkg/cli"
	idpkg "github.com/poweur/identity"
)

var safetyNumber = regexp.MustCompile(`\d{5} \d{5} \d{5} \d{5}`)

// runCLIOutput is runCLI without the fatal-on-nonzero: the refusal paths
// below are supposed to exit non-zero and still be inspected.
func runCLIOutput(t *testing.T, home string, args ...string) (int, string, string) {
	t.Helper()
	t.Setenv("HOME", home)
	var stdout, stderr bytes.Buffer
	code := clipkg.Run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// TestINT_CONTACTS_03: the safety number is shown where it is needed — at
// pin time, in the contact list, on lookup — and both sides derive the same
// one for the same key.
func TestINT_CONTACTS_03_SafetyNumbers(t *testing.T) {
	zone := newZone(t)
	ts, addr := newHostedRelay(t, zone, t.TempDir())
	defer ts.Close()
	relayURL := ts.URL
	zone.SetHost("fpalice.poweur.net", addr)
	zone.SetHost("fpbob.poweur.net", addr)

	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	aliceHome := t.TempDir()
	bobHome := t.TempDir()
	runCLI(t, aliceHome, "identity", "create", "fpalice.poweur.net", "--hosted", "--relay", relayURL, "--json")
	runCLI(t, bobHome, "identity", "create", "fpbob.poweur.net", "--hosted", "--relay", relayURL, "--json")

	// `identity lookup` publishes the number a contact would read to you.
	stdout, _ := runCLI(t, aliceHome, "identity", "lookup", "fpbob.poweur.net")
	lookupFP := safetyNumber.FindString(stdout)
	if lookupFP == "" {
		t.Fatalf("identity lookup printed no safety number:\n%s", stdout)
	}
	if !strings.Contains(stdout, "safety number:") {
		t.Fatalf("identity lookup output is missing the label:\n%s", stdout)
	}

	// Adding pins the key and shows the number while verification is cheap.
	stdout, _ = runCLI(t, aliceHome, "contacts", "add", "fpbob.poweur.net")
	addFP := safetyNumber.FindString(stdout)
	if addFP == "" {
		t.Fatalf("contacts add printed no safety number:\n%s", stdout)
	}
	if addFP != lookupFP {
		t.Fatalf("lookup said %q but the pin says %q — the same key must give one number", lookupFP, addFP)
	}

	// The list shows it too, so a user can re-verify later.
	stdout, _ = runCLI(t, aliceHome, "contacts", "ls")
	if !strings.Contains(stdout, addFP) {
		t.Fatalf("contacts ls does not show the safety number %q:\n%s", addFP, stdout)
	}

	// Bob, on his own machine, derives the same number for his own key —
	// which is what makes reading it aloud a verification and not a ritual.
	stdout, _ = runCLI(t, bobHome, "identity", "lookup", "fpbob.poweur.net")
	if bobFP := safetyNumber.FindString(stdout); bobFP != addFP {
		t.Fatalf("bob sees %q for his own key, alice pinned %q", bobFP, addFP)
	}

	// And it is the canonical derivation, not something the CLI invented:
	// re-derive it from the very key the CLI printed next to it.
	want, err := idpkg.KeyFingerprint(publicKeyLine(t, stdout))
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	if want != addFP {
		t.Fatalf("CLI printed %q, packages/identity derives %q", addFP, want)
	}
}

// publicKeyLine pulls `public_key: …` out of `identity lookup` output.
func publicKeyLine(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "public_key:"); ok {
			return strings.TrimSpace(rest)
		}
	}
	t.Fatalf("no public_key line in:\n%s", out)
	return ""
}

// TestINT_CONTACTS_04: the send-time refusal is the moment a user is asked
// to verify out of band, so it must hand them both safety numbers — not
// only the base64 keys nobody can read to each other.
func TestINT_CONTACTS_04_MismatchShowsSafetyNumbers(t *testing.T) {
	zone := newZone(t)
	dataDir := t.TempDir()
	ts, addr := newHostedRelay(t, zone, dataDir)
	defer ts.Close()
	relayURL := ts.URL
	zone.SetHost("fpmalice.poweur.net", addr)
	zone.SetHost("fpmbob.poweur.net", addr)

	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	aliceHome := t.TempDir()
	bobHome := t.TempDir()
	runCLI(t, aliceHome, "identity", "create", "fpmalice.poweur.net", "--hosted", "--relay", relayURL, "--json")
	runCLI(t, bobHome, "identity", "create", "fpmbob.poweur.net", "--hosted", "--relay", relayURL, "--json")
	runCLI(t, aliceHome, "contacts", "add", "fpmbob.poweur.net")

	// Swap the pin for a valid-but-wrong key, as an impersonating relay would.
	const wrongKey = "ed25519:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	raw := []byte(`{"version":1,"contacts":[{"identity":"fpmbob.poweur.net","state":"accepted","pinned_key":"` + wrongKey + `"}]}`)
	writeRelaySysFile(t, dataDir, "fpmalice.poweur.net", ".poweur/relay/contacts.json", raw)

	code, _, stderr := runCLIOutput(t, aliceHome, "send", "fpmbob.poweur.net", "should not go")
	if code == 0 {
		t.Fatal("send must refuse on pinned-key mismatch")
	}
	numbers := safetyNumber.FindAllString(stderr, -1)
	if len(numbers) < 2 {
		t.Fatalf("refusal must show both safety numbers, got %d:\n%s", len(numbers), stderr)
	}
	if numbers[0] == numbers[1] {
		t.Fatalf("pinned and resolved safety numbers must differ:\n%s", stderr)
	}
	wrongFP, err := idpkg.KeyFingerprint(wrongKey)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	if numbers[0] != wrongFP {
		t.Fatalf("first number must be the pin (%q), got %q", wrongFP, numbers[0])
	}
	// The full keys stay in the message: the fingerprint is for humans, the
	// key is what a support ticket or a script needs.
	if !strings.Contains(stderr, wrongKey) {
		t.Fatalf("refusal dropped the raw pinned key:\n%s", stderr)
	}
}
