// Package session stores short-lived session keys on disk. Each session binds
// a device-local ephemeral Ed25519 keypair to a relay-assigned session id and
// the long-lived identity that authorized it.
package session

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/pelletier/go-toml/v2"
)

type Session struct {
	Identity          string    `toml:"identity"`
	SessionID         string    `toml:"session_id"`
	SessionPrivateKey string    `toml:"session_private_key"`
	SessionPublicKey  string    `toml:"session_public_key"`
	IssuedAt          time.Time `toml:"issued_at"`
	ExpiresAt         time.Time `toml:"expires_at"`
	RelayURL          string    `toml:"relay_url"`

	// Raw strings and identity signature produced at registration time, used
	// to attach a self-contained SessionProof to outbound messages so any
	// relay (not only the one that issued the session) can verify them.
	IssuedAtRaw       string `toml:"issued_at_raw"`
	ExpiresAtRaw      string `toml:"expires_at_raw"`
	Nonce             string `toml:"nonce"`
	IdentitySignature string `toml:"identity_signature"`
}

// Dir returns the sessions directory under the user's home.
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".eurything", "sessions"), nil
}

// Path returns the TOML file path for the given identity.
func Path(identity string) (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, identity+".toml"), nil
}

func Load(identity string) (Session, error) {
	path, err := Path(identity)
	if err != nil {
		return Session{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Session{}, err
	}
	var sess Session
	if err := toml.Unmarshal(data, &sess); err != nil {
		return Session{}, err
	}
	return sess, nil
}

func Save(sess Session) error {
	path, err := Path(sess.Identity)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := toml.Marshal(sess)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func Delete(identity string) error {
	path, err := Path(identity)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// IsValid reports whether the session has an id and is still within its TTL
// with a small clock-skew buffer. Expired or missing sessions trigger a
// re-registration flow in the CLI (which on mobile means prompting passkey).
func (s Session) IsValid() bool {
	if s.SessionID == "" || s.SessionPrivateKey == "" {
		return false
	}
	return time.Now().Add(30 * time.Second).Before(s.ExpiresAt)
}

func (s Session) PrivateKey() (ed25519.PrivateKey, error) {
	decoded, err := base64.RawStdEncoding.DecodeString(s.SessionPrivateKey)
	if err != nil {
		decoded, err = base64.StdEncoding.DecodeString(s.SessionPrivateKey)
		if err != nil {
			return nil, err
		}
	}
	if len(decoded) != ed25519.PrivateKeySize {
		return nil, errors.New("invalid session private key size")
	}
	return ed25519.PrivateKey(decoded), nil
}
