package main

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
