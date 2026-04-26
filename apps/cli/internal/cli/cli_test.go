package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/eurything/cli/internal/config"
	cryptoe2e "github.com/eurything/cli/internal/crypto"
	"github.com/eurything/cli/internal/identity"
	"github.com/eurything/cli/internal/session"
)

// stubResolver is a minimal identity.Resolver that the send-path tests use to
// stand in for real DNS so they can publish a recipient encryption key.
type stubResolver struct {
	txt map[string][]string
}

func (s stubResolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	if v, ok := s.txt[name]; ok {
		return v, nil
	}
	return nil, errors.New("no TXT for " + name)
}

func (s stubResolver) LookupHost(_ context.Context, name string) ([]string, error) {
	return nil, errors.New("no host for " + name)
}

func (s stubResolver) LookupCNAME(_ context.Context, name string) (string, error) {
	return "", errors.New("no cname for " + name)
}

func TestIdentityCreateWritesConfig(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	var stdout, stderr bytes.Buffer
	code := Run([]string{"identity", "create", "alice", "--parent-domain", "example.com"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, stderr.String())
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Identity != "alice.example.com" {
		t.Fatalf("unexpected identity: %s", cfg.Identity)
	}
	if cfg.KeysDir == "" {
		t.Fatal("expected keys dir")
	}
	keyPath := identity.KeyPath(cfg.KeysDir, "alice.example.com")
	if _, err := os.Stat(keyPath); err != nil {
		t.Fatalf("missing key file: %v", err)
	}
	encKeyPath := identity.EncryptionKeyPath(cfg.KeysDir, "alice.example.com")
	if _, err := os.Stat(encKeyPath); err != nil {
		t.Fatalf("missing encryption key file: %v", err)
	}
}

// mockRelay runs an in-process relay that accepts session registrations and
// messages. It records the most recently received message for assertions.
type mockRelay struct {
	server      *httptest.Server
	received    Message
	sessionID   string
	sessionPub  []byte
	identityPub ed25519.PublicKey
}

func newMockRelay(t *testing.T) *mockRelay {
	t.Helper()
	m := &mockRelay{}
	mux := http.NewServeMux()

	mux.HandleFunc("POST /sessions", func(w http.ResponseWriter, r *http.Request) {
		var req SessionCreateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		decoded, err := base64.RawURLEncoding.DecodeString(req.SessionPublicKey)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		m.sessionPub = decoded
		m.sessionID = "sess_test_" + req.Nonce
		resp := SessionResponse{
			SessionID:        m.sessionID,
			Identity:         req.Identity,
			SessionPublicKey: req.SessionPublicKey,
			IssuedAt:         req.IssuedAt,
			ExpiresAt:        req.ExpiresAt,
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(resp)
	})

	mux.HandleFunc("POST /messages", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&m.received); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(HealthResponse{Status: "ok", Version: "test"})
	})

	m.server = httptest.NewServer(mux)
	return m
}

