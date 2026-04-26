package config

import (
	"os"
	"testing"
	"time"
)

func TestValidateMissingRelayAddress(t *testing.T) {
	c := Config{ListenAddr: ":8080"}
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for missing RELAY_ADDRESS")
	}
}

func TestValidateInvalidDNSProxyMode(t *testing.T) {
	c := Config{
		ListenAddr:   ":8080",
		RelayAddress: "r.test",
		DNSProxyMode: "nope",
	}
	if err := c.Validate(); err == nil {
		t.Fatal("expected invalid DNS_PROXY_MODE")
	}
}

func TestValidateOK(t *testing.T) {
	c := Config{
		ListenAddr:   ":8080",
		RelayAddress: "r.test",
		DNSProxyMode: "auto",
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv("LISTEN_ADDR", ":9999")
	t.Setenv("RELAY_ADDRESS", "relay.fromenv")
	t.Setenv("RELAY_SCHEME", "http")
	t.Setenv("VERSION", "9.9.9")
	t.Setenv("RATE_LIMIT_MINUTE", "7")
	t.Setenv("GLOBAL_RATE_LIMIT_MINUTE", "3")
	t.Setenv("DNS_TTL", "120s")
	t.Cleanup(func() {
		_ = os.Unsetenv("LISTEN_ADDR")
		_ = os.Unsetenv("RELAY_ADDRESS")
		_ = os.Unsetenv("RELAY_SCHEME")
		_ = os.Unsetenv("VERSION")
		_ = os.Unsetenv("RATE_LIMIT_MINUTE")
		_ = os.Unsetenv("GLOBAL_RATE_LIMIT_MINUTE")
		_ = os.Unsetenv("DNS_TTL")
	})
	c := FromEnv()
	if c.ListenAddr != ":9999" || c.RelayAddress != "relay.fromenv" {
		t.Fatalf("listen/relay: %+v", c)
	}
	if c.RateLimits.PerMinute != 7 || c.GlobalRateLimits.PerMinute != 3 {
		t.Fatalf("limits: %+v / %+v", c.RateLimits, c.GlobalRateLimits)
	}
	if c.Version != "9.9.9" {
		t.Fatalf("version %q", c.Version)
	}
	if c.DNSTTL != 120*time.Second {
		t.Fatalf("dnsttl %v", c.DNSTTL)
	}
}
