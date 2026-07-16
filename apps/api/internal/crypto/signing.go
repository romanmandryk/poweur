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
// including the client-assigned message id, session id, and encryption
// metadata when present. The id line binds the relay-stored identifier
// into the signature so delivery acknowledgements can reference a value
// the sender themselves committed to.
//
// Order is fixed:
//
//	<sender>
//	<recipient>
//	<timestamp>
//	<payload>
//	id:<message_id>           (only when id != "")
//	session:<session_id>      (only when sessionID != "")
//	enc:<alg>:<eph>:<nonce>   (only when enc != nil)
//
// When id, sessionID and enc are all empty/nil the output equals
// CanonicalMessage.
func CanonicalMessageFull(sender, recipient, timestamp, payload, id, sessionID string, enc *EncryptionMeta) string {
	parts := []string{sender, recipient, timestamp, payload}
	if id != "" {
		parts = append(parts, "id:"+id)
	}
	if sessionID != "" {
		parts = append(parts, "session:"+sessionID)
	}
	if enc != nil && enc.Alg != "" {
		parts = append(parts, "enc:"+enc.Alg+":"+enc.EphemeralPublicKey+":"+enc.Nonce)
	}
	return strings.Join(parts, "\n")
}

// CanonicalAck is the signing input for a delivery acknowledgement. Acks
// reference a previously-sent message by id and are signed by the
// recipient (or, in v1, whichever party transitioned the message into
// the new state). The state enum is open-ended for future expansion
// (`read`, etc.); v1 only emits `delivered_client`.
//
// Order:
//
//	ack
//	<id>
//	<message_id>
//	<state>
//	<sender>            (the party producing the ack)
//	<recipient>         (the party the ack is destined for, == original message sender)
//	<timestamp>
//	session:<session_id>  (only when sessionID != "")
func CanonicalAck(id, messageID, state, sender, recipient, timestamp, sessionID string) string {
	parts := []string{
		"ack",
		id,
		messageID,
		state,
		sender,
		recipient,
		timestamp,
	}
	if sessionID != "" {
		parts = append(parts, "session:"+sessionID)
	}
	return strings.Join(parts, "\n")
}

// CanonicalIdentityRegistration is the string an identity owner signs over
// a fresh registration body to prove they hold the private key matching the
// `public_key` they are publishing. Bound to the relay address so a
// captured registration cannot be replayed against a different relay.
func CanonicalIdentityRegistration(identity, publicKey, encryptionPublicKey, relayAddress, issuedAt, nonce string) string {
	return strings.Join([]string{
		"identity-registration",
		identity,
		publicKey,
		encryptionPublicKey,
		relayAddress,
		issuedAt,
		nonce,
	}, "\n")
}

// CanonicalIdentityExport is signed by the owner to authorize a full export.
func CanonicalIdentityExport(identity, issuedAt, nonce string) string {
	return strings.Join([]string{
		"identity-export",
		identity,
		issuedAt,
		nonce,
	}, "\n")
}

// CanonicalIdentityRotation is signed by the *old* key to authorize a key change.
func CanonicalIdentityRotation(identity, oldPublicKey, newPublicKey, issuedAt, nonce string) string {
	return strings.Join([]string{
		"identity-rotation",
		identity,
		oldPublicKey,
		newPublicKey,
		issuedAt,
		nonce,
	}, "\n")
}

// CanonicalEncryptionKeyUpdate is the string an identity owner signs to
// authorize an encryption-key (re)publication. Verified against the
// identity's long-lived signing key (resolved via DNS or local store).
func CanonicalEncryptionKeyUpdate(identity, encryptionPublicKey, issuedAt, nonce string) string {
	return strings.Join([]string{
		"identity-encryption-key",
		identity,
		encryptionPublicKey,
		issuedAt,
		nonce,
	}, "\n")
}

// CanonicalSessionRevocation is the string the identity owner signs to
// authorize a `DELETE /sessions/:id`. Verified against the identity's
// long-lived signing key.
func CanonicalSessionRevocation(identity, sessionID, issuedAt, nonce string) string {
	return strings.Join([]string{
		"session-revocation",
		identity,
		sessionID,
		issuedAt,
		nonce,
	}, "\n")
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
		if strings.HasPrefix(record, "poweur-pubkey=") {
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
	return nil, errors.New("poweur public key record not found")
}

// ParseEncryptionTXTRecord extracts an X25519 encryption public key from
// DNS TXT records under `_poweur-enc.<identity>`.
func ParseEncryptionTXTRecord(records []string) ([]byte, error) {
	for _, record := range records {
		record = strings.TrimSpace(record)
		if strings.HasPrefix(record, "poweur-enckey=") {
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
	return nil, errors.New("poweur encryption key record not found")
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
