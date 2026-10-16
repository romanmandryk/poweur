package bridge

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/poweur/identity"
)

// Config is everything the bridge needs; it has no UI and reads only the
// environment (FromEnv). Tests build it directly.
type Config struct {
	// HTTPAddr serves the RCPT hook, blobs, uploads, /health and /metrics.
	HTTPAddr string
	// PublicURL is the bridge's HTTPS origin, used in blob URLs and
	// advertised as the bridge identity's `email_api` endpoint.
	PublicURL string

	// BridgeID is the bridge's own Poweur ID; Seed derives its keys.
	BridgeID string
	Seed     []byte
	// RelayURL is the bridge identity's home relay (it forwards to others).
	RelayURL string

	// Domains maps each mail domain the bridge answers for to the identity
	// domain its users live under: `poweur.net` → `poweur.net`, and an alias
	// such as `poweur.org` → `poweur.net`. The first entry is the primary.
	Domains []DomainMap
	// Aliases route whole addresses to an identity (`postmaster@poweur.net`
	// → `support.poweur.net`); the target must still be opted in.
	Aliases map[string]string

	// Inbound is how mail arrives: "smtp" (the bridge is the MX — only an MX
	// record needed) or "lmtp" (behind a mail server that asks the RCPT
	// hook and relays over LMTP).
	Inbound string
	// Direct mode (Inbound=smtp): the public listener, the name it greets
	// with (the MX host, matching its PTR) and an optional STARTTLS
	// certificate. Without one, senders deliver in the clear.
	SMTPListen  string
	MXHostname  string
	SMTPTLSCert string
	SMTPTLSKey  string

	// HookSecret authenticates the mail server's RCPT hook calls.
	HookSecret string

	// Inbound LMTP from the mail server.
	LMTPAddr        string
	LMTPUsername    string
	LMTPPassword    string
	LMTPAllow       []*net.IPNet
	LMTPTLSCert     string
	LMTPTLSKey      string
	LMTPInsecure    bool // allow AUTH without TLS (same-host networks, tests)
	LMTPHostname    string
	MaxMessageBytes int64

	// AuthServID is the authserv-id of the mail server whose
	// Authentication-Results header is trusted (RFC 8601). Empty trusts none.
	AuthServID string

	// Outbound submission.
	Transport string // smtp | log | none (inbound only)
	// SendMode "allowlist" pauses sending for everyone but SendAllow (the
	// operator's own IDs): others get a polite refusal and clients see
	// `email_send: "paused"`. The list itself is never published.
	SendMode     string // open | allowlist
	SendAllow    map[string]bool
	SMTPHost     string
	SMTPPort     int
	SMTPUsername string
	SMTPPassword string
	SMTPTLS      string // starttls | tls | none
	// SMTPTLSServerName is the certificate name to expect when SMTPHost is an
	// internal name (a container on the same Docker network); default SMTPHost.
	SMTPTLSServerName      string
	SMTPInsecureSkipVerify bool
	LogDir                 string // for Transport=log
	// DKIMSelector and DKIMKeyFile make the bridge sign outbound mail itself,
	// for relays that do not sign as the sender's domain.
	DKIMSelector string
	DKIMKeyFile  string

	// MessageIDDomain is the right-hand side of Message-IDs the bridge mints.
	MessageIDDomain string

	DataDir string

	BlobTTL       time.Duration
	OptInCacheTTL time.Duration
	// Sending is paced, not just capped: inbound mail (sign-in links,
	// notifications) is what the address is for, and sending from a free
	// address is what gets a domain blocklisted. Per identity, in a rolling
	// 24 hours: at most OutDailyLimit messages; after the first, each waits
	// OutSpacing × 2^(n-1) since the previous one (5m, 10m, 20m, …) up to
	// OutSpacingMax. A send that comes too early waits in the queue rather
	// than failing. Operators raise one identity's limits with
	// `poweur-email limits set` (Store.Limits).
	OutDailyLimit     int
	OutSpacing        time.Duration
	OutSpacingMax     time.Duration
	OutNewRecipients  int
	OutMinIdentityAge time.Duration
	// OutGlobalDailyLimit caps the whole bridge per rolling 24 hours (below
	// the outbound provider's cap, e.g. Brevo's 300); 0 = no cap.
	OutGlobalDailyLimit int
	// OutMaxPending bounds the sends one identity may have waiting.
	OutMaxPending       int
	InDailyPerRecipient int

	Footer     string // off | first | always
	FooterText string
	SignupURL  string

	// Resolve options for looking up identities (tests use http + private).
	Resolve identity.ResolveOptions
}

