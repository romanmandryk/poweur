// Command poweur-oauth runs the Poweur OAuth 2.0 / OpenID Connect bridge
// (EPIC-022) and its operator commands.
//
//	poweur-oauth [serve]                  run the bridge
//	poweur-oauth keys list|rotate         inspect or rotate issuer signing keys
//	poweur-oauth clients list             list registered clients
//	poweur-oauth clients suspend <id> [reason]
//	poweur-oauth clients unsuspend <id>
//	poweur-oauth prune                    delete expired state now
//	poweur-oauth gen-key                  print a new OAUTH_KEY_ENCRYPTION_KEY
//	poweur-oauth hash-secret < secret     print client_secret_sha256 for a static client
//	poweur-oauth version
//
// Configuration is environment variables; see apps/oauth/README.md.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/poweur/identity"
	"github.com/poweur/identity/signin"
	"github.com/poweur/oauth/bridge"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	cmd := "serve"
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "version", "--version", "-v":
		fmt.Fprintf(stdout, "poweur-oauth %s\n", bridge.Version)
		return 0
	case "gen-key":
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintln(stdout, base64.StdEncoding.EncodeToString(key))
		return 0
	case "hash-secret":
		raw, err := io.ReadAll(io.LimitReader(stdin, 4096))
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		secret := strings.TrimRight(string(raw), "\r\n")
		if len(secret) < 16 {
			fmt.Fprintln(stderr, "a client secret must be at least 16 characters")
			return 1
		}
		fmt.Fprintln(stdout, signin.HashSecret(secret))
		return 0
	case "serve", "keys", "clients", "prune":
	default:
		fmt.Fprintf(stderr, "unknown command %q (want serve, keys, clients, prune, gen-key, hash-secret, version)\n", cmd)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log := slog.New(slog.NewJSONHandler(stderr, nil))
	cfg, err := configFromEnv(ctx, log)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer cfg.Store.Close()
	srv, err := bridge.New(ctx, cfg)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	switch cmd {
	case "serve":
		return serve(ctx, srv, log)
	case "keys":
		return keysCmd(ctx, srv, args, stdout, stderr)
	case "clients":
		return clientsCmd(ctx, cfg.Store, args, stdout, stderr)
	case "prune":
		if err := cfg.Store.Prune(ctx, cfg.Retention); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	return 0
}

func serve(ctx context.Context, srv *bridge.Server, log *slog.Logger) int {
	addr := env("OAUTH_ADDR", ":8090")
	hs := &http.Server{
		Addr:              addr,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 * 1024,
	}
	go srv.Run(ctx)
	errc := make(chan error, 1)
	go func() {
		log.Info("poweur-oauth listening", "addr", addr, "issuer", srv.Issuer(), "version", bridge.Version)
		errc <- hs.ListenAndServe()
	}()
	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Error("serve", "err", err)
			return 1
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = hs.Shutdown(shutdown)
	}
	return 0
}

func keysCmd(ctx context.Context, srv *bridge.Server, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: poweur-oauth keys list|rotate")
		return 2
	}
	switch args[0] {
	case "list":
	case "rotate":
		kid, err := srv.Keys().Rotate(ctx)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "new signing key %s; previous keys stay published for 24h\n", kid)
	default:
		fmt.Fprintln(stderr, "usage: poweur-oauth keys list|rotate")
		return 2
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "KID\tCREATED\tRETIRED\tPUBLISHED")
	for _, k := range srv.Keys().List() {
		retired := "-"
		if !k.RetiredAt.IsZero() {
			retired = k.RetiredAt.Format(time.RFC3339)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%v\n", k.KID, k.CreatedAt.Format(time.RFC3339), retired, k.Published)
	}
	_ = tw.Flush()
	return 0
}

