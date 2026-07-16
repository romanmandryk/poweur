package relay

import "encoding/json"

type EncryptionMeta struct {
	Alg                string `json:"alg"`
	EphemeralPublicKey string `json:"ephemeral_public_key"`
	Nonce              string `json:"nonce"`
}

// SessionProof is a self-contained proof that a given session public key was
// authorized by the long-lived identity. It allows any relay to verify a
// session-signed forwarded message without consulting the session cache of the
// relay that originally issued the session.
type SessionProof struct {
	SessionPublicKey  string `json:"session_public_key"`
	IssuedAt          string `json:"issued_at"`
	ExpiresAt         string `json:"expires_at"`
	Nonce             string `json:"nonce"`
	IdentitySignature string `json:"identity_signature"`
}

type Message struct {
	ID           string          `json:"id"`
	Sender       string          `json:"sender"`
	Recipient    string          `json:"recipient"`
	Timestamp    string          `json:"timestamp"`
	Payload      string          `json:"payload"`
	Signature    string          `json:"signature"`
	SessionID    string          `json:"session_id,omitempty"`
	SessionProof *SessionProof   `json:"session_proof,omitempty"`
	Encryption   *EncryptionMeta `json:"encryption,omitempty"`
}

// Ack is the on-wire envelope for a delivery acknowledgement. Tick 1
// (delivered to recipient relay) is conveyed in the HTTP layer (POST
// /messages returning 202) and never travels as an Ack object. Tick 2
// (delivered_client) is the recipient client telling the original sender
// that they have decrypted the message.
//
// `Sender` is the party that produced the ack (Bob's client). `Recipient`
// is who the ack is destined for (== the original message's sender, Alice).
//
// State is reserved for future expansion. v1 emits only `delivered_client`.
type Ack struct {
	Type         string        `json:"type"`
	ID           string        `json:"id"`
	MessageID    string        `json:"message_id"`
	State        string        `json:"state"`
	Sender       string        `json:"sender"`
	Recipient    string        `json:"recipient"`
	Timestamp    string        `json:"timestamp"`
	Signature    string        `json:"signature"`
	SessionID    string        `json:"session_id,omitempty"`
	SessionProof *SessionProof `json:"session_proof,omitempty"`
}

const (
	AckTypeDeliveryAck     = "ack"
	AckStateDeliveredClient = "delivered_client"
)

type InboxResponse struct {
	Messages []any `json:"messages"`
	Acks     []any `json:"acks"`
}

type ErrorResponse struct {
	Error  string `json:"error"`
	Detail string `json:"detail"`
}

type ChallengeResponse struct {
	Challenge string `json:"challenge"`
	ExpiresAt string `json:"expires_at"`
}

type IdentityRequest struct {
	Identity            string `json:"identity"`
	PublicKey           string `json:"public_key"`
	EncryptionPublicKey string `json:"encryption_public_key,omitempty"`
	DNSProvider         string `json:"dns_provider,omitempty"`
	DNSToken            string `json:"dns_token,omitempty"`

	// IdentityDocument is the signed web identity document (EPIC-001).
	// Required for hosted registration; optional for DNS registration
	// (relay synthesizes one from fields when absent).
	IdentityDocument json.RawMessage `json:"identity_document,omitempty"`

	// Identity-signed admin envelope. The relay verifies IdentitySignature
	// over the canonical identity-registration string against the
	// `public_key` in the body so that whoever calls this endpoint must
	// also hold the matching private key (DNS-token possession alone is no
	// longer sufficient). For hosted registration with a full identity
	// document, the document signature is authoritative and this envelope
	// may still be required for replay protection.
	IssuedAt          string `json:"issued_at"`
	Nonce             string `json:"nonce"`
	IdentitySignature string `json:"identity_signature"`
}

type EncryptionKeyRequest struct {
	EncryptionPublicKey string `json:"encryption_public_key"`
	DNSProvider         string `json:"dns_provider"`
	DNSToken            string `json:"dns_token"`

	// Identity-signed admin envelope. Verified against the long-lived
	// signing key registered for `:identity`.
	IssuedAt          string `json:"issued_at"`
	Nonce             string `json:"nonce"`
	IdentitySignature string `json:"identity_signature"`
}

// SessionRevokeRequest is the body of DELETE /sessions/:id. The relay
// resolves the session by id, then verifies IdentitySignature against the
// session's owning identity (via the long-lived signing key).
type SessionRevokeRequest struct {
	Identity          string `json:"identity"`
	IssuedAt          string `json:"issued_at"`
	Nonce             string `json:"nonce"`
	IdentitySignature string `json:"identity_signature"`
}

type EncryptionKeyResponse struct {
	Identity            string `json:"identity"`
	EncryptionPublicKey string `json:"encryption_public_key"`
	UpdatedAt           string `json:"updated_at"`
}

type IdentityResponse struct {
	Identity            string          `json:"identity"`
	PublicKey           string          `json:"public_key"`
	EncryptionPublicKey string          `json:"encryption_public_key,omitempty"`
	Relay               string          `json:"relay"`
	CreatedAt           string          `json:"created_at"`
	IdentityDocument    json.RawMessage `json:"identity_document,omitempty"`
}

type HealthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

type SessionCreateRequest struct {
	Identity          string `json:"identity"`
	SessionPublicKey  string `json:"session_public_key"`
	IssuedAt          string `json:"issued_at"`
	ExpiresAt         string `json:"expires_at"`
	Nonce             string `json:"nonce"`
	IdentitySignature string `json:"identity_signature"`
	DeviceFingerprint string `json:"device_fingerprint,omitempty"`
}

type SessionResponse struct {
	SessionID        string `json:"session_id"`
	Identity         string `json:"identity"`
	SessionPublicKey string `json:"session_public_key"`
	IssuedAt         string `json:"issued_at"`
	ExpiresAt        string `json:"expires_at"`
}
