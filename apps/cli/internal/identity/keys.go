package identity

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"

	"github.com/eurything/cli/internal/config"
	cryptoe2e "github.com/eurything/cli/internal/crypto"
)

// GenerateKeypair returns a new Ed25519 signing keypair for long-lived identity signing.
func GenerateKeypair() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return ed25519.GenerateKey(nil)
}

// GenerateEncryptionKeypair returns a new X25519 keypair used for end-to-end
// payload encryption. Public keys are published in DNS alongside signing keys.
func GenerateEncryptionKeypair() (publicKey, privateKey []byte, err error) {
	return cryptoe2e.GenerateX25519Keypair()
}

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

func LoadPrivateKey(path string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	decoded, err := base64.RawStdEncoding.DecodeString(string(data))
	if err != nil {
		decoded, err = base64.StdEncoding.DecodeString(string(data))
		if err != nil {
			return nil, err
		}
	}
	if len(decoded) != ed25519.PrivateKeySize {
		return nil, errors.New("invalid private key size")
	}
	return ed25519.PrivateKey(decoded), nil
}

// LoadEncryptionPrivateKey reads the X25519 private key for an identity.
// Returns os.ErrNotExist wrapped when the key file is missing so callers can
// gracefully degrade to plaintext messaging.
func LoadEncryptionPrivateKey(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	decoded, err := base64.RawStdEncoding.DecodeString(string(data))
	if err != nil {
		decoded, err = base64.StdEncoding.DecodeString(string(data))
		if err != nil {
			return nil, err
		}
	}
	if len(decoded) != 32 {
		return nil, errors.New("invalid x25519 private key size")
	}
	return decoded, nil
}

func KeyPath(keysDir, identity string) string {
	return filepath.Join(keysDir, identity+".key")
}

// EncryptionKeyPath returns the path for the X25519 private key file.
func EncryptionKeyPath(keysDir, identity string) string {
	return filepath.Join(keysDir, identity+".enc")
}

func PublicKeyString(publicKey ed25519.PublicKey) string {
	return base64.RawURLEncoding.EncodeToString(publicKey)
}