// DomainMap is one served mail domain.
type DomainMap struct {
	Mail     string
	Identity string
}

// Defaults applied by FromEnv and Validate.
const (
	DefaultHTTPAddr        = ":8091"
	DefaultLMTPAddr        = ":2525"
	DefaultMaxMessageBytes = 26214400
	DefaultBlobTTL         = 30 * 24 * time.Hour
	DefaultOptInCacheTTL   = 2 * time.Minute
	DefaultFooterText      = "Sent with Poweur ID {id} · Get your own free ID: {signup_url}"

	DefaultOutDailyLimit    = 10
	DefaultOutNewRecipients = 10
	DefaultOutSpacing       = 5 * time.Minute
	DefaultOutSpacingMax    = 2 * time.Hour
	DefaultOutMaxPending    = 20
)

// ParseDomains reads `poweur.net,poweur.org=poweur.net`.
func ParseDomains(raw string) ([]DomainMap, error) {
	var out []DomainMap
	for _, item := range strings.Split(raw, ",") {
		item = strings.ToLower(strings.TrimSpace(item))
		if item == "" {
			continue
		}
		mailDomain, idDomain, found := strings.Cut(item, "=")
		if !found {
			idDomain = mailDomain
		}
		mailDomain, idDomain = strings.TrimSpace(mailDomain), strings.TrimSpace(idDomain)
		for _, d := range []string{mailDomain, idDomain} {
			if err := identity.ValidateIdentityName(d); err != nil {
				return nil, fmt.Errorf("domain %q: %w", d, err)
			}
		}
		out = append(out, DomainMap{Mail: mailDomain, Identity: idDomain})
	}
	return out, nil
}

// ParseAliases reads `postmaster@poweur.net=support.poweur.net,…`.
func ParseAliases(raw string) (map[string]string, error) {
	out := map[string]string{}
	for _, item := range strings.Split(raw, ",") {
		item = strings.ToLower(strings.TrimSpace(item))
		if item == "" {
			continue
		}
		addr, target, ok := strings.Cut(item, "=")
		if !ok || !strings.Contains(addr, "@") {
			return nil, fmt.Errorf("alias %q: want address=identity", item)
		}
		if err := identity.ValidateIdentityName(strings.TrimSpace(target)); err != nil {
			return nil, fmt.Errorf("alias %q: %w", item, err)
		}
		out[strings.TrimSpace(addr)] = strings.TrimSpace(target)
	}
	return out, nil
}

// SendPausedReason is what a sender outside the allowlist is told.
const SendPausedReason = "Sending email is paused for now. Your address still receives mail, so you can keep using it to sign up to apps and services."

// SendPaused reports whether sending is limited to the allowlist.
func (c Config) SendPaused() bool { return c.SendMode == "allowlist" }

// SendAllowed reports whether sender may send at all.
func (c Config) SendAllowed(sender string) bool {
	return !c.SendPaused() || c.SendAllow[strings.ToLower(sender)]
}

// ParseSendAllow reads `support.poweur.net,john.poweur.net`.
func ParseSendAllow(raw string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, item := range strings.Split(raw, ",") {
		item = strings.ToLower(strings.TrimSpace(item))
		if item == "" {
			continue
		}
		if err := identity.ValidateIdentityName(item); err != nil {
			return nil, fmt.Errorf("EMAIL_SEND_ALLOW %q: %w", item, err)
		}
		out[item] = true
	}
	return out, nil
}

func parseCIDRs(raw string) ([]*net.IPNet, error) {
	var out []*net.IPNet
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if !strings.Contains(item, "/") {
			if ip := net.ParseIP(item); ip != nil && ip.To4() != nil {
				item += "/32"
			} else {
				item += "/128"
			}
		}
		_, n, err := net.ParseCIDR(item)
		if err != nil {
			return nil, fmt.Errorf("EMAIL_LMTP_ALLOW %q: %w", item, err)
		}
		out = append(out, n)
	}
	return out, nil
}

