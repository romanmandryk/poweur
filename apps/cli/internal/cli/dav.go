package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/poweur/cli/internal/config"
	"github.com/poweur/cli/internal/identity"
	idpkg "github.com/poweur/identity"
)

// DAV bridge commands (EPIC-003 E03-T3): mint bearer tokens for WebDAV
// clients, print ready-to-paste mount commands, and manage app passwords
// for legacy Basic-auth clients (Finder & friends).

func runDAV(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: poweur dav <token|mount|password>")
		return 1
	}
	switch args[0] {
	case "token":
		return runDAVToken(args[1:], stdout, stderr)
	case "mount":
		return runDAVMount(args[1:], stdout, stderr)
	case "password":
		return runDAVPassword(args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, "unknown dav subcommand (want token, mount, password)")
		return 1
	}
}

// DAVTokenRequest mirrors the relay's POST /auth/dav-token body.
type DAVTokenRequest struct {
	Identity  string `json:"identity"`
	Audience  string `json:"audience,omitempty"`
	Scope     string `json:"scope,omitempty"`
	IssuedAt  string `json:"issued_at"`
	Nonce     string `json:"nonce"`
	SessionID string `json:"session_id,omitempty"`
	Signature string `json:"signature"`
}

type DAVTokenResponse struct {
	Token     string `json:"token"`
	Identity  string `json:"identity"`
	Audience  string `json:"audience"`
	Scope     string `json:"scope"`
	ExpiresAt string `json:"expires_at"`
}

// MintDAVToken requests a WebDAV bearer token, signing the canonical
// dav-token string with the long-lived identity key.
func MintDAVToken(ctx context.Context, relayURL, identityValue, audience, scope string, priv ed25519.PrivateKey) (DAVTokenResponse, error) {
	if relayURL == "" {
		return DAVTokenResponse{}, errors.New("relay url is required")
	}
	if audience == "" {
		audience = identityValue
	}
	if scope == "" {
		if strings.EqualFold(audience, identityValue) {
			scope = "dav:full"
		} else {
			scope = "dav:read"
		}
	}
	issuedAt := time.Now().UTC().Format(time.RFC3339)
	nonce := newAdminNonce()
	canonical := strings.Join([]string{"dav-token", identityValue, audience, scope, issuedAt, nonce}, "\n")
	sig := base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, []byte(canonical)))
	payload, err := json.Marshal(DAVTokenRequest{
		Identity: identityValue, Audience: audience, Scope: scope,
		IssuedAt: issuedAt, Nonce: nonce, Signature: sig,
	})
	if err != nil {
		return DAVTokenResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, relayURL+"/auth/dav-token", bytes.NewReader(payload))
	if err != nil {
		return DAVTokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	// Identify this device so the relay can bind the token to a devices.json
	// row and revoking the device actually kills it (EPIC-004 E04-T6).
	applyDeviceHeaders(req.Header)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return DAVTokenResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return DAVTokenResponse{}, parseErrorResponse("dav token request failed", resp)
	}
	var out DAVTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return DAVTokenResponse{}, err
	}
	return out, nil
}

func loadIdentityForDAV(useIdentity string, stderr io.Writer) (cfg config.Config, identityValue string, priv ed25519.PrivateKey, ok bool) {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return cfg, "", nil, false
	}
	identityValue = resolveIdentity(useIdentity, cfg.Identity)
	if identityValue == "" {
		fmt.Fprintln(stderr, "identity not configured (run `poweur identity create` or pass --use-identity)")
		return cfg, "", nil, false
	}
	if cfg.RelayURL == "" {
		fmt.Fprintln(stderr, "relay url not configured")
		return cfg, "", nil, false
	}
	priv, err = identity.LoadPrivateKey(identity.KeyPath(cfg.KeysDir, identityValue))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return cfg, "", nil, false
	}
	return cfg, identityValue, priv, true
}

func runDAVToken(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("dav token", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "identity to authenticate as")
	audience := fs.String("audience", "", "tree owner to access (default: own tree)")
	scope := fs.String("scope", "", "dav:full | dav:read | dav:rw:<path> | dav:read:<path>")
	relayFlag := fs.String("relay", "", "relay to mint from (default: own relay; use the audience's relay for visits)")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	cfg, identityValue, priv, ok := loadIdentityForDAV(*useIdentity, stderr)
	if !ok {
		return 1
	}
	relayURL := cfg.RelayURL
	if *relayFlag != "" {
		relayURL = *relayFlag
	}
	tok, err := MintDAVToken(context.Background(), relayURL, identityValue, *audience, *scope, priv)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *jsonOut {
		return writeOutput(stdout, true, tok, "")
	}
	fmt.Fprintf(stdout, "token:      %s\n", tok.Token)
	fmt.Fprintf(stdout, "audience:   %s\n", tok.Audience)
	fmt.Fprintf(stdout, "scope:      %s\n", tok.Scope)
	fmt.Fprintf(stdout, "expires_at: %s\n", tok.ExpiresAt)
	fmt.Fprintf(stdout, "\ncurl example:\n  curl -H 'Authorization: Bearer %s' -X PROPFIND -H 'Depth: 1' %s/dav/%s/\n",
		tok.Token, relayURL, tok.Audience)
	return 0
}

