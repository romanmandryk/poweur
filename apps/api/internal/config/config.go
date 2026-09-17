package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/poweur/api/internal/buildinfo"
	"github.com/poweur/api/internal/telemetry"
	idpkg "github.com/poweur/identity"
)

const (
	DefaultListenAddr   = ":8080"
	DefaultRelayScheme  = "https"
	DefaultDNSTTL       = 300 * time.Second
	DefaultChallengeTTL = 60 * time.Second
	DefaultMinuteLimit  = 20
	DefaultHourLimit    = 200
	DefaultDayLimit     = 1000
	// Contact-request admissions charged to the *peer relay* that carried
	// them (EPIC-007 E07-T5). Hosted identities are cheap, so a flood is a
	// thousand fresh identities sending one request each — invisible to a
	// per-identity cap and obvious to a per-relay one. The numbers are
	// deliberately small: a contact request is a rare event per person, and
	// this bucket counts only requests-queue admissions, never conversation.
	DefaultRequestRelayMinuteLimit = 10
	DefaultRequestRelayHourLimit   = 60
	DefaultRequestRelayDayLimit    = 300
	// Global default is roughly 10× the per-sender hourly cap times an
	// expected-active-senders constant (here: 50). Tune per deployment via
	// env vars; 0 disables the global cap entirely.
	DefaultGlobalMinuteLimit   = 1000
	DefaultGlobalHourLimit     = 100000
	DefaultGlobalDayLimit      = 1000000
	DefaultMaxInboxPerIdentity = 50
	// DefaultSpoolTTL is how long undelivered mail waits for a recipient who
	// never comes back (EPIC-009 E09-T1). Long enough for a holiday, short
	// enough that a dead identity is not a permanent storage cost.
	DefaultSpoolTTL = 30 * 24 * time.Hour
	// DefaultMaxStreamsPerIdentity allows a handful of devices without letting
	// a reconnect loop pin a connection per attempt.
	DefaultMaxStreamsPerIdentity = 8
	// DefaultStreamIdleTimeout closes a push stream after an hour; clients
	// reconnect, and catching up is a cursor read.
	DefaultStreamIdleTimeout  = time.Hour
	DefaultMaxAcksPerIdentity = 50
	// EPIC-003 storage defaults: 5 GiB per identity, 2 GiB max single file.
	DefaultMaxIdentityBytes = int64(5) << 30
	DefaultMaxFileBytes     = int64(2) << 30
	DefaultStorageProvider  = "relay-fs"
)

// RateLimits configures per-sender token-bucket caps. Each window resets on
// its own schedule (rolling minute / hour / day).
type RateLimits struct {
	PerMinute int
	PerHour   int
	PerDay    int
}

// GlobalRateLimits configures relay-wide caps that apply across all
// senders combined. They serve as a back-stop against many-sender DDoS
// patterns that individually stay under the per-sender cap. A zero value
// in any window disables that cap.
type GlobalRateLimits struct {
	PerMinute int
	PerHour   int
	PerDay    int
}

