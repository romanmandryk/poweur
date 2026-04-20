package relay

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
	Sender       string          `json:"sender"`
	Recipient    string          `json:"recipient"`
	Timestamp    string          `json:"timestamp"`
	Payload      string          `json:"payload"`
	Signature    string          `json:"signature"`
	SessionID    string          `json:"session_id,omitempty"`
	SessionProof *SessionProof   `json:"session_proof,omitempty"`
	Encryption   *EncryptionMeta `json:"encryption,omitempty"`
}

type InboxResponse struct {
	Messages []any `json:"messages"`
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
	DNSProvider         string `json:"dns_provider"`
	DNSToken            string `json:"dns_token"`
}

type IdentityResponse struct {
	Identity            string `json:"identity"`
	PublicKey           string `json:"public_key"`
	EncryptionPublicKey string `json:"encryption_public_key,omitempty"`
	Relay               string `json:"relay"`
	CreatedAt           string `json:"created_at"`
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
