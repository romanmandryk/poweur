package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/eurything/cli/internal/config"
	"github.com/eurything/cli/internal/identity"
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
	if cfg.PrivateKeyPath == "" {
		t.Fatal("expected private key path")
	}
	if _, err := os.Stat(cfg.PrivateKeyPath); err != nil {
		t.Fatalf("missing key file: %v", err)
	}
}

func TestSendMessagePostsToRelay(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	pub, priv, _ := identity.GenerateKeypair()
	keyPath, err := identity.SavePrivateKey("alice.example.com", priv)
	if err != nil {
		t.Fatalf("save key: %v", err)
	}

	var received Message
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	cfg := config.Config{
		RelayURL:       server.URL,
		Identity:       "alice.example.com",
		PrivateKeyPath: keyPath,
	}
	if err := config.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := Run([]string{"send", "bob.example.org", "Hello"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, stderr.String())
	}
	if received.Sender != "alice.example.com" || received.Recipient != "bob.example.org" {
		t.Fatalf("unexpected message: %#v", received)
	}
	if received.Signature == "" {
		t.Fatal("missing signature")
	}
	if received.Payload != "Hello" {
		t.Fatalf("unexpected payload: %s", received.Payload)
	}
	if received.Timestamp == "" {
		t.Fatal("missing timestamp")
	}

	_ = identity.PublicKeyString(pub)
}
