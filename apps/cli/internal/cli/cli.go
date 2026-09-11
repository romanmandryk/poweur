package cli

import (
	"bytes"
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

	"github.com/poweur/cli/internal/buildinfo"
	"github.com/poweur/cli/internal/config"
	cryptoe2e "github.com/poweur/cli/internal/crypto"
	"github.com/poweur/cli/internal/identity"
	"github.com/poweur/cli/internal/journal"
	"github.com/poweur/cli/internal/session"
	idpkg "github.com/poweur/identity"
)

func Run(args []string, stdout, stderr io.Writer) int {
	// Optional override: if DNS_SERVER is set (e.g. "1.1.1.1" or
	// "8.8.8.8:53") route every DNS lookup this CLI performs through that
	// resolver using Go's pure-Go DNS client. Defeats broken LAN resolvers
	// and cached NXDOMAINs without touching system DNS. Empty env = use the
	// system resolver (default).
	if server := strings.TrimSpace(os.Getenv("DNS_SERVER")); server != "" {
		identity.SetResolver(identity.CustomNetResolver(server))
	}
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
	case "listen":
		return runListen(args[1:], stdout, stderr)
	case "devices":
		return runDevices(args[1:], stdout, stderr)
	case "history":
		return runHistory(args[1:], stdout, stderr)
	case "messages":
		return runMessages(args[1:], stdout, stderr)
	case "relay":
		return runRelay(args[1:], stdout, stderr)
	case "key":
		return runKey(args[1:], stdout, stderr)
	case "dav":
		return runDAV(args[1:], stdout, stderr)
	case "sync":
		return runSync(args[1:], stdout, stderr)
	case "share":
		return runShare(args[1:], stdout, stderr)
	case "group":
		return runGroup(args[1:], stdout, stderr)
	case "contacts":
		return runContacts(args[1:], stdout, stderr)
	case "requests":
		return runRequests(args[1:], stdout, stderr)
	case "analytics":
		return runAnalytics(args[1:], stdout, stderr)
	case "policy":
		return runPolicy(args[1:], stdout, stderr)
	case "blocks":
		return runBlocks(args[1:], stdout, stderr)
	case "report":
		return runReport(args[1:], stdout, stderr)
	case "anon":
		return runAnon(args[1:], stdout, stderr)
	case "session":
		return runSession(args[1:], stdout, stderr)
	case "auth":
		return runAuth(args[1:], stdout, stderr)
	case "version", "--version", "-v":
		buildinfo.Write(stdout)
		return 0
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
	case "add-encryption-key":
		return runIdentityAddEncryptionKey(args[1:], stdout, stderr)
	case "lookup":
		return runIdentityLookup(args[1:], stdout, stderr)
	case "export":
		return runIdentityExport(args[1:], stdout, stderr)
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
	hosted := fs.Bool("hosted", false, "register as hosted identity (no DNS token; requires HOSTED_DOMAINS on relay)")
	inviteCode := fs.String("invite-code", "", "invite code when REGISTRATION_GATE=invite")
	parentDomain := fs.String("parent-domain", cfg.ParentDomain, "parent domain for identity handle")
	relayURL := fs.String("relay", cfg.RelayURL, "relay base url")
	useIdentity := fs.String("use-identity", "", "override identity for this command")
	seedFlag := fs.String("seed", "", "derive keys from this base64url master seed (EPIC-011)")
	fromSeed := fs.Bool("from-seed", false, "generate a master seed and derive keys from it")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true, "--hosted": true, "--from-seed": true})); err != nil {
		return 1
	}
	if *seedFlag != "" && *fromSeed {
		fmt.Fprintln(stderr, "use either --seed or --from-seed, not both")
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

	// EPIC-011: one master seed derives both long-lived keys, so a recovery
	// kit is 32 bytes rather than two independent keys. Without --seed or
	// --from-seed the legacy path (independent random keys) is unchanged.
	var seed []byte
	switch {
	case *seedFlag != "":
		if seed, err = identity.ParseSeed(*seedFlag); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	case *fromSeed:
		if seed, err = identity.NewSeed(); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}

	var (
		pub     ed25519.PublicKey
		priv    ed25519.PrivateKey
		encPub  []byte
		encPriv []byte
	)
	if seed != nil {
		if pub, priv, err = identity.KeypairFromSeed(seed); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if encPub, encPriv, err = identity.EncryptionKeypairFromSeed(seed); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	} else {
		if pub, priv, err = identity.GenerateKeypair(); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if encPub, encPriv, err = identity.GenerateEncryptionKeypair(); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}

	keyPath, err := identity.SavePrivateKey(identityValue, priv)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	encKeyPath, err := identity.SaveEncryptionPrivateKey(identityValue, encPriv)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	publicKey := identity.PublicKeyString(pub)
	encPublicKey := cryptoe2e.EncodePublicKey(encPub)
	registered := false
	if *relayURL != "" {
		if err := CheckRelayHealth(context.Background(), *relayURL); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		issuedAt := time.Now().UTC().Format(time.RFC3339)
		nonce := newAdminNonce()
		relayAddr := relayAddressFromURL(*relayURL)

		req := IdentityRegisterRequest{
			Identity:            identityValue,
			PublicKey:           publicKey,
			EncryptionPublicKey: encPublicKey,
			IssuedAt:            issuedAt,
			Nonce:               nonce,
			IdentitySignature:   signIdentityRegistration(priv, identityValue, publicKey, encPublicKey, relayAddr, issuedAt, nonce),
		}

		if *inviteCode != "" {
			req.InviteCode = *inviteCode
		}
		if *hosted {
			docRaw, err := buildSignedIdentityDocument(priv, identityValue, publicKey, encPublicKey, relayAddr, issuedAt)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			req.IdentityDocument = docRaw
		} else {
			provider := resolveDNSProvider(*dnsProvider)
			token := resolveDNSToken(provider, *dnsToken)
			if token == "" {
				fmt.Fprintln(stderr, "dns token is required to register with relay (set --dns-token or CLOUDFLARE_API_TOKEN/HETZNER_API_TOKEN), or use --hosted")
				return 1
			}
			req.DNSProvider = provider
			req.DNSToken = token
			// Optional document for DNS path when relay has POWEUR_DATA
			if docRaw, err := buildSignedIdentityDocument(priv, identityValue, publicKey, encPublicKey, relayAddr, issuedAt); err == nil {
				req.IdentityDocument = docRaw
			}
		}

		if _, err := RegisterIdentity(context.Background(), *relayURL, req); err != nil {
			// REGISTRATION_GATE=pow (EPIC-014): fetch the challenge, solve
			// it locally, retry once.
			if *hosted && strings.Contains(err.Error(), "pow_required") {
				token, solution, powErr := solveRegistrationPow(context.Background(), *relayURL, stderr)
				if powErr != nil {
					fmt.Fprintln(stderr, powErr)
					return 1
				}
				req.PowToken = token
				req.PowSolution = solution
				if _, err := RegisterIdentity(context.Background(), *relayURL, req); err != nil {
					fmt.Fprintln(stderr, err)
					return 1
				}
			} else {
				fmt.Fprintln(stderr, err)
				return 1
			}
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
		"hosted":                *hosted,
		"seed_derived":          seed != nil,
	}
	// Echo the seed only when we generated it: with --seed the caller already
	// has it, and reprinting secrets into shell history buys nothing. This is
	// the user's only copy, so it also goes to stderr in human mode.
	if *fromSeed {
		output["seed"] = identity.FormatSeed(seed)
		// The mnemonic ships alongside the raw seed: same 32 bytes, but
		// checksummed and safe to copy by hand. Which one a user keeps is
		// their choice; withholding either would make that choice for them.
		if mnemonic, mErr := identity.SeedToMnemonic(seed); mErr == nil {
			output["mnemonic"] = mnemonic
			if !*jsonOut {
				fmt.Fprintf(stderr,
					"recovery kit for %s — store this; it is the only way back\n"+
						"  seed:     %s\n  mnemonic: %s\n",
					identityValue, identity.FormatSeed(seed), mnemonic)
			}
		} else if !*jsonOut {
			fmt.Fprintf(stderr, "master seed (store this — it is the only way to recover %s):\n  %s\n",
				identityValue, identity.FormatSeed(seed))
		}
	}
	if registered {
		mode := "registered with relay"
		if *hosted {
			mode = "hosted registration"
		}
		return writeOutput(stdout, *jsonOut, output, fmt.Sprintf("created identity %s (%s, e2e encryption enabled)\n", identityValue, mode))
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
		fmt.Fprintln(stderr, "usage: poweur identity dns <identity>")
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
		fmt.Fprintln(stderr, "usage: poweur identity use <identity>")
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

// runIdentityAddEncryptionKey generates a fresh X25519 keypair for an
// already-registered identity, saves the private half locally, and asks the
// relay to publish the public half to DNS under `_poweur-enc.<identity>`.
//
// Modes:
//   - no existing .enc file: a new keypair is minted (the normal "retro-fit"
//     path for identities created before E2E support landed).
//   - existing .enc file and --rotate: the local file is overwritten and the
//     DNS record is rewritten (useful after suspected compromise).
//   - existing .enc file without --rotate: command aborts to avoid silently
//     invalidating ciphertext that was encrypted to the old key.
func runIdentityAddEncryptionKey(args []string, stdout, stderr io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fs := flag.NewFlagSet("identity add-encryption-key", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dnsProvider := fs.String("dns-provider", "", "dns provider (cloudflare, hetzner)")
	dnsToken := fs.String("dns-token", "", "dns provider api token")
	relayURL := fs.String("relay", cfg.RelayURL, "relay base url")
	useIdentity := fs.String("use-identity", "", "override identity for this command")
	rotate := fs.Bool("rotate", false, "overwrite an existing encryption key")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true, "--rotate": true})); err != nil {
		return 1
	}

	identityValue := fs.Arg(0)
	if identityValue == "" {
		identityValue = resolveIdentity(*useIdentity, cfg.Identity)
	}
	if identityValue == "" || cfg.KeysDir == "" {
		fmt.Fprintln(stderr, "identity not configured (pass <identity> or run `poweur identity use <identity>` first)")
		return 1
	}
	if *relayURL == "" {
		fmt.Fprintln(stderr, "relay url not configured")
		return 1
	}

	signingKeyPath := identity.KeyPath(cfg.KeysDir, identityValue)
	if _, err := os.Stat(signingKeyPath); err != nil {
		fmt.Fprintf(stderr, "signing key not found for %s at %s\n", identityValue, signingKeyPath)
		return 1
	}

	encKeyPath := identity.EncryptionKeyPath(cfg.KeysDir, identityValue)
	if _, err := os.Stat(encKeyPath); err == nil && !*rotate {
		fmt.Fprintf(stderr, "encryption key already exists at %s; pass --rotate to overwrite\n", encKeyPath)
		return 1
	}

	provider := resolveDNSProvider(*dnsProvider)
	token := resolveDNSToken(provider, *dnsToken)
	if token == "" {
		fmt.Fprintln(stderr, "dns token is required (set --dns-token or CLOUDFLARE_API_TOKEN/HETZNER_API_TOKEN)")
		return 1
	}

	if err := CheckRelayHealth(context.Background(), *relayURL); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	encPub, encPriv, err := identity.GenerateEncryptionKeypair()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	savedPath, err := identity.SaveEncryptionPrivateKey(identityValue, encPriv)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	encPublicKey := cryptoe2e.EncodePublicKey(encPub)
	identityPriv, err := identity.LoadPrivateKey(signingKeyPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	issuedAt := time.Now().UTC().Format(time.RFC3339)
	nonce := newAdminNonce()
	publishReq := EncryptionKeyPublishRequest{
		EncryptionPublicKey: encPublicKey,
		DNSProvider:         provider,
		DNSToken:            token,
		IssuedAt:            issuedAt,
		Nonce:               nonce,
		IdentitySignature:   signEncryptionKeyUpdate(identityPriv, identityValue, encPublicKey, issuedAt, nonce),
	}
	resp, err := PublishEncryptionKey(context.Background(), *relayURL, identityValue, publishReq)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	output := map[string]any{
		"identity":              identityValue,
		"encryption_public_key": resp.EncryptionPublicKey,
		"encryption_key_path":   savedPath,
		"relay":                 *relayURL,
		"updated_at":            resp.UpdatedAt,
		"rotated":               *rotate,
	}
	verb := "added encryption key"
	if *rotate {
		verb = "rotated encryption key"
	}
	return writeOutput(stdout, *jsonOut, output, fmt.Sprintf("%s for %s (published to DNS)\n", verb, identityValue))
}

// runSend posts an encrypted, signed message envelope to the recipient's
// relay. Routing is two-mode:
//
//   - Default: look up the recipient via DNS, derive their relay URL, and
//     POST directly there. The home relay sees zero outbound traffic.
//   - --via-home-relay (or via_home_relay=true in config): POST to the
//     home relay (cfg.RelayURL) instead. The home relay enforces the
//     at-least-one-local rule, accepts because the sender is local, and
//     forwards on to the recipient relay. Useful when the user wants to
//     hide their IP from the recipient relay.
//
// Either way, every successful send is recorded in the per-identity
// pending journal so `poweur messages status` can render WhatsApp-style
// ticks later. A 202 advances the local state to delivered_recipient_relay
// (tick 1); tick 2 (delivered_client) shows up later via inbox polling.
func runSend(args []string, stdout, stderr io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "override identity for this command")
	signWith := fs.String("sign-with", "session", "signing key to use: session (default) or identity")
	viaHomeRelay := fs.Bool("via-home-relay", false, "route through your own home relay (privacy proxy: hides your IP from the recipient relay)")
	msgType := fs.String("type", "", "envelope message type (default chat.text; sys.* reserved for the platform)")
	threadID := fs.String("thread", "", "group this message into a conversation thread")
	expiresAt := fs.String("expires", "", "RFC3339 timestamp after which this message stops being meaningful")
	meta := &metaFlag{}
	fs.Var(meta, "meta", "envelope metadata as key=value (repeatable; plaintext — addressing, not content)")
	acceptNewKey := fs.Bool("accept-new-key", false, "accept and re-pin a changed contact key (see key pinning)")
	anonFlag := fs.Bool("anon", false, "send anonymously: unsigned, no identity attached (recipient must opt in; may require proof-of-work)")
	requestOnReject := fs.Bool("request-on-reject", false, "if the recipient's inbox policy rejects the message, send it as a contact request instead (no prompt)")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true, "--via-home-relay": true, "--accept-new-key": true, "--anon": true, "--request-on-reject": true})); err != nil {
		return 1
	}
	if fs.NArg() < 2 {
		fmt.Fprintln(stderr, "usage: poweur send <to> <message> [--sign-with=session|identity] [--via-home-relay] [--anon]")
		return 1
	}
	// Check the envelope before anything is encrypted, signed, journalled or
	// posted: a `sys.*` typo should not become a recorded send attempt.
	if err := validateOutgoingEnvelope(*msgType, *threadID, *expiresAt, meta.Map()); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *anonFlag {
		// An anonymous envelope carries no signature, so nothing binds these
		// fields to a sender — any relay on the path could add, drop or
		// rewrite them. Rather than ship routing metadata nobody can trust,
		// the combination is refused.
		if *msgType != "" || *threadID != "" || *expiresAt != "" || meta.Map() != nil {
			fmt.Fprintln(stderr, "--anon cannot carry --type/--thread/--expires/--meta: an unsigned envelope binds nothing")
			return 1
		}
		return runSendAnon(cfg, fs.Arg(0), fs.Arg(1), *jsonOut, stdout, stderr)
	}
	mode := strings.ToLower(strings.TrimSpace(*signWith))
	if mode != "session" && mode != "identity" {
		fmt.Fprintf(stderr, "invalid --sign-with value %q: must be \"session\" or \"identity\"\n", *signWith)
		return 1
	}
	identityValue := resolveIdentity(*useIdentity, cfg.Identity)
	if identityValue == "" || cfg.KeysDir == "" {
		fmt.Fprintln(stderr, "identity not configured")
		return 1
	}
	useViaHomeRelay := *viaHomeRelay || cfg.ViaHomeRelay
	if useViaHomeRelay && cfg.RelayURL == "" {
		fmt.Fprintln(stderr, "--via-home-relay requires a configured home relay (RelayURL)")
		return 1
	}

	identityPriv, err := identity.LoadPrivateKey(identity.KeyPath(cfg.KeysDir, identityValue))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	recipient := fs.Arg(0)
	plaintext := fs.Arg(1)

	// Key pinning (E07-T4): when the recipient is a pinned contact, the
	// resolved signing key must match the pin (or be covered by a signed
	// rotation statement) — the known-hosts / safety-number model.
	if code := checkPinnedKey(cfg, identityValue, identityPriv, recipient, *acceptNewKey, stderr); code != 0 {
		return code
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	recipientEncPub, err := identity.LookupEncryptionKey(ctx, recipient)
	cancel()
	if err != nil {
		fmt.Fprintf(stderr, "cannot look up recipient encryption key for %s: %v\n", recipient, err)
		return 1
	}
	if len(recipientEncPub) != 32 {
		fmt.Fprintf(stderr, "recipient %s has no published encryption key; refusing to send in plaintext.\n"+
			"Ask them to run `poweur identity add-encryption-key %s` to publish one.\n",
			recipient, recipient)
		return 1
	}
	sealed, err := cryptoe2e.Encrypt(recipientEncPub, []byte(plaintext))
	if err != nil {
		fmt.Fprintln(stderr, "encrypt:", err)
		return 1
	}
	payloadString := sealed.Ciphertext
	encMeta := &EncryptionMeta{
		Alg:                cryptoe2e.AlgName,
		EphemeralPublicKey: sealed.EphemeralPublicKey,
		Nonce:              sealed.Nonce,
	}

	targetURL := cfg.RelayURL
	if !useViaHomeRelay {
		resolved, err := resolveRecipientRelayURL(context.Background(), recipient, cfg)
		if err != nil {
			fmt.Fprintf(stderr, "cannot resolve recipient relay for %s: %v\n", recipient, err)
			return 1
		}
		targetURL = resolved
	}
	if targetURL == "" {
		fmt.Fprintln(stderr, "no target relay url available")
		return 1
	}

	messageID := identity.NewMessageID()
	timestamp := time.Now().UTC().Format(time.RFC3339)

	_ = journal.Append(journal.Entry{
		MessageID:    messageID,
		Sender:       identityValue,
		Recipient:    recipient,
		Timestamp:    time.Now().UTC(),
		State:        journal.StateQueued,
		ViaHomeRelay: useViaHomeRelay,
	})

	if mode == "identity" {
		msg := Message{
			ID:         messageID,
			Sender:     identityValue,
			Recipient:  recipient,
			Timestamp:  timestamp,
			Payload:    payloadString,
			Type:       *msgType,
			ThreadID:   *threadID,
			ExpiresAt:  *expiresAt,
			Metadata:   meta.Map(),
			Encryption: encMeta,
		}
		msg.Signature = signMessage(identityPriv, msg)

		resp, err := SendMessage(context.Background(), targetURL, msg)
		if err != nil {
			recordSendFailure(identityValue, messageID, recipient, useViaHomeRelay, err.Error())
			fmt.Fprintln(stderr, err)
			return 1
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			body, _ := io.ReadAll(resp.Body)
			recordSendFailure(identityValue, messageID, recipient, useViaHomeRelay,
				fmt.Sprintf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body))))
			fmt.Fprintf(stderr, "relay rejected message (%d): %s\n", resp.StatusCode, strings.TrimSpace(string(body)))
			// E07-T3: a `policy_rejected` refusal is an invitation to ask.
			if offerContactRequest(contactRequestOffer{
				recipient: recipient, plaintext: plaintext, msgType: *msgType,
				useIdentity: identityValue, status: resp.StatusCode, body: body,
				auto: *requestOnReject, jsonOut: *jsonOut,
			}, stdout, stderr) {
				return 0
			}
			return 1
		}

		recordTick1(identityValue, messageID, recipient, useViaHomeRelay)

		// The relay never hands a sender their own message back, so this copy
		// is the only record that the conversation has two sides.
		archiveRecords(*useIdentity, []idpkg.HistoryRecord{historyRecordThreaded(
			identityValue, idpkg.HistoryQueueSent, messageID, identityValue,
			recipient, timestamp, *msgType, *threadID, plaintext)}, stderr)

		output := map[string]any{
			"id":             messageID,
			"status":         resp.StatusCode,
			"message":        msg,
			"encrypted":      true,
			"sign_with":      "identity",
			"target_relay":   targetURL,
			"via_home_relay": useViaHomeRelay,
		}
		return writeOutput(stdout, *jsonOut, output, fmt.Sprintf("sent encrypted message to %s (id=%s, signed with identity key, tick 1 ✓)\n", msg.Recipient, messageID))
	}

	// Session-signed path. Sessions are registered with the home relay
	// (which always knows our identity); we attach a SessionProof so the
	// recipient relay (which has no prior session state) can verify too.
	sessionRelayURL := cfg.RelayURL
	if sessionRelayURL == "" {
		sessionRelayURL = targetURL
	}
	sess, err := ensureSession(context.Background(), sessionRelayURL, identityValue, identityPriv)
	if err != nil {
		recordSendFailure(identityValue, messageID, recipient, useViaHomeRelay, "session error: "+err.Error())
		fmt.Fprintln(stderr, "session error:", err)
		return 1
	}

	sessionPriv, err := sess.PrivateKey()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	msg := Message{
		ID:           messageID,
		Sender:       identityValue,
		Recipient:    recipient,
		Timestamp:    timestamp,
		Payload:      payloadString,
		Type:         *msgType,
		ThreadID:     *threadID,
		ExpiresAt:    *expiresAt,
		Metadata:     meta.Map(),
		SessionID:    sess.SessionID,
		SessionProof: sessionProofFrom(sess),
		Encryption:   encMeta,
	}
	msg.Signature = signMessage(sessionPriv, msg)

	resp, err := SendMessage(context.Background(), targetURL, msg)
	if err != nil {
		recordSendFailure(identityValue, messageID, recipient, useViaHomeRelay, err.Error())
		fmt.Fprintln(stderr, err)
		return 1
	}
	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		if _, err := session.Load(identityValue); err == nil {
			_ = session.Delete(identityValue)
		}
		sess, err = ensureSession(context.Background(), sessionRelayURL, identityValue, identityPriv)
		if err != nil {
			recordSendFailure(identityValue, messageID, recipient, useViaHomeRelay, "session error (retry): "+err.Error())
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
		resp, err = SendMessage(context.Background(), targetURL, msg)
		if err != nil {
			recordSendFailure(identityValue, messageID, recipient, useViaHomeRelay, err.Error())
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		recordSendFailure(identityValue, messageID, recipient, useViaHomeRelay,
			fmt.Sprintf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body))))
		fmt.Fprintf(stderr, "relay rejected message (%d): %s\n", resp.StatusCode, strings.TrimSpace(string(body)))
		// E07-T3: a `policy_rejected` refusal is an invitation to ask.
		if offerContactRequest(contactRequestOffer{
			recipient: recipient, plaintext: plaintext, msgType: *msgType,
			useIdentity: identityValue, status: resp.StatusCode, body: body,
			auto: *requestOnReject, jsonOut: *jsonOut,
		}, stdout, stderr) {
			return 0
		}
		return 1
	}

	recordTick1(identityValue, messageID, recipient, useViaHomeRelay)

	archiveRecords(*useIdentity, []idpkg.HistoryRecord{historyRecordThreaded(
		identityValue, idpkg.HistoryQueueSent, messageID, identityValue,
		recipient, timestamp, *msgType, *threadID, plaintext)}, stderr)

	output := map[string]any{
		"id":             messageID,
		"status":         resp.StatusCode,
		"message":        msg,
		"encrypted":      true,
		"sign_with":      "session",
		"target_relay":   targetURL,
		"via_home_relay": useViaHomeRelay,
	}
	return writeOutput(stdout, *jsonOut, output, fmt.Sprintf("sent encrypted message to %s (id=%s, tick 1 ✓)\n", msg.Recipient, messageID))
}

