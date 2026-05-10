// Integration tests for owner-only ("admin") relay endpoints.
//
// In v1 of the protocol the messaging surface is intentionally open
// (anyone may POST a properly-signed message or ack to /messages or /acks)
// but state-mutating endpoints that act on a specific identity are
// owner-only. The relay enforces ownership by requiring an identity-signed
// admin envelope (issued_at, nonce, identity_signature) over a canonical
// string the relay reconstructs locally.
//
// These tests assert that the unauthenticated and forged-signature paths
// are both rejected; the positive (CLI signs successfully) paths are
// already exercised by every other test that runs `identity create` or
// `identity add-encryption-key`, so we don't duplicate them here.
package integration_test

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestINT_ADMIN_01_IdentityCreateRejectsUnsigned: a POST /identities body
// that omits the identity-signed admin envelope must be rejected with
// 400 invalid_request before any DNS write happens.
func TestINT_ADMIN_01_IdentityCreateRejectsUnsigned(t *testing.T) {
	zone := newZone(t)
	_, relayAddr := newRelay(t, zone)

	pub, _, _ := ed25519.GenerateKey(nil)
	body, _ := json.Marshal(map[string]any{
		"identity":     "mallory.poweur.net",
		"public_key":   base64.RawURLEncoding.EncodeToString(pub),
		"dns_provider": "mock",
		"dns_token":    "integration",
	})
	resp, err := http.Post("http://"+relayAddr+"/identities", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post unsigned identity: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 invalid_request for unsigned identity create, got %d", resp.StatusCode)
	}
	if zone.Snapshot()["TXT:_poweur.mallory.poweur.net"] != nil {
		t.Fatalf("DNS was written despite missing identity signature")
	}
}

// TestINT_ADMIN_02_IdentityCreateRejectsForgedSignature: a POST /identities
// body that includes an identity-signed envelope produced by a key
// different from the body's public_key must be rejected with 401
// unauthorized. The body and the signature must agree; otherwise nothing
// stops a hostile DNS-token holder from registering an arbitrary public
// key.
func TestINT_ADMIN_02_IdentityCreateRejectsForgedSignature(t *testing.T) {
	zone := newZone(t)
	_, relayAddr := newRelay(t, zone)

	pub, _, _ := ed25519.GenerateKey(nil) // body's public_key (claimed)
	_, forgedPriv, _ := ed25519.GenerateKey(nil) // signs with a different key
	issuedAt := time.Now().UTC().Format(time.RFC3339)
	nonce := "n0123456789abcdef"
	canonical := strings.Join([]string{
		"identity-registration",
		"mallory.poweur.net",
		base64.RawURLEncoding.EncodeToString(pub),
		"",
		relayAddr,
		issuedAt,
		nonce,
	}, "\n")
	sig := ed25519.Sign(forgedPriv, []byte(canonical))

	body, _ := json.Marshal(map[string]any{
		"identity":           "mallory.poweur.net",
		"public_key":         base64.RawURLEncoding.EncodeToString(pub),
		"dns_provider":       "mock",
		"dns_token":          "integration",
		"issued_at":          issuedAt,
		"nonce":              nonce,
		"identity_signature": base64.StdEncoding.EncodeToString(sig),
	})
	resp, err := http.Post("http://"+relayAddr+"/identities", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post forged identity: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for forged identity create, got %d", resp.StatusCode)
	}
}

// TestINT_ADMIN_03_EncryptionKeyRejectsUnsigned: POST /identities/:identity/
// encryption-key must reject an unsigned body with 400, even from a
// caller who can satisfy the DNS-token check.
func TestINT_ADMIN_03_EncryptionKeyRejectsUnsigned(t *testing.T) {
	zone := newZone(t)
	_, relayAddr := newRelay(t, zone)

	bobHome := t.TempDir()
	runCLI(t, bobHome, "identity", "create", "bob",
		"--parent-domain", "poweur.net", "--relay", "http://"+relayAddr,
		"--dns-provider", "mock", "--dns-token", "integration")

	body, _ := json.Marshal(map[string]any{
		"encryption_public_key": base64.RawURLEncoding.EncodeToString(make([]byte, 32)),
		"dns_provider":          "mock",
		"dns_token":             "integration",
	})
	resp, err := http.Post("http://"+relayAddr+"/identities/bob.poweur.net/encryption-key",
		"application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post unsigned encryption-key: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 invalid_request for unsigned encryption-key publish, got %d", resp.StatusCode)
	}
}

