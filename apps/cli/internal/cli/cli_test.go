package cli

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
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
	code := Run([]string{"send", "bob.example.org", "Hello", "--no-encrypt"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, stderr.String())
	}
	if mr.received.Sender != "alice.example.com" || mr.received.Recipient != "bob.example.org" {
		t.Fatalf("unexpected message: %#v", mr.received)
	}
	if mr.received.Signature == "" {
		t.Fatal("missing signature")
	}
	if mr.received.Payload != "Hello" {
		t.Fatalf("unexpected payload (expected plaintext): %s", mr.received.Payload)
	}
	if mr.received.SessionID == "" {
		t.Fatal("expected session id on message")
	}
	if mr.received.SessionID != mr.sessionID {
		t.Fatalf("session id mismatch: got %s want %s", mr.received.SessionID, mr.sessionID)
	}

	// Verify signature under the session public key.
	parts := []string{mr.received.Sender, mr.received.Recipient, mr.received.Timestamp, mr.received.Payload, "session:" + mr.received.SessionID}
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
