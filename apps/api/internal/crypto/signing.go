package crypto

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// EncryptionMeta matches the encryption envelope attached to a message.
// It is intentionally defined in this package so canonical signing can be
// computed without depending on the higher level relay types.
type EncryptionMeta struct {
	Alg                string
	EphemeralPublicKey string
	Nonce              string
}

// CanonicalMessage is the v1 canonical string used when neither session keys
// nor end-to-end encryption metadata are present. Kept for backward compatibility.
func CanonicalMessage(sender, recipient, timestamp, payload string) string {
	return fmt.Sprintf("%s\n%s\n%s\n%s", sender, recipient, timestamp, payload)
}

// CanonicalMessageFull returns the canonical signing input for a message,
// including session id and encryption metadata when present.
// When sessionID is empty and enc is nil, the output equals CanonicalMessage.
func CanonicalMessageFull(sender, recipient, timestamp, payload, sessionID string, enc *EncryptionMeta) string {
	parts := []string{sender, recipient, timestamp, payload}
	if sessionID != "" {
		parts = append(parts, "session:"+sessionID)
	}
	if enc != nil && enc.Alg != "" {
		parts = append(parts, "enc:"+enc.Alg+":"+enc.EphemeralPublicKey+":"+enc.Nonce)
	}
	return strings.Join(parts, "\n")
}

// CanonicalSessionRegistration is the string signed by the long-lived identity
// key to authorize a short-lived session public key.
func CanonicalSessionRegistration(identity, sessionPublicKey, issuedAt, expiresAt, nonce string) string {
	return fmt.Sprintf("session-registration\n%s\n%s\n%s\n%s\n%s",
		identity, sessionPublicKey, issuedAt, expiresAt, nonce)
}

func ParsePublicKey(publicKey string) (ed25519.PublicKey, error) {
	decoded, err := decodeAnyBase64(publicKey)
	if err != nil {
		return nil, err
	}
	if len(decoded) != ed25519.PublicKeySize {
		return nil, errors.New("invalid public key length")
	}
	return ed25519.PublicKey(decoded), nil
}

func NormalizePublicKey(publicKey string) (string, ed25519.PublicKey, error) {
	decoded, err := decodeAnyBase64(publicKey)
	if err != nil {
		return "", nil, err
	}
	if len(decoded) != ed25519.PublicKeySize {
		return "", nil, errors.New("invalid public key length")
	}
	normalized := base64.RawURLEncoding.EncodeToString(decoded)
	return normalized, ed25519.PublicKey(decoded), nil
}

// NormalizeX25519PublicKey validates and normalizes an X25519 encryption
// public key. Returned bytes are the raw 32-byte key.
func NormalizeX25519PublicKey(publicKey string) (string, []byte, error) {
	decoded, err := decodeAnyBase64(publicKey)
	if err != nil {
		return "", nil, err
	}
	if len(decoded) != 32 {
		return "", nil, errors.New("invalid x25519 public key length")
	}
	normalized := base64.RawURLEncoding.EncodeToString(decoded)
	return normalized, decoded, nil
}

func VerifySignature(publicKey ed25519.PublicKey, payload string, signatureB64 string) error {
	signature, err := decodeAnyBase64(signatureB64)
	if err != nil {
		return err
	}
	if len(signature) != ed25519.SignatureSize {
		return errors.New("invalid signature length")
	}
	if !ed25519.Verify(publicKey, []byte(payload), signature) {
		return errors.New("signature verification failed")
	}
	return nil
}

func ParseTXTRecord(records []string) (ed25519.PublicKey, error) {
	for _, record := range records {
		record = strings.TrimSpace(record)
		if strings.HasPrefix(record, "eurything-pubkey=") {
			parts := strings.SplitN(record, "=", 2)
			if len(parts) != 2 {
				continue
			}
			value := parts[1]
			if strings.HasPrefix(value, "ed25519:") {
				value = strings.TrimPrefix(value, "ed25519:")
			}
			return ParsePublicKey(value)
		}
	}
	return nil, errors.New("eurything public key record not found")
}

// ParseEncryptionTXTRecord extracts an X25519 encryption public key from
// DNS TXT records under `_eurything-enc.<identity>`.
func ParseEncryptionTXTRecord(records []string) ([]byte, error) {
	for _, record := range records {
		record = strings.TrimSpace(record)
		if strings.HasPrefix(record, "eurything-enckey=") {
			parts := strings.SplitN(record, "=", 2)
			if len(parts) != 2 {
				continue
			}
			value := parts[1]
			if strings.HasPrefix(value, "x25519:") {
				value = strings.TrimPrefix(value, "x25519:")
			}
			_, raw, err := NormalizeX25519PublicKey(value)
			if err != nil {
				return nil, err
			}
			return raw, nil
		}
	}
	return nil, errors.New("eurything encryption key record not found")
}

func decodeAnyBase64(value string) ([]byte, error) {
	if decoded, err := base64.RawStdEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	if decoded, err := base64.RawURLEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	if decoded, err := base64.URLEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	return base64.StdEncoding.DecodeString(value)
}
