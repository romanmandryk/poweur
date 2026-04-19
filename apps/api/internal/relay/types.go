package relay

type Message struct {
	Sender    string `json:"sender"`
	Recipient string `json:"recipient"`
	Timestamp string `json:"timestamp"`
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
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
	Identity    string `json:"identity"`
	PublicKey   string `json:"public_key"`
	DNSProvider string `json:"dns_provider"`
	DNSToken    string `json:"dns_token"`
}

type IdentityResponse struct {
	Identity  string `json:"identity"`
	PublicKey string `json:"public_key"`
	Relay     string `json:"relay"`
	CreatedAt string `json:"created_at"`
}

type HealthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}