type Config struct {
	Telemetry           telemetry.Config
	TelemetryProxyError error
	ListenAddr          string
	// WebStaticDir, when set, serves the bundled web client SPA under GET /app/.
	WebStaticDir string
	RelayAddress string
	RelayScheme  string
	DNSTTL       time.Duration
	ChallengeTTL time.Duration
	// Version is the relay semver (buildinfo.Version unless VERSION is set).
	Version string
	// BuildTime and VersionHash are release metadata for GET / and /health.
	BuildTime        string
	VersionHash      string
	RateLimits       RateLimits
	GlobalRateLimits GlobalRateLimits
	// RequestRelayLimits caps contact-request admissions per *sending relay*
	// (EPIC-007 E07-T5). Keyed on the relay accountable for the sender, not
	// on the sender, because identities are cheap and relays are not.
	RequestRelayLimits  RateLimits
	DNSProxyMode        string
	MaxInboxPerIdentity int
	// SpoolTTL retires undelivered messages and acks. 0 disables expiry.
	SpoolTTL time.Duration
	// MaxStreamsPerIdentity caps concurrent push streams per identity
	// (EPIC-009 E09-T2); 0 = unlimited.
	MaxStreamsPerIdentity int
	// StreamIdleTimeout closes a push stream after this long regardless of
	// traffic, so a forgotten tab does not hold a connection forever.
	// 0 disables the timeout.
	StreamIdleTimeout    time.Duration
	MaxAcksPerIdentity   int
	DataDir              string
	HostedDomains        []string
	ResolverAllowPrivate bool
	// RegistrationGate is "open" (default), "invite" or "pow" (EPIC-014:
	// hosted registration requires a solved proof-of-work challenge).
	RegistrationGate string
	// RegistrationPowBits is the PoW difficulty for REGISTRATION_GATE=pow
	// (0 = default; clamped by the pow window).
	RegistrationPowBits int
	// NamePolicy governs which hosted handles may be claimed (EPIC-018 E18-T1).
	// Operator configuration, loaded from NAME_* below; the character set is
	// not part of it and never configurable.
	NamePolicy idpkg.NamePolicy
	// NameBlockedFile is the path the blocked-terms list was read from, kept
	// for the startup log — an operator who typos it should see that no terms
	// loaded rather than assume the list is live.
	NameBlockedFile string
	// LauncherHost is the host that serves the claim flow for people who have
	// no identity yet (EPIC-018 E18-T3). Defaults to `id.<first hosted
	// domain>`; empty when nothing is hosted here. It is the *same* SPA on a
	// dedicated host, not a second app.
	//
	// It stays a single value because it is the *canonical* launcher — the one
	// a hand-off targets and the one docs name. LauncherHosts below is the set
	// that actually serves the flow.
	LauncherHost string
	// LauncherHosts is every host that serves the claim flow (EPIC-015 E15-T7).
	// Defaults to `id.<d>` *and* the bare `<d>` for each hosted domain: someone
	// typing the product's own domain is the commonest way to arrive, and
	// before this they were served the JSON service banner.
	//
	// LauncherHost is always the first entry, so a deployment that sets only
	// LAUNCHER_HOST keeps exactly today's behaviour.
	LauncherHosts []string
	// RegistrationInviteCodes are accepted invite_code values when gate=invite.
	RegistrationInviteCodes []string
	// MaxIdentityBytes is the per-identity storage quota (0 = unlimited).
	// Enforced on WebDAV PUT/MKCOL with 507 Insufficient Storage.
	MaxIdentityBytes int64
	// MaxFileBytes caps a single uploaded file (0 = unlimited).
	MaxFileBytes int64
	// StorageProvider selects the file-body backend (E03-T8). v1: "relay-fs".
	StorageProvider string
	// OAuthBridgeURL is the issuer of the OAuth/OIDC bridge this operator
	// runs or recommends (EPIC-022), e.g. https://oauth.poweur.org. Optional
	// and secret-free: hosted identities advertise it for IndieAuth discovery
	// and in their capabilities; the relay never talks to it.
	OAuthBridgeURL string
}

