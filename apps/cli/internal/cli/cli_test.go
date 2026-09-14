package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/poweur/cli/internal/config"
	cryptoe2e "github.com/poweur/cli/internal/crypto"
	"github.com/poweur/cli/internal/identity"
	"github.com/poweur/cli/internal/session"
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
	code := Run([]string{"identity", "create", "alice", "--parent-domain", "poweur.net"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, stderr.String())
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Identity != "alice.poweur.net" {
		t.Fatalf("unexpected identity: %s", cfg.Identity)
	}
	if cfg.KeysDir == "" {
		t.Fatal("expected keys dir")
	}
	keyPath := identity.KeyPath(cfg.KeysDir, "alice.poweur.net")
	if _, err := os.Stat(keyPath); err != nil {
		t.Fatalf("missing key file: %v", err)
	}
	encKeyPath := identity.EncryptionKeyPath(cfg.KeysDir, "alice.poweur.net")
	if _, err := os.Stat(encKeyPath); err != nil {
		t.Fatalf("missing encryption key file: %v", err)
	}
}

func TestIdentityCreateRefusesExistingKeys(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"identity", "create", "alice", "--parent-domain", "poweur.net"}, &stdout, &stderr); code != 0 {
		t.Fatalf("first create: %d: %s", code, stderr.String())
	}
	keysDir, err := config.KeysDir()
	if err != nil {
		t.Fatal(err)
	}
	keyPath := identity.KeyPath(keysDir, "alice.poweur.net")
	before, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	stderr.Reset()
	code := Run([]string{"identity", "create", "alice", "--parent-domain", "poweur.net"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("second create must fail rather than overwrite")
	}
	if !bytes.Contains(stderr.Bytes(), []byte("keys already exist")) {
		t.Fatalf("want keys-already-exist error, got %q", stderr.String())
	}
	after, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("existing key file was overwritten")
	}
}

func TestIdentityCreateCleansKeysWhenRelayRefuses(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(HealthResponse{Status: "ok", Version: "test"})
	})
	mux.HandleFunc("POST /identities", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(ErrorResponse{Error: "identity_exists", Detail: "identity already registered"})
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"identity", "create", "alice.poweur.net",
		"--hosted", "--relay", ts.URL,
	}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("create must fail when the relay refuses, stderr=%s", stderr.String())
	}
	if !bytes.Contains(stderr.Bytes(), []byte("identity_exists")) {
		t.Fatalf("want identity_exists in stderr, got %q", stderr.String())
	}
	keysDir, err := config.KeysDir()
	if err != nil {
		t.Fatal(err)
	}
	if identity.AnyKeyFileExists(
		identity.KeyPath(keysDir, "alice.poweur.net"),
		identity.EncryptionKeyPath(keysDir, "alice.poweur.net"),
	) {
		t.Fatal("refused create must not leave key files")
	}
}

