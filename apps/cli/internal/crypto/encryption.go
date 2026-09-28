// Package crypto provides CLI encryption using the shared identity construction.
package crypto

import (
	"encoding/base64"
	"errors"
	idpkg "github.com/poweur/identity"
	"golang.org/x/crypto/curve25519"
)

const AlgName = idpkg.SealAlgorithm

type EncryptedPayload = idpkg.SealedPayload

func GenerateX25519Keypair() (publicKey, privateKey []byte, err error) {
	return idpkg.GenerateX25519Keypair()
}

func Encrypt(recipientPublicKey, plaintext []byte) (EncryptedPayload, error) {
	return idpkg.Seal(recipientPublicKey, plaintext, idpkg.MessageSealDomain, nil)
}

func Decrypt(recipientPrivateKey []byte, payload EncryptedPayload) ([]byte, error) {
	return idpkg.OpenSeal(recipientPrivateKey, payload, idpkg.MessageSealDomain, nil)
}

func decodeBase64(value string) ([]byte, error) {
	if decoded, err := base64.RawURLEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	if decoded, err := base64.URLEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	if decoded, err := base64.RawStdEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	return base64.StdEncoding.DecodeString(value)
}

// EncodePublicKey returns the base64url (unpadded) string form of an X25519 public key.
func EncodePublicKey(key []byte) string {
	return base64.RawURLEncoding.EncodeToString(key)
}

// PublicFromPrivate derives the X25519 public key for a given private key.
func PublicFromPrivate(privateKey []byte) ([]byte, error) {
	if len(privateKey) != 32 {
		return nil, errors.New("private key must be 32 bytes")
	}
	return curve25519.X25519(privateKey, curve25519.Basepoint)
}

// DecodePublicKey decodes a base64-encoded X25519 public key. Accepts URL-safe or standard base64.
func DecodePublicKey(value string) ([]byte, error) {
	raw, err := decodeBase64(value)
	if err != nil {
		return nil, err
	}
	if len(raw) != 32 {
		return nil, errors.New("invalid x25519 public key length")
	}
	return raw, nil
}