// TestINT_ADMIN_04_EncryptionKeyRejectsForgedSignature: even with a
// well-formed admin envelope, if the signature comes from a key other
// than the registered identity's signing key, the relay rejects with
// 401 unauthorized.
func TestINT_ADMIN_04_EncryptionKeyRejectsForgedSignature(t *testing.T) {
	zone := newZone(t)
	_, relayAddr := newRelay(t, zone)

	bobHome := t.TempDir()
	runCLI(t, bobHome, "identity", "create", "bob",
		"--parent-domain", "poweur.net", "--relay", "http://"+relayAddr,
		"--dns-provider", "mock", "--dns-token", "integration")

	encPub := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	_, forgedPriv, _ := ed25519.GenerateKey(nil)
	issuedAt := time.Now().UTC().Format(time.RFC3339)
	nonce := "nffeeddccbbaa9988"
	canonical := strings.Join([]string{
		"identity-encryption-key",
		"bob.poweur.net",
		encPub,
		issuedAt,
		nonce,
	}, "\n")
	sig := ed25519.Sign(forgedPriv, []byte(canonical))

	body, _ := json.Marshal(map[string]any{
		"encryption_public_key": encPub,
		"dns_provider":          "mock",
		"dns_token":             "integration",
		"issued_at":             issuedAt,
		"nonce":                 nonce,
		"identity_signature":    base64.StdEncoding.EncodeToString(sig),
	})
	resp, err := http.Post("http://"+relayAddr+"/identities/bob.poweur.net/encryption-key",
		"application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post forged encryption-key: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for forged encryption-key publish, got %d", resp.StatusCode)
	}
}

// TestINT_ADMIN_05_SessionDeleteRejectsUnsigned: DELETE /sessions/:id must
// reject a request that omits the admin envelope. v1 of the protocol
// requires identity-signed revocation so a network observer cannot
// invalidate someone else's sessions just by knowing their session id.
func TestINT_ADMIN_05_SessionDeleteRejectsUnsigned(t *testing.T) {
	zone := newZone(t)
	_, relayAddr := newRelay(t, zone)

	req, _ := http.NewRequest(http.MethodDelete, "http://"+relayAddr+"/sessions/sess_unknown", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete unsigned session: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 invalid_request for unsigned session delete, got %d", resp.StatusCode)
	}
}

// TestINT_ADMIN_06_SessionDeleteRejectsForgedSignature: even a structurally-
// valid admin envelope is rejected with 401 if the signature does not
// match the long-lived identity key the relay has on file (or DNS-resolves
// to). This blocks a reentrant attacker who somehow guessed a session id.
func TestINT_ADMIN_06_SessionDeleteRejectsForgedSignature(t *testing.T) {
	zone := newZone(t)
	_, relayAddr := newRelay(t, zone)

	bobHome := t.TempDir()
	runCLI(t, bobHome, "identity", "create", "bob",
		"--parent-domain", "poweur.net", "--relay", "http://"+relayAddr,
		"--dns-provider", "mock", "--dns-token", "integration")

	_, forgedPriv, _ := ed25519.GenerateKey(nil)
	issuedAt := time.Now().UTC().Format(time.RFC3339)
	nonce := "nadbeef0011223344"
	sessionID := "sess_unknown"
	canonical := strings.Join([]string{
		"session-revocation",
		"bob.poweur.net",
		sessionID,
		issuedAt,
		nonce,
	}, "\n")
	sig := ed25519.Sign(forgedPriv, []byte(canonical))

	body, _ := json.Marshal(map[string]any{
		"identity":           "bob.poweur.net",
		"issued_at":          issuedAt,
		"nonce":              nonce,
		"identity_signature": base64.StdEncoding.EncodeToString(sig),
	})
	req, _ := http.NewRequest(http.MethodDelete, "http://"+relayAddr+"/sessions/"+sessionID, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete forged session: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for forged session delete, got %d", resp.StatusCode)
	}
}