func TestIdentityCreateCleansKeysWhenRelayUnreachable(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(ErrorResponse{Error: "unavailable", Detail: "down"})
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"identity", "create", "alice.poweur.net",
		"--hosted", "--relay", ts.URL,
	}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("create must fail when health check fails")
	}
	keysDir, err := config.KeysDir()
	if err != nil {
		t.Fatal(err)
	}
	if identity.AnyKeyFileExists(
		identity.KeyPath(keysDir, "alice.poweur.net"),
		identity.EncryptionKeyPath(keysDir, "alice.poweur.net"),
	) {
		t.Fatal("unreachable relay must not leave key files")
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
	keyPath, err := identity.SavePrivateKey("alice.poweur.net", priv)
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
		"_poweur-enc.bob.example.org": {
			"poweur-enckey=x25519:" + cryptoe2e.EncodePublicKey(bobEncPub),
		},
	}})
	defer identity.ResetResolver()

	mr := newMockRelay(t)
	defer mr.server.Close()

	cfg := config.Config{
		RelayURL: mr.server.URL,
		Identity: "alice.poweur.net",
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
	if mr.received.Sender != "alice.poweur.net" || mr.received.Recipient != "bob.example.org" {
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
	sess, err := session.Load("alice.poweur.net")
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
	keyPath, err := identity.SavePrivateKey("alice.poweur.net", idPriv)
	if err != nil {
		t.Fatalf("save key: %v", err)
	}

	bobEncPub, _, err := cryptoe2e.GenerateX25519Keypair()
	if err != nil {
		t.Fatalf("bob keygen: %v", err)
	}
	identity.SetResolver(stubResolver{txt: map[string][]string{
		"_poweur-enc.bob.example.org": {
			"poweur-enckey=x25519:" + cryptoe2e.EncodePublicKey(bobEncPub),
		},
	}})
	defer identity.ResetResolver()

	mr := newMockRelay(t)
	defer mr.server.Close()

	cfg := config.Config{
		RelayURL: mr.server.URL,
		Identity: "alice.poweur.net",
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
	if _, err := session.Load("alice.poweur.net"); err == nil {
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
	keyPath, err := identity.SavePrivateKey("alice.poweur.net", priv)
	if err != nil {
		t.Fatalf("save key: %v", err)
	}
	cfg := config.Config{
		RelayURL: "http://unused.invalid",
		Identity: "alice.poweur.net",
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
	plaintext := []byte("hello poweur")
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

// Flags after a positional argument (EPIC-007 follow-up). Go's flag package
// stops parsing at the first non-flag word, so `contacts accept bob
// --petname Bob` reached the handler as three positionals and failed with a
// usage line — while `contacts --petname Bob accept bob` worked. Every other
// command in the CLI normalises first; the contacts subcommands did not, and
// nobody types the working order.
func TestNormalizeArgsMovesFlagsAheadOfPositionals(t *testing.T) {
	tests := []struct {
		name  string
		in    []string
		bools map[string]bool
		want  []string
	}{
		{
			name: "value flag after positional",
			in:   []string{"bob.poweur.net", "--petname", "Bob"},
			want: []string{"--petname", "Bob", "--", "bob.poweur.net"},
		},
		{
			name: "equals form needs no value pickup",
			in:   []string{"bob.poweur.net", "--petname=Bob"},
			want: []string{"--petname=Bob", "--", "bob.poweur.net"},
		},
		{
			name:  "bool flag does not swallow the next positional",
			in:    []string{"bob.poweur.net", "--json"},
			bools: map[string]bool{"--json": true},
			want:  []string{"--json", "--", "bob.poweur.net"},
		},
		{
			name: "two positionals keep their order",
			in:   []string{"bob.poweur.net", "hi there", "--use-identity", "me.poweur.net"},
			want: []string{"--use-identity", "me.poweur.net", "--", "bob.poweur.net", "hi there"},
		},
		{
			name: "already normalised keeps a terminator before positionals",
			in:   []string{"--petname", "Bob", "bob.poweur.net"},
			want: []string{"--petname", "Bob", "--", "bob.poweur.net"},
		},
		{
			name:  "dash-prefixed base64url stays positional",
			in:    []string{"alice.poweur.net", "-jeCR-4zJRkkEUe3K70Erw", "--ephemeral-key", "secret", "--json"},
			bools: map[string]bool{"--json": true},
			want:  []string{"--ephemeral-key", "secret", "--json", "--", "alice.poweur.net", "-jeCR-4zJRkkEUe3K70Erw"},
		},
		{
			name:  "value flag keeps a dash-prefixed base64url argument",
			in:    []string{"alice.poweur.net", "rid", "--ephemeral-key", "-kaAdNETajiziu797SRGfEhxo6hf23QrPF7i4D80RdU", "--json"},
			bools: map[string]bool{"--json": true},
			want:  []string{"--ephemeral-key", "-kaAdNETajiziu797SRGfEhxo6hf23QrPF7i4D80RdU", "--json", "--", "alice.poweur.net", "rid"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeArgs(tt.in, tt.bools)
			if len(got) != len(tt.want) {
				t.Fatalf("normalizeArgs(%v) = %v, want %v", tt.in, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("normalizeArgs(%v) = %v, want %v", tt.in, got, tt.want)
				}
			}
		})
	}
}

func TestNormalizeArgsDashPrefixedRendezvousParses(t *testing.T) {
	fs := flag.NewFlagSet("key claim", flag.ContinueOnError)
	var buf bytes.Buffer
	fs.SetOutput(&buf)
	ephemeral := fs.String("ephemeral-key", "", "")
	relay := fs.String("relay", "", "")
	jsonOut := fs.Bool("json", false, "")
	args := normalizeArgs([]string{
		"tsenroll.poweur.net", "-jeCR-4zJRkkEUe3K70Erw",
		"--ephemeral-key", "secret", "--relay", "http://127.0.0.1:8080", "--json",
	}, map[string]bool{"--json": true})
	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse: %v (%s)", err, buf.String())
	}
	if fs.Arg(0) != "tsenroll.poweur.net" || fs.Arg(1) != "-jeCR-4zJRkkEUe3K70Erw" {
		t.Fatalf("positionals %v", fs.Args())
	}
	if *ephemeral != "secret" || *relay != "http://127.0.0.1:8080" || !*jsonOut {
		t.Fatalf("flags ephemeral=%q relay=%q json=%v", *ephemeral, *relay, *jsonOut)
	}
}

func TestNormalizeArgsDashPrefixedFlagValueParses(t *testing.T) {
	fs := flag.NewFlagSet("key claim", flag.ContinueOnError)
	var buf bytes.Buffer
	fs.SetOutput(&buf)
	ephemeral := fs.String("ephemeral-key", "", "")
	_ = fs.Bool("json", false, "")
	const key = "-kaAdNETajiziu797SRGfEhxo6hf23QrPF7i4D80RdU"
	args := normalizeArgs([]string{
		"tsenroll.poweur.net", "0uix_IswSd_Geu6AVxKbRA",
		"--ephemeral-key", key, "--json",
	}, map[string]bool{"--json": true})
	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse: %v (%s)", err, buf.String())
	}
	if *ephemeral != key {
		t.Fatalf("ephemeral-key=%q want %q (args=%v)", *ephemeral, key, args)
	}
}

func TestVersionFlag(t *testing.T) {
	for _, arg := range []string{"version", "--version", "-v"} {
		var stdout, stderr bytes.Buffer
		code := Run([]string{arg}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("%s: exit %d: %s", arg, code, stderr.String())
		}
		if !bytes.Contains(stdout.Bytes(), []byte("poweur ")) {
			t.Fatalf("%s: output %q", arg, stdout.String())
		}
	}
}

func TestHostForRelayURL(t *testing.T) {
	const recipient = "bobbob.poweur.net"
	cases := []struct {
		name, scheme, recipient, want string
		dns                           identity.DNSStatus
		fail                          bool
	}{
		{
			name:      "https skips cloudflare anycast IPs",
			scheme:    "https",
			recipient: recipient,
			dns:       identity.DNSStatus{RelayHosts: []string{"172.67.136.22", "104.21.32.214"}},
			want:      recipient,
		},
		{
			name:      "http keeps httptest host:port",
			scheme:    "http",
			recipient: recipient,
			dns:       identity.DNSStatus{RelayHosts: []string{"127.0.0.1:54321"}},
			want:      "127.0.0.1:54321",
		},
		{
			name:      "https prefers the identity name over a hostname A-record",
			scheme:    "https",
			recipient: recipient,
			dns:       identity.DNSStatus{RelayHosts: []string{"relay.example.org"}},
			want:      recipient,
		},
		{
			name:      "https with only IPs and no identity name fails closed",
			scheme:    "https",
			recipient: "",
			dns:       identity.DNSStatus{RelayHosts: []string{"172.67.136.22"}},
			fail:      true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := hostForRelayURL(tc.scheme, tc.recipient, tc.dns)
			if tc.fail {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestRelayURLHostIsBareIP(t *testing.T) {
	if !relayURLHostIsBareIP("https://172.67.136.22") {
		t.Fatal("cloudflare IP URL must be rewritten")
	}
	if relayURLHostIsBareIP("http://127.0.0.1:54321") {
		t.Fatal("httptest host:port must be kept")
	}
	if relayURLHostIsBareIP("https://bobbob.poweur.net") {
		t.Fatal("hostname URL must be kept")
	}
}
