package identity

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Key files at rest (EPIC-011 E11-T4).

func writePlaintextKey(t *testing.T, dir string, priv ed25519.PrivateKey) string {
	t.Helper()
	path := filepath.Join(dir, "alice.poweur.net.key")
	encoded := base64.RawStdEncoding.EncodeToString(priv)
	if err := os.WriteFile(path, []byte(encoded), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestKeyMaterial_EncryptDecryptRoundTrip(t *testing.T) {
	raw := []byte("some private key bytes")
	envelope, err := EncryptKeyMaterial(raw, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !IsEncryptedKeyFile(envelope) {
		t.Fatal("envelope must be detectable as encrypted")
	}
	if bytes.Contains(envelope, raw) {
		t.Fatal("plaintext key bytes present in the envelope")
	}
	back, err := DecryptKeyMaterial(envelope, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(back, raw) {
		t.Fatal("round-trip mismatch")
	}
}

func TestKeyMaterial_WrongPassphraseFails(t *testing.T) {
	envelope, err := EncryptKeyMaterial([]byte("secret"), "right")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecryptKeyMaterial(envelope, "wrong"); err != ErrWrongPassphrase {
		t.Fatalf("want ErrWrongPassphrase, got %v", err)
	}
	if _, err := DecryptKeyMaterial(envelope, ""); err != ErrPassphraseRequired {
		t.Fatalf("want ErrPassphraseRequired, got %v", err)
	}
}

// Two encryptions of the same key must differ: fresh salt and nonce each time.
func TestKeyMaterial_EnvelopesAreNotDeterministic(t *testing.T) {
	a, err := EncryptKeyMaterial([]byte("secret"), "pw")
	if err != nil {
		t.Fatal(err)
	}
	b, err := EncryptKeyMaterial([]byte("secret"), "pw")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("envelopes must use a fresh salt and nonce")
	}
}

// Existing plaintext key files must keep working — nobody is locked out by an
// upgrade — while reporting that they are unprotected.
func TestLoadPrivateKey_ReadsLegacyPlaintextAndReportsIt(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	path := writePlaintextKey(t, t.TempDir(), priv)

	loaded, legacy, err := LoadPrivateKeyWithPassphrase(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if !legacy {
		t.Fatal("a plaintext key file must be reported as legacy")
	}
	if !bytes.Equal(loaded, priv) {
		t.Fatal("legacy key did not load correctly")
	}
}

func TestProtectKeyFile_MigratesInPlace(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	path := writePlaintextKey(t, t.TempDir(), priv)

	changed, err := ProtectKeyFile(path, "pw")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected the file to be migrated")
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !IsEncryptedKeyFile(onDisk) {
		t.Fatal("file is still plaintext after protect")
	}
	if bytes.Contains(onDisk, []byte(base64.RawStdEncoding.EncodeToString(priv))) {
		t.Fatal("plaintext key still present on disk")
	}

	// Protected files load with the passphrase and are no longer legacy.
	loaded, legacy, err := LoadPrivateKeyWithPassphrase(path, "pw")
	if err != nil {
		t.Fatal(err)
	}
	if legacy {
		t.Fatal("an encrypted file must not be reported as legacy")
	}
	if !bytes.Equal(loaded, priv) {
		t.Fatal("decrypted key mismatch")
	}

	// And refuse to load without it, rather than failing obscurely.
	if _, _, err := LoadPrivateKeyWithPassphrase(path, ""); err != ErrPassphraseRequired {
		t.Fatalf("want ErrPassphraseRequired, got %v", err)
	}

	// Re-protecting is a no-op rather than double-wrapping.
	if changed, err = ProtectKeyFile(path, "pw"); err != nil || changed {
		t.Fatalf("re-protect should be a no-op: changed=%v err=%v", changed, err)
	}
}

func TestUnprotectKeyFile_RestoresPlaintext(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	path := writePlaintextKey(t, t.TempDir(), priv)
	if _, err := ProtectKeyFile(path, "pw"); err != nil {
		t.Fatal(err)
	}
	changed, err := UnprotectKeyFile(path, "pw")
	if err != nil || !changed {
		t.Fatalf("unprotect failed: changed=%v err=%v", changed, err)
	}
	loaded, legacy, err := LoadPrivateKeyWithPassphrase(path, "")
	if err != nil || !legacy || !bytes.Equal(loaded, priv) {
		t.Fatalf("plaintext not restored: legacy=%v err=%v", legacy, err)
	}
}

func TestEncryptionKey_ProtectsToo(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "alice.poweur.net.enc")
	raw := bytes.Repeat([]byte{7}, 32)
	if err := os.WriteFile(path, []byte(base64.RawStdEncoding.EncodeToString(raw)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ProtectKeyFile(path, "pw"); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := LoadEncryptionPrivateKeyWithPassphrase(path, "pw")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(loaded, raw) {
		t.Fatal("encryption key round-trip mismatch")
	}
}

func TestAnyKeyFileExists(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "alice.poweur.net.key")
	missing := filepath.Join(dir, "alice.poweur.net.enc")
	if AnyKeyFileExists(present, missing) {
		t.Fatal("must be false when nothing is on disk")
	}
	if err := os.WriteFile(present, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !AnyKeyFileExists(present, missing) {
		t.Fatal("must be true when either path exists")
	}
	if AnyKeyFileExists("", missing) {
		t.Fatal("empty paths must be ignored")
	}
}

func TestRemoveKeyFilesIgnoresMissingAndDeletesPresent(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "alice.poweur.net.key")
	missing := filepath.Join(dir, "alice.poweur.net.enc")
	if err := os.WriteFile(present, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	RemoveKeyFiles(present, missing, "")
	if _, err := os.Stat(present); !os.IsNotExist(err) {
		t.Fatalf("present file should be gone, stat=%v", err)
	}
}

func TestPassphrase_PrefersExplicitOverEnvironment(t *testing.T) {
	t.Setenv(EnvKeyPassphrase, "from-env")
	if got := Passphrase("explicit"); got != "explicit" {
		t.Fatalf("explicit passphrase must win, got %q", got)
	}
	if got := Passphrase(""); got != "from-env" {
		t.Fatalf("want env fallback, got %q", got)
	}
}

// A stored seed is read back exactly, is written 0600, and follows the same
// `key protect` envelope as the keys it derives.
func TestSeed_SaveLoadAndProtect(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	seed, err := NewSeed()
	if err != nil {
		t.Fatal(err)
	}
	path, err := SaveSeed("alice.poweur.net", seed)
	if err != nil {
		t.Fatal(err)
	}
	if want := SeedPath(filepath.Join(home, ".poweur", "keys"), "alice.poweur.net"); path != want {
		t.Fatalf("path = %s, want %s", path, want)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("seed file mode = %v, %v", info, err)
	}
	got, err := LoadSeed(path)
	if err != nil || !bytes.Equal(got, seed) {
		t.Fatalf("LoadSeed = %x, %v", got, err)
	}

	t.Setenv(EnvKeyPassphrase, "correct horse")
	if did, err := ProtectKeyFile(path, "correct horse"); err != nil || !did {
		t.Fatalf("protect = %v, %v", did, err)
	}
	if got, err := LoadSeed(path); err != nil || !bytes.Equal(got, seed) {
		t.Fatalf("LoadSeed (protected) = %x, %v", got, err)
	}
	t.Setenv(EnvKeyPassphrase, "")
	if _, err := LoadSeed(path); err == nil {
		t.Fatal("a protected seed loaded without the passphrase")
	}
}

func TestLoadSeed_MissingIsNotExist(t *testing.T) {
	_, err := LoadSeed(filepath.Join(t.TempDir(), "nobody.seed"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v", err)
	}
}

// Recovery from a seed stores the seed too, so the recovered device can approve.
func TestSaveKeysFromSeed_StoresTheSeed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	seed, _ := NewSeed()
	if _, _, err := SaveKeysFromSeed("alice.poweur.net", seed); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSeed(SeedPath(filepath.Join(home, ".poweur", "keys"), "alice.poweur.net"))
	if err != nil || !bytes.Equal(got, seed) {
		t.Fatalf("stored seed = %x, %v", got, err)
	}
}
