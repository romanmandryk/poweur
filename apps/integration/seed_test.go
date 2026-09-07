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
		"--hosted", "--from-seed", "--relay", relayURL, "--json",
	)
	var out struct {
		Seed        string `json:"seed"`
		SeedDerived bool   `json:"seed_derived"`
		PublicKey   string `json:"public_key"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("parse create output: %v\n%s", err, stdout)
	}
	if !out.SeedDerived {
		t.Fatal("expected seed_derived=true")
	}
	if out.Seed == "" {
		t.Fatal("--from-seed must return the seed; without it the identity is unrecoverable")
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
	zone.SetHost("verify.poweur.net", addr)
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	home := t.TempDir()
	seed := createSeedIdentity(t, home, "verify.poweur.net", relayURL)

	stdout, _ := runCLI(t, t.TempDir(), "key", "derive", "--seed", seed, "--json")
	var derived struct {
		PublicKey           string `json:"public_key"`
		EncryptionPublicKey string `json:"encryption_public_key"`
	}
	if err := json.Unmarshal([]byte(stdout), &derived); err != nil {
		t.Fatalf("parse derive output: %v\n%s", err, stdout)
	}

	doc := fetchDoc(t, relayURL+"/identities/verify.poweur.net")
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
