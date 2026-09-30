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
	ID        string `json:"id"`
	Sender    string `json:"sender"`
	Recipient string `json:"recipient"`
	Timestamp string `json:"timestamp"`
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
	// Type is the optional envelope-level message type (EPIC-007
	// `sys.contact.*`). Plaintext by design: the relay routes on it (policy
	// enforcement) without reading the E2E-encrypted payload. Absent means
	// `chat.text`. Bound into the signature via CanonicalMessageEnvelope.
	Type string `json:"type,omitempty"`
	// ThreadID groups messages into a conversation thread (EPIC-009 E09-T3).
	// Opaque to the relay: it never invents one and never rewrites one.
	ThreadID string `json:"thread_id,omitempty"`
	// ExpiresAt is when the sender says this message stops being meaningful.
	// Signed and carried here; refusing delivery past it is E09-T6.
	ExpiresAt string `json:"expires_at,omitempty"`
	// Metadata is small, flat, signed, and **plaintext** — addressing, not
	// content. Values are strings so two implementations cannot disagree
	// about how to serialize it for signing.
	Metadata     map[string]string `json:"metadata,omitempty"`
	SessionID    string            `json:"session_id,omitempty"`
	SessionProof *SessionProof     `json:"session_proof,omitempty"`
	Encryption   *EncryptionMeta   `json:"encryption,omitempty"`

	// Anonymous-sender challenge response (EPIC-014): set only on unsigned
	// messages (Sender and Signature empty) answering a 428 challenge.
	ChallengeToken    string `json:"challenge_token,omitempty"`
	ChallengeSolution string `json:"challenge_solution,omitempty"`
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
// State progresses from `delivered_client` to `read`.
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
	AckTypeDeliveryAck      = "ack"
	AckStateDeliveredClient = "delivered_client"
	AckStateRead            = "read"
)

type InboxResponse struct {
	Messages []any `json:"messages"`
	Acks     []any `json:"acks"`
}

type ErrorResponse struct {
	Error  string `json:"error"`
	Detail string `json:"detail"`
	// Contact is who to ask about this error, where the operator has said
	// (quota_exceeded names QUOTA_CONTACT).
	Contact string `json:"contact,omitempty"`
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
	// InviteCode is required when REGISTRATION_GATE=invite (hosted registrations).
	InviteCode string `json:"invite_code,omitempty"`
	// PowToken/PowSolution answer the relay's proof-of-work challenge when
	// REGISTRATION_GATE=pow (GET /auth/pow?purpose=registration).
	PowToken    string `json:"pow_token,omitempty"`
	PowSolution string `json:"pow_solution,omitempty"`

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

// ExportRequest is the owner-signed body for POST /identities/{id}/export.
type ExportRequest struct {
	IssuedAt          string `json:"issued_at"`
	Nonce             string `json:"nonce"`
	IdentitySignature string `json:"identity_signature"`
}

// RotateRequest rotates the long-lived signing key for an identity.
type RotateRequest struct {
	// IdentityDocument is the new document signed by the *new* key, with previous_keys set.
	IdentityDocument json.RawMessage `json:"identity_document"`
	// NewPublicKey is the bare/ed25519 public key of the new identity key.
	NewPublicKey string `json:"new_public_key"`
	// EncryptionPublicKey optional updated enc key (base64url / x25519:).
	EncryptionPublicKey string `json:"encryption_public_key,omitempty"`
	IssuedAt            string `json:"issued_at"`
	Nonce               string `json:"nonce"`
	// RotationSignature is ed25519 signature by the *old* key over
	// CanonicalIdentityRotation(identity, oldKey, newKey, issuedAt, nonce).
	RotationSignature string `json:"rotation_signature"`
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
	// BuildTime is UTC "2006-01-02 15:04" when known (EPIC-013 E13-T6).
	BuildTime string `json:"buildTime,omitempty"`
	// VersionHash is the git revision the binary was built from.
	VersionHash string `json:"versionHash,omitempty"`
	// Storage reports POWEUR_DATA health when configured.
	Storage *StorageHealth `json:"storage,omitempty"`
}

type StorageHealth struct {
	Configured bool   `json:"configured"`
	Path       string `json:"path,omitempty"`
	Writable   bool   `json:"writable"`
	FreeBytes  uint64 `json:"free_bytes,omitempty"`
	Error      string `json:"error,omitempty"`
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