func TestSendMessageUsesSessionAndRetainsPayload(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	_, priv, _ := identity.GenerateKeypair()
	keyPath, err := identity.SavePrivateKey("alice.example.com", priv)
	if err != nil {
		t.Fatalf("save key: %v", err)
	}

	// Publish an encryption key for Bob via a fake DNS resolver so the CLI's
	// strict encrypt-only policy is satisfied. Without this the CLI would
	// (correctly) refuse to send.
	bobEncPub, _, err := cryptoe2e.GenerateX25519Keypair()
	if err != nil {
		t.Fatalf("bob keygen: %v", err)
	}
	identity.SetResolver(stubResolver{txt: map[string][]string{
		"_eurything-enc.bob.example.org": {
			"eurything-enckey=x25519:" + cryptoe2e.EncodePublicKey(bobEncPub),
		},
	}})
	defer identity.ResetResolver()

	mr := newMockRelay(t)
	defer mr.server.Close()

	cfg := config.Config{
		RelayURL: mr.server.URL,
		Identity: "alice.example.com",
		KeysDir:  filepath.Dir(keyPath),
	}
	if err := config.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	plaintext := "Hello"
	var stdout, stderr bytes.Buffer
	// Use --via-home-relay so the test mock relay (= home relay) receives
	// the message; without this, the CLI would now try to DNS-resolve
	// Bob's relay and post directly there.
	code := Run([]string{"send", "--via-home-relay", "bob.example.org", plaintext}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, stderr.String())
	}
	if mr.received.ID == "" {
		t.Fatal("expected client-assigned message id on outgoing message")
	}
	if mr.received.Sender != "alice.example.com" || mr.received.Recipient != "bob.example.org" {
		t.Fatalf("unexpected message: %#v", mr.received)
	}
	if mr.received.Signature == "" {
		t.Fatal("missing signature")
	}
	if mr.received.Encryption == nil || mr.received.Encryption.Alg == "" {
		t.Fatalf("expected encryption metadata on outgoing message: %#v", mr.received)
	}
	if mr.received.Payload == plaintext {
		t.Fatalf("payload must not equal plaintext (relay should only see ciphertext): %q", mr.received.Payload)
	}
	if mr.received.SessionID == "" {
		t.Fatal("expected session id on message")
	}
	if mr.received.SessionID != mr.sessionID {
		t.Fatalf("session id mismatch: got %s want %s", mr.received.SessionID, mr.sessionID)
	}

	// Verify signature under the session public key (canonical form now
	// includes the message id line and the encryption line).
	parts := []string{
		mr.received.Sender, mr.received.Recipient, mr.received.Timestamp, mr.received.Payload,
		"id:" + mr.received.ID,
		"session:" + mr.received.SessionID,
		"enc:" + mr.received.Encryption.Alg + ":" + mr.received.Encryption.EphemeralPublicKey + ":" + mr.received.Encryption.Nonce,
	}
	canonical := []byte(joinNewlines(parts))
	sig, err := base64.StdEncoding.DecodeString(mr.received.Signature)
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	if !ed25519.Verify(mr.sessionPub, canonical, sig) {
		t.Fatal("session signature does not verify under session public key")
	}

	// Session should have been persisted locally.
	sess, err := session.Load("alice.example.com")
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if sess.SessionID != mr.sessionID {
		t.Fatalf("local session id mismatch: got %s", sess.SessionID)
	}
	if sess.RelayURL != mr.server.URL {
		t.Fatalf("local session relay mismatch: got %s", sess.RelayURL)
	}
	if !sess.IsValid() {
		t.Fatalf("expected session valid, expires at %s", sess.ExpiresAt)
	}

	// Sanity check: TTL is ~24h.
	if diff := time.Until(sess.ExpiresAt); diff < 23*time.Hour || diff > 25*time.Hour {
		t.Fatalf("unexpected session TTL: %s", diff)
	}
}

