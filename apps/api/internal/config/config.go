package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultListenAddr   = ":8080"
	DefaultRelayScheme  = "https"
	DefaultDNSTTL       = 300 * time.Second
	DefaultChallengeTTL = 60 * time.Second
	DefaultMinuteLimit  = 20
	DefaultHourLimit    = 200
	DefaultDayLimit     = 1000
	// Global default is roughly 10× the per-sender hourly cap times an
	// expected-active-senders constant (here: 50). Tune per deployment via
	// env vars; 0 disables the global cap entirely.
	DefaultGlobalMinuteLimit    = 1000
	DefaultGlobalHourLimit      = 100000
	DefaultGlobalDayLimit       = 1000000
	DefaultVersion              = "0.1.0"
	DefaultMaxInboxPerIdentity  = 50
	DefaultMaxAcksPerIdentity   = 50
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
	ListenAddr string
	// WebStaticDir, when set, serves the bundled web client SPA under GET /app/.
	WebStaticDir        string
	RelayAddress        string
	RelayScheme         string
	DNSTTL              time.Duration
	ChallengeTTL        time.Duration
	Version             string
	RateLimits          RateLimits
	GlobalRateLimits    GlobalRateLimits
	DNSProxyMode        string
	MaxInboxPerIdentity int
	MaxAcksPerIdentity  int
}

func (c Config) Validate() error {
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
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("missing required config values: %s", strings.Join(missing, ", "))
}

func FromEnv() Config {
	return Config{
		ListenAddr:   getenv("LISTEN_ADDR", DefaultListenAddr),
		WebStaticDir: strings.TrimSpace(os.Getenv("WEB_STATIC_DIR")),
		RelayAddress: os.Getenv("RELAY_ADDRESS"),
		RelayScheme:  getenv("RELAY_SCHEME", DefaultRelayScheme),
		DNSTTL:       getenvDuration("DNS_TTL", DefaultDNSTTL),
		ChallengeTTL: getenvDuration("CHALLENGE_TTL", DefaultChallengeTTL),
		Version:      getenv("VERSION", DefaultVersion),
		DNSProxyMode:        strings.ToLower(getenv("DNS_PROXY_MODE", "auto")),
		MaxInboxPerIdentity: getenvInt("MAX_INBOX_PER_IDENTITY", DefaultMaxInboxPerIdentity),
		MaxAcksPerIdentity:  getenvInt("MAX_ACKS_PER_IDENTITY", DefaultMaxAcksPerIdentity),
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
	}
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

func getenvDuration(key string, fallback time.Duration) time.Duration {
	if value := os.Getenv(key); value != "" {
		parsed, err := time.ParseDuration(value)
		if err == nil {
			return parsed
		}
	}
	return fallback
}
