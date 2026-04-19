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
	DefaultVersion      = "0.1.0"
)

type RateLimits struct {
	PerMinute int
	PerHour   int
	PerDay    int
}

type Config struct {
	ListenAddr   string
	RelayAddress string
	RelayScheme  string
	DNSTTL       time.Duration
	ChallengeTTL time.Duration
	Version      string
	RateLimits   RateLimits
	DNSProxyMode string
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
		RelayAddress: os.Getenv("RELAY_ADDRESS"),
		RelayScheme:  getenv("RELAY_SCHEME", DefaultRelayScheme),
		DNSTTL:       getenvDuration("DNS_TTL", DefaultDNSTTL),
		ChallengeTTL: getenvDuration("CHALLENGE_TTL", DefaultChallengeTTL),
		Version:      getenv("VERSION", DefaultVersion),
		DNSProxyMode: strings.ToLower(getenv("DNS_PROXY_MODE", "auto")),
		RateLimits: RateLimits{
			PerMinute: getenvInt("RATE_LIMIT_MINUTE", DefaultMinuteLimit),
			PerHour:   getenvInt("RATE_LIMIT_HOUR", DefaultHourLimit),
			PerDay:    getenvInt("RATE_LIMIT_DAY", DefaultDayLimit),
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