func clientsCmd(ctx context.Context, store *bridge.Store, args []string, stdout, stderr io.Writer) int {
	usage := "usage: poweur-oauth clients list | suspend <client_id> [reason] | unsuspend <client_id>"
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	switch args[0] {
	case "list":
		clients, err := store.AllClients(ctx)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "CLIENT_ID\tNAME\tOWNERS\tSUSPENDED")
		for _, c := range clients {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%v\n", c.ID, c.Name, strings.Join(c.Owners, ","), c.Suspended)
		}
		_ = tw.Flush()
		return 0
	case "suspend", "unsuspend":
		if len(args) < 2 {
			fmt.Fprintln(stderr, usage)
			return 2
		}
		c, err := store.GetClient(ctx, args[1])
		if err != nil {
			fmt.Fprintf(stderr, "client %s: %v\n", args[1], err)
			return 1
		}
		c.Suspended = args[0] == "suspend"
		c.SuspendedReason = ""
		if c.Suspended && len(args) > 2 {
			c.SuspendedReason = strings.Join(args[2:], " ")
		}
		if err := store.PutClient(ctx, c); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		_ = store.Audit(ctx, "client."+args[0]+"ed", map[string]any{"client_id": c.ID, "by": "operator"})
		fmt.Fprintf(stdout, "%s %sed\n", c.ID, args[0])
		return 0
	default:
		fmt.Fprintln(stderr, usage)
		return 2
	}
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func csv(key string) []string {
	var out []string
	for _, v := range strings.Split(os.Getenv(key), ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func configFromEnv(ctx context.Context, log *slog.Logger) (bridge.Config, error) {
	issuer := env("OAUTH_ISSUER", "")
	if issuer == "" {
		return bridge.Config{}, errors.New("OAUTH_ISSUER is required, e.g. https://oauth.poweur.org")
	}
	kek, err := loadKEK()
	if err != nil {
		return bridge.Config{}, err
	}
	store, err := bridge.OpenStore(ctx, env("OAUTH_DATABASE", "oauth.db"))
	if err != nil {
		return bridge.Config{}, err
	}
	cfg := bridge.Config{
		Issuer:                issuer,
		Name:                  env("OAUTH_NAME", ""),
		Store:                 store,
		DefaultSigner:         env("OAUTH_DEFAULT_SIGNER", ""),
		KeyEncryptionKey:      kek,
		SubjectType:           env("OAUTH_SUBJECT_TYPE", ""),
		ClientRegistration:    env("OAUTH_CLIENT_REGISTRATION", ""),
		RegistrationAllowlist: csv("OAUTH_REGISTRATION_ALLOWLIST"),
		URLClients:            env("OAUTH_URL_CLIENTS", ""),
		ContactURI:            env("OAUTH_CONTACT_URI", ""),
		AbuseContact:          env("OAUTH_ABUSE_CONTACT", ""),
		TrustProxyHeaders:     os.Getenv("OAUTH_TRUST_PROXY") == "1",
		Logger:                log,
		ResolveOptions: identity.ResolveOptions{
			Scheme:       env("POWEUR_RESOLVER_SCHEME", "https"),
			AllowPrivate: os.Getenv("RESOLVER_ALLOW_PRIVATE") == "1",
		},
	}
	if dial := env("OAUTH_RESOLVER_DIAL", ""); dial != "" {
		// Local development: every identity host is served by one relay
		// (mirrors the CLI's POWEUR_RESOLVER_DIAL).
		if !cfg.ResolveOptions.AllowPrivate {
			store.Close()
			return bridge.Config{}, errors.New("OAUTH_RESOLVER_DIAL is for local development and needs RESOLVER_ALLOW_PRIVATE=1")
		}
		dialer := &net.Dialer{Timeout: 5 * time.Second}
		cfg.ResolveOptions.HTTPClient = &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, network, dial)
			}},
			CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirects are not followed") },
		}
	}
	if v := env("OAUTH_MAX_CLIENTS_PER_OWNER", ""); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			store.Close()
			return bridge.Config{}, errors.New("OAUTH_MAX_CLIENTS_PER_OWNER must be a positive integer")
		}
		cfg.MaxClientsPerOwner = n
	}
	for name, dst := range map[string]*int{
		"OAUTH_RATE_AUTHORIZE": &cfg.RateLimits.Authorize,
		"OAUTH_RATE_IDENTIFY":  &cfg.RateLimits.Identify,
		"OAUTH_RATE_CALLBACK":  &cfg.RateLimits.Callback,
		"OAUTH_RATE_TOKEN":     &cfg.RateLimits.Token,
		"OAUTH_RATE_CONSOLE":   &cfg.RateLimits.Console,
	} {
		if v := env(name, ""); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				store.Close()
				return bridge.Config{}, fmt.Errorf("%s must be an integer per minute (-1 disables)", name)
			}
			*dst = n
		}
	}
	if v := env("OAUTH_SESSION_TTL", ""); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			store.Close()
			return bridge.Config{}, errors.New("OAUTH_SESSION_TTL must be a duration such as 12h")
		}
		cfg.SessionTTL = d
	}
	if path := env("OAUTH_STATIC_CLIENTS", ""); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			store.Close()
			return bridge.Config{}, fmt.Errorf("OAUTH_STATIC_CLIENTS: %w", err)
		}
		var file struct {
			Clients []bridge.Client `json:"clients"`
		}
		if err := json.Unmarshal(raw, &file); err != nil {
			store.Close()
			return bridge.Config{}, fmt.Errorf("OAUTH_STATIC_CLIENTS: %w", err)
		}
		cfg.StaticClients = file.Clients
	}
	return cfg, nil
}

// loadKEK reads the 32-byte key that seals issuer signing keys at rest, from
// OAUTH_KEY_ENCRYPTION_KEY or the file named by OAUTH_KEY_ENCRYPTION_KEY_FILE.
func loadKEK() ([]byte, error) {
	raw := strings.TrimSpace(os.Getenv("OAUTH_KEY_ENCRYPTION_KEY"))
	if path := strings.TrimSpace(os.Getenv("OAUTH_KEY_ENCRYPTION_KEY_FILE")); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("OAUTH_KEY_ENCRYPTION_KEY_FILE: %w", err)
		}
		raw = strings.TrimSpace(string(b))
	}
	if raw == "" {
		return nil, errors.New("OAUTH_KEY_ENCRYPTION_KEY (or _FILE) is required; create one with `poweur-oauth gen-key` and back it up")
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		key, err = base64.RawURLEncoding.DecodeString(raw)
	}
	if err != nil || len(key) != 32 {
		return nil, errors.New("OAUTH_KEY_ENCRYPTION_KEY must be 32 bytes, base64")
	}
	return key, nil
}