func (c Config) Validate() error {
	if c.TelemetryProxyError != nil {
		return c.TelemetryProxyError
	}
	if err := c.Telemetry.Validate(); err != nil {
		return err
	}
	var missing []string
	if strings.TrimSpace(c.RelayAddress) == "" {
		missing = append(missing, "RELAY_ADDRESS")
	}
	if strings.TrimSpace(c.ListenAddr) == "" {
		missing = append(missing, "LISTEN_ADDR")
	}
	if c.DNSProxyMode != "" {
		switch strings.ToLower(c.DNSProxyMode) {
		case "auto", "always", "never":
		default:
			return fmt.Errorf("invalid DNS_PROXY_MODE: %s (use auto|always|never)", c.DNSProxyMode)
		}
	}
	switch strings.ToLower(strings.TrimSpace(c.RegistrationGate)) {
	case "", "open", "invite", "pow":
	default:
		return fmt.Errorf("invalid REGISTRATION_GATE: %s (use open|invite|pow)", c.RegistrationGate)
	}
	switch strings.ToLower(strings.TrimSpace(c.StorageProvider)) {
	case "", "relay-fs":
	default:
		return fmt.Errorf("invalid STORAGE_PROVIDER: %s (v1 supports relay-fs)", c.StorageProvider)
	}
	switch c.NamePolicy.BlockMode {
	case "", idpkg.BlockModeSubstring, idpkg.BlockModeExact:
	default:
		return fmt.Errorf("invalid NAME_BLOCK_MODE: %s (use substring|exact)", c.NamePolicy.BlockMode)
	}
	if c.NamePolicy.MinLen > c.NamePolicy.MaxLen && c.NamePolicy.MaxLen > 0 {
		return fmt.Errorf("NAME_MIN_LEN (%d) exceeds NAME_MAX_LEN (%d)",
			c.NamePolicy.MinLen, c.NamePolicy.MaxLen)
	}
	if c.NameBlockedFile != "" {
		if _, err := idpkg.LoadBlockedTerms(c.NameBlockedFile); err != nil {
			return fmt.Errorf("NAME_BLOCKED_FILE %s: %w", c.NameBlockedFile, err)
		}
	}
	if c.OAuthBridgeURL != "" {
		if _, err := idpkg.NormalizeOrigin(c.OAuthBridgeURL); err != nil {
			return fmt.Errorf("invalid OAUTH_BRIDGE_URL: %w", err)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("missing required config values: %s", strings.Join(missing, ", "))
}

// IsHostedDomain reports whether identity is under a configured hosted parent.
func (c Config) IsHostedDomain(identity string) bool {
	identity = strings.ToLower(strings.TrimSuffix(identity, "."))
	for _, d := range c.HostedDomains {
		parent := strings.ToLower(strings.TrimSuffix(d, "."))
		if parent == "" {
			continue
		}
		if identity == parent || strings.HasSuffix(identity, "."+parent) {
			return true
		}
	}
	return false
}

func FromEnv() Config {
	tc := telemetry.FromEnv()
	proxies, proxyErr := telemetry.ParseTrustedProxies(os.Getenv("TELEMETRY_TRUSTED_PROXIES"))
	tc.TrustedProxies = proxies
	return Config{
		Telemetry: tc, TelemetryProxyError: proxyErr,
		ListenAddr:              getenv("LISTEN_ADDR", DefaultListenAddr),
		WebStaticDir:            strings.TrimSpace(os.Getenv("WEB_STATIC_DIR")),
		OAuthBridgeURL:          strings.TrimRight(strings.TrimSpace(os.Getenv("OAUTH_BRIDGE_URL")), "/"),
		RelayAddress:            os.Getenv("RELAY_ADDRESS"),
		RelayScheme:             getenv("RELAY_SCHEME", DefaultRelayScheme),
		DNSTTL:                  getenvDuration("DNS_TTL", DefaultDNSTTL),
		ChallengeTTL:            getenvDuration("CHALLENGE_TTL", DefaultChallengeTTL),
		Version:                 getenv("VERSION", buildinfo.Version),
		BuildTime:               getenv("BUILD_TIME", buildinfo.Time),
		VersionHash:             getenv("VERSION_HASH", buildinfo.Hash),
		DNSProxyMode:            strings.ToLower(getenv("DNS_PROXY_MODE", "auto")),
		MaxInboxPerIdentity:     getenvInt("MAX_INBOX_PER_IDENTITY", DefaultMaxInboxPerIdentity),
		SpoolTTL:                getenvDuration("SPOOL_TTL", DefaultSpoolTTL),
		MaxStreamsPerIdentity:   getenvInt("MAX_STREAMS_PER_IDENTITY", DefaultMaxStreamsPerIdentity),
		StreamIdleTimeout:       getenvDuration("STREAM_IDLE_TIMEOUT", DefaultStreamIdleTimeout),
		MaxAcksPerIdentity:      getenvInt("MAX_ACKS_PER_IDENTITY", DefaultMaxAcksPerIdentity),
		DataDir:                 strings.TrimSpace(os.Getenv("POWEUR_DATA")),
		HostedDomains:           splitCSV(os.Getenv("HOSTED_DOMAINS")),
		ResolverAllowPrivate:    getenvBool("RESOLVER_ALLOW_PRIVATE"),
		RegistrationGate:        strings.ToLower(getenv("REGISTRATION_GATE", "open")),
		RegistrationPowBits:     int(getenvInt64("REGISTRATION_POW_BITS", 0)),
		NamePolicy:              namePolicyFromEnv(),
		LauncherHost:            launcherHostFromEnv(),
		LauncherHosts:           launcherHostsFromEnv(),
		NameBlockedFile:         strings.TrimSpace(os.Getenv("NAME_BLOCKED_FILE")),
		RegistrationInviteCodes: splitCSVRaw(os.Getenv("REGISTRATION_INVITE_CODES")),
		MaxIdentityBytes:        getenvInt64("MAX_IDENTITY_BYTES", DefaultMaxIdentityBytes),
		MaxFileBytes:            getenvInt64("MAX_FILE_BYTES", DefaultMaxFileBytes),
		StorageProvider:         strings.ToLower(getenv("STORAGE_PROVIDER", DefaultStorageProvider)),
		RateLimits: RateLimits{
			PerMinute: getenvInt("RATE_LIMIT_MINUTE", DefaultMinuteLimit),
			PerHour:   getenvInt("RATE_LIMIT_HOUR", DefaultHourLimit),
			PerDay:    getenvInt("RATE_LIMIT_DAY", DefaultDayLimit),
		},
		GlobalRateLimits: GlobalRateLimits{
			PerMinute: getenvInt("GLOBAL_RATE_LIMIT_MINUTE", DefaultGlobalMinuteLimit),
			PerHour:   getenvInt("GLOBAL_RATE_LIMIT_HOUR", DefaultGlobalHourLimit),
			PerDay:    getenvInt("GLOBAL_RATE_LIMIT_DAY", DefaultGlobalDayLimit),
		},
		RequestRelayLimits: RateLimits{
			PerMinute: getenvInt("REQUEST_RELAY_LIMIT_MINUTE", DefaultRequestRelayMinuteLimit),
			PerHour:   getenvInt("REQUEST_RELAY_LIMIT_HOUR", DefaultRequestRelayHourLimit),
			PerDay:    getenvInt("REQUEST_RELAY_LIMIT_DAY", DefaultRequestRelayDayLimit),
		},
	}
}

// namePolicyFromEnv builds the hosted name policy from NAME_*.
//
// Defaults are the package's dev-friendly ones (MinLen 3), not the production
// values: the fixtures and the integration suite are full of "bob", and a
// production deployment sets NAME_MIN_LEN=6 in its own config. A blocked-terms
// file that cannot be read is logged by the caller and treated as no terms —
// a relay must not refuse to boot over a profanity list.
func namePolicyFromEnv() idpkg.NamePolicy {
	policy := idpkg.DefaultHostedPolicy()
	policy.MinLen = getenvInt("NAME_MIN_LEN", policy.MinLen)
	policy.MaxLen = getenvInt("NAME_MAX_LEN", 24)
	policy.DisallowHyphen = !getenvBoolDefault("NAME_ALLOW_HYPHEN", true)
	policy.DisallowDigits = !getenvBoolDefault("NAME_ALLOW_DIGITS", true)
	policy.Reserved = splitCSV(os.Getenv("NAME_RESERVED"))
	policy.BlockMode = strings.ToLower(getenv("NAME_BLOCK_MODE", idpkg.BlockModeSubstring))
	if terms, err := idpkg.LoadBlockedTerms(os.Getenv("NAME_BLOCKED_FILE")); err == nil {
		policy.Blocked = terms
	}
	return policy
}

// launcherHostFromEnv resolves LAUNCHER_HOST, defaulting to `id.<first hosted
// domain>`. `id` is a reserved label (E18-T1), so the host can never collide
// with an identity someone claimed.
func launcherHostFromEnv() string {
	if explicit := strings.ToLower(strings.TrimSpace(os.Getenv("LAUNCHER_HOST"))); explicit != "" {
		return explicit
	}
	for _, domain := range splitCSV(os.Getenv("HOSTED_DOMAINS")) {
		parent := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
		if parent != "" {
			return "id." + parent
		}
	}
	return ""
}

// launcherHostsFromEnv resolves the set of hosts that serve the claim flow.
//
// LAUNCHER_HOSTS wins when set. Otherwise an explicit LAUNCHER_HOST stays a set
// of one — an operator who named a single host meant a single host, and
// silently adding the apex to their deployment would serve the SPA somewhere
// they did not ask for. Only the *default* expands to both `id.<d>` and `<d>`.
func launcherHostsFromEnv() []string {
	if explicit := splitCSV(os.Getenv("LAUNCHER_HOSTS")); len(explicit) > 0 {
		return normalizeHosts(explicit)
	}
	if explicit := strings.TrimSpace(os.Getenv("LAUNCHER_HOST")); explicit != "" {
		return normalizeHosts([]string{explicit})
	}
	var hosts []string
	for _, domain := range splitCSV(os.Getenv("HOSTED_DOMAINS")) {
		parent := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
		if parent == "" {
			continue
		}
		hosts = append(hosts, "id."+parent, parent)
	}
	return normalizeHosts(hosts)
}

// normalizeHosts lowercases, strips the root dot and drops duplicates while
// keeping first-seen order — the first entry is the canonical launcher.
func normalizeHosts(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, h := range in {
		host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(h), "."))
		if host == "" || seen[host] {
			continue
		}
		seen[host] = true
		out = append(out, host)
	}
	return out
}

