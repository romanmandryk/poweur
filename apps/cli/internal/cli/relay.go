package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

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
	Sender       string          `json:"sender"`
	Recipient    string          `json:"recipient"`
	Timestamp    string          `json:"timestamp"`
	Payload      string          `json:"payload"`
	Signature    string          `json:"signature"`
	SessionID    string          `json:"session_id,omitempty"`
	SessionProof *SessionProof   `json:"session_proof,omitempty"`
	Encryption   *EncryptionMeta `json:"encryption,omitempty"`
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
	Status  string `json:"status"`
	Version string `json:"version"`
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

func RegisterIdentity(ctx context.Context, relayURL, identity, publicKey, encryptionPublicKey, dnsProvider, dnsToken string) (IdentityResponse, error) {
	if relayURL == "" {
		return IdentityResponse{}, errors.New("relay url is required")
	}
	payload, err := json.Marshal(map[string]string{
		"identity":              identity,
		"public_key":            publicKey,
		"encryption_public_key": encryptionPublicKey,
		"dns_provider":          dnsProvider,
		"dns_token":             dnsToken,
	})
	if err != nil {
		return IdentityResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, relayURL+"/identities", bytes.NewReader(payload))
	if err != nil {
		return IdentityResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
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
	client := &http.Client{Timeout: 10 * time.Second}
	return client.Do(req)
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
	req.Header.Set("X-Eurything-Identity", identity)
	req.Header.Set("X-Eurything-Signature", signature)
	if sessionID != "" {
		req.Header.Set("X-Eurything-Session-Id", sessionID)
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
