package crypto

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

func CanonicalMessage(sender, recipient, timestamp, payload string) string {
	return fmt.Sprintf("%s\n%s\n%s\n%s", sender, recipient, timestamp, payload)
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

func VerifySignature(publicKey ed25519.PublicKey, payload string, signatureB64 string) error {
	signature, err := base64.StdEncoding.DecodeString(signatureB64)
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

func decodeAnyBase64(value string) ([]byte, error) {
	if decoded, err := base64.RawStdEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	if decoded, err := base64.RawURLEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	return base64.StdEncoding.DecodeString(value)
}
