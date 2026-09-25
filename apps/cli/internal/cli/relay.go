package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"
)

// debugHTTP is flipped on when DEBUG_HTTP=1 is set. It causes the relay
// client to dump every request (and truncated response) to stderr — handy
// when CF returns a mystery 502 and we need to know exactly what hit the
// origin.
var debugHTTP = os.Getenv("DEBUG_HTTP") == "1"

func dumpRequest(req *http.Request) {
	if !debugHTTP {
		return
	}
	dump, err := httputil.DumpRequestOut(req, true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[debug-http] dump request: %v\n", err)
		return
	}
	fmt.Fprintf(os.Stderr, "[debug-http] --- request ---\n%s\n[debug-http] --- end request ---\n", dump)
}

func dumpResponse(resp *http.Response) {
	if !debugHTTP || resp == nil {
		return
	}
	dump, err := httputil.DumpResponse(resp, true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[debug-http] dump response: %v\n", err)
		return
	}
	fmt.Fprintf(os.Stderr, "[debug-http] --- response ---\n%s\n[debug-http] --- end response ---\n", dump)
}

type EncryptionMeta struct {
	Alg                string `json:"alg"`
	EphemeralPublicKey string `json:"ephemeral_public_key"`
	Nonce              string `json:"nonce"`
}