// TestSendMessageWithSignWithIdentity exercises the --sign-with=identity
// path: the CLI must skip session registration entirely, leave session_id
// and session_proof empty on the wire, and sign the canonical message with
// the long-lived identity Ed25519 key (the mock relay asserts the
// signature verifies under Alice's identity public key).
func TestSendMessageWithSignWithIdentity(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	idPub, idPriv, _ := identity.GenerateKeypair()
	keyPath, err := identity.SavePrivateKey("alice.example.com", idPriv)
	if err != nil {
		t.Fatalf("save key: %v", err)
	}

	bobEncPub, _, err := cryptoe2e.GenerateX25519Keypair()
	if err != nil {
		t.Fatalf("bob keygen: %v", err)
	}
	identity.SetResolver(stubResolver{txt: map[string][]string{
		"_eurything-enc.bob.example.org": {
			"eurything-enckey=x25519:" + cryptoe2e.EncodePublicKey(bobEncPub),
		},
	}})
	defer identity.ResetResolver()

	mr := newMockRelay(t)
	defer mr.server.Close()

	cfg := config.Config{
		RelayURL: mr.server.URL,
		Identity: "alice.example.com",
		KeysDir:  filepath.Dir(keyPath),
	}
	if err := config.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := Run([]string{"send", "--via-home-relay", "--sign-with", "identity", "bob.example.org", "hi bob"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, stderr.String())
	}
	if mr.received.ID == "" {
		t.Fatal("expected client-assigned message id on outgoing message")
	}

	if mr.received.SessionID != "" {
		t.Fatalf("expected empty session_id for identity-signed message, got %q", mr.received.SessionID)
	}
	if mr.received.SessionProof != nil {
		t.Fatalf("expected nil session_proof for identity-signed message, got %+v", mr.received.SessionProof)
	}
	if mr.received.Signature == "" {
		t.Fatal("missing signature")
	}
	if mr.received.Encryption == nil || mr.received.Encryption.Alg == "" {
		t.Fatal("expected encryption metadata on outgoing message")
	}

	// Mock relay never saw a session registration when --sign-with=identity
	// was used (no POST /sessions happened). The local session cache must
	// therefore be empty too.
	if mr.sessionID != "" {
		t.Fatalf("mock relay unexpectedly observed a session registration: %s", mr.sessionID)
	}
	if _, err := session.Load("alice.example.com"); err == nil {
		t.Fatal("expected no local session file after --sign-with=identity send")
	}

	// Signature must verify under the identity public key (no session: line
	// in the canonical string because SessionID is empty; the id line is
	// always present because the client always assigns one).
	parts := []string{
		mr.received.Sender, mr.received.Recipient, mr.received.Timestamp, mr.received.Payload,
		"id:" + mr.received.ID,
		"enc:" + mr.received.Encryption.Alg + ":" + mr.received.Encryption.EphemeralPublicKey + ":" + mr.received.Encryption.Nonce,
	}
	canonical := []byte(joinNewlines(parts))
	sig, err := base64.StdEncoding.DecodeString(mr.received.Signature)
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	if !ed25519.Verify(idPub, canonical, sig) {
		t.Fatal("identity signature does not verify under identity public key")
	}
}

// TestSendMessageRejectsInvalidSignWith ensures the CLI refuses to send if
// --sign-with is set to something other than "session" or "identity",
// before any network IO or crypto work.
func TestSendMessageRejectsInvalidSignWith(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	_, priv, _ := identity.GenerateKeypair()
	keyPath, err := identity.SavePrivateKey("alice.example.com", priv)
	if err != nil {
		t.Fatalf("save key: %v", err)
	}
	cfg := config.Config{
		RelayURL: "http://unused.invalid",
		Identity: "alice.example.com",
		KeysDir:  filepath.Dir(keyPath),
	}
	if err := config.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := Run([]string{"send", "--sign-with", "device", "bob.example.org", "hi"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit for invalid --sign-with value, got 0; stderr=%s", stderr.String())
	}
	if !bytes.Contains(stderr.Bytes(), []byte("invalid --sign-with")) {
		t.Fatalf("expected error message mentioning invalid --sign-with, got: %s", stderr.String())
	}
}

func TestEncryptDecryptRoundtrip(t *testing.T) {
	pub, priv, err := cryptoe2e.GenerateX25519Keypair()
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	plaintext := []byte("hello eurything")
	sealed, err := cryptoe2e.Encrypt(pub, plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if sealed.Ciphertext == "" || sealed.EphemeralPublicKey == "" || sealed.Nonce == "" {
		t.Fatal("missing fields in sealed payload")
	}
	opened, err := cryptoe2e.Decrypt(priv, sealed)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if string(opened) != string(plaintext) {
		t.Fatalf("roundtrip mismatch: got %s want %s", opened, plaintext)
	}
}

func joinNewlines(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += "\n"
		}
		out += p
	}
	return out
}
