package identity

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"

	"github.com/eurything/cli/internal/config"
)

func GenerateKeypair() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return ed25519.GenerateKey(nil)
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

func KeyPath(keysDir, identity string) string {
	return filepath.Join(keysDir, identity+".key")
}

func PublicKeyString(publicKey ed25519.PublicKey) string {
	return base64.RawURLEncoding.EncodeToString(publicKey)
}