// SessionProof lets any relay verify a session-signed message without
// consulting the session cache of the relay that originally issued the
// session. It is produced once at session registration and re-used on every
// message sent from that session.
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
	// sys.contact.*); plaintext so the recipient relay can route on it,
	// and bound into the signature. Absent means `chat.text`.
	Type string `json:"type,omitempty"`
	// ThreadID, ExpiresAt and Metadata are the rest of the E09-T3 envelope
	// extensions: plaintext, signed, and opaque to the relay. Each one only
	// appears in the canonical string when it is set, which is what keeps an
	// envelope that uses none of them byte-identical to a pre-E09-T3 one.
	ThreadID     string            `json:"thread_id,omitempty"`
	ExpiresAt    string            `json:"expires_at,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	SessionID    string            `json:"session_id,omitempty"`
	SessionProof *SessionProof     `json:"session_proof,omitempty"`
	Encryption   *EncryptionMeta   `json:"encryption,omitempty"`
}

// Ack is the wire format of a delivery acknowledgement. v1 carries a
// states `delivered_client` and `read`, emitted by the recipient client.
//
// `sender` is the producer of the ack (the recipient of the original
// message). `recipient` is the original message's sender — this is who
// the ack is *about*, and whose home relay must store the ack so it
// surfaces on their next inbox poll. The `message_id` field references
// the client-assigned id of the original message, threading the local
// pending journal through to tick-2 surfacing.
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

// InboxResponse is the shape returned by GET /messages/{identity}.
// Both arrays are drained on every request: messages are inbound
// payloads, acks are tick-2 receipts for previously sent messages.
type InboxResponse struct {
	Messages []json.RawMessage `json:"messages"`
	Acks     []Ack             `json:"acks"`
}

type ChallengeResponse struct {
	Challenge string `json:"challenge"`
	ExpiresAt string `json:"expires_at"`
}

type IdentityResponse struct {
	Identity            string `json:"identity"`
	PublicKey           string `json:"public_key"`
	EncryptionPublicKey string `json:"encryption_public_key,omitempty"`
	Relay               string `json:"relay"`
	CreatedAt           string `json:"created_at"`
}

type HealthResponse struct {
	Status      string         `json:"status"`
	Version     string         `json:"version"`
	BuildTime   string         `json:"buildTime,omitempty"`
	VersionHash string         `json:"versionHash,omitempty"`
	Storage     *StorageHealth `json:"storage,omitempty"`
}

type StorageHealth struct {
	Configured bool   `json:"configured"`
	Path       string `json:"path,omitempty"`
	Writable   bool   `json:"writable"`
	FreeBytes  uint64 `json:"free_bytes,omitempty"`
	Error      string `json:"error,omitempty"`
}

type ErrorResponse struct {
	Error  string `json:"error"`
	Detail string `json:"detail"`
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

// SessionRevokeRequest is the body of DELETE /sessions/:id. The relay's
// admin auth check expects an identity-signed envelope so an attacker
// who knows the session id alone cannot revoke someone else's session.
type SessionRevokeRequest struct {
	Identity          string `json:"identity"`
	IssuedAt          string `json:"issued_at"`
	Nonce             string `json:"nonce"`
	IdentitySignature string `json:"identity_signature"`
}

// RevokeSession sends an identity-signed DELETE /sessions/:id. A 204 or
// 404 is treated as success: the relay reports 404 only when the id is
// already absent, which is the desired post-condition.
func RevokeSession(ctx context.Context, relayURL, sessionID string, req SessionRevokeRequest) error {
	if relayURL == "" || sessionID == "" {
		return errors.New("relay url and session id required")
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/sessions/%s", relayURL, sessionID)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return parseErrorResponse("session revoke failed", resp)
}

type SessionResponse struct {
	SessionID        string `json:"session_id"`
	Identity         string `json:"identity"`
	SessionPublicKey string `json:"session_public_key"`
	IssuedAt         string `json:"issued_at"`
	ExpiresAt        string `json:"expires_at"`
}

// ErrSessionExpired is returned when a relay call rejects the current session.
// Callers can use this sentinel to trigger an automatic session re-registration.
var ErrSessionExpired = errors.New("session expired")

func CheckRelayHealth(ctx context.Context, relayURL string) error {
	_, err := FetchHealth(ctx, relayURL)
	return err
}

// IdentityRegisterRequest is the wire shape of POST /identities. The
// owner-only fields (issued_at, nonce, identity_signature) prove that the
// caller holds the private key matching public_key — the relay verifies
// them in addition to the DNS-token check that authorizes the DNS write.
type IdentityRegisterRequest struct {
	Identity            string          `json:"identity"`
	PublicKey           string          `json:"public_key"`
	EncryptionPublicKey string          `json:"encryption_public_key,omitempty"`
	DNSProvider         string          `json:"dns_provider,omitempty"`
	DNSToken            string          `json:"dns_token,omitempty"`
	InviteCode          string          `json:"invite_code,omitempty"`
	PowToken            string          `json:"pow_token,omitempty"`
	PowSolution         string          `json:"pow_solution,omitempty"`
	IdentityDocument    json.RawMessage `json:"identity_document,omitempty"`
	IssuedAt            string          `json:"issued_at"`
	Nonce               string          `json:"nonce"`
	IdentitySignature   string          `json:"identity_signature"`
	// OperatorToken is the relay's OPERATOR_TOKEN, sent as a header (never in
	// the body) so the operator can register a name the policy holds back.
	OperatorToken string `json:"-"`
}

type IdentityExportRequest struct {
	IssuedAt          string `json:"issued_at"`
	Nonce             string `json:"nonce"`
	IdentitySignature string `json:"identity_signature"`
}

type IdentityRotateRequest struct {
	IdentityDocument    json.RawMessage `json:"identity_document"`
	NewPublicKey        string          `json:"new_public_key"`
	EncryptionPublicKey string          `json:"encryption_public_key,omitempty"`
	IssuedAt            string          `json:"issued_at"`
	Nonce               string          `json:"nonce"`
	RotationSignature   string          `json:"rotation_signature"`
}

func RegisterIdentity(ctx context.Context, relayURL string, req IdentityRegisterRequest) (IdentityResponse, error) {
	if relayURL == "" {
		return IdentityResponse{}, errors.New("relay url is required")
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return IdentityResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, relayURL+"/identities", bytes.NewReader(payload))
	if err != nil {
		return IdentityResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if req.OperatorToken != "" {
		httpReq.Header.Set("X-Poweur-Operator-Token", req.OperatorToken)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return IdentityResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return IdentityResponse{}, parseErrorResponse("identity registration failed", resp)
	}
	var response IdentityResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return IdentityResponse{}, err
	}
	return response, nil
}

func ExportIdentity(ctx context.Context, relayURL, identity string, req IdentityExportRequest) ([]byte, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, relayURL+"/identities/"+url.PathEscape(identity)+"/export", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("export failed: HTTP %d: %s", resp.StatusCode, string(body))
	}
	return body, nil
}

func RotateIdentity(ctx context.Context, relayURL, identity string, req IdentityRotateRequest) (IdentityResponse, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return IdentityResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, relayURL+"/identities/"+url.PathEscape(identity)+"/rotate", bytes.NewReader(payload))
	if err != nil {
		return IdentityResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return IdentityResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return IdentityResponse{}, parseErrorResponse("identity rotation failed", resp)
	}
	var response IdentityResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return IdentityResponse{}, err
	}
	return response, nil
}

// EncryptionKeyResponse matches the relay's reply when publishing or rotating
// an identity's X25519 encryption key via POST /identities/{identity}/encryption-key.
type EncryptionKeyResponse struct {
	Identity            string `json:"identity"`
	EncryptionPublicKey string `json:"encryption_public_key"`
	UpdatedAt           string `json:"updated_at"`
}

// EncryptionKeyPublishRequest is the wire shape of POST
// /identities/{identity}/encryption-key. As with identity registration,
// the owner-only fields (issued_at, nonce, identity_signature) prove
// ownership of the long-lived signing key — without them the relay
// rejects the request.
type EncryptionKeyPublishRequest struct {
	EncryptionPublicKey string `json:"encryption_public_key"`
	DNSProvider         string `json:"dns_provider,omitempty"`
	DNSToken            string `json:"dns_token,omitempty"`
	IssuedAt            string `json:"issued_at"`
	Nonce               string `json:"nonce"`
	IdentitySignature   string `json:"identity_signature"`
}

func PublishEncryptionKey(ctx context.Context, relayURL, identityID string, req EncryptionKeyPublishRequest) (EncryptionKeyResponse, error) {
	if relayURL == "" {
		return EncryptionKeyResponse{}, errors.New("relay url is required")
	}
	if identityID == "" {
		return EncryptionKeyResponse{}, errors.New("identity is required")
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return EncryptionKeyResponse{}, err
	}
	url := fmt.Sprintf("%s/identities/%s/encryption-key", relayURL, identityID)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return EncryptionKeyResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return EncryptionKeyResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return EncryptionKeyResponse{}, parseErrorResponse("encryption key publish failed", resp)
	}
	var response EncryptionKeyResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return EncryptionKeyResponse{}, err
	}
	return response, nil
}

func RegisterSession(ctx context.Context, relayURL string, req SessionCreateRequest) (SessionResponse, error) {
	if relayURL == "" {
		return SessionResponse{}, errors.New("relay url is required")
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return SessionResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, relayURL+"/sessions", bytes.NewReader(payload))
	if err != nil {
		return SessionResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	// Name this device so the session lands in devices.json and revoking the
	// device can find the session again (EPIC-004 E04-T6).
	applyDeviceHeaders(httpReq.Header)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return SessionResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return SessionResponse{}, parseErrorResponse("session registration failed", resp)
	}
	var response SessionResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return SessionResponse{}, err
	}
	return response, nil
}

// SendAck POSTs a signed delivery ack to the *original sender's* home
// relay (i.e. the relay that hosts the identity named in `ack.recipient`).
// The endpoint is open/messaging-class; the relay will rate-limit, verify
// the signature, and enforce the at-least-one-local rule.
func SendAck(ctx context.Context, relayURL string, ack Ack) (*http.Response, error) {
	payload, err := json.Marshal(ack)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, relayURL+"/acks", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	dumpRequest(req)
	// A relay that redirects is not accepting the message: following it would
	// turn the POST into a GET and read the redirect target's 200 as delivery.
	client := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err == nil {
		dumpResponse(resp)
	}
	return resp, err
}

func SendMessage(ctx context.Context, relayURL string, msg Message) (*http.Response, error) {
	payload, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, relayURL+"/messages", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	dumpRequest(req)
	// A relay that redirects is not accepting the message: following it would
	// turn the POST into a GET and read the redirect target's 200 as delivery.
	client := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err == nil {
		dumpResponse(resp)
	}
	return resp, err
}

func FetchChallenge(ctx context.Context, relayURL, identity string) (ChallengeResponse, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/auth/challenge?identity=%s", relayURL, identity), nil)
	if err != nil {
		return ChallengeResponse{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return ChallengeResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ChallengeResponse{}, parseErrorResponse("challenge request failed", resp)
	}
	var challenge ChallengeResponse
	if err := json.NewDecoder(resp.Body).Decode(&challenge); err != nil {
		return ChallengeResponse{}, err
	}
	return challenge, nil
}

// FetchInbox retrieves messages. When sessionID is non-empty, the relay verifies
// the signature using the session public key. If the relay reports the session
// has expired, ErrSessionExpired is returned so the CLI can re-register.
func FetchInbox(ctx context.Context, relayURL, identity, signature, sessionID string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, relayURL+"/messages/"+identity, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Poweur-Identity", identity)
	req.Header.Set("X-Poweur-Signature", signature)
	if sessionID != "" {
		req.Header.Set("X-Poweur-Session-Id", sessionID)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if err := detectSessionExpired(resp); err != nil {
			return nil, err
		}
		return nil, parseErrorResponse("inbox request failed", resp)
	}
	return io.ReadAll(resp.Body)
}

func FetchHealth(ctx context.Context, relayURL string) (HealthResponse, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, relayURL+"/health", nil)
	if err != nil {
		return HealthResponse{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return HealthResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return HealthResponse{}, parseErrorResponse("health check failed", resp)
	}
	var response HealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return HealthResponse{}, err
	}
	return response, nil
}

func detectSessionExpired(resp *http.Response) error {
	if resp.StatusCode != http.StatusUnauthorized {
		return nil
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	var payload ErrorResponse
	if err := json.Unmarshal(body, &payload); err == nil {
		if payload.Error == "session_expired" {
			return ErrSessionExpired
		}
		if strings.Contains(strings.ToLower(payload.Detail), "session expired") {
			return ErrSessionExpired
		}
	}
	return nil
}

func parseErrorResponse(prefix string, resp *http.Response) error {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("%s with status %d", prefix, resp.StatusCode)
	}
	var payload ErrorResponse
	if err := json.Unmarshal(body, &payload); err == nil && payload.Error != "" {
		if payload.Detail != "" {
			return fmt.Errorf("%s (%d): %s - %s", prefix, resp.StatusCode, payload.Error, payload.Detail)
		}
		return fmt.Errorf("%s (%d): %s", prefix, resp.StatusCode, payload.Error)
	}
	trimmed := strings.TrimSpace(string(body))
	if isCloudflareError(resp, trimmed) {
		target := ""
		if resp.Request != nil && resp.Request.URL != nil {
			target = resp.Request.URL.String()
		}
		if target != "" {
			return fmt.Errorf("%s (%d): cloudflare error (%s). Check tunnel is running and relay is reachable at %s", prefix, resp.StatusCode, trimmed, target)
		}
		return fmt.Errorf("%s (%d): cloudflare error (%s). Check tunnel is running and relay is reachable", prefix, resp.StatusCode, trimmed)
	}
	if trimmed != "" {
		return fmt.Errorf("%s (%d): %s", prefix, resp.StatusCode, trimmed)
	}
	return fmt.Errorf("%s with status %d", prefix, resp.StatusCode)
}

func isCloudflareError(resp *http.Response, body string) bool {
	if resp.StatusCode == 530 || resp.StatusCode == 502 {
		return true
	}
	if strings.Contains(strings.ToLower(resp.Header.Get("Server")), "cloudflare") {
		return true
	}
	return strings.Contains(strings.ToLower(body), "cloudflare") ||
		strings.Contains(strings.ToLower(body), "error code: 502") ||
		strings.Contains(strings.ToLower(body), "error code: 1033")
}
