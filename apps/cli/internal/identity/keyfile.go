package identity

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/crypto/scrypt"
)

// Key files at rest (EPIC-011 E11-T4).
//
// The CLI historically wrote private keys as plaintext base64 with mode 0600 —
// defensible for a bot on a hardened host, not for a human's laptop. This adds
// a passphrase-encrypted envelope alongside it.
//
// Both formats stay readable: an encrypted file is JSON and begins with '{',
// plaintext is bare base64. Detection is by shape rather than by extension, so
// migration needs no rename and no config flag.

// EnvKeyPassphrase supplies the passphrase non-interactively (daemons, CI).
const EnvKeyPassphrase = "POWEUR_KEY_PASSPHRASE"

// scrypt parameters. N=32768 is ~100ms on commodity hardware in 2026 — enough
// to make a stolen key file expensive to attack, cheap enough to run on every
// CLI invocation.
const (
	scryptN      = 32768
	scryptR      = 8
	scryptP      = 1
	scryptKeyLen = 32
	saltLen      = 16
)

// ErrPassphraseRequired is returned when a key file is encrypted but no
// passphrase was supplied.
var ErrPassphraseRequired = errors.New("key file is encrypted: set " + EnvKeyPassphrase + " or pass --passphrase")

// ErrWrongPassphrase is returned when decryption fails authentication.
var ErrWrongPassphrase = errors.New("wrong passphrase for key file")

// keyEnvelope is the on-disk form of an encrypted key.
type keyEnvelope struct {
	Version    int    `json:"v"`
	KDF        string `json:"kdf"`
	N          int    `json:"n"`
	R          int    `json:"r"`
	P          int    `json:"p"`
	Salt       string `json:"salt"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
}

// IsEncryptedKeyFile reports whether raw holds an encrypted envelope.
func IsEncryptedKeyFile(raw []byte) bool {
	return strings.HasPrefix(strings.TrimSpace(string(raw)), "{")
}

func deriveFileKey(passphrase string, salt []byte, n, r, p int) ([]byte, error) {
	if passphrase == "" {
		return nil, ErrPassphraseRequired
	}
	return scrypt.Key([]byte(passphrase), salt, n, r, p, scryptKeyLen)
}

// EncryptKeyMaterial wraps raw key bytes in a passphrase-protected envelope.
func EncryptKeyMaterial(raw []byte, passphrase string) ([]byte, error) {
	if passphrase == "" {
		return nil, ErrPassphraseRequired
	}
	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, err
	}
	key, err := deriveFileKey(passphrase, salt, scryptN, scryptR, scryptP)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	envelope := keyEnvelope{
		Version: 1, KDF: "scrypt", N: scryptN, R: scryptR, P: scryptP,
		Salt:       base64.RawURLEncoding.EncodeToString(salt),
		Nonce:      base64.RawURLEncoding.EncodeToString(nonce),
		Ciphertext: base64.RawURLEncoding.EncodeToString(aead.Seal(nil, nonce, raw, nil)),
	}
	return json.MarshalIndent(envelope, "", "  ")
}

// DecryptKeyMaterial unwraps an envelope written by EncryptKeyMaterial.
func DecryptKeyMaterial(raw []byte, passphrase string) ([]byte, error) {
	var envelope keyEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("malformed encrypted key file: %w", err)
	}
	if envelope.Version != 1 || envelope.KDF != "scrypt" {
		return nil, fmt.Errorf("unsupported key file (v%d, kdf %q)", envelope.Version, envelope.KDF)
	}
	salt, err := base64.RawURLEncoding.DecodeString(envelope.Salt)
	if err != nil {
		return nil, fmt.Errorf("malformed salt: %w", err)
	}
	nonce, err := base64.RawURLEncoding.DecodeString(envelope.Nonce)
	if err != nil {
		return nil, fmt.Errorf("malformed nonce: %w", err)
	}
	ciphertext, err := base64.RawURLEncoding.DecodeString(envelope.Ciphertext)
	if err != nil {
		return nil, fmt.Errorf("malformed ciphertext: %w", err)
	}
	key, err := deriveFileKey(passphrase, salt, envelope.N, envelope.R, envelope.P)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		// GCM authentication failure is indistinguishable from corruption; a
		// wrong passphrase is overwhelmingly the likely cause.
		return nil, ErrWrongPassphrase
	}
	return plain, nil
}

// Passphrase returns the configured passphrase, preferring an explicit value
// over the environment. Empty means none was supplied.
func Passphrase(explicit string) string {
	if explicit != "" {
		return explicit
	}
	return os.Getenv(EnvKeyPassphrase)
}

// ProtectKeyFile encrypts a plaintext key file in place. Idempotent: an
// already-encrypted file is left alone rather than double-wrapped.
func ProtectKeyFile(path, passphrase string) (bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	if IsEncryptedKeyFile(raw) {
		return false, nil
	}
	envelope, err := EncryptKeyMaterial(raw, passphrase)
	if err != nil {
		return false, err
	}
	// Write via a temp file so an interrupted migration cannot truncate the
	// only copy of an identity's key.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, envelope, 0o600); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return false, err
	}
	return true, nil
}

// UnprotectKeyFile decrypts an encrypted key file back to plaintext.
func UnprotectKeyFile(path, passphrase string) (bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	if !IsEncryptedKeyFile(raw) {
		return false, nil
	}
	plain, err := DecryptKeyMaterial(raw, passphrase)
	if err != nil {
		return false, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, plain, 0o600); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return false, err
	}
	return true, nil
}

// readKeyFile returns the decoded key bytes, transparently decrypting an
// envelope. legacyPlaintext reports whether the file was unprotected, so
// callers can warn.
func readKeyFile(path, passphrase string) (raw []byte, legacyPlaintext bool, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	if IsEncryptedKeyFile(data) {
		plain, err := DecryptKeyMaterial(data, Passphrase(passphrase))
		if err != nil {
			return nil, false, err
		}
		return plain, false, nil
	}
	return data, true, nil
}

func decodeKeyBytes(data []byte) ([]byte, error) {
	text := strings.TrimSpace(string(data))
	if decoded, err := base64.RawStdEncoding.DecodeString(text); err == nil {
		return decoded, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(text)
	if err != nil {
		return nil, err
	}
	return decoded, nil
}

// LoadPrivateKeyWithPassphrase reads a signing key, decrypting if needed.
func LoadPrivateKeyWithPassphrase(path, passphrase string) (ed25519.PrivateKey, bool, error) {
	raw, legacy, err := readKeyFile(path, passphrase)
	if err != nil {
		return nil, false, err
	}
	decoded, err := decodeKeyBytes(raw)
	if err != nil {
		return nil, legacy, err
	}
	if len(decoded) != ed25519.PrivateKeySize {
		return nil, legacy, errors.New("invalid private key size")
	}
	return ed25519.PrivateKey(decoded), legacy, nil
}

// LoadEncryptionPrivateKeyWithPassphrase reads an X25519 key, decrypting if needed.
func LoadEncryptionPrivateKeyWithPassphrase(path, passphrase string) ([]byte, bool, error) {
	raw, legacy, err := readKeyFile(path, passphrase)
	if err != nil {
		return nil, false, err
	}
	decoded, err := decodeKeyBytes(raw)
	if err != nil {
		return nil, legacy, err
	}
	if len(decoded) != 32 {
		return nil, legacy, errors.New("invalid x25519 private key size")
	}
	return decoded, legacy, nil
}
