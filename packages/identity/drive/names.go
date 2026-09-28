package drive

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	identity "github.com/poweur/identity"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
	"golang.org/x/text/unicode/norm"
)

// NormalizeName returns the canonical, case-sensitive NFC filename. Reserved
// root names are enforced by the drive engine, not this general filename codec.
func NormalizeName(name string) (string, error) {
	if !utf8.ValidString(name) {
		return "", errors.New("name is not UTF-8")
	}
	name = norm.NFC.String(name)
	if len(name) == 0 || len(name) > 255 || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
		return "", errors.New("invalid drive name")
	}
	return name, nil
}

// NameHash is the parent-private lookup token. It deliberately cannot be
// computed by a create-only visitor holding only the folder's public key.
func NameHash(parentPrivate []byte, name string) (string, error) {
	normalized, err := NormalizeName(name)
	if err != nil {
		return "", err
	}
	if len(parentPrivate) != 32 {
		return "", errors.New("parent key must be 32 bytes")
	}
	pub, err := curve25519.X25519(parentPrivate, curve25519.Basepoint)
	if err != nil {
		return "", err
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, parentPrivate, pub, []byte("poweur/drive/name-index/v1")), key); err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(normalized))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func SealName(parentPublic []byte, name string, context []byte) (identity.SealedPayload, error) {
	normalized, err := NormalizeName(name)
	if err != nil {
		return identity.SealedPayload{}, err
	}
	return identity.Seal(parentPublic, []byte(normalized), identity.DriveNameDomain, context)
}

// OpenName verifies the encrypted UTF-8 name is already canonical; accepting
// alternate encodings here would let a signed token mean different names.
func OpenName(parentPrivate []byte, payload identity.SealedPayload, context []byte) (string, error) {
	raw, err := identity.OpenSeal(parentPrivate, payload, identity.DriveNameDomain, context)
	if err != nil {
		return "", err
	}
	name, err := NormalizeName(string(raw))
	if err != nil {
		return "", err
	}
	if name != string(raw) {
		return "", errors.New("encrypted name is not NFC")
	}
	return name, nil
}