// resolveRecipientRelayURL looks up the recipient identity in DNS and
// returns the URL to POST messages directly to that relay. Reuses the
// home relay's scheme by default (http for tests, https for prod) since
// scheme is not carried in DNS records.
func resolveRecipientRelayURL(ctx context.Context, recipient string, cfg config.Config) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	dns, err := identity.LookupDNS(ctx, recipient)
	if err != nil {
		return "", err
	}
	var host string
	if len(dns.RelayHosts) > 0 {
		host = dns.RelayHosts[0]
	} else if dns.CNAME != "" {
		host = strings.TrimSuffix(dns.CNAME, ".")
	}
	if host == "" {
		return "", errors.New("recipient relay host not resolvable")
	}
	scheme := schemeFromConfig(cfg)
	return scheme + "://" + host, nil
}

// schemeFromConfig peels the http/https scheme off cfg.RelayURL so direct
// sends use the same protocol as the home relay (works for both http
// integration tests and https production).
func schemeFromConfig(cfg config.Config) string {
	if strings.HasPrefix(cfg.RelayURL, "http://") {
		return "http"
	}
	return "https"
}

// recordTick1 marks an outbound message as delivered_recipient_relay (HTTP
// 202 from the recipient relay was the trigger). Best-effort: a journal
// write failure is logged but never aborts the send pipeline.
func recordTick1(sender, messageID, recipient string, viaHomeRelay bool) {
	_ = journal.Append(journal.Entry{
		MessageID:    messageID,
		Sender:       sender,
		Recipient:    recipient,
		Timestamp:    time.Now().UTC(),
		State:        journal.StateDeliveredRecipientRelay,
		ViaHomeRelay: viaHomeRelay,
	})
}