func runDAVMount(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("dav mount", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "identity to mount")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	identityValue := resolveIdentity(*useIdentity, cfg.Identity)
	if identityValue == "" || cfg.RelayURL == "" {
		fmt.Fprintln(stderr, "identity and relay url required")
		return 1
	}
	davURL := fmt.Sprintf("%s/dav/%s/", strings.TrimSuffix(cfg.RelayURL, "/"), identityValue)
	fmt.Fprintf(stdout, "WebDAV URL: %s\n", davURL)
	fmt.Fprintf(stdout, "Username:   %s\n", identityValue)
	fmt.Fprintf(stdout, "Password:   an app password — create one with `poweur dav password add --name mymac`\n\n")
	fmt.Fprintln(stdout, "macOS (Finder):")
	fmt.Fprintf(stdout, "  open \"%s\"   # or Finder → Go → Connect to Server…\n", davURL)
	fmt.Fprintln(stdout, "macOS (terminal):")
	fmt.Fprintf(stdout, "  mkdir -p ~/poweur && mount_webdav -i %s ~/poweur\n", davURL)
	fmt.Fprintln(stdout, "Linux (davfs2):")
	fmt.Fprintf(stdout, "  sudo mount -t davfs %s /mnt/poweur\n", davURL)
	fmt.Fprintln(stdout, "Windows:")
	fmt.Fprintf(stdout, "  net use P: %s /user:%s\n", davURL, identityValue)
	fmt.Fprintln(stdout, "rclone:")
	fmt.Fprintf(stdout, "  rclone lsd :webdav: --webdav-url %s --webdav-user %s --webdav-pass <app-password>\n", davURL, identityValue)
	return 0
}

// --- app password management (stored in poweur-sys/relay/app-passwords.json) ---

const appPasswordsPath = "poweur-sys/relay/app-passwords.json"

func davFileURL(relayURL, identityValue, treePath string) string {
	return fmt.Sprintf("%s/dav/%s/%s", strings.TrimSuffix(relayURL, "/"), url.PathEscape(identityValue), treePath)
}

func fetchAppPasswords(ctx context.Context, relayURL, identityValue, token string) (idpkg.AppPasswordsFile, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, davFileURL(relayURL, identityValue, appPasswordsPath), nil)
	if err != nil {
		return idpkg.AppPasswordsFile{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return idpkg.AppPasswordsFile{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return idpkg.AppPasswordsFile{}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return idpkg.AppPasswordsFile{}, parseErrorResponse("fetch app passwords failed", resp)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return idpkg.AppPasswordsFile{}, err
	}
	return idpkg.ParseAppPasswordsFile(raw)
}

func putAppPasswords(ctx context.Context, relayURL, identityValue, token string, file idpkg.AppPasswordsFile) error {
	raw, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, davFileURL(relayURL, identityValue, appPasswordsPath), bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return parseErrorResponse("store app passwords failed", resp)
	}
	return nil
}

func runDAVPassword(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: poweur dav password <add|list|remove>")
		return 1
	}
	sub := args[0]
	fs := flag.NewFlagSet("dav password "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "identity")
	name := fs.String("name", "", "app password name (e.g. finder, phone)")
	scope := fs.String("scope", "dav:full", "scope granted to this password")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args[1:], map[string]bool{"--json": true})); err != nil {
		return 1
	}
	cfg, identityValue, priv, ok := loadIdentityForDAV(*useIdentity, stderr)
	if !ok {
		return 1
	}
	ctx := context.Background()
	tok, err := MintDAVToken(ctx, cfg.RelayURL, identityValue, "", "dav:full", priv)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	file, err := fetchAppPasswords(ctx, cfg.RelayURL, identityValue, tok.Token)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	switch sub {
	case "add":
		if *name == "" {
			fmt.Fprintln(stderr, "--name is required")
			return 1
		}
		for _, p := range file.Passwords {
			if p.Name == *name {
				fmt.Fprintf(stderr, "app password %q already exists (remove it first)\n", *name)
				return 1
			}
		}
		password, err := idpkg.GenerateAppPassword()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		hash, err := idpkg.HashAppPassword(password)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		file.Passwords = append(file.Passwords, idpkg.AppPassword{
			Name: *name, Hash: hash, Scope: *scope,
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
		})
		if err := putAppPasswords(ctx, cfg.RelayURL, identityValue, tok.Token, file); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		out := map[string]any{"name": *name, "password": password, "scope": *scope, "username": identityValue}
		return writeOutput(stdout, *jsonOut, out, fmt.Sprintf(
			"app password %q created.\n  username: %s\n  password: %s\nStore it now — it is shown only once.\n",
			*name, identityValue, password))
	case "list":
		if *jsonOut {
			return writeOutput(stdout, true, file, "")
		}
		if len(file.Passwords) == 0 {
			fmt.Fprintln(stdout, "no app passwords")
			return 0
		}
		for _, p := range file.Passwords {
			fmt.Fprintf(stdout, "%s\tscope=%s\tcreated=%s\n", p.Name, p.Scope, p.CreatedAt)
		}
		return 0
	case "remove":
		if *name == "" {
			fmt.Fprintln(stderr, "--name is required")
			return 1
		}
		kept := file.Passwords[:0]
		removed := false
		for _, p := range file.Passwords {
			if p.Name == *name {
				removed = true
				continue
			}
			kept = append(kept, p)
		}
		if !removed {
			fmt.Fprintf(stderr, "app password %q not found\n", *name)
			return 1
		}
		file.Passwords = kept
		if err := putAppPasswords(ctx, cfg.RelayURL, identityValue, tok.Token, file); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "app password %q removed\n", *name)
		return 0
	default:
		fmt.Fprintln(stderr, "unknown dav password subcommand (want add, list, remove)")
		return 1
	}
}
