// Integration test INT-10: retro-fit an encryption key onto an identity that
// was created before E2E support existed.
//
// Scenario recap:
//
//   - Alice and Bob both have working signing keys and relay host records in
//     DNS, but Bob is a "legacy" identity that never published an
//     `_poweur-enc.<bob>` TXT record (and has no local .enc file).
//   - Under the strict encrypt-only policy, Alice's CLI MUST refuse to send
//     to Bob (exit non-zero with a clear "publish an encryption key" hint).
//   - Bob runs `poweur identity add-encryption-key`. The CLI mints an
//     X25519 keypair, saves the private half locally, and hits the new
//     POST /identities/{identity}/encryption-key relay endpoint. The relay
//     uses the caller's DNS token to upsert the TXT record.
//   - Alice now sends an encrypted message. Bob's inbox is decrypted end-to-end.
//
// This is the flow we'll run against poweur.net for id1.poweur.net and
// id2.poweur.net; keeping the scenario here in-process gives us fast,
// deterministic coverage of the same code path.
package integration_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	clipkg "github.com/poweur/cli/pkg/cli"

	"github.com/poweur/integration/fakedns"
)

func TestINT10_AddEncryptionKeyRetrofit(t *testing.T) {
	zone := fakedns.NewZone()
	installZone(t, zone)
	_, relayAddr := newRelay(t, zone)
	relayURL := "http://" + relayAddr

	aliceHome := t.TempDir()
	bobHome := t.TempDir()

	// Register both identities with full E2E support, then delete Bob's
	// encryption artefacts (DNS record + local .enc file) to simulate a
	// legacy identity.
	runCLI(t, aliceHome,
		"identity", "create", "alice",
		"--parent-domain", "poweur.net",
		"--relay", relayURL,
		"--dns-provider", "mock",
		"--dns-token", "integration",
	)
	runCLI(t, bobHome,
		"identity", "create", "bob",
		"--parent-domain", "poweur.net",
		"--relay", relayURL,
		"--dns-provider", "mock",
		"--dns-token", "integration",
	)

	// Rewind Bob to the "legacy" state.
	zone.SetTXT("_poweur-enc.bob.poweur.net")
	bobEncPath := filepath.Join(bobHome, ".poweur", "keys", "bob.poweur.net.enc")
	if err := os.Remove(bobEncPath); err != nil {
		t.Fatalf("remove bob enc key: %v", err)
	}

	// Sanity: with Bob's enc record gone, Alice's send MUST fail. The
	// encrypt-only policy refuses the plaintext fallback.
	t.Setenv("HOME", aliceHome)
	var preOut, preErr bytes.Buffer
	preCode := clipkg.Run(
		[]string{"send", "bob.poweur.net", "pre-retrofit ping"},
		&preOut, &preErr,
	)
	if preCode == 0 {
		t.Fatalf("send should fail when recipient has no encryption key\nstdout:%s\nstderr:%s",
			preOut.String(), preErr.String())
	}
	if !strings.Contains(preErr.String(), "no published encryption key") {
		t.Fatalf("expected stderr to mention missing enc key, got:\n%s", preErr.String())
	}
	if !strings.Contains(preErr.String(), "identity add-encryption-key") {
		t.Fatalf("expected stderr to recommend `identity add-encryption-key`, got:\n%s", preErr.String())
	}

	// Bob retro-fits an encryption key.
	runCLI(t, bobHome,
		"identity", "add-encryption-key",
		"--relay", relayURL,
		"--dns-provider", "mock",
		"--dns-token", "integration",
	)

	// Zone now has a non-empty enc record again, and the local file is back.
	snapshot := zone.Snapshot()
	encTXT, ok := snapshot["TXT:_poweur-enc.bob.poweur.net"]
	if !ok || len(encTXT) == 0 || !strings.HasPrefix(encTXT[0], "poweur-enckey=x25519:") {
		t.Fatalf("encryption TXT record missing after add-encryption-key:\n%v", snapshot)
	}
	if _, err := os.Stat(bobEncPath); err != nil {
		t.Fatalf("bob's local .enc file should exist after add-encryption-key: %v", err)
	}

	// Drain any previous messages so the next inbox read sees only the
	// encrypted one.
	runCLI(t, bobHome, "inbox")

	// Alice sends an encrypted follow-up; Bob decrypts end-to-end.
	secret := "post-retrofit payload"
	runCLI(t, aliceHome, "send", "bob.poweur.net", secret)
	stdout, _ := runCLI(t, bobHome, "inbox")
	assertDecryptedInbox(t, stdout, "alice.poweur.net", secret)
}

func TestINT10b_RotateEncryptionKeyRefusesWithoutFlag(t *testing.T) {
	zone := fakedns.NewZone()
	installZone(t, zone)
	_, relayAddr := newRelay(t, zone)
	relayURL := "http://" + relayAddr

	aliceHome := t.TempDir()
	runCLI(t, aliceHome,
		"identity", "create", "alice",
		"--parent-domain", "poweur.net",
		"--relay", relayURL,
		"--dns-provider", "mock",
		"--dns-token", "integration",
	)

	// Without --rotate, the CLI must refuse (exit non-zero) because Alice
	// already has a .enc on disk; overwriting it would silently invalidate
	// any ciphertext encrypted to the old key.
	t.Setenv("HOME", aliceHome)
	var stdout, stderr bytes.Buffer
	code := clipkg.Run([]string{
		"identity", "add-encryption-key",
		"--relay", relayURL,
		"--dns-provider", "mock",
		"--dns-token", "integration",
	}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit without --rotate, got stdout=%q stderr=%q",
			stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "--rotate") {
		t.Fatalf("expected --rotate hint in stderr, got:\n%s", stderr.String())
	}

	// With --rotate the command succeeds and the DNS record changes.
	before := zone.Snapshot()["TXT:_poweur-enc.alice.poweur.net"][0]
	runCLI(t, aliceHome,
		"identity", "add-encryption-key",
		"--rotate",
		"--relay", relayURL,
		"--dns-provider", "mock",
		"--dns-token", "integration",
	)
	after := zone.Snapshot()["TXT:_poweur-enc.alice.poweur.net"][0]
	if before == after {
		t.Fatalf("enc TXT record did not change after rotate: %s", after)
	}
}