// IsLauncherHost reports whether host serves the claim flow. Host may carry a
// port and a trailing root dot; both are stripped before comparison.
func (c Config) IsLauncherHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if host == "" {
		return false
	}
	for _, h := range c.LauncherHosts {
		if host == h {
			return true
		}
	}
	// A Config literal built in a test may set LauncherHost alone.
	return c.LauncherHost != "" && host == strings.ToLower(strings.TrimSuffix(c.LauncherHost, "."))
}

// getenvBoolDefault differs from getenvBool: these two flags default to true,
// so an unset variable must not read as false.
func getenvBoolDefault(key string, fallback bool) bool {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	switch strings.ToLower(raw) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func splitCSV(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.ToLower(strings.TrimSpace(p))
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// splitCSVRaw splits on commas without lowercasing (invite codes).
func splitCSVRaw(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func getenvBool(key string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	return v == "1" || v == "true" || v == "yes"
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func getenvInt(key string, fallback int) int {
	if value := os.Getenv(key); value != "" {
		parsed, err := strconv.Atoi(value)
		if err == nil {
			return parsed
		}
	}
	return fallback
}

func getenvInt64(key string, fallback int64) int64 {
	if value := os.Getenv(key); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err == nil {
			return parsed
		}
	}
	return fallback
}

func getenvDuration(key string, fallback time.Duration) time.Duration {
	if value := os.Getenv(key); value != "" {
		parsed, err := time.ParseDuration(value)
		if err == nil {
			return parsed
		}
	}
	return fallback
}
