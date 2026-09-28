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
	t.Setenv("BUILD_TIME", "2026-09-10T12:00:00Z")
	t.Setenv("VERSION_HASH", "abc1234")
	t.Setenv("RATE_LIMIT_MINUTE", "7")
	t.Setenv("GLOBAL_RATE_LIMIT_MINUTE", "3")
	t.Setenv("DNS_TTL", "120s")
	t.Cleanup(func() {
		_ = os.Unsetenv("LISTEN_ADDR")
		_ = os.Unsetenv("RELAY_ADDRESS")
		_ = os.Unsetenv("RELAY_SCHEME")
		_ = os.Unsetenv("VERSION")
		_ = os.Unsetenv("BUILD_TIME")
		_ = os.Unsetenv("VERSION_HASH")
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
	if c.BuildTime != "2026-09-10T12:00:00Z" || c.VersionHash != "abc1234" {
		t.Fatalf("build metadata %q / %q", c.BuildTime, c.VersionHash)
	}
	if c.DNSTTL != 120*time.Second {
		t.Fatalf("dnsttl %v", c.DNSTTL)
	}
}

// EPIC-007 E07-T5: the per-sender-relay request meter ships on by default —
// an operator who never sets the env vars still gets one — and 0 is how they
// turn a window off, the same convention every other relay-wide knob uses.
func TestRequestRelayLimitsFromEnv(t *testing.T) {
	t.Setenv("RELAY_ADDRESS", "relay.fromenv")

	defaults := FromEnv().RequestRelayLimits
	if defaults.PerMinute != DefaultRequestRelayMinuteLimit ||
		defaults.PerHour != DefaultRequestRelayHourLimit ||
		defaults.PerDay != DefaultRequestRelayDayLimit {
		t.Fatalf("defaults: %+v", defaults)
	}

	t.Setenv("REQUEST_RELAY_LIMIT_MINUTE", "4")
	t.Setenv("REQUEST_RELAY_LIMIT_HOUR", "0")
	t.Setenv("REQUEST_RELAY_LIMIT_DAY", "40")
	got := FromEnv().RequestRelayLimits
	if got.PerMinute != 4 || got.PerHour != 0 || got.PerDay != 40 {
		t.Fatalf("overrides: %+v", got)
	}
}

func TestStorageQuotaSettingsFromEnv(t *testing.T) {
	t.Setenv("POWEUR_DATA", "/data")
	t.Setenv("STORAGE_QUOTAS_FILE", "")
	t.Setenv("QUOTA_CONTACT", " HelpDesk.poweur.net ")
	cfg := FromEnv()
	if cfg.StorageQuotasFile != "/data/storage-quotas.json" {
		t.Fatalf("default quotas file = %q", cfg.StorageQuotasFile)
	}
	if cfg.QuotaContact != "helpdesk.poweur.net" {
		t.Fatalf("contact = %q", cfg.QuotaContact)
	}

	t.Setenv("STORAGE_QUOTAS_FILE", "/etc/poweur/quotas.json")
	if got := FromEnv().StorageQuotasFile; got != "/etc/poweur/quotas.json" {
		t.Fatalf("explicit quotas file = %q", got)
	}

	t.Setenv("STORAGE_QUOTAS_FILE", "")
	t.Setenv("POWEUR_DATA", "")
	if got := FromEnv().StorageQuotasFile; got != "" {
		t.Fatalf("no data dir, no file: %q", got)
	}
}

func TestStorageProviderConfig(t *testing.T) {
	base := Config{ListenAddr: ":8080", RelayAddress: "r.test"}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	rejected := base
	rejected.StorageProvider = "relay-fs"
	if err := rejected.Validate(); err == nil {
		t.Fatal("relay-fs accepted")
	}
	partial := base
	partial.StorageProvider = StorageS3
	partial.S3Endpoint = "127.0.0.1:9000"
	if err := partial.Validate(); err == nil {
		t.Fatal("s3 without bucket accepted")
	}
	partial.S3Bucket = "bucket"
	partial.S3AccessKey = "only-one"
	if err := partial.Validate(); err == nil {
		t.Fatal("partial credentials accepted")
	}
	withBucket := partial
	withBucket.S3AccessKey = ""
	if err := withBucket.Validate(); err != nil {
		t.Fatal(err)
	}
	fsWithS3 := base
	fsWithS3.StorageProvider = StorageFS
	fsWithS3.S3Bucket = "bucket"
	if err := fsWithS3.Validate(); err == nil {
		t.Fatal("s3 settings on fs accepted")
	}

	t.Setenv("RELAY_ADDRESS", "relay.test")
	t.Setenv("LISTEN_ADDR", ":8080")
	t.Setenv("STORAGE_PROVIDER", "s3")
	t.Setenv("S3_ENDPOINT", "http://127.0.0.1:9000")
	t.Setenv("S3_BUCKET", "poweur")
	t.Setenv("S3_PREFIX", "/tenant/")
	t.Setenv("S3_ACCESS_KEY", "access")
	t.Setenv("S3_SECRET_KEY", "secret")
	t.Setenv("S3_SECURE", "0")
	t.Setenv("S3_PRESIGN", "off")
	t.Setenv("S3_REGION", "auto")
	cfg := FromEnv()
	if cfg.StorageProvider != "s3" || cfg.S3Endpoint != "http://127.0.0.1:9000" || cfg.S3Bucket != "poweur" || cfg.S3Prefix != "tenant" {
		t.Fatalf("s3 config: %+v", cfg)
	}
	if cfg.S3Secure || cfg.S3Presign || cfg.S3Region != "auto" || cfg.S3AccessKey != "access" {
		t.Fatalf("s3 flags: %+v", cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}