// recordSendFailure marks a message as failed. Used for any non-2xx
// status the recipient relay returns (and for transport errors before we
// even got a response).
func recordSendFailure(sender, messageID, recipient string, viaHomeRelay bool, detail string) {
	_ = journal.Append(journal.Entry{
		MessageID:    messageID,
		Sender:       sender,
		Recipient:    recipient,
		Timestamp:    time.Now().UTC(),
		State:        journal.StateFailed,
		Detail:       detail,
		ViaHomeRelay: viaHomeRelay,
	})
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

	// Printing, decrypting, acking and archiving the pickup is shared with
	// `poweur listen` (EPIC-009 E09-T2), which drains the same way when the
	// push stream says something arrived.
	// An empty inbox is not an error, so the "did anything arrive" answer is
	// only of interest to `poweur listen --once`.
	renderInboxPayload(payload, inboxRender{
		cfg:          cfg,
		identity:     identityValue,
		useIdentity:  *useIdentity,
		identityPriv: identityPriv,
		session:      sess,
		jsonOut:      *jsonOut,
	}, stdout, stderr)
	return 0
}

// applyInboundAck advances the local pending journal when an ack arrives
// for an outbound message we previously sent. We keep this best-effort:
// an unknown message_id is silently ignored (could be from a different
// device or a client-side journal that was reset).
func applyInboundAck(localIdentity string, ack Ack) {
	if ack.State != AckStateDeliveredClient || ack.MessageID == "" {
		return
	}
	_ = journal.Append(journal.Entry{
		MessageID: ack.MessageID,
		Sender:    localIdentity,
		Recipient: ack.Sender,
		Timestamp: time.Now().UTC(),
		State:     journal.StateDeliveredClient,
		Detail:    "ack id=" + ack.ID,
	})
}

