package identity

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"

	"github.com/poweur/cli/internal/config"
	idpkg "github.com/poweur/identity"
)

func SavePrivateKey(identity string, privateKey ed25519.PrivateKey) (string, error) {
	keysDir, err := config.KeysDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(keysDir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(keysDir, identity+".key")
	encoded := base64.RawStdEncoding.EncodeToString(privateKey)
	if err := os.WriteFile(path, []byte(encoded), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// SaveEncryptionPrivateKey writes the X25519 private key for an identity.
func SaveEncryptionPrivateKey(identity string, privateKey []byte) (string, error) {
	keysDir, err := config.KeysDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(keysDir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(keysDir, identity+".enc")
	encoded := base64.RawStdEncoding.EncodeToString(privateKey)
	if err := os.WriteFile(path, []byte(encoded), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// SeedPath returns the path for an identity's stored master seed.
func SeedPath(keysDir, identity string) string {
	return filepath.Join(keysDir, identity+".seed")
}

// SaveSeed writes the master seed next to the derived keys, in the same format
// and with the same protections (mode 0600, `key protect`). The seed derives
// both keys, so it grants nothing the key files don't already; keeping it lets
// this device approve other devices (`poweur key approve`) the way the app does.
func SaveSeed(identity string, seed []byte) (string, error) {
	keysDir, err := config.KeysDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(keysDir, 0o700); err != nil {
		return "", err
	}
	path := SeedPath(keysDir, identity)
	encoded := base64.RawStdEncoding.EncodeToString(seed)
	if err := os.WriteFile(path, []byte(encoded), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// LoadSeed reads a stored master seed, decrypting a protected file with
// POWEUR_KEY_PASSPHRASE. A missing file returns an error wrapping os.ErrNotExist
// (identities created before seeds were stored have none).
func LoadSeed(path string) ([]byte, error) {
	raw, _, err := readKeyFile(path, "")
	if err != nil {
		return nil, err
	}
	decoded, err := decodeKeyBytes(raw)
	if err != nil {
		return nil, err
	}
	if len(decoded) != SeedLen {
		return nil, errors.New("invalid seed size")
	}
	return decoded, nil
}

// LoadPrivateKey reads a signing key, transparently decrypting a
// passphrase-protected file (EPIC-011 E11-T4). The passphrase comes from
// POWEUR_KEY_PASSPHRASE; use LoadPrivateKeyWithPassphrase to supply one
// directly.
func LoadPrivateKey(path string) (ed25519.PrivateKey, error) {
	priv, _, err := LoadPrivateKeyWithPassphrase(path, "")
	return priv, err
}

// LoadEncryptionPrivateKey reads the X25519 private key for an identity.
// Returns os.ErrNotExist wrapped when the key file is missing so callers can
// surface an actionable error — there is no plaintext fallback, so a missing
// key means the identity cannot decrypt inbound messages; restore it with
// `poweur key recover` or `poweur key enroll`.
func LoadEncryptionPrivateKey(path string) ([]byte, error) {
	key, _, err := LoadEncryptionPrivateKeyWithPassphrase(path, "")
	return key, err
}

func KeyPath(keysDir, identity string) string {
	return filepath.Join(keysDir, identity+".key")
}

// EncryptionKeyPath returns the path for the X25519 private key file.
func EncryptionKeyPath(keysDir, identity string) string {
	return filepath.Join(keysDir, identity+".enc")
}

// AnyKeyFileExists reports whether any of the paths is already on disk.
// `identity create` uses this so it never overwrites a real keypair, then
// deletes leftovers if the relay refuses the name.
func AnyKeyFileExists(paths ...string) bool {
	for _, path := range paths {
		if path == "" {
			continue
		}
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	return false
}

// RemoveKeyFiles deletes the given paths, ignoring missing files. Used to
// unwind a failed `identity create` so a later enroll/recover is not
// shadowed by a keypair the relay never accepted.
func RemoveKeyFiles(paths ...string) {
	for _, path := range paths {
		if path == "" {
			continue
		}
		_ = os.Remove(path)
	}
}

func PublicKeyString(publicKey ed25519.PublicKey) string {
	return base64.RawURLEncoding.EncodeToString(publicKey)
}

// ── Master-seed derivation (EPIC-011 E11-T1) ─────────────────────────────────
//
// Derivation itself lives in packages/identity so the relay, the CLI and the
// TypeScript client all agree; these are the CLI-shaped wrappers.

// SeedLen is the length of a master seed in bytes.
const SeedLen = idpkg.SeedLen

// NewSeed returns a fresh random master seed.
func NewSeed() ([]byte, error) { return idpkg.NewSeed() }

// ParseSeed accepts either encoding of a master seed: unpadded base64url, or a
// 24-word BIP39 recovery mnemonic. Every --seed flag therefore takes a pasted
// recovery kit without a second option.
func ParseSeed(encoded string) ([]byte, error) { return idpkg.ParseSeedOrMnemonic(encoded) }

// SeedToMnemonic encodes a seed as a 24-word recovery mnemonic.
func SeedToMnemonic(seed []byte) (string, error) { return idpkg.SeedToMnemonic(seed) }

// NewRecoveryKit builds both encodings of a seed for one identity.
func NewRecoveryKit(identity, relay string, seed []byte) (idpkg.RecoveryKit, error) {
	return idpkg.NewRecoveryKit(identity, relay, seed)
}

// FormatSeed renders a master seed for display or storage.
func FormatSeed(seed []byte) string { return idpkg.EncodeSeed(seed) }

// KeypairFromSeed derives the identity's Ed25519 signing keypair.
func KeypairFromSeed(seed []byte) (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return idpkg.DeriveSigningKey(seed)
}

// EncryptionKeypairFromSeed derives the identity's X25519 encryption keypair.
func EncryptionKeypairFromSeed(seed []byte) (publicKey, privateKey []byte, err error) {
	return idpkg.DeriveEncryptionKey(seed)
}

// SaveKeysFromSeed derives both long-lived keys and writes them to the keys
// directory — the recovery path, requiring nothing but the seed.
func SaveKeysFromSeed(identity string, seed []byte) (keyPath, encKeyPath string, err error) {
	_, priv, err := KeypairFromSeed(seed)
	if err != nil {
		return "", "", err
	}
	_, encPriv, err := EncryptionKeypairFromSeed(seed)
	if err != nil {
		return "", "", err
	}
	if keyPath, err = SavePrivateKey(identity, priv); err != nil {
		return "", "", err
	}
	if encKeyPath, err = SaveEncryptionPrivateKey(identity, encPriv); err != nil {
		return "", "", err
	}
	if _, err = SaveSeed(identity, seed); err != nil {
		return "", "", err
	}
	return keyPath, encKeyPath, nil
}
