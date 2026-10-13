package identity

import "strings"

// EnvelopeEncryption is the `encryption` block of a message envelope.
type EnvelopeEncryption struct {
	Alg                string `json:"alg"`
	EphemeralPublicKey string `json:"ephemeral_public_key"`
	Nonce              string `json:"nonce"`
}

// CanonicalMessageEnvelope is the string a message's sender signs — the same
// construction the relay verifies (apps/api/internal/crypto) and the CLI and
// `@poweur/client` produce. Every optional line appears only when its field
// is set, in this fixed order:
//
//	<sender>
//	<recipient>
//	<timestamp>
//	<payload>
//	id:<message_id>
//	session:<session_id>
//	enc:<alg>:<eph>:<nonce>
//	type:<msgType>
//	thread:<threadID>
//	expires:<expiresAt>
//	meta:<key>:<value>   (keys ascending)
//
// It lives here so services that send as their own identity (the email
// bridge) need no copy of it.
func CanonicalMessageEnvelope(sender, recipient, timestamp, payload, id, sessionID, msgType, threadID, expiresAt string,
	metadata map[string]string, enc *EnvelopeEncryption) string {
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
	if msgType != "" {
		parts = append(parts, "type:"+msgType)
	}
	if threadID != "" {
		parts = append(parts, "thread:"+threadID)
	}
	if expiresAt != "" {
		parts = append(parts, "expires:"+expiresAt)
	}
	parts = append(parts, MetadataLines(metadata)...)
	return strings.Join(parts, "\n")
}
