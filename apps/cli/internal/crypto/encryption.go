// Package crypto provides the CLI-side end-to-end encryption primitives.
// Messages use X25519 ECDH between an ephemeral sender keypair and the
// recipient's long-lived X25519 public key, followed by HKDF-SHA256 key
// derivation and ChaCha20-Poly1305 authenticated encryption.
package crypto

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
)

// AlgName is the fixed identifier for the current scheme. Versioned so the
// relay and receivers can reject unknown algorithms.
const AlgName = "x25519-chacha20-poly1305"

// EncryptedPayload is the CLI-level result of encrypting a message body.
// All fields are base64url-encoded (no padding) for transport.
type EncryptedPayload struct {
	Ciphertext         string
	EphemeralPublicKey string
	Nonce              string
}

// GenerateX25519Keypair returns a fresh X25519 keypair. Both slices are 32 bytes.
func GenerateX25519Keypair() (publicKey, privateKey []byte, err error) {
	priv := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, priv); err != nil {
		return nil, nil, err
	}
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		return nil, nil, err
	}
	return pub, priv, nil
}

// Encrypt seals plaintext for the recipient's X25519 public key.
func Encrypt(recipientPublicKey, plaintext []byte) (EncryptedPayload, error) {
	if len(recipientPublicKey) != 32 {
		return EncryptedPayload{}, errors.New("recipient public key must be 32 bytes")
	}
	ephemeralPub, ephemeralPriv, err := GenerateX25519Keypair()
	if err != nil {
		return EncryptedPayload{}, err
	}
	shared, err := curve25519.X25519(ephemeralPriv, recipientPublicKey)
	if err != nil {
		return EncryptedPayload{}, err
	}
	key, err := deriveKey(shared, ephemeralPub, recipientPublicKey)
	if err != nil {
		return EncryptedPayload{}, err
	}
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return EncryptedPayload{}, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return EncryptedPayload{}, err
	}
	ad := buildAAD(ephemeralPub, recipientPublicKey)
	ciphertext := aead.Seal(nil, nonce, plaintext, ad)

	return EncryptedPayload{
		Ciphertext:         base64.RawURLEncoding.EncodeToString(ciphertext),
		EphemeralPublicKey: base64.RawURLEncoding.EncodeToString(ephemeralPub),
		Nonce:              base64.RawURLEncoding.EncodeToString(nonce),
	}, nil
}

// Decrypt opens a message that was sealed with the recipient's matching X25519 private key.
func Decrypt(recipientPrivateKey []byte, payload EncryptedPayload) ([]byte, error) {
	if len(recipientPrivateKey) != 32 {
		return nil, errors.New("recipient private key must be 32 bytes")
	}
	ephemeralPub, err := decodeBase64(payload.EphemeralPublicKey)
	if err != nil {
		return nil, fmt.Errorf("decode ephemeral pub: %w", err)
	}
	if len(ephemeralPub) != 32 {
		return nil, errors.New("ephemeral public key must be 32 bytes")
	}
	nonce, err := decodeBase64(payload.Nonce)
	if err != nil {
		return nil, fmt.Errorf("decode nonce: %w", err)
	}
	ciphertext, err := decodeBase64(payload.Ciphertext)
	if err != nil {
		return nil, fmt.Errorf("decode ciphertext: %w", err)
	}

	recipientPublicKey, err := curve25519.X25519(recipientPrivateKey, curve25519.Basepoint)
	if err != nil {
		return nil, err
	}
	shared, err := curve25519.X25519(recipientPrivateKey, ephemeralPub)
	if err != nil {
		return nil, err
	}
	key, err := deriveKey(shared, ephemeralPub, recipientPublicKey)
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}
	ad := buildAAD(ephemeralPub, recipientPublicKey)
	return aead.Open(nil, nonce, ciphertext, ad)
}

func deriveKey(shared, ephemeralPub, recipientPub []byte) ([]byte, error) {
	salt := append([]byte{}, ephemeralPub...)
	salt = append(salt, recipientPub...)
	info := []byte("eurything/msg/v1")
	kdf := hkdf.New(sha256.New, shared, salt, info)
	key := make([]byte, chacha20poly1305.KeySize)
	if _, err := io.ReadFull(kdf, key); err != nil {
		return nil, err
	}
	return key, nil
}

func buildAAD(ephemeralPub, recipientPub []byte) []byte {
	ad := []byte("eurything/msg/v1\n")
	ad = append(ad, ephemeralPub...)
	ad = append(ad, recipientPub...)
	return ad
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