// FromEnv reads the configuration. Secrets may be given as files
// (EMAIL_BRIDGE_SEED_FILE, …_FILE variants), the form Ansible deploys.
func FromEnv() (Config, error) {
	env := func(k, def string) string {
		if v, ok := os.LookupEnv(k); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
		return def
	}
	secret := func(k string) (string, error) {
		if file := env(k+"_FILE", ""); file != "" {
			raw, err := os.ReadFile(file)
			if err != nil {
				return "", fmt.Errorf("%s_FILE: %w", k, err)
			}
			return strings.TrimSpace(string(raw)), nil
		}
		return env(k, ""), nil
	}
	intEnv := func(k string, def int) (int, error) {
		v := env(k, "")
		if v == "" {
			return def, nil
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", k, err)
		}
		return n, nil
	}
	durEnv := func(k string, def time.Duration) (time.Duration, error) {
		v := env(k, "")
		if v == "" {
			return def, nil
		}
		d, err := time.ParseDuration(v)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", k, err)
		}
		return d, nil
	}
	var err error
	c := Config{
		HTTPAddr:               env("EMAIL_ADDR", DefaultHTTPAddr),
		PublicURL:              strings.TrimRight(env("EMAIL_PUBLIC_URL", ""), "/"),
		BridgeID:               strings.ToLower(env("EMAIL_BRIDGE_ID", "")),
		RelayURL:               strings.TrimRight(env("EMAIL_RELAY_URL", ""), "/"),
		LMTPAddr:               env("EMAIL_LMTP_ADDR", DefaultLMTPAddr),
		Inbound:                env("EMAIL_INBOUND", "smtp"),
		SMTPListen:             env("EMAIL_SMTP_LISTEN", ":25"),
		MXHostname:             strings.ToLower(env("EMAIL_MX_HOSTNAME", "")),
		SMTPTLSCert:            env("EMAIL_TLS_CERT_FILE", ""),
		SMTPTLSKey:             env("EMAIL_TLS_KEY_FILE", ""),
		DKIMSelector:           env("EMAIL_DKIM_SELECTOR", ""),
		DKIMKeyFile:            env("EMAIL_DKIM_KEY_FILE", ""),
		LMTPUsername:           env("EMAIL_LMTP_USERNAME", ""),
		LMTPTLSCert:            env("EMAIL_LMTP_TLS_CERT_FILE", ""),
		LMTPTLSKey:             env("EMAIL_LMTP_TLS_KEY_FILE", ""),
		LMTPInsecure:           env("EMAIL_LMTP_INSECURE", "") == "1",
		LMTPHostname:           env("EMAIL_LMTP_HOSTNAME", ""),
		AuthServID:             strings.ToLower(env("EMAIL_AUTHSERV_ID", "")),
		Transport:              env("EMAIL_TRANSPORT", "smtp"),
		SendMode:               env("EMAIL_SEND_MODE", "open"),
		SMTPHost:               env("EMAIL_SMTP_HOST", ""),
		SMTPUsername:           env("EMAIL_SMTP_USERNAME", ""),
		SMTPTLS:                env("EMAIL_SMTP_TLS", "starttls"),
		SMTPTLSServerName:      env("EMAIL_SMTP_TLS_SERVER_NAME", ""),
		SMTPInsecureSkipVerify: env("EMAIL_SMTP_INSECURE_SKIP_VERIFY", "") == "1",
		LogDir:                 env("EMAIL_LOG_DIR", ""),
		MessageIDDomain:        strings.ToLower(env("EMAIL_MESSAGE_ID_DOMAIN", "")),
		DataDir:                env("EMAIL_DATA", "/data"),
		Footer:                 env("EMAIL_FOOTER", "first"),
		FooterText:             env("EMAIL_FOOTER_TEXT", DefaultFooterText),
		SignupURL:              env("EMAIL_SIGNUP_URL", "https://poweur.net"),
	}
	if c.Domains, err = ParseDomains(env("EMAIL_DOMAINS", "")); err != nil {
		return c, err
	}
	if c.Aliases, err = ParseAliases(env("EMAIL_ALIASES", "")); err != nil {
		return c, err
	}
	if c.SendAllow, err = ParseSendAllow(env("EMAIL_SEND_ALLOW", "")); err != nil {
		return c, err
	}
	if c.LMTPAllow, err = parseCIDRs(env("EMAIL_LMTP_ALLOW", "")); err != nil {
		return c, err
	}
	seed, err := secret("EMAIL_BRIDGE_SEED")
	if err != nil {
		return c, err
	}
	if seed != "" {
		if c.Seed, err = identity.ParseSeedOrMnemonic(seed); err != nil {
			return c, fmt.Errorf("EMAIL_BRIDGE_SEED: %w", err)
		}
	}
	if c.HookSecret, err = secret("EMAIL_HOOK_SECRET"); err != nil {
		return c, err
	}
	if c.LMTPPassword, err = secret("EMAIL_LMTP_PASSWORD"); err != nil {
		return c, err
	}
	if c.SMTPPassword, err = secret("EMAIL_SMTP_PASSWORD"); err != nil {
		return c, err
	}
	if c.SMTPPort, err = intEnv("EMAIL_SMTP_PORT", 587); err != nil {
		return c, err
	}
	maxBytes, err := intEnv("EMAIL_MAX_MESSAGE_BYTES", DefaultMaxMessageBytes)
	if err != nil {
		return c, err
	}
	c.MaxMessageBytes = int64(maxBytes)
	if c.OutDailyLimit, err = intEnv("EMAIL_OUT_DAILY_LIMIT", DefaultOutDailyLimit); err != nil {
		return c, err
	}
	if c.OutNewRecipients, err = intEnv("EMAIL_OUT_NEW_RECIPIENTS_DAILY", DefaultOutNewRecipients); err != nil {
		return c, err
	}
	if c.OutGlobalDailyLimit, err = intEnv("EMAIL_OUT_GLOBAL_DAILY_LIMIT", 0); err != nil {
		return c, err
	}
	if c.OutMaxPending, err = intEnv("EMAIL_OUT_MAX_PENDING", DefaultOutMaxPending); err != nil {
		return c, err
	}
	if c.OutSpacing, err = durEnv("EMAIL_OUT_SPACING", DefaultOutSpacing); err != nil {
		return c, err
	}
	if c.OutSpacingMax, err = durEnv("EMAIL_OUT_SPACING_MAX", DefaultOutSpacingMax); err != nil {
		return c, err
	}
	if c.InDailyPerRecipient, err = intEnv("EMAIL_IN_DAILY_PER_RECIPIENT", 500); err != nil {
		return c, err
	}
	if c.BlobTTL, err = durEnv("EMAIL_BLOB_TTL", DefaultBlobTTL); err != nil {
		return c, err
	}
	if c.OptInCacheTTL, err = durEnv("EMAIL_OPTIN_CACHE", DefaultOptInCacheTTL); err != nil {
		return c, err
	}
	if c.OutMinIdentityAge, err = durEnv("EMAIL_OUT_MIN_AGE", 0); err != nil {
		return c, err
	}
	scheme := env("EMAIL_RESOLVE_SCHEME", "https")
	c.Resolve = identity.ResolveOptions{Scheme: scheme, AllowPrivate: env("EMAIL_RESOLVE_ALLOW_PRIVATE", "") == "1"}
	// Development and tests only: send every identity lookup to one address
	// (a local relay) instead of resolving identity hosts in DNS.
	if dial := env("EMAIL_RESOLVE_DIAL", ""); dial != "" {
		c.Resolve.HTTPClient = &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, dial)
			}},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	return c, nil
}

