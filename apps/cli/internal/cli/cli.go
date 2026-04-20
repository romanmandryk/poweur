package cli

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/eurything/cli/internal/config"
	cryptoe2e "github.com/eurything/cli/internal/crypto"
	"github.com/eurything/cli/internal/identity"
	"github.com/eurything/cli/internal/session"
)

func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printHelp(stdout)
		return 0
	}

	switch args[0] {
	case "identity":
		return runIdentity(args[1:], stdout, stderr)
	case "send":
		return runSend(args[1:], stdout, stderr)
	case "inbox":
		return runInbox(args[1:], stdout, stderr)
	case "relay":
		return runRelay(args[1:], stdout, stderr)
	case "session":
		return runSession(args[1:], stdout, stderr)
	case "auth":
		return runAuth(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		printHelp(stdout)
		return 0
	default:
		fmt.Fprintln(stderr, "unknown command")
		printHelp(stderr)
		return 1
	}
}

func runIdentity(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "identity subcommand required")
		return 1
	}
	switch args[0] {
	case "create":
		return runIdentityCreate(args[1:], stdout, stderr)
	case "show":
		return runIdentityShow(args[1:], stdout, stderr)
	case "dns":
		return runIdentityDNS(args[1:], stdout, stderr)
	case "use":
		return runIdentityUse(args[1:], stdout, stderr)
	case "list":
		return runIdentityList(args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, "unknown identity subcommand")
		return 1
	}
}

