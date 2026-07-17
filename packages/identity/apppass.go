package identity

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// App passwords (EPIC-003 E03-T3): named Basic-auth credentials for legacy
// WebDAV clients. The owner's client generates the password, hashes it with
// argon2id, and stores the hash in `poweur-sys/relay/app-passwords.json`;
// the relay verifies Basic auth against that file. This lives in the shared
// identity package so CLI/web (writers) and relay (verifier) agree on the
// format.

// AppPassword is one entry in app-passwords.json.
type AppPassword struct {
	Name      string `json:"name"`
	Hash      string `json:"hash"` // PHC-format argon2id
	Scope     string `json:"scope,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

// AppPasswordsFile is the schema of poweur-sys/relay/app-passwords.json.
type AppPasswordsFile struct {
	Passwords []AppPassword `json:"passwords"`
}

// ParseAppPasswordsFile decodes app-passwords.json.
func ParseAppPasswordsFile(raw []byte) (AppPasswordsFile, error) {
	var f AppPasswordsFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return AppPasswordsFile{}, fmt.Errorf("invalid app-passwords.json: %w", err)
	}
	return f, nil
}

const (
	argonTime    = 1
	argonMemory  = 64 * 1024 // 64 MiB
	argonThreads = 4
	argonKeyLen  = 32
	argonSaltLen = 16
)

// HashAppPassword returns a PHC-format argon2id hash of password.
func HashAppPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyAppPassword reports whether password matches a PHC argon2id hash.
func VerifyAppPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	// "", "argon2id", "v=19", "m=...,t=...,p=...", salt, hash
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}
	var m uint32
	var t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// GenerateAppPassword returns a new random password in the poweur-ap-… form.
func GenerateAppPassword() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "poweur-ap-" + base64.RawURLEncoding.EncodeToString(buf), nil
}