// emitDeliveredClientAck builds, signs, and POSTs a delivered_client ack
// to the original sender's home relay. The relay is resolved via DNS so
// this works whether the original message came directly or was forwarded
// through the sender's home relay (privacy proxy mode).
//
// Signing prefers the active session key (cheap, common path); when no
// session is loaded we fall back to the long-lived identity key. The
// canonical layout is identical in both cases (matches relay's
// crypto.CanonicalAck).
func emitDeliveredClientAck(ctx context.Context, cfg config.Config, localIdentity string, identityPriv ed25519.PrivateKey, messageID, originalSender, originalRecipient string, sess session.Session) error {
	if messageID == "" || originalSender == "" {
		return errors.New("ack requires message id and original sender")
	}

	senderRelay, err := resolveRecipientRelayURL(ctx, originalSender, cfg)
	if err != nil {
		return fmt.Errorf("resolve sender relay: %w", err)
	}

	ack := Ack{
		Type:      AckTypeDeliveryAck,
		ID:        identity.NewAckID(),
		MessageID: messageID,
		State:     AckStateDeliveredClient,
		Sender:    originalRecipient,
		Recipient: originalSender,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	if sess.IsValid() {
		sessionPriv, perr := sess.PrivateKey()
		if perr == nil {
			ack.SessionID = sess.SessionID
			ack.SessionProof = sessionProofFrom(sess)
			ack.Signature = signAck(sessionPriv, ack)
		}
	}
	if ack.Signature == "" {
		ack.Signature = signAck(identityPriv, ack)
	}

	resp, err := SendAck(ctx, senderRelay, ack)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("ack rejected (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// runMessages dispatches the `poweur messages …` subcommands.
func runMessages(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "messages subcommand required: status")
		return 1
	}
	switch args[0] {
	case "status":
		return runMessagesStatus(args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, "unknown messages subcommand")
		return 1
	}
}

// runMessagesStatus prints the local pending journal — every outbound
// message and its current tick state. Filters by --id when supplied.
// `--json` renders the full collapsed view (including transition history)
// for piping into other tools.
func runMessagesStatus(args []string, stdout, stderr io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fs := flag.NewFlagSet("messages status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "override identity for this command")
	idFilter := fs.String("id", "", "filter to a single message id")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	identityValue := resolveIdentity(*useIdentity, cfg.Identity)
	if identityValue == "" {
		fmt.Fprintln(stderr, "identity not configured")
		return 1
	}

	statuses, err := journal.Statuses(identityValue)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *idFilter != "" {
		filtered := make([]journal.Status, 0, 1)
		for _, s := range statuses {
			if s.MessageID == *idFilter {
				filtered = append(filtered, s)
			}
		}
		statuses = filtered
	}

	if *jsonOut {
		encoded, err := json.MarshalIndent(statuses, "", "  ")
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintln(stdout, string(encoded))
		return 0
	}

	if len(statuses) == 0 {
		fmt.Fprintln(stdout, "no pending messages")
		return 0
	}
	for _, s := range statuses {
		fmt.Fprintf(stdout, "%s  %s  to=%s  state=%s\n", tickGlyph(s.State), s.MessageID, s.Recipient, s.State)
	}
	return 0
}

// tickGlyph maps journal states to a compact terminal-friendly indicator,
// matching the WhatsApp metaphor: ✓ = relay accepted, ✓✓ = client decrypted.
func tickGlyph(state journal.State) string {
	switch state {
	case journal.StateQueued:
		return " · "
	case journal.StateDeliveredHomeRelay:
		return " ✓ "
	case journal.StateDeliveredRecipientRelay:
		return " ✓ "
	case journal.StateDeliveredClient:
		return " ✓✓"
	case journal.StateFailed:
		return " ✗ "
	}
	return "   "
}

func runRelay(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: poweur relay status|set <url>")
		return 1
	}
	switch args[0] {
	case "status":
		return runRelayStatus(args[1:], stdout, stderr)
	case "set":
		return runRelaySet(args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, "usage: poweur relay status|set <url>")
		return 1
	}
}

func runRelayStatus(args []string, stdout, stderr io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fs := flag.NewFlagSet("relay status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "override identity for this command")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
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
	output := map[string]any{
		"status":  health.Status,
		"version": health.Version,
	}
	if health.BuildTime != "" {
		output["buildTime"] = health.BuildTime
	}
	if health.VersionHash != "" {
		output["versionHash"] = health.VersionHash
	}
	if health.Storage != nil {
		output["storage"] = health.Storage
	}
	line := fmt.Sprintf("relay %s (version %s)\n", health.Status, health.Version)
	if health.BuildTime != "" {
		line = fmt.Sprintf("relay %s (version %s, %s)\n", health.Status, health.Version, health.BuildTime)
	}
	return writeOutput(stdout, *jsonOut, output, line)
}

func runRelaySet(args []string, stdout, stderr io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fs := flag.NewFlagSet("relay set", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(stderr, "usage: poweur relay set <url>")
		return 1
	}
	cfg.RelayURL = strings.TrimRight(fs.Arg(0), "/")
	if err := config.Save(cfg); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "relay url set to %s\n", cfg.RelayURL)
	fmt.Fprintln(stdout, "note: self-hosted IDs should also update id.json relay field and DNS A/CNAME")
	return 0
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

// runSessionRevoke deletes the local session record and (when a relay
// session_id is known) issues an identity-signed DELETE /sessions/:id to
// the home relay so the relay drops its server-side state too. Relay
// failures don't block local deletion; the local cache is the source of
// truth for `poweur session status`.
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

	relayRevoked := false
	if existing, err := session.Load(identityValue); err == nil && existing.SessionID != "" && cfg.RelayURL != "" && cfg.KeysDir != "" {
		identityPriv, perr := identity.LoadPrivateKey(identity.KeyPath(cfg.KeysDir, identityValue))
		if perr == nil {
			issuedAt := time.Now().UTC().Format(time.RFC3339)
			nonce := newAdminNonce()
			req := SessionRevokeRequest{
				Identity:          identityValue,
				IssuedAt:          issuedAt,
				Nonce:             nonce,
				IdentitySignature: signSessionRevocation(identityPriv, identityValue, existing.SessionID, issuedAt, nonce),
			}
			if rerr := RevokeSession(context.Background(), cfg.RelayURL, existing.SessionID, req); rerr != nil {
				fmt.Fprintf(stderr, "warning: relay session revoke failed: %v\n", rerr)
			} else {
				relayRevoked = true
			}
		}
	}

	if err := session.Delete(identityValue); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	output := map[string]any{
		"identity":      identityValue,
		"relay_revoked": relayRevoked,
	}
	return writeOutput(stdout, *jsonOut, output, fmt.Sprintf("session revoked for %s\n", identityValue))
}

func runAuth(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "auth subcommand required")
		return 1
	}
	switch args[0] {
	case "approve":
		return runAuthApprove(args[1:], stdout, stderr)
	case "inspect":
		return runAuthInspect(args[1:], stdout, stderr)
	case "sign":
		return runAuthSign(args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, "unknown auth subcommand (want approve, inspect, sign)")
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
// message, including session id and encryption metadata when present. The
// caller chooses which Ed25519 private key to pass: the short-lived session
// key (normal path, SessionID is set) or the long-lived identity key
// (headless/opt-out path, SessionID is empty).
//
// The optional lines are appended in a fixed order and only when the field is
// set (crypto.CanonicalMessageEnvelope on the relay, canonicalMessage() in
// @poweur/client): id, session, enc, type, thread, expires, meta:<key> in
// ascending key order.
func signMessage(priv ed25519.PrivateKey, msg Message) string {
	parts := []string{msg.Sender, msg.Recipient, msg.Timestamp, msg.Payload}
	if msg.ID != "" {
		parts = append(parts, "id:"+msg.ID)
	}
	if msg.SessionID != "" {
		parts = append(parts, "session:"+msg.SessionID)
	}
	if msg.Encryption != nil && msg.Encryption.Alg != "" {
		parts = append(parts, "enc:"+msg.Encryption.Alg+":"+msg.Encryption.EphemeralPublicKey+":"+msg.Encryption.Nonce)
	}
	if msg.Type != "" {
		parts = append(parts, "type:"+msg.Type)
	}
	if msg.ThreadID != "" {
		parts = append(parts, "thread:"+msg.ThreadID)
	}
	if msg.ExpiresAt != "" {
		parts = append(parts, "expires:"+msg.ExpiresAt)
	}
	parts = append(parts, idpkg.MetadataLines(msg.Metadata)...)
	canonical := strings.Join(parts, "\n")
	sig := ed25519.Sign(priv, []byte(canonical))
	return base64.StdEncoding.EncodeToString(sig)
}

// signIdentityRegistration builds the canonical signing string for a
// POST /identities request. Mirrors crypto.CanonicalIdentityRegistration
// on the relay so the owner-only check can be recomputed by any verifier.
// The relay address is bound into the canonical so a captured signature
// cannot be replayed against a different relay.
func signIdentityRegistration(priv ed25519.PrivateKey, identityValue, publicKey, encPublicKey, relayAddress, issuedAt, nonce string) string {
	parts := []string{
		"identity-registration",
		identityValue,
		publicKey,
		encPublicKey,
		relayAddress,
		issuedAt,
		nonce,
	}
	canonical := strings.Join(parts, "\n")
	sig := ed25519.Sign(priv, []byte(canonical))
	return base64.StdEncoding.EncodeToString(sig)
}

func buildSignedIdentityDocument(priv ed25519.PrivateKey, identityValue, publicKey, encPublicKey, relayAddress, updatedAt string) (json.RawMessage, error) {
	encFmt := ""
	if encPublicKey != "" {
		encFmt = "x25519:" + encPublicKey
		if strings.HasPrefix(encPublicKey, "x25519:") {
			encFmt = encPublicKey
		}
	}
	pubFmt := publicKey
	if !strings.HasPrefix(pubFmt, "ed25519:") {
		pubFmt = "ed25519:" + pubFmt
	}
	doc := idpkg.NewDocument(identityValue, pubFmt, encFmt, relayAddress, nil)
	doc.UpdatedAt = updatedAt
	if err := doc.Sign(priv); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(raw), nil
}

func runIdentityLookup(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("identity lookup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(stderr, "usage: poweur identity lookup <identity>")
		return 1
	}
	identityValue := fs.Arg(0)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := identity.ResolveIdentity(ctx, identityValue)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	// The identity document answers "which keys"; profile.json and
	// capabilities.json answer "who" and "what do they speak" (E06-T2).
	// Both are optional and live on a host we do not control, so they are
	// fetched best-effort: a lookup that can verify a stranger's keys but
	// cannot show the user their name is a lookup that stops half way.
	profile, capabilities, profileNote := lookupPublicFiles(ctx, res)

	out := map[string]any{
		"identity":              res.Document.Identity,
		"source":                res.Source,
		"public_key":            res.Document.PublicKey,
		"encryption_public_key": res.Document.EncryptionPublicKey,
		"relay":                 res.Document.Relay,
		// `capabilities` stays the identity document's own string list (its
		// wire shape is a contract); `capabilities_document` is the richer
		// capabilities.json when the identity publishes one.
		"capabilities":          res.Document.Capabilities,
		"capabilities_document": capabilities,
		"profile":               profile,
		"fingerprint":           idpkg.FingerprintOrKey(res.Document.PublicKey),
	}
	if *jsonOut {
		return writeOutput(stdout, true, out, "")
	}
	fmt.Fprintf(stdout, "identity: %s\n", res.Document.Identity)
	fmt.Fprintf(stdout, "source: %s\n", res.Source)
	fmt.Fprintf(stdout, "public_key: %s\n", res.Document.PublicKey)
	// The safety number (E07-T4) is the form a human can compare out of band.
	fmt.Fprintf(stdout, "safety number: %s\n", idpkg.FingerprintOrKey(res.Document.PublicKey))
	fmt.Fprintf(stdout, "encryption_public_key: %s\n", res.Document.EncryptionPublicKey)
	fmt.Fprintf(stdout, "relay: %s\n", res.Document.Relay)
	printProfile(stdout, res.Document.Identity, profile)
	printCapabilities(stdout, capabilities)
	if profileNote != "" {
		fmt.Fprintln(stderr, profileNote)
	}
	return 0
}

// relayAddressFromURL extracts the host[:port] portion of a relay URL.
// Used for owner-only canonical signing; must match cfg.RelayAddress on
// the relay side. The port is preserved when present (matters for local
// httptest setups; in production the relay address is the bare hostname).
func relayAddressFromURL(relayURL string) string {
	trimmed := strings.TrimPrefix(relayURL, "https://")
	trimmed = strings.TrimPrefix(trimmed, "http://")
	if i := strings.Index(trimmed, "/"); i >= 0 {
		trimmed = trimmed[:i]
	}
	return trimmed
}

// signEncryptionKeyUpdate matches crypto.CanonicalEncryptionKeyUpdate on
// the relay; covers both initial publish and rotation flows since the
// payload shape is identical.
func signEncryptionKeyUpdate(priv ed25519.PrivateKey, identityValue, encPublicKey, issuedAt, nonce string) string {
	parts := []string{
		"identity-encryption-key",
		identityValue,
		encPublicKey,
		issuedAt,
		nonce,
	}
	canonical := strings.Join(parts, "\n")
	sig := ed25519.Sign(priv, []byte(canonical))
	return base64.StdEncoding.EncodeToString(sig)
}

// signSessionRevocation matches crypto.CanonicalSessionRevocation on the
// relay. Used by the (admin-only) DELETE /sessions/:id call.
func signSessionRevocation(priv ed25519.PrivateKey, identityValue, sessionID, issuedAt, nonce string) string {
	parts := []string{
		"session-revocation",
		identityValue,
		sessionID,
		issuedAt,
		nonce,
	}
	canonical := strings.Join(parts, "\n")
	sig := ed25519.Sign(priv, []byte(canonical))
	return base64.StdEncoding.EncodeToString(sig)
}

// newAdminNonce returns a fresh URL-safe nonce for owner-only admin
// envelopes. 16 bytes of entropy is enough that collisions are
// unobservable across a relay's session lifetime; the relay also pins
// timestamp recency separately so replay windows are tiny anyway.
func newAdminNonce() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("nonce-%d", time.Now().UnixNano())
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

// signAck builds the canonical Ack signing string and produces an Ed25519
// signature. The canonical layout matches crypto.CanonicalAck on the relay
// so any verifier can recompute it from the wire fields.
func signAck(priv ed25519.PrivateKey, ack Ack) string {
	parts := []string{
		"ack",
		ack.ID,
		ack.MessageID,
		ack.State,
		ack.Sender,
		ack.Recipient,
		ack.Timestamp,
	}
	if ack.SessionID != "" {
		parts = append(parts, "session:"+ack.SessionID)
	}
	canonical := strings.Join(parts, "\n")
	sig := ed25519.Sign(priv, []byte(canonical))
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
  poweur identity create <name> [--dns-provider=cloudflare|hetzner] [--dns-token=...] [--parent-domain=...] [--relay=...] [--seed=<b64url>|--from-seed] [--json]
  poweur key recover <identity> --seed <base64url|mnemonic> [--relay=...] [--parent-domain=...] [--json]
  poweur key derive --seed <base64url|mnemonic> [--json]
  poweur key kit --seed <base64url|mnemonic> [--use-identity=...] [--json]
  poweur key ls [--use-identity=...] [--relay=...] [--json]
  poweur key enroll <identity> [--relay=...] [--label=...] [--wait]
  poweur key approve <rendezvous-id> [--use-identity=...] [--seed=<b64url|mnemonic>] [--sas=<digits>] [--json]
  poweur key claim <identity> <rendezvous-id> --ephemeral-key <b64url> [--relay=...] [--json]
  poweur key protect|unprotect [--use-identity=...] [--passphrase=...] [--json]
  poweur identity show [--use-identity=...] [--json]
  poweur identity dns <identity> [--use-identity=...] [--json]
  poweur identity use <identity> [--json]
  poweur identity list [--json]
  poweur identity add-encryption-key [<identity>] [--rotate] [--dns-provider=cloudflare|hetzner] [--dns-token=...] [--relay=...] [--json]
  poweur send <to> <message> [--sign-with=session|identity] [--use-identity=...] [--request-on-reject] [--json]
  poweur inbox [--use-identity=...] [--json]
  poweur listen [--use-identity=...] [--json] [--once]
  poweur devices show|name <name>|list|revoke <dev_...> [--use-identity=...] [--relay=...] [--json]
  poweur history [<peer>] [--keep-unread] [--use-identity=...] [--json]
  poweur session status [--use-identity=...] [--json]
  poweur session refresh [--use-identity=...] [--json]
  poweur session revoke [--use-identity=...] [--json]
  poweur relay status [--json]
  poweur dav token [--audience=...] [--scope=dav:full|dav:read|dav:rw:<path>] [--use-identity=...] [--json]
  poweur dav mount [--use-identity=...]
  poweur dav password add --name=<name> [--scope=...] [--use-identity=...] [--json]
  poweur dav password list [--use-identity=...] [--json]
  poweur dav password remove --name=<name> [--use-identity=...]
  poweur sync <pull|push|run|status> <local-dir> [--path=<prefix> ...] [--audience=...] [--use-identity=...]
  poweur share add <path> --with=<id> [--with-group=<name>] [--perm=read|rw] [--expires=<rfc3339>] [--json]
  poweur share ls [--json]
  poweur share revoke <share-id>
  poweur share group set <name> --members=<id,id,...> [--json]
  poweur share group ls [--json]
  poweur share group remove <name>
  poweur share link add <path> [--password=... | --password-stdin] [--expires=<rfc3339>] [--max-downloads=N] [--json]
  poweur share link ls [--json]      (revoke with: poweur share revoke <share-id>)
  poweur group create <group-id> [--admin=<id> ...] [--member=<id> ...] [--json]
  poweur group show <group-id> [--json]
  poweur group add <group-id> [--member=<id> ...] [--admin=<id> ...] [--json]
  poweur group remove <group-id> [--member=<id> ...] [--admin=<id> ...] [--json]
  poweur contacts <ls|add|request|accept|block|rm> [<identity>] [--petname=...] [--use-identity=...]
  poweur requests [--use-identity=...] [--json]
  poweur analytics <show|on|off> [--use-identity=...] [--json]
  poweur blocks export [--name=...] [--out=<file>] [--no-publish] [--use-identity=...] [--json]
  poweur blocks import <publisher>|--file=<path> [--path=...] [--force] [--dry-run] [--use-identity=...]
  poweur report <identity> [--reason=spam|harassment|phishing|malware|impersonation|other] [--note=...] [--message-ids=id,id]
  poweur policy <show|set open|contacts_only|contacts_and_requests> [--anon-allow=true|false] [--anon-challenge=none|pow] [--anon-bits=N] [--use-identity=...]
  poweur send <to> <message> --anon      (unsigned; recipient must allow anonymous senders)
  poweur anon [--use-identity=...] [--json]      (read your anonymous queue)
  poweur auth inspect <request-file-or-url> [--json]
  poweur auth sign <request-file-or-url> [--use-identity=...] [--json]
  poweur version
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

func runIdentityExport(args []string, stdout, stderr io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fs := flag.NewFlagSet("identity export", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "identity to export")
	outPath := fs.String("out", "", "output .tar.gz path (default: <identity>.tar.gz)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	identityValue := resolveIdentity(*useIdentity, cfg.Identity)
	if identityValue == "" {
		fmt.Fprintln(stderr, "identity not configured")
		return 1
	}
	if cfg.RelayURL == "" {
		fmt.Fprintln(stderr, "relay url not configured")
		return 1
	}
	priv, err := identity.LoadPrivateKey(identity.KeyPath(cfg.KeysDir, identityValue))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	issuedAt := time.Now().UTC().Format(time.RFC3339)
	nonce := newAdminNonce()
	canonical := strings.Join([]string{"identity-export", identityValue, issuedAt, nonce}, "\n")
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(canonical)))
	raw, err := ExportIdentity(context.Background(), cfg.RelayURL, identityValue, IdentityExportRequest{
		IssuedAt: issuedAt, Nonce: nonce, IdentitySignature: sig,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	path := *outPath
	if path == "" {
		path = identityValue + ".tar.gz"
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "exported %s (%d bytes)\n", path, len(raw))
	return 0
}

func runKey(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: poweur key <rotate|recover|derive>")
		return 1
	}
	switch args[0] {
	case "rotate":
		return runKeyRotate(args[1:], stdout, stderr)
	case "recover":
		return runKeyRecover(args[1:], stdout, stderr)
	case "derive":
		return runKeyDerive(args[1:], stdout, stderr)
	case "kit":
		return runKeyKit(args[1:], stdout, stderr)
	case "ls", "list":
		return runKeyList(args[1:], stdout, stderr)
	case "enroll":
		return runKeyEnroll(args[1:], stdout, stderr)
	case "approve":
		return runKeyApprove(args[1:], stdout, stderr)
	case "claim":
		return runKeyClaim(args[1:], stdout, stderr)
	case "protect":
		return runKeyProtect(args[1:], stdout, stderr, true)
	case "unprotect":
		return runKeyProtect(args[1:], stdout, stderr, false)
	default:
		fmt.Fprintln(stderr, "usage: poweur key <rotate|recover|derive|kit|ls|enroll|approve|claim|protect|unprotect>")
		return 1
	}
}

// runKeyProtect encrypts (or decrypts) an identity's key files at rest
// (EPIC-011 E11-T4). The CLI historically wrote plaintext base64 with mode
// 0600 — fine for a bot on a hardened host, thin for a laptop.
func runKeyProtect(args []string, stdout, stderr io.Writer, protect bool) int {
	verb := "protect"
	if !protect {
		verb = "unprotect"
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fs := flag.NewFlagSet("key "+verb, flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "identity whose keys to "+verb)
	passphraseFlag := fs.String("passphrase", "", "passphrase (prefer "+identity.EnvKeyPassphrase+")")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	identityValue := resolveIdentity(*useIdentity, cfg.Identity)
	if identityValue == "" {
		fmt.Fprintln(stderr, "identity not configured (run `poweur identity create` or pass --use-identity)")
		return 1
	}
	passphrase := identity.Passphrase(*passphraseFlag)
	if passphrase == "" {
		fmt.Fprintf(stderr, "passphrase required: set %s or pass --passphrase\n", identity.EnvKeyPassphrase)
		return 1
	}
	keysDir := cfg.KeysDir
	if keysDir == "" {
		if keysDir, err = config.KeysDir(); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	changed := map[string]bool{}
	for _, suffix := range []string{".key", ".enc"} {
		path := filepath.Join(keysDir, identityValue+suffix)
		if _, statErr := os.Stat(path); statErr != nil {
			continue
		}
		var did bool
		if protect {
			did, err = identity.ProtectKeyFile(path, passphrase)
		} else {
			did, err = identity.UnprotectKeyFile(path, passphrase)
		}
		if err != nil {
			fmt.Fprintf(stderr, "%s %s: %v\n", verb, path, err)
			return 1
		}
		changed[path] = did
	}
	if len(changed) == 0 {
		fmt.Fprintf(stderr, "no key files found for %s in %s\n", identityValue, keysDir)
		return 1
	}
	output := map[string]any{"identity": identityValue, "files": changed, "protected": protect}
	human := fmt.Sprintf("%sed key files for %s\n", verb, identityValue)
	if protect {
		human += "keep the passphrase safe: without it these keys cannot be loaded\n"
	}
	return writeOutput(stdout, *jsonOut, output, human)
}

// runKeyKit renders a master seed as a recovery kit: 24 BIP39 words for paper
// (checksummed, so a transcription slip is caught) plus the base64url seed for
// machines. Offline — it converts, it does not generate or store.
func runKeyKit(args []string, stdout, stderr io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fs := flag.NewFlagSet("key kit", flag.ContinueOnError)
	fs.SetOutput(stderr)
	seedFlag := fs.String("seed", "", "master seed (base64url or 24-word mnemonic)")
	useIdentity := fs.String("use-identity", "", "identity the kit is for")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	if *seedFlag == "" {
		fmt.Fprintln(stderr, "--seed is required")
		return 1
	}
	seed, err := identity.ParseSeed(*seedFlag)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	identityValue := resolveIdentity(*useIdentity, cfg.Identity)
	kit, err := identity.NewRecoveryKit(identityValue, cfg.RelayURL, seed)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	output := map[string]any{
		"identity": kit.Identity,
		"relay":    kit.Relay,
		"mnemonic": kit.Mnemonic,
		"seed":     kit.Seed,
	}
	human := fmt.Sprintf("Recovery kit for %s\n\nrelay:    %s\nseed:     %s\n\nmnemonic: %s\n\n"+
		"Anyone holding this can act as %s. Store it offline.\n",
		kit.Identity, kit.Relay, kit.Seed, kit.Mnemonic, kit.Identity)
	return writeOutput(stdout, *jsonOut, output, human)
}

// runKeyList shows every enrollment registered for an identity — the CLI half
// of the "Keys & devices" inventory. Metadata only: listing your devices needs
// no access to the wrapped seed copies.
func runKeyList(args []string, stdout, stderr io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fs := flag.NewFlagSet("key ls", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "identity to list")
	relayURL := fs.String("relay", cfg.RelayURL, "relay base url")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	identityValue := resolveIdentity(*useIdentity, cfg.Identity)
	if identityValue == "" || *relayURL == "" {
		fmt.Fprintln(stderr, "identity and relay url required")
		return 1
	}
	cfg.Identity = identityValue
	_, _, priv, ok := loadIdentityForDAV(*useIdentity, stderr)
	if !ok {
		return 1
	}
	issuedAt := time.Now().UTC().Format(time.RFC3339)
	nonce := newAdminNonce()
	// Canonical strings are built inline throughout this package; keep
	// "keystore-list" in step with crypto.CanonicalKeystoreList on the relay.
	canonical := strings.Join([]string{
		"keystore-list", strings.ToLower(identityValue), issuedAt, nonce,
	}, "\n")
	payload, err := json.Marshal(map[string]any{
		"issued_at":          issuedAt,
		"nonce":              nonce,
		"identity_signature": base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, []byte(canonical))),
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	url := strings.TrimRight(*relayURL, "/") + "/identities/" + identityValue + "/keystore/list"
	httpReq, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpResp, err := (&http.Client{Timeout: 10 * time.Second}).Do(httpReq)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusOK {
		fmt.Fprintln(stderr, parseErrorResponse("keystore list failed", httpResp))
		return 1
	}
	var resp struct {
		Enrollments []map[string]any `json:"enrollments"`
	}
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *jsonOut {
		return writeOutput(stdout, true, map[string]any{"enrollments": resp.Enrollments}, "")
	}
	if len(resp.Enrollments) == 0 {
		fmt.Fprintln(stdout, "no enrollments registered")
		fmt.Fprintln(stdout, "this identity can only be recovered from its seed — keep the kit safe")
		return 0
	}
	for _, e := range resp.Enrollments {
		role, _ := e["role"].(string)
		if role == "" {
			role = "device"
		}
		label, _ := e["label"].(string)
		if label == "" {
			label = "(unlabelled)"
		}
		lastUsed, _ := e["last_used_at"].(string)
		if lastUsed == "" {
			lastUsed = "never"
		}
		fmt.Fprintf(stdout, "%-16s %-14s %-16s %-10s created=%v last-used=%s\n",
			e["enrollment_id"], e["kind"], label, role, e["created_at"], lastUsed)
	}
	return 0
}

// runKeyRecover rebuilds an identity's key files from its master seed
// (EPIC-011 E11-T1). Offline by design: no relay call, no network. The relay
// already holds the public half, so restoring the private half locally is all
// that is needed to use the identity again.
func runKeyRecover(args []string, stdout, stderr io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fs := flag.NewFlagSet("key recover", flag.ContinueOnError)
	fs.SetOutput(stderr)
	seedFlag := fs.String("seed", "", "base64url master seed")
	// Recovery usually happens on a machine with no config at all, so the
	// relay cannot be assumed to already be known.
	relayURL := fs.String("relay", cfg.RelayURL, "relay base url")
	parentDomain := fs.String("parent-domain", cfg.ParentDomain, "parent domain")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(stderr, "usage: poweur key recover <identity> --seed <base64url>")
		return 1
	}
	identityValue := fs.Arg(0)
	if *seedFlag == "" {
		fmt.Fprintln(stderr, "--seed is required")
		return 1
	}
	seed, err := identity.ParseSeed(*seedFlag)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	keyPath, encKeyPath, err := identity.SaveKeysFromSeed(identityValue, seed)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	pub, _, err := identity.KeypairFromSeed(seed)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	encPub, _, err := identity.EncryptionKeypairFromSeed(seed)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
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
		"public_key":            identity.PublicKeyString(pub),
		"encryption_public_key": cryptoe2e.EncodePublicKey(encPub),
		"key_path":              keyPath,
		"encryption_key_path":   encKeyPath,
		"relay":                 *relayURL,
	}
	return writeOutput(stdout, *jsonOut, output,
		fmt.Sprintf("recovered keys for %s from seed\n", identityValue))
}

// runKeyDerive prints the public keys a seed derives without writing anything.
// Lets a holder check a recovery kit against a published identity document
// before trusting it — and gives tests a pure function to assert on.
func runKeyDerive(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("key derive", flag.ContinueOnError)
	fs.SetOutput(stderr)
	seedFlag := fs.String("seed", "", "base64url master seed")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	if *seedFlag == "" {
		fmt.Fprintln(stderr, "--seed is required")
		return 1
	}
	seed, err := identity.ParseSeed(*seedFlag)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	pub, _, err := identity.KeypairFromSeed(seed)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	encPub, _, err := identity.EncryptionKeypairFromSeed(seed)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	output := map[string]any{
		"public_key":            identity.PublicKeyString(pub),
		"encryption_public_key": cryptoe2e.EncodePublicKey(encPub),
	}
	return writeOutput(stdout, *jsonOut, output, fmt.Sprintf("%s\n%s\n",
		identity.PublicKeyString(pub), cryptoe2e.EncodePublicKey(encPub)))
}

func runKeyRotate(args []string, stdout, stderr io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fs := flag.NewFlagSet("key rotate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "identity to rotate")
	grace := fs.Duration("grace", 7*24*time.Hour, "previous key grace period")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	identityValue := resolveIdentity(*useIdentity, cfg.Identity)
	if identityValue == "" || cfg.RelayURL == "" {
		fmt.Fprintln(stderr, "identity and relay url required")
		return 1
	}
	oldPriv, err := identity.LoadPrivateKey(identity.KeyPath(cfg.KeysDir, identityValue))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	oldPub := oldPriv.Public().(ed25519.PublicKey)
	oldPubStr := identity.PublicKeyString(oldPub)

	newPub, newPriv, err := identity.GenerateKeypair()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	newPubStr := identity.PublicKeyString(newPub)
	encPubStr := ""
	if encPriv, err := identity.LoadEncryptionPrivateKey(identity.EncryptionKeyPath(cfg.KeysDir, identityValue)); err == nil {
		if encPub, err := publicFromPrivateX25519(encPriv); err == nil {
			encPubStr = cryptoe2e.EncodePublicKey(encPub)
		}
	}

	issuedAt := time.Now().UTC().Format(time.RFC3339)
	nonce := newAdminNonce()
	relayAddr := relayAddressFromURL(cfg.RelayURL)
	validUntil := time.Now().UTC().Add(*grace).Format(time.RFC3339)

	encFmt := ""
	if encPubStr != "" {
		encFmt = "x25519:" + encPubStr
	}
	doc := idpkg.NewDocument(identityValue, "ed25519:"+newPubStr, encFmt, relayAddr, nil)
	doc.UpdatedAt = issuedAt
	doc.PreviousKeys = []idpkg.PreviousKey{{
		PublicKey:  "ed25519:" + oldPubStr,
		ValidUntil: validUntil,
	}}
	if err := doc.Sign(newPriv); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	docRaw, err := json.Marshal(doc)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	rotCanon := strings.Join([]string{
		"identity-rotation", identityValue, oldPubStr, newPubStr, issuedAt, nonce,
	}, "\n")
	rotSig := base64.StdEncoding.EncodeToString(ed25519.Sign(oldPriv, []byte(rotCanon)))

	resp, err := RotateIdentity(context.Background(), cfg.RelayURL, identityValue, IdentityRotateRequest{
		IdentityDocument:    docRaw,
		NewPublicKey:        newPubStr,
		EncryptionPublicKey: encPubStr,
		IssuedAt:            issuedAt,
		Nonce:               nonce,
		RotationSignature:   rotSig,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	// Backup old key, then overwrite with new key material.
	oldPath := identity.KeyPath(cfg.KeysDir, identityValue)
	_ = os.Rename(oldPath, oldPath+".pre-rotate")
	if _, err := identity.SavePrivateKey(identityValue, newPriv); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	out := map[string]any{
		"identity":     identityValue,
		"public_key":   resp.PublicKey,
		"previous_key": oldPubStr,
		"valid_until":  validUntil,
	}
	return writeOutput(stdout, *jsonOut, out, fmt.Sprintf("rotated signing key for %s (old key valid until %s)\n", identityValue, validUntil))
}