func runIdentityCreate(args []string, stdout, stderr io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fs := flag.NewFlagSet("identity create", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dnsProvider := fs.String("dns-provider", "", "dns provider (cloudflare, hetzner)")
	dnsToken := fs.String("dns-token", "", "dns provider api token")
	parentDomain := fs.String("parent-domain", cfg.ParentDomain, "parent domain for identity handle")
	relayURL := fs.String("relay", cfg.RelayURL, "relay base url")
	useIdentity := fs.String("use-identity", "", "override identity for this command")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	_ = useIdentity
	if fs.NArg() < 1 {
		fmt.Fprintln(stderr, "identity handle is required")
		return 1
	}
	raw := fs.Arg(0)
	identityValue := raw
	if !strings.Contains(raw, ".") {
		if *parentDomain == "" {
			fmt.Fprintln(stderr, "parent domain is required when using a handle")
			return 1
		}
		identityValue = raw + "." + strings.TrimPrefix(*parentDomain, ".")
	}

	pub, priv, err := identity.GenerateKeypair()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	keyPath, err := identity.SavePrivateKey(identityValue, priv)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	encPub, encPriv, err := identity.GenerateEncryptionKeypair()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	encKeyPath, err := identity.SaveEncryptionPrivateKey(identityValue, encPriv)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	provider := resolveDNSProvider(*dnsProvider)
	token := resolveDNSToken(provider, *dnsToken)

	publicKey := identity.PublicKeyString(pub)
	encPublicKey := cryptoe2e.EncodePublicKey(encPub)
	registered := false
	if *relayURL != "" {
		if err := CheckRelayHealth(context.Background(), *relayURL); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if token == "" {
			fmt.Fprintln(stderr, "dns token is required to register with relay (set --dns-token or CLOUDFLARE_API_TOKEN/HETZNER_API_TOKEN)")
			return 1
		}
		_, err := RegisterIdentity(context.Background(), *relayURL, identityValue, publicKey, encPublicKey, provider, token)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		registered = true
	}

	cfg.Identity = identityValue
	cfg.KeysDir = filepath.Dir(keyPath)
	cfg.RelayURL = *relayURL
	cfg.ParentDomain = *parentDomain
	if err := config.Save(cfg); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	output := map[string]any{
		"identity":              identityValue,
		"public_key":            publicKey,
		"encryption_public_key": encPublicKey,
		"key_path":              keyPath,
		"encryption_key_path":   encKeyPath,
		"relay":                 *relayURL,
		"registered":            registered,
	}
	if registered {
		return writeOutput(stdout, *jsonOut, output, fmt.Sprintf("created identity %s (registered with relay, e2e encryption enabled)\n", identityValue))
	}
	return writeOutput(stdout, *jsonOut, output, fmt.Sprintf("created identity %s (local only; relay not configured)\n", identityValue))
}

func runIdentityShow(args []string, stdout, stderr io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fs := flag.NewFlagSet("identity show", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "override identity for this command")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	identityValue := resolveIdentity(*useIdentity, cfg.Identity)
	if identityValue == "" || cfg.KeysDir == "" {
		fmt.Fprintln(stderr, "no identity configured")
		return 1
	}
	privateKey, err := identity.LoadPrivateKey(identity.KeyPath(cfg.KeysDir, identityValue))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	publicKey := identity.PublicKeyString(privateKey.Public().(ed25519.PublicKey))
	output := map[string]string{
		"identity":   identityValue,
		"public_key": publicKey,
		"key_path":   identity.KeyPath(cfg.KeysDir, identityValue),
	}
	encPriv, err := identity.LoadEncryptionPrivateKey(identity.EncryptionKeyPath(cfg.KeysDir, identityValue))
	if err == nil {
		encPub, err := publicFromPrivateX25519(encPriv)
		if err == nil {
			output["encryption_public_key"] = cryptoe2e.EncodePublicKey(encPub)
			output["encryption_key_path"] = identity.EncryptionKeyPath(cfg.KeysDir, identityValue)
		}
	}
	return writeOutput(stdout, *jsonOut, output, fmt.Sprintf("%s\n", identityValue))
}

func runIdentityDNS(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("identity dns", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "override identity for this command")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	identityValue := resolveIdentity(fs.Arg(0), *useIdentity)
	if identityValue == "" {
		fmt.Fprintln(stderr, "usage: eurything identity dns <identity>")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := identity.LookupDNS(ctx, identityValue)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *jsonOut {
		return writeOutput(stdout, true, result, "")
	}

	fmt.Fprintf(stdout, "identity: %s\n", identityValue)
	if result.PublicKeyTXT != "" {
		fmt.Fprintf(stdout, "public key TXT: %s\n", result.PublicKeyTXT)
	} else {
		fmt.Fprintln(stdout, "public key TXT: not found")
	}
	if result.EncryptionKeyTXT != "" {
		fmt.Fprintf(stdout, "encryption key TXT: %s\n", result.EncryptionKeyTXT)
	} else {
		fmt.Fprintln(stdout, "encryption key TXT: not found")
	}
	if len(result.RelayHosts) > 0 {
		fmt.Fprintf(stdout, "relay A/CNAME: %s\n", strings.Join(result.RelayHosts, ", "))
	} else {
		fmt.Fprintln(stdout, "relay A/CNAME: not found")
	}
	if result.CNAME != "" {
		fmt.Fprintf(stdout, "cname: %s\n", result.CNAME)
	}
	return 0
}

func runIdentityUse(args []string, stdout, stderr io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fs := flag.NewFlagSet("identity use", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(stderr, "usage: eurything identity use <identity>")
		return 1
	}
	identityValue := fs.Arg(0)
	if cfg.KeysDir == "" {
		fmt.Fprintln(stderr, "keys directory not configured")
		return 1
	}
	keyPath := identity.KeyPath(cfg.KeysDir, identityValue)
	if _, err := os.Stat(keyPath); err != nil {
		fmt.Fprintf(stderr, "key not found for %s at %s\n", identityValue, keyPath)
		return 1
	}
	cfg.Identity = identityValue
	if err := config.Save(cfg); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	output := map[string]string{
		"identity": identityValue,
		"key_path": keyPath,
	}
	return writeOutput(stdout, *jsonOut, output, fmt.Sprintf("active identity set to %s\n", identityValue))
}

func runIdentityList(args []string, stdout, stderr io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fs := flag.NewFlagSet("identity list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	if cfg.KeysDir == "" {
		fmt.Fprintln(stderr, "keys directory not configured")
		return 1
	}
	entries, err := os.ReadDir(cfg.KeysDir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	var identities []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".key") {
			identities = append(identities, strings.TrimSuffix(name, ".key"))
		}
	}
	if *jsonOut {
		return writeOutput(stdout, true, map[string]any{
			"identities": identities,
			"active":     cfg.Identity,
		}, "")
	}
	if len(identities) == 0 {
		fmt.Fprintln(stdout, "no identities found")
		return 0
	}
	for _, identityValue := range identities {
		if identityValue == cfg.Identity {
			fmt.Fprintf(stdout, "* %s\n", identityValue)
		} else {
			fmt.Fprintf(stdout, "  %s\n", identityValue)
		}
	}
	return 0
}

func runSend(args []string, stdout, stderr io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "override identity for this command")
	noEncrypt := fs.Bool("no-encrypt", false, "send the payload in plaintext (not recommended)")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true, "--no-encrypt": true})); err != nil {
		return 1
	}
	if fs.NArg() < 2 {
		fmt.Fprintln(stderr, "usage: eurything send <to> <message>")
		return 1
	}
	identityValue := resolveIdentity(*useIdentity, cfg.Identity)
	if identityValue == "" || cfg.KeysDir == "" {
		fmt.Fprintln(stderr, "identity not configured")
		return 1
	}
	if cfg.RelayURL == "" {
		fmt.Fprintln(stderr, "relay url not configured")
		return 1
	}

	identityPriv, err := identity.LoadPrivateKey(identity.KeyPath(cfg.KeysDir, identityValue))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	sess, err := ensureSession(context.Background(), cfg.RelayURL, identityValue, identityPriv)
	if err != nil {
		fmt.Fprintln(stderr, "session error:", err)
		return 1
	}

	recipient := fs.Arg(0)
	plaintext := fs.Arg(1)

	var payloadString string
	var encMeta *EncryptionMeta
	if !*noEncrypt {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		recipientEncPub, err := identity.LookupEncryptionKey(ctx, recipient)
		cancel()
		if err == nil && len(recipientEncPub) == 32 {
			sealed, err := cryptoe2e.Encrypt(recipientEncPub, []byte(plaintext))
			if err != nil {
				fmt.Fprintln(stderr, "encrypt:", err)
				return 1
			}
			payloadString = sealed.Ciphertext
			encMeta = &EncryptionMeta{
				Alg:                cryptoe2e.AlgName,
				EphemeralPublicKey: sealed.EphemeralPublicKey,
				Nonce:              sealed.Nonce,
			}
		} else {
			fmt.Fprintln(stderr, "warning: recipient has no published encryption key, sending plaintext")
			payloadString = plaintext
		}
	} else {
		payloadString = plaintext
	}

	sessionPriv, err := sess.PrivateKey()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	msg := Message{
		Sender:       identityValue,
		Recipient:    recipient,
		Timestamp:    time.Now().UTC().Format(time.RFC3339),
		Payload:      payloadString,
		SessionID:    sess.SessionID,
		SessionProof: sessionProofFrom(sess),
		Encryption:   encMeta,
	}
	msg.Signature = signMessage(sessionPriv, msg)

	resp, err := SendMessage(context.Background(), cfg.RelayURL, msg)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		if _, err := session.Load(identityValue); err == nil {
			_ = session.Delete(identityValue)
		}
		sess, err = ensureSession(context.Background(), cfg.RelayURL, identityValue, identityPriv)
		if err != nil {
			fmt.Fprintln(stderr, "session error (after retry):", err)
			return 1
		}
		sessionPriv, err = sess.PrivateKey()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		msg.SessionID = sess.SessionID
		msg.SessionProof = sessionProofFrom(sess)
		msg.Signature = signMessage(sessionPriv, msg)
		resp, err = SendMessage(context.Background(), cfg.RelayURL, msg)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(stderr, "relay rejected message (%d): %s\n", resp.StatusCode, strings.TrimSpace(string(body)))
		return 1
	}

	output := map[string]any{
		"status":    resp.StatusCode,
		"message":   msg,
		"encrypted": encMeta != nil,
	}
	if encMeta != nil {
		return writeOutput(stdout, *jsonOut, output, fmt.Sprintf("sent encrypted message to %s\n", msg.Recipient))
	}
	return writeOutput(stdout, *jsonOut, output, fmt.Sprintf("sent message to %s\n", msg.Recipient))
}