// Validate checks the configuration and fills defaults.
func (c *Config) Validate() error {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	if err := identity.ValidateIdentityName(c.BridgeID); err != nil {
		add("EMAIL_BRIDGE_ID: %v", err)
	}
	if len(c.Seed) != 32 {
		add("EMAIL_BRIDGE_SEED (or _FILE) is required: the bridge identity's 32-byte seed or recovery phrase")
	}
	if u, err := url.Parse(c.RelayURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		add("EMAIL_RELAY_URL must be the bridge identity's relay, e.g. https://relay.poweur.net")
	}
	if c.PublicURL != "" {
		if u, err := url.Parse(c.PublicURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.Path != "" {
			add("EMAIL_PUBLIC_URL must be an origin such as https://email.poweur.org")
		}
	}
	if len(c.Domains) == 0 {
		add("EMAIL_DOMAINS is required, e.g. poweur.net or poweur.net,poweur.org=poweur.net")
	}
	switch c.Inbound {
	case "smtp":
		if c.MXHostname == "" {
			add("EMAIL_MX_HOSTNAME is required for EMAIL_INBOUND=smtp: the name your MX record points at")
		}
		if (c.SMTPTLSCert == "") != (c.SMTPTLSKey == "") {
			add("EMAIL_TLS_CERT_FILE and EMAIL_TLS_KEY_FILE go together")
		}
		if c.SMTPListen == "" {
			c.SMTPListen = ":25"
		}
	case "lmtp":
		if len(c.HookSecret) < 16 {
			add("EMAIL_HOOK_SECRET must be at least 16 characters")
		}
		if c.LMTPUsername == "" || len(c.LMTPPassword) < 16 {
			add("EMAIL_LMTP_USERNAME and an EMAIL_LMTP_PASSWORD of at least 16 characters are required")
		}
		if !c.LMTPInsecure && (c.LMTPTLSCert == "" || c.LMTPTLSKey == "") {
			add("EMAIL_LMTP_TLS_CERT_FILE and _KEY_FILE are required unless EMAIL_LMTP_INSECURE=1 (same-host network only)")
		}
	default:
		add("EMAIL_INBOUND must be smtp (the bridge is the MX) or lmtp (behind a mail server)")
	}
	if (c.DKIMSelector == "") != (c.DKIMKeyFile == "") {
		add("EMAIL_DKIM_SELECTOR and EMAIL_DKIM_KEY_FILE go together")
	}
	switch c.Transport {
	case "smtp":
		if c.SMTPHost == "" {
			add("EMAIL_SMTP_HOST is required for EMAIL_TRANSPORT=smtp")
		}
		switch c.SMTPTLS {
		case "starttls", "tls", "none":
		default:
			add("EMAIL_SMTP_TLS must be starttls, tls or none")
		}
	case "log":
		if c.LogDir == "" {
			add("EMAIL_LOG_DIR is required for EMAIL_TRANSPORT=log")
		}
	case "none":
		// Inbound only: email.send is answered with a refusal.
	default:
		add("EMAIL_TRANSPORT must be smtp, log or none (receive only)")
	}
	switch c.SendMode {
	case "", "open", "allowlist":
	default:
		add("EMAIL_SEND_MODE must be open or allowlist")
	}
	switch c.Footer {
	case "off", "first", "always":
	default:
		add("EMAIL_FOOTER must be off, first or always")
	}
	if c.DataDir == "" {
		add("EMAIL_DATA is required")
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	if c.HTTPAddr == "" {
		c.HTTPAddr = DefaultHTTPAddr
	}
	if c.LMTPAddr == "" {
		c.LMTPAddr = DefaultLMTPAddr
	}
	if c.MaxMessageBytes <= 0 {
		c.MaxMessageBytes = DefaultMaxMessageBytes
	}
	if c.BlobTTL <= 0 {
		c.BlobTTL = DefaultBlobTTL
	}
	if c.OptInCacheTTL <= 0 {
		c.OptInCacheTTL = DefaultOptInCacheTTL
	}
	if c.MessageIDDomain == "" {
		c.MessageIDDomain = c.Domains[0].Mail
	}
	if c.FooterText == "" {
		c.FooterText = DefaultFooterText
	}
	if c.LMTPHostname == "" {
		c.LMTPHostname = c.BridgeID
	}
	if c.Aliases == nil {
		c.Aliases = map[string]string{}
	}
	return nil
}

// Keys derives the bridge identity's signing and encryption keys.
func (c Config) Keys() (ed25519.PrivateKey, []byte, []byte, error) {
	_, signing, err := identity.DeriveSigningKey(c.Seed)
	if err != nil {
		return nil, nil, nil, err
	}
	encPub, encPriv, err := identity.DeriveEncryptionKey(c.Seed)
	if err != nil {
		return nil, nil, nil, err
	}
	return signing, encPub, encPriv, nil
}

// IdentityDomain maps a mail domain to the identity domain it routes to.
func (c Config) IdentityDomain(mailDomain string) (string, bool) {
	mailDomain = strings.ToLower(strings.TrimSuffix(mailDomain, "."))
	for _, d := range c.Domains {
		if d.Mail == mailDomain {
			return d.Identity, true
		}
	}
	return "", false
}
