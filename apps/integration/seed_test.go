package integration_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	clipkg "github.com/poweur/cli/pkg/cli"
	idpkg "github.com/poweur/identity"
)

// Master-seed identities end to end (EPIC-011 E11-T1).
//
// The unit and conformance suites prove Go and TypeScript derive identical
// bytes. These prove the derived bytes make a *working identity* against a
// real relay, and that 32 bytes are enough to get it back — the promise the
// whole recovery epic rests on.

// createSeedIdentity registers a seed-derived hosted identity and returns the
// seed the CLI generated for it.
func createSeedIdentity(t *testing.T, home, identity, relayURL string) string {
	t.Helper()
	stdout, _ := runCLI(t, home,
		"identity", "create", identity,
		"--hosted", "--relay", relayURL, "--json",
	)
	var out struct {
		Seed      string `json:"seed"`
		PublicKey string `json:"public_key"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("parse create output: %v\n%s", err, stdout)
	}
	if out.Seed == "" {
		t.Fatal("identity create must return the seed it generated; without it the identity is unrecoverable")
	}
	return out.Seed
}

// TestINT_SEED_01 registers a seed-derived identity and checks the relay
// serves exactly the keys the seed derives.
func TestINT_SEED_01_SeedDerivedIdentityRegisters(t *testing.T) {
	zone := newZone(t)
	dataDir := t.TempDir()
	ts, addr := newHostedRelay(t, zone, dataDir)
	defer ts.Close()
	relayURL := "http://" + addr
	zone.SetHost("seeded.poweur.net", addr)
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	home := t.TempDir()
	seed := createSeedIdentity(t, home, "seeded.poweur.net", relayURL)

	// What the relay published must equal what the seed derives, independently
	// of anything the CLI wrote to disk.
	raw, err := idpkg.ParseSeed(seed)
	if err != nil {
		t.Fatalf("decode seed: %v", err)
	}
	wantPub, _, err := idpkg.DeriveSigningKey(raw)
	if err != nil {
		t.Fatal(err)
	}
	wantEnc, _, err := idpkg.DeriveEncryptionKey(raw)
	if err != nil {
		t.Fatal(err)
	}

	doc := fetchDoc(t, relayURL+"/identities/seeded.poweur.net")
	if got, want := idpkg.NormalizePublicKeyKey(doc.PublicKey),
		idpkg.NormalizePublicKeyKey(idpkg.FormatEd25519PublicKey(wantPub)); got != want {
		t.Fatalf("published signing key %q != derived %q", got, want)
	}
	if got, want := idpkg.NormalizePublicKeyKey(doc.EncryptionPublicKey),
		idpkg.NormalizePublicKeyKey(idpkg.FormatX25519PublicKey(wantEnc)); got != want {
		t.Fatalf("published encryption key %q != derived %q", got, want)
	}
	// The document is self-signed by the derived key, so it must still verify.
	if err := doc.Verify(); err != nil {
		t.Fatalf("document signed by derived key does not verify: %v", err)
	}
}

// TestINT_SEED_02 is the recovery drill: delete every local key file, restore
// from the seed alone, and read the inbox that arrived in the meantime.
func TestINT_SEED_02_RecoverFromSeedReadsInbox(t *testing.T) {
	zone := newZone(t)
	dataDir := t.TempDir()
	ts, addr := newHostedRelay(t, zone, dataDir)
	defer ts.Close()
	relayURL := "http://" + addr
	zone.SetHost("recovered.poweur.net", addr)
	zone.SetHost("sender.poweur.net", addr)
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	recoveredHome := t.TempDir()
	senderHome := t.TempDir()

	seed := createSeedIdentity(t, recoveredHome, "recovered.poweur.net", relayURL)
	runCLI(t, senderHome,
		"identity", "create", "sender.poweur.net",
		"--hosted", "--relay", relayURL, "--json",
	)

	const secret = "sent while the device was lost"
	runCLI(t, senderHome, "send", "recovered.poweur.net", secret)

	// Simulate total local loss: wipe the key directory the CLI just wrote.
	keysDir := filepath.Join(recoveredHome, ".poweur", "keys")
	if _, err := os.Stat(keysDir); err != nil {
		t.Fatalf("expected keys at %s: %v", keysDir, err)
	}
	if err := os.RemoveAll(keysDir); err != nil {
		t.Fatal(err)
	}

	// A fresh machine that has only the seed.
	freshHome := t.TempDir()
	runCLI(t, freshHome, "key", "recover", "recovered.poweur.net",
		"--seed", seed, "--relay", relayURL, "--json")

	inbox, _ := runCLI(t, freshHome, "inbox")
	assertDecryptedInbox(t, inbox, "sender.poweur.net", secret)
}

// TestINT_SEED_03 covers the negative case: the wrong seed must not recover
// the identity, and must not be able to read its inbox.
func TestINT_SEED_03_WrongSeedCannotRecover(t *testing.T) {
	zone := newZone(t)
	dataDir := t.TempDir()
	ts, addr := newHostedRelay(t, zone, dataDir)
	defer ts.Close()
	relayURL := "http://" + addr
	zone.SetHost("victim.poweur.net", addr)
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	home := t.TempDir()
	seed := createSeedIdentity(t, home, "victim.poweur.net", relayURL)

	other, err := idpkg.NewSeed()
	if err != nil {
		t.Fatal(err)
	}
	wrongSeed := idpkg.EncodeSeed(other)
	if wrongSeed == seed {
		t.Fatal("random seeds collided")
	}

	attackerHome := t.TempDir()
	runCLI(t, attackerHome, "key", "recover", "victim.poweur.net",
		"--seed", wrongSeed, "--relay", relayURL, "--json")

	// The recovery "succeeds" locally — it is offline key derivation — but the
	// keys are not the identity's, so the relay must refuse the signed read.
	t.Setenv("HOME", attackerHome)
	var stdout, stderr strings.Builder
	if code := clipkg.Run([]string{"inbox"}, &stdout, &stderr); code == 0 {
		t.Fatalf("inbox with wrong seed must fail, got success:\n%s", stdout.String())
	}
}

// TestINT_SEED_04 pins the CLI's offline derivation against the relay's view:
// `key derive` must print exactly the keys the relay published, so a holder
// can verify a recovery kit before trusting it.
func TestINT_SEED_04_KeyDeriveMatchesPublishedDocument(t *testing.T) {
	zone := newZone(t)
	dataDir := t.TempDir()
	ts, addr := newHostedRelay(t, zone, dataDir)
	defer ts.Close()
	relayURL := "http://" + addr
	zone.SetHost("seedcheck.poweur.net", addr)
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	home := t.TempDir()
	seed := createSeedIdentity(t, home, "seedcheck.poweur.net", relayURL)

	stdout, _ := runCLI(t, t.TempDir(), "key", "derive", "--seed", seed, "--json")
	var derived struct {
		PublicKey           string `json:"public_key"`
		EncryptionPublicKey string `json:"encryption_public_key"`
	}
	if err := json.Unmarshal([]byte(stdout), &derived); err != nil {
		t.Fatalf("parse derive output: %v\n%s", err, stdout)
	}

	doc := fetchDoc(t, relayURL+"/identities/seedcheck.poweur.net")
	if idpkg.NormalizePublicKeyKey(derived.PublicKey) != idpkg.NormalizePublicKeyKey(doc.PublicKey) {
		t.Fatalf("derive printed %q, relay published %q", derived.PublicKey, doc.PublicKey)
	}
	// The CLI prints bare base64 for encryption_public_key (matching
	// `identity create`); identity documents carry the "x25519:" prefix.
	if derived.EncryptionPublicKey != strings.TrimPrefix(doc.EncryptionPublicKey, "x25519:") {
		t.Fatalf("derive printed enc %q, relay published %q",
			derived.EncryptionPublicKey, doc.EncryptionPublicKey)
	}
}

// TestINT_SEED_05 is the paper-kit drill: recover using only the 24 words a
// user could have written down, with the base64url seed discarded.
func TestINT_SEED_05_RecoverFromMnemonic(t *testing.T) {
	zone := newZone(t)
	dataDir := t.TempDir()
	ts, addr := newHostedRelay(t, zone, dataDir)
	defer ts.Close()
	relayURL := "http://" + addr
	zone.SetHost("paperkit.poweur.net", addr)
	zone.SetHost("mailer.poweur.net", addr)
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	home := t.TempDir()
	senderHome := t.TempDir()

	stdout, _ := runCLI(t, home,
		"identity", "create", "paperkit.poweur.net",
		"--hosted", "--relay", relayURL, "--json",
	)
	var created struct {
		Seed     string `json:"seed"`
		Mnemonic string `json:"mnemonic"`
	}
	if err := json.Unmarshal([]byte(stdout), &created); err != nil {
		t.Fatalf("parse create output: %v\n%s", err, stdout)
	}
	if created.Mnemonic == "" {
		t.Fatal("identity create must emit a mnemonic; a seed nobody can transcribe is a poor kit")
	}
	if got := len(strings.Fields(created.Mnemonic)); got != idpkg.MnemonicWords {
		t.Fatalf("want %d words, got %d", idpkg.MnemonicWords, got)
	}
	// Both encodings must name the same secret.
	fromWords, err := idpkg.MnemonicToSeed(created.Mnemonic)
	if err != nil {
		t.Fatal(err)
	}
	if idpkg.EncodeSeed(fromWords) != created.Seed {
		t.Fatal("mnemonic and seed disagree")
	}

	runCLI(t, senderHome, "identity", "create", "mailer.poweur.net",
		"--hosted", "--relay", relayURL, "--json")
	const note = "written on paper, recovered from paper"
	runCLI(t, senderHome, "send", "paperkit.poweur.net", note)

	// Recover with the words alone, on a machine that has never seen the seed.
	freshHome := t.TempDir()
	runCLI(t, freshHome, "key", "recover", "paperkit.poweur.net",
		"--seed", created.Mnemonic, "--relay", relayURL, "--json")
	assertDecryptedInbox(t, mustInbox(t, freshHome), "mailer.poweur.net", note)
}

// TestINT_SEED_06 covers `key kit`: an offline converter whose output restores
// the same identity.
func TestINT_SEED_06_KeyKitRoundTrips(t *testing.T) {
	zone := newZone(t)
	dataDir := t.TempDir()
	ts, addr := newHostedRelay(t, zone, dataDir)
	defer ts.Close()
	relayURL := "http://" + addr
	zone.SetHost("kitted.poweur.net", addr)
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	home := t.TempDir()
	seed := createSeedIdentity(t, home, "kitted.poweur.net", relayURL)

	stdout, _ := runCLI(t, home, "key", "kit", "--seed", seed, "--json")
	var kit struct {
		Identity string `json:"identity"`
		Mnemonic string `json:"mnemonic"`
		Seed     string `json:"seed"`
	}
	if err := json.Unmarshal([]byte(stdout), &kit); err != nil {
		t.Fatalf("parse kit: %v\n%s", err, stdout)
	}
	if kit.Seed != seed || kit.Identity != "kitted.poweur.net" {
		t.Fatalf("unexpected kit: %+v", kit)
	}

	// The kit's words derive the key the relay published.
	derived, _ := runCLI(t, t.TempDir(), "key", "derive", "--seed", kit.Mnemonic, "--json")
	var out struct {
		PublicKey string `json:"public_key"`
	}
	if err := json.Unmarshal([]byte(derived), &out); err != nil {
		t.Fatal(err)
	}
	doc := fetchDoc(t, relayURL+"/identities/kitted.poweur.net")
	if idpkg.NormalizePublicKeyKey(out.PublicKey) != idpkg.NormalizePublicKeyKey(doc.PublicKey) {
		t.Fatalf("kit mnemonic derives %q, relay published %q", out.PublicKey, doc.PublicKey)
	}
}

// TestINT_SEED_07 exercises the inventory: `key ls` against a real relay.
func TestINT_SEED_07_KeyListShowsEnrollments(t *testing.T) {
	zone := newZone(t)
	dataDir := t.TempDir()
	ts, addr := newHostedRelay(t, zone, dataDir)
	defer ts.Close()
	relayURL := "http://" + addr
	zone.SetHost("inventory.poweur.net", addr)
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	home := t.TempDir()
	createSeedIdentity(t, home, "inventory.poweur.net", relayURL)

	// A fresh identity has no enrollments; the CLI must say so plainly rather
	// than printing an empty table.
	stdout, _ := runCLI(t, home, "key", "ls", "--relay", relayURL)
	if !strings.Contains(stdout, "no enrollments") {
		t.Fatalf("expected an explicit empty state, got: %s", stdout)
	}
	jsonOut, _ := runCLI(t, home, "key", "ls", "--relay", relayURL, "--json")
	var listed struct {
		Enrollments []map[string]any `json:"enrollments"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &listed); err != nil {
		t.Fatalf("parse key ls: %v\n%s", err, jsonOut)
	}
	if len(listed.Enrollments) != 0 {
		t.Fatalf("expected no enrollments, got %d", len(listed.Enrollments))
	}
}

func mustInbox(t *testing.T, home string) string {
	t.Helper()
	out, _ := runCLI(t, home, "inbox")
	return out
}