func runInbox(args []string, stdout, stderr io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fs := flag.NewFlagSet("inbox", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "override identity for this command")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	identityValue := resolveIdentity(*useIdentity, cfg.Identity)
	if identityValue == "" || cfg.KeysDir == "" {
		fmt.Fprintln(stderr, "identity not configured")
		return 1
	}
	if cfg.RelayURL == "" {
		fmt.Fprintln(stderr, "relay url not configured")
		return 1
	}

	identityPriv, err := identity.LoadPrivateKey(identity.KeyPath(cfg.KeysDir, identityValue))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	sess, err := ensureSession(context.Background(), cfg.RelayURL, identityValue, identityPriv)
	if err != nil {
		fmt.Fprintln(stderr, "session error:", err)
		return 1
	}

	payload, err := fetchInboxWithSessionRetry(context.Background(), cfg.RelayURL, identityValue, identityPriv, sess)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	if *jsonOut {
		fmt.Fprintln(stdout, string(payload))
		return 0
	}

	var inbox struct {
		Messages []struct {
			ID         string          `json:"id"`
			Sender     string          `json:"sender"`
			Timestamp  string          `json:"timestamp"`
			Payload    string          `json:"payload"`
			Signature  string          `json:"signature"`
			SessionID  string          `json:"session_id,omitempty"`
			Encryption *EncryptionMeta `json:"encryption,omitempty"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(payload, &inbox); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if len(inbox.Messages) == 0 {
		fmt.Fprintln(stdout, "no messages")
		return 0
	}

	encPriv, _ := identity.LoadEncryptionPrivateKey(identity.EncryptionKeyPath(cfg.KeysDir, identityValue))

	for _, msg := range inbox.Messages {
		display := msg.Payload
		decrypted := false
		if msg.Encryption != nil && msg.Encryption.Alg != "" {
			if encPriv == nil {
				display = "[encrypted: no local encryption key]"
			} else {
				plaintext, err := cryptoe2e.Decrypt(encPriv, cryptoe2e.EncryptedPayload{
					Ciphertext:         msg.Payload,
					EphemeralPublicKey: msg.Encryption.EphemeralPublicKey,
					Nonce:              msg.Encryption.Nonce,
				})
				if err != nil {
					display = fmt.Sprintf("[decrypt failed: %v]", err)
				} else {
					display = string(plaintext)
					decrypted = true
				}
			}
		}
		prefix := "  "
		if decrypted {
			prefix = "🔒"
		}
		fmt.Fprintf(stdout, "%s [%s] %s: %s\n", prefix, msg.Timestamp, msg.Sender, display)
	}
	return 0
}

func runRelay(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "status" {
		fmt.Fprintln(stderr, "usage: eurything relay status")
		return 1
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fs := flag.NewFlagSet("relay status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "override identity for this command")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args[1:], map[string]bool{"--json": true})); err != nil {
		return 1
	}
	if *useIdentity != "" {
		cfg.Identity = *useIdentity
	}
	if cfg.RelayURL == "" {
		fmt.Fprintln(stderr, "relay url not configured")
		return 1
	}
	health, err := FetchHealth(context.Background(), cfg.RelayURL)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	output := map[string]string{
		"status":  health.Status,
		"version": health.Version,
	}
	return writeOutput(stdout, *jsonOut, output, fmt.Sprintf("relay %s (version %s)\n", health.Status, health.Version))
}

func runSession(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "session subcommand required: status|refresh|revoke")
		return 1
	}
	switch args[0] {
	case "status":
		return runSessionStatus(args[1:], stdout, stderr)
	case "refresh":
		return runSessionRefresh(args[1:], stdout, stderr)
	case "revoke":
		return runSessionRevoke(args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, "unknown session subcommand")
		return 1
	}
}

func runSessionStatus(args []string, stdout, stderr io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fs := flag.NewFlagSet("session status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "override identity for this command")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	identityValue := resolveIdentity(*useIdentity, cfg.Identity)
	if identityValue == "" {
		fmt.Fprintln(stderr, "identity not configured")
		return 1
	}
	sess, err := session.Load(identityValue)
	if err != nil {
		fmt.Fprintf(stdout, "no session for %s\n", identityValue)
		return 0
	}
	output := map[string]any{
		"identity":   sess.Identity,
		"session_id": sess.SessionID,
		"issued_at":  sess.IssuedAt.Format(time.RFC3339),
		"expires_at": sess.ExpiresAt.Format(time.RFC3339),
		"relay_url":  sess.RelayURL,
		"valid":      sess.IsValid(),
	}
	return writeOutput(stdout, *jsonOut, output, fmt.Sprintf("session %s for %s (valid=%t, expires %s)\n",
		sess.SessionID, sess.Identity, sess.IsValid(), sess.ExpiresAt.Format(time.RFC3339)))
}

func runSessionRefresh(args []string, stdout, stderr io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fs := flag.NewFlagSet("session refresh", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "override identity for this command")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	identityValue := resolveIdentity(*useIdentity, cfg.Identity)
	if identityValue == "" || cfg.KeysDir == "" {
		fmt.Fprintln(stderr, "identity not configured")
		return 1
	}
	if cfg.RelayURL == "" {
		fmt.Fprintln(stderr, "relay url not configured")
		return 1
	}
	_ = session.Delete(identityValue)
	priv, err := identity.LoadPrivateKey(identity.KeyPath(cfg.KeysDir, identityValue))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	sess, err := ensureSession(context.Background(), cfg.RelayURL, identityValue, priv)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	output := map[string]any{
		"session_id": sess.SessionID,
		"expires_at": sess.ExpiresAt.Format(time.RFC3339),
	}
	return writeOutput(stdout, *jsonOut, output, fmt.Sprintf("session refreshed: %s (expires %s)\n", sess.SessionID, sess.ExpiresAt.Format(time.RFC3339)))
}

func runSessionRevoke(args []string, stdout, stderr io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fs := flag.NewFlagSet("session revoke", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "override identity for this command")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	identityValue := resolveIdentity(*useIdentity, cfg.Identity)
	if identityValue == "" {
		fmt.Fprintln(stderr, "identity not configured")
		return 1
	}
	if err := session.Delete(identityValue); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return writeOutput(stdout, *jsonOut, map[string]string{"identity": identityValue}, fmt.Sprintf("session revoked for %s\n", identityValue))
}

func runAuth(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "auth subcommand required")
		return 1
	}
	switch args[0] {
	case "inspect":
		return runAuthInspect(args[1:], stdout, stderr)
	case "sign":
		return runAuthSign(args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, "unknown auth subcommand")
		return 1
	}
}

func runAuthInspect(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("auth inspect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "override identity for this command")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	_ = useIdentity
	if fs.NArg() < 1 {
		fmt.Fprintln(stderr, "request file or url is required")
		return 1
	}
	payload, err := readRequestPayload(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *jsonOut {
		fmt.Fprintln(stdout, string(payload))
		return 0
	}
	fmt.Fprintln(stdout, string(payload))
	return 0
}

func runAuthSign(args []string, stdout, stderr io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fs := flag.NewFlagSet("auth sign", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "override identity for this command")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(stderr, "request file or url is required")
		return 1
	}
	identityValue := resolveIdentity(*useIdentity, cfg.Identity)
	if identityValue == "" || cfg.KeysDir == "" {
		fmt.Fprintln(stderr, "identity not configured")
		return 1
	}
	privateKey, err := identity.LoadPrivateKey(identity.KeyPath(cfg.KeysDir, identityValue))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	payload, err := readRequestPayload(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	signature := ed25519.Sign(privateKey, payload)
	response := map[string]any{
		"identity":   identityValue,
		"issued_at":  time.Now().UTC().Format(time.RFC3339),
		"signature":  base64.StdEncoding.EncodeToString(signature),
		"request_id": extractRequestID(payload),
	}
	return writeOutput(stdout, *jsonOut, response, "auth request signed\n")
}

// ensureSession returns a valid session for the given identity on the given relay.
// It loads the cached session if present and still valid; otherwise it generates
// a new short-lived Ed25519 keypair, signs the registration with the long-lived
// identity key, registers it with the relay, and persists it locally.
//
// On mobile the long-lived identity private key would live in the secure
// enclave; the passkey/biometric unlock step happens here and nowhere else
// under normal operation (i.e. at most once per session lifetime, typically 24h).
func ensureSession(ctx context.Context, relayURL, identityValue string, identityPriv ed25519.PrivateKey) (session.Session, error) {
	if existing, err := session.Load(identityValue); err == nil {
		if existing.IsValid() && existing.RelayURL == relayURL {
			return existing, nil
		}
	}
	sessionPub, sessionPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return session.Session{}, err
	}
	sessionPubB64 := base64.RawURLEncoding.EncodeToString(sessionPub)

	issuedAt := time.Now().UTC()
	expiresAt := issuedAt.Add(24 * time.Hour)
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		return session.Session{}, err
	}
	nonce := base64.RawURLEncoding.EncodeToString(nonceBytes)

	issuedAtStr := issuedAt.Format(time.RFC3339)
	expiresAtStr := expiresAt.Format(time.RFC3339)
	canonical := fmt.Sprintf("session-registration\n%s\n%s\n%s\n%s\n%s",
		identityValue, sessionPubB64, issuedAtStr, expiresAtStr, nonce)
	identitySig := base64.StdEncoding.EncodeToString(ed25519.Sign(identityPriv, []byte(canonical)))

	resp, err := RegisterSession(ctx, relayURL, SessionCreateRequest{
		Identity:          identityValue,
		SessionPublicKey:  sessionPubB64,
		IssuedAt:          issuedAtStr,
		ExpiresAt:         expiresAtStr,
		Nonce:             nonce,
		IdentitySignature: identitySig,
	})
	if err != nil {
		return session.Session{}, err
	}

	sess := session.Session{
		Identity:          identityValue,
		SessionID:         resp.SessionID,
		SessionPrivateKey: base64.RawStdEncoding.EncodeToString(sessionPriv),
		SessionPublicKey:  sessionPubB64,
		IssuedAt:          issuedAt,
		ExpiresAt:         expiresAt,
		RelayURL:          relayURL,
		IssuedAtRaw:       issuedAtStr,
		ExpiresAtRaw:      expiresAtStr,
		Nonce:             nonce,
		IdentitySignature: identitySig,
	}
	if err := session.Save(sess); err != nil {
		return session.Session{}, err
	}
	return sess, nil
}

func fetchInboxWithSessionRetry(ctx context.Context, relayURL, identityValue string, identityPriv ed25519.PrivateKey, sess session.Session) ([]byte, error) {
	challenge, err := FetchChallenge(ctx, relayURL, identityValue)
	if err != nil {
		return nil, err
	}
	sessionPriv, err := sess.PrivateKey()
	if err != nil {
		return nil, err
	}
	signature := base64.StdEncoding.EncodeToString(ed25519.Sign(sessionPriv, []byte(challenge.Challenge)))
	payload, err := FetchInbox(ctx, relayURL, identityValue, signature, sess.SessionID)
	if err == nil {
		return payload, nil
	}
	if !errors.Is(err, ErrSessionExpired) {
		return nil, err
	}
	_ = session.Delete(identityValue)
	sess, err = ensureSession(ctx, relayURL, identityValue, identityPriv)
	if err != nil {
		return nil, err
	}
	challenge, err = FetchChallenge(ctx, relayURL, identityValue)
	if err != nil {
		return nil, err
	}
	sessionPriv, err = sess.PrivateKey()
	if err != nil {
		return nil, err
	}
	signature = base64.StdEncoding.EncodeToString(ed25519.Sign(sessionPriv, []byte(challenge.Challenge)))
	return FetchInbox(ctx, relayURL, identityValue, signature, sess.SessionID)
}

// sessionProofFrom builds a SessionProof from the locally stored session. If
// the session lacks the raw fields (e.g. created before proof support was
// added) the sender falls back to a nil proof; the home relay will still
// verify via its cache, and cross-relay delivery would need a `session
// refresh` to regenerate the proof.
func sessionProofFrom(sess session.Session) *SessionProof {
	if sess.IssuedAtRaw == "" || sess.ExpiresAtRaw == "" ||
		sess.Nonce == "" || sess.IdentitySignature == "" ||
		sess.SessionPublicKey == "" {
		return nil
	}
	return &SessionProof{
		SessionPublicKey:  sess.SessionPublicKey,
		IssuedAt:          sess.IssuedAtRaw,
		ExpiresAt:         sess.ExpiresAtRaw,
		Nonce:             sess.Nonce,
		IdentitySignature: sess.IdentitySignature,
	}
}

// signMessage produces a signature over the canonical representation of the
// message, including session id and encryption metadata when present.
func signMessage(sessionPriv ed25519.PrivateKey, msg Message) string {
	parts := []string{msg.Sender, msg.Recipient, msg.Timestamp, msg.Payload}
	if msg.SessionID != "" {
		parts = append(parts, "session:"+msg.SessionID)
	}
	if msg.Encryption != nil && msg.Encryption.Alg != "" {
		parts = append(parts, "enc:"+msg.Encryption.Alg+":"+msg.Encryption.EphemeralPublicKey+":"+msg.Encryption.Nonce)
	}
	canonical := strings.Join(parts, "\n")
	sig := ed25519.Sign(sessionPriv, []byte(canonical))
	return base64.StdEncoding.EncodeToString(sig)
}

func readRequestPayload(source string) ([]byte, error) {
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		resp, err := http.Get(source)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("request fetch failed with status %d", resp.StatusCode)
		}
		return io.ReadAll(resp.Body)
	}
	path, err := filepath.Abs(source)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

func extractRequestID(payload []byte) string {
	var data map[string]any
	if err := json.Unmarshal(payload, &data); err != nil {
		return ""
	}
	if value, ok := data["request_id"].(string); ok {
		return value
	}
	return ""
}

func writeOutput(w io.Writer, jsonOut bool, payload any, message string) int {
	if jsonOut {
		encoded, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			fmt.Fprintln(w, "{}")
			return 1
		}
		fmt.Fprintln(w, string(encoded))
		return 0
	}
	fmt.Fprint(w, message)
	return 0
}

func printHelp(w io.Writer) {
	fmt.Fprint(w, `Usage:
  eurything identity create <name> [--dns-provider=cloudflare|hetzner] [--dns-token=...] [--parent-domain=...] [--relay=...] [--json]
  eurything identity show [--use-identity=...] [--json]
  eurything identity dns <identity> [--use-identity=...] [--json]
  eurything identity use <identity> [--json]
  eurything identity list [--json]
  eurything send <to> <message> [--use-identity=...] [--no-encrypt] [--json]
  eurything inbox [--use-identity=...] [--json]
  eurything session status [--use-identity=...] [--json]
  eurything session refresh [--use-identity=...] [--json]
  eurything session revoke [--use-identity=...] [--json]
  eurything relay status [--json]
  eurything auth inspect <request-file-or-url> [--json]
  eurything auth sign <request-file-or-url> [--use-identity=...] [--json]
`)
}

func resolveIdentity(flagValue string, fallback string) string {
	if flagValue != "" {
		return flagValue
	}
	return fallback
}

func normalizeArgs(args []string, boolFlags map[string]bool) []string {
	var flags []string
	var positional []string

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-") {
			flags = append(flags, arg)
			if boolFlags[arg] || strings.Contains(arg, "=") {
				continue
			}
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				flags = append(flags, args[i+1])
				i++
			}
			continue
		}
		positional = append(positional, arg)
	}

	return append(flags, positional...)
}

func resolveDNSProvider(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if value := os.Getenv("DNS_PROVIDER"); value != "" {
		return value
	}
	return "cloudflare"
}

func resolveDNSToken(provider, flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	switch strings.ToLower(provider) {
	case "cloudflare":
		if value := os.Getenv("CLOUDFLARE_API_TOKEN"); value != "" {
			return value
		}
	case "hetzner":
		if value := os.Getenv("HETZNER_API_TOKEN"); value != "" {
			return value
		}
	}
	if value := os.Getenv("DNS_TOKEN"); value != "" {
		return value
	}
	return ""
}

// publicFromPrivateX25519 derives the X25519 public key for an identity's stored encryption private key.
func publicFromPrivateX25519(privateKey []byte) ([]byte, error) {
	return cryptoe2e.PublicFromPrivate(privateKey)
}
