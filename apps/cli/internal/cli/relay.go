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

type Message struct {
	Sender    string `json:"sender"`
	Recipient string `json:"recipient"`
	Timestamp string `json:"timestamp"`
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}

type ChallengeResponse struct {
	Challenge string `json:"challenge"`
	ExpiresAt string `json:"expires_at"`
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

type ErrorResponse struct {
	Error  string `json:"error"`
	Detail string `json:"detail"`
}

func CheckRelayHealth(ctx context.Context, relayURL string) error {
	_, err := FetchHealth(ctx, relayURL)
	if err != nil {
		return err
	}
	return nil
}

func RegisterIdentity(ctx context.Context, relayURL, identity, publicKey, dnsProvider, dnsToken string) (IdentityResponse, error) {
	if relayURL == "" {
		return IdentityResponse{}, errors.New("relay url is required")
	}
	payload, err := json.Marshal(map[string]string{
		"identity":     identity,
		"public_key":   publicKey,
		"dns_provider": dnsProvider,
		"dns_token":    dnsToken,
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

func FetchInbox(ctx context.Context, relayURL, identity, signature string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, relayURL+"/messages/"+identity, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Eurything-Identity", identity)
	req.Header.Set("X-Eurything-Signature", signature)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
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
