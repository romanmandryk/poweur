package cli

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/eurything/cli/internal/config"
	"github.com/eurything/cli/internal/identity"
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
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
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

	provider := resolveDNSProvider(*dnsProvider)
	token := resolveDNSToken(provider, *dnsToken)

	publicKey := identity.PublicKeyString(pub)
	registered := false
	if *relayURL != "" {
		if err := CheckRelayHealth(context.Background(), *relayURL); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if token == "" {
			fmt.Fprintln(stderr, "dns token is required to register with relay (set --dns-token or CLOUDFFLARE_API_TOKEN/HETZNER_API_TOKEN)")
			return 1
		}
		_, err := RegisterIdentity(context.Background(), *relayURL, identityValue, publicKey, provider, token)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		registered = true
	}

	cfg.Identity = identityValue
	cfg.PrivateKeyPath = keyPath
	cfg.RelayURL = *relayURL
	cfg.ParentDomain = *parentDomain
	if err := config.Save(cfg); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	output := map[string]any{
		"identity":   identityValue,
		"public_key": publicKey,
		"key_path":   keyPath,
		"relay":      *relayURL,
		"registered": registered,
	}
	if registered {
		return writeOutput(stdout, *jsonOut, output, fmt.Sprintf("created identity %s and registered with relay\n", identityValue))
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
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	if cfg.Identity == "" || cfg.PrivateKeyPath == "" {
		fmt.Fprintln(stderr, "no identity configured")
		return 1
	}
	privateKey, err := identity.LoadPrivateKey(cfg.PrivateKeyPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	publicKey := identity.PublicKeyString(privateKey.Public().(ed25519.PublicKey))
	output := map[string]string{
		"identity":   cfg.Identity,
		"public_key": publicKey,
		"key_path":   cfg.PrivateKeyPath,
	}
	return writeOutput(stdout, *jsonOut, output, fmt.Sprintf("%s\n", cfg.Identity))
}

func runIdentityDNS(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("identity dns", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(stderr, "usage: eurything identity dns <identity>")
		return 1
	}
	identityValue := fs.Arg(0)
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

func runSend(args []string, stdout, stderr io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	if fs.NArg() < 2 {
		fmt.Fprintln(stderr, "usage: eurything send <to> <message>")
		return 1
	}
	if cfg.Identity == "" || cfg.PrivateKeyPath == "" {
		fmt.Fprintln(stderr, "identity not configured")
		return 1
	}
	if cfg.RelayURL == "" {
		fmt.Fprintln(stderr, "relay url not configured")
		return 1
	}

	privateKey, err := identity.LoadPrivateKey(cfg.PrivateKeyPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	msg := Message{
		Sender:    cfg.Identity,
		Recipient: fs.Arg(0),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Payload:   fs.Arg(1),
	}
	canonical := fmt.Sprintf("%s\n%s\n%s\n%s", msg.Sender, msg.Recipient, msg.Timestamp, msg.Payload)
	msg.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, []byte(canonical)))

	resp, err := SendMessage(context.Background(), cfg.RelayURL, msg)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer resp.Body.Close()

	output := map[string]any{
		"status":  resp.StatusCode,
		"message": msg,
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
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	if cfg.Identity == "" || cfg.PrivateKeyPath == "" {
		fmt.Fprintln(stderr, "identity not configured")
		return 1
	}
	if cfg.RelayURL == "" {
		fmt.Fprintln(stderr, "relay url not configured")
		return 1
	}

	privateKey, err := identity.LoadPrivateKey(cfg.PrivateKeyPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	challenge, err := FetchChallenge(context.Background(), cfg.RelayURL, cfg.Identity)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	challengeSig := base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, []byte(challenge.Challenge)))
	payload, err := FetchInbox(context.Background(), cfg.RelayURL, cfg.Identity, challengeSig)
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
			ID        string `json:"id"`
			Sender    string `json:"sender"`
			Timestamp string `json:"timestamp"`
			Payload   string `json:"payload"`
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
	for _, msg := range inbox.Messages {
		fmt.Fprintf(stdout, "[%s] %s: %s\n", msg.Timestamp, msg.Sender, msg.Payload)
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
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args[1:], map[string]bool{"--json": true})); err != nil {
		return 1
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
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
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
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(stderr, "request file or url is required")
		return 1
	}
	if cfg.Identity == "" || cfg.PrivateKeyPath == "" {
		fmt.Fprintln(stderr, "identity not configured")
		return 1
	}
	privateKey, err := identity.LoadPrivateKey(cfg.PrivateKeyPath)
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
		"identity":   cfg.Identity,
		"issued_at":  time.Now().UTC().Format(time.RFC3339),
		"signature":  base64.StdEncoding.EncodeToString(signature),
		"request_id": extractRequestID(payload),
	}
	return writeOutput(stdout, *jsonOut, response, "auth request signed\n")
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
  eurything identity show [--json]
  eurything identity dns <identity> [--json]
  eurything send <to> <message> [--json]
  eurything inbox [--json]
  eurything relay status [--json]
  eurything auth inspect <request-file-or-url> [--json]
  eurything auth sign <request-file-or-url> [--json]
`)
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
