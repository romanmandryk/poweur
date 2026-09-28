package identity

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

// SealAlgorithm is the fixed identifier for the current scheme. Versioned so the
// relay and receivers can reject unknown algorithms.
const SealAlgorithm = "x25519-chacha20-poly1305"

// SealedPayload is the shared result of encrypting a message body.
// All fields are base64url-encoded (no padding) for transport.
type SealedPayload struct {
	Ciphertext         string `json:"ciphertext"`
	EphemeralPublicKey string `json:"ephemeral_public_key"`
	Nonce              string `json:"nonce"`
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

// Seal seals plaintext for the recipient's X25519 public key.
func Seal(recipientPublicKey, plaintext []byte, domain string, context []byte) (SealedPayload, error) {
	return sealWithRandom(recipientPublicKey, plaintext, domain, context, rand.Reader)
}

func sealWithRandom(recipientPublicKey, plaintext []byte, domain string, context []byte, random io.Reader) (SealedPayload, error) {
	if err := validateSealDomain(domain, context); err != nil {
		return SealedPayload{}, err
	}
	if len(recipientPublicKey) != 32 {
		return SealedPayload{}, errors.New("recipient public key must be 32 bytes")
	}
	ephemeralPriv := make([]byte, 32)
	if _, err := io.ReadFull(random, ephemeralPriv); err != nil {
		return SealedPayload{}, err
	}
	ephemeralPub, err := curve25519.X25519(ephemeralPriv, curve25519.Basepoint)
	if err != nil {
		return SealedPayload{}, err
	}
	shared, err := curve25519.X25519(ephemeralPriv, recipientPublicKey)
	if err != nil {
		return SealedPayload{}, err
	}
	key, err := deriveSealKey(shared, ephemeralPub, recipientPublicKey, domain)
	if err != nil {
		return SealedPayload{}, err
	}
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return SealedPayload{}, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(random, nonce); err != nil {
		return SealedPayload{}, err
	}
	ad := sealAAD(ephemeralPub, recipientPublicKey, domain, context)
	ciphertext := aead.Seal(nil, nonce, plaintext, ad)

	return SealedPayload{
		Ciphertext:         base64.RawURLEncoding.EncodeToString(ciphertext),
		EphemeralPublicKey: base64.RawURLEncoding.EncodeToString(ephemeralPub),
		Nonce:              base64.RawURLEncoding.EncodeToString(nonce),
	}, nil
}

// OpenSeal opens a payload that was sealed with the recipient's matching X25519 private key.
func OpenSeal(recipientPrivateKey []byte, payload SealedPayload, domain string, context []byte) ([]byte, error) {
	if err := validateSealDomain(domain, context); err != nil {
		return nil, err
	}
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
	if len(nonce) != chacha20poly1305.NonceSize {
		return nil, errors.New("nonce must be 12 bytes")
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
	key, err := deriveSealKey(shared, ephemeralPub, recipientPublicKey, domain)
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}
	ad := sealAAD(ephemeralPub, recipientPublicKey, domain, context)
	return aead.Open(nil, nonce, ciphertext, ad)
}

func deriveSealKey(shared, ephemeralPub, recipientPub []byte, domain string) ([]byte, error) {
	salt := append([]byte{}, ephemeralPub...)
	salt = append(salt, recipientPub...)
	info := []byte(domain)
	kdf := hkdf.New(sha256.New, shared, salt, info)
	key := make([]byte, chacha20poly1305.KeySize)
	if _, err := io.ReadFull(kdf, key); err != nil {
		return nil, err
	}
	return key, nil
}

func sealAAD(ephemeralPub, recipientPub []byte, domain string, context []byte) []byte {
	ad := []byte(domain + "\n")
	ad = append(ad, ephemeralPub...)
	ad = append(ad, recipientPub...)
	ad = append(ad, context...)
	return ad
}

// Domains are closed to prevent accidentally changing the message wire construction.
const (
	MessageSealDomain = "poweur/msg/v1"
	DriveSealDomain   = "poweur/drive/seal/v1"
	DriveNameDomain   = "poweur/drive/name/v1"
	DriveRecordDomain = "poweur/drive/record/v1"
)

func validateSealDomain(domain string, context []byte) error {
	switch domain {
	case MessageSealDomain:
		if len(context) != 0 {
			return errors.New("message seals do not accept extra context")
		}
	case DriveSealDomain, DriveNameDomain, DriveRecordDomain:
		if len(context) == 0 {
			return errors.New("drive seals require context")
		}
	default:
		return errors.New("unsupported seal domain")
	}
	return nil
}
