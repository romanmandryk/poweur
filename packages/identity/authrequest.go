package identity

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// AuthRequestPayload is the decrypted body of a `sys.auth.request` message
// (EPIC-022 E22-T7): an OAuth bridge forwarding a pending sign-in to the
// user's app so it can be approved there.
//
// It is only a notification. It carries the same public request the bridge
// shows as a QR code — never a code the user must match, which only the
// starting screen shows — and approving it still means verifying the
// request's audience, entering that code, and POSTing to the bridge.
type AuthRequestPayload struct {
	Version int `json:"version"`
	// Request is the encoded native sign-in request.
	Request string `json:"request"`
	// Client and ClientHost name the application the sign-in is for.
	Client     string `json:"client,omitempty"`
	ClientHost string `json:"client_host,omitempty"`
	// ExpiresAt mirrors the request's expiry.
	ExpiresAt string `json:"expires_at"`
}

// MaxAuthRequestBytes caps a sign-in prompt body.
const MaxAuthRequestBytes = 8 * 1024

// Validate checks the payload shape and that its request decodes.
func (p AuthRequestPayload) Validate(now time.Time) (SignInRequest, error) {
	if p.Version != 1 {
		return SignInRequest{}, fmt.Errorf("unsupported sign-in prompt version %d", p.Version)
	}
	req, err := DecodeSignInRequest(p.Request)
	if err != nil {
		return SignInRequest{}, fmt.Errorf("sign-in prompt request: %w", err)
	}
	if err := req.Validate(now); err != nil {
		return SignInRequest{}, err
	}
	if req.ExpiresAt != p.ExpiresAt {
		return SignInRequest{}, errors.New("sign-in prompt expiry does not match its request")
	}
	if len(p.Client) > 80 || len(p.ClientHost) > 253 || strings.ContainsAny(p.Client+p.ClientHost, "\r\n") {
		return SignInRequest{}, errors.New("sign-in prompt labels are malformed")
	}
	return req, nil
}

// ParseAuthRequestPayload decodes and validates a prompt body.
func ParseAuthRequestPayload(raw []byte, now time.Time) (AuthRequestPayload, SignInRequest, error) {
	if len(raw) > MaxAuthRequestBytes {
		return AuthRequestPayload{}, SignInRequest{}, errors.New("sign-in prompt too large")
	}
	var p AuthRequestPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return AuthRequestPayload{}, SignInRequest{}, fmt.Errorf("sign-in prompt is not JSON: %w", err)
	}
	req, err := p.Validate(now)
	return p, req, err
}
