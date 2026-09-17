package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/poweur/identity/signin"
)

func runCmd(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(args, strings.NewReader(stdin), &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestVersionGenKeyHashSecret(t *testing.T) {
	if code, out, _ := runCmd(t, "", "version"); code != 0 || !strings.HasPrefix(out, "poweur-oauth ") {
		t.Fatalf("version = %d %q", code, out)
	}
	code, out, _ := runCmd(t, "", "gen-key")
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(out))
	if code != 0 || err != nil || len(key) != 32 {
		t.Fatalf("gen-key = %d %q %v", code, out, err)
	}
	code, out, _ = runCmd(t, "a-very-long-client-secret\n", "hash-secret")
	if code != 0 || strings.TrimSpace(out) != signin.HashSecret("a-very-long-client-secret") {
		t.Fatalf("hash-secret = %d %q", code, out)
	}
	if code, _, _ := runCmd(t, "short", "hash-secret"); code == 0 {
		t.Fatal("a short secret was hashed")
	}
	if code, _, _ := runCmd(t, "", "bogus"); code != 2 {
		t.Fatalf("unknown command = %d", code)
	}
}

func TestConfigFromEnvRequiresIssuerAndKey(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OAUTH_DATABASE", filepath.Join(dir, "oauth.db"))
	t.Setenv("OAUTH_ISSUER", "")
	if code, _, errOut := runCmd(t, "", "keys", "list"); code == 0 || !strings.Contains(errOut, "OAUTH_ISSUER") {
		t.Fatalf("missing issuer = %d %q", code, errOut)
	}
	t.Setenv("OAUTH_ISSUER", "https://auth.example.org")
	t.Setenv("OAUTH_KEY_ENCRYPTION_KEY", "")
	if code, _, errOut := runCmd(t, "", "keys", "list"); code == 0 || !strings.Contains(errOut, "gen-key") {
		t.Fatalf("missing KEK = %d %q", code, errOut)
	}
	t.Setenv("OAUTH_KEY_ENCRYPTION_KEY", "dG9vIHNob3J0")
	if code, _, errOut := runCmd(t, "", "keys", "list"); code == 0 || !strings.Contains(errOut, "32 bytes") {
		t.Fatalf("short KEK = %d %q", code, errOut)
	}
}

func TestKeysAndClientsCommands(t *testing.T) {
	dir := t.TempDir()
	kek := filepath.Join(dir, "kek")
	_, key, _ := runCmd(t, "", "gen-key")
	_ = key
	_, out, _ := runCmd(t, "", "gen-key")
	if err := os.WriteFile(kek, []byte(out), 0o600); err != nil {
		t.Fatal(err)
	}
	clients := filepath.Join(dir, "clients.json")
	os.WriteFile(clients, []byte(`{"clients":[{"client_id":"grafana","client_name":"Grafana",
		"redirect_uris":["https://grafana.example.org/login/generic_oauth"],"client_secret":"static-secret-value-123"}]}`), 0o600)
	t.Setenv("OAUTH_ISSUER", "https://auth.example.org")
	t.Setenv("OAUTH_DATABASE", filepath.Join(dir, "oauth.db"))
	t.Setenv("OAUTH_KEY_ENCRYPTION_KEY", "")
	t.Setenv("OAUTH_KEY_ENCRYPTION_KEY_FILE", kek)
	t.Setenv("OAUTH_STATIC_CLIENTS", clients)

	code, list, errOut := runCmd(t, "", "keys", "list")
	if code != 0 || strings.Count(list, "\n") != 2 {
		t.Fatalf("keys list = %d %q %q", code, list, errOut)
	}
	code, rotated, _ := runCmd(t, "", "keys", "rotate")
	if code != 0 || !strings.Contains(rotated, "new signing key") || strings.Count(rotated, "true") != 2 {
		t.Fatalf("keys rotate = %d %q", code, rotated)
	}
	if code, _, _ := runCmd(t, "", "keys"); code != 2 {
		t.Fatal("keys without a subcommand")
	}
	if code, out, _ := runCmd(t, "", "clients", "list"); code != 0 || !strings.Contains(out, "CLIENT_ID") {
		t.Fatalf("clients list = %d %q", code, out)
	}
	if code, _, errOut := runCmd(t, "", "clients", "suspend", "pwc_missing"); code == 0 || !strings.Contains(errOut, "pwc_missing") {
		t.Fatalf("suspend missing = %d %q", code, errOut)
	}
	if code, _, _ := runCmd(t, "", "prune"); code != 0 {
		t.Fatal("prune failed")
	}

	t.Setenv("OAUTH_STATIC_CLIENTS", filepath.Join(dir, "nope.json"))
	if code, _, errOut := runCmd(t, "", "keys", "list"); code == 0 || !strings.Contains(errOut, "OAUTH_STATIC_CLIENTS") {
		t.Fatalf("missing clients file = %d %q", code, errOut)
	}
	t.Setenv("OAUTH_STATIC_CLIENTS", "")
	t.Setenv("OAUTH_SESSION_TTL", "soon")
	if code, _, errOut := runCmd(t, "", "keys", "list"); code == 0 || !strings.Contains(errOut, "OAUTH_SESSION_TTL") {
		t.Fatalf("bad session ttl = %d %q", code, errOut)
	}
}

func TestCLIPusher(t *testing.T) {
	home := t.TempDir()
	if _, err := newCLIPusher("poweur", "", "bridge.poweur.org"); err == nil {
		t.Fatal("missing home accepted")
	}
	if _, err := newCLIPusher("poweur", home, "not a name"); err == nil {
		t.Fatal("bad identity accepted")
	}
	if _, err := newCLIPusher("poweur", filepath.Join(home, "missing"), "bridge.poweur.org"); err == nil {
		t.Fatal("missing directory accepted")
	}
	p, err := newCLIPusher("poweur", home, "Bridge.Poweur.org")
	if err != nil {
		t.Fatal(err)
	}
	var gotArgs, gotEnv []string
	p.run = func(_ context.Context, name string, args, env []string) ([]byte, error) {
		gotArgs, gotEnv = append([]string{name}, args...), env
		return []byte(`{"id":"m1","status":202}`), nil
	}
	t.Setenv("POWEUR_RESOLVER_SCHEME", "http")
	t.Setenv("OAUTH_KEY_ENCRYPTION_KEY", "must-not-leak")
	exp := time.Date(2026, 9, 17, 12, 3, 0, 0, time.UTC)
	if err := p.Push(context.Background(), "alice.poweur.net", []byte(`{"version":1}`), exp); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(gotArgs, " ")
	for _, want := range []string{"poweur send alice.poweur.net", "--type sys.auth.request", "--expires 2026-09-17T12:03:00Z", "--use-identity bridge.poweur.org", "--via-home-relay"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args %q lack %q", joined, want)
		}
	}
	env := strings.Join(gotEnv, "\n")
	if !strings.Contains(env, "HOME="+home) || !strings.Contains(env, "POWEUR_RESOLVER_SCHEME=http") || strings.Contains(env, "must-not-leak") {
		t.Fatalf("env = %q", env)
	}
	p.run = func(context.Context, string, []string, []string) ([]byte, error) { return nil, errors.New("boom") }
	if err := p.Push(context.Background(), "alice.poweur.net", nil, exp); err == nil {
		t.Fatal("a failed send was reported as sent")
	}
	for _, out := range []string{`{"id":"m1","status":"queued"}`, `{"status":403}`, `not json`} {
		out := out
		p.run = func(context.Context, string, []string, []string) ([]byte, error) { return []byte(out), nil }
		if err := p.Push(context.Background(), "alice.poweur.net", nil, exp); err == nil {
			t.Fatalf("output %s was reported as delivered", out)
		}
	}
}
