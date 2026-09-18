// Package telemetry owns optional OTLP export. It never discovers user data from
// HTTP payloads: callers must explicitly mark a verified actor.
package telemetry

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/netip"
	"net/url"
	"os"
	"strings"
)

type Config struct {
	Endpoint, Protocol, Headers, HashKey, Level, Environment string
	// SecondaryEndpoint is an extra OTLP/HTTP sink (Grafana stays on Endpoint).
	// Point it at any OTLP intake — Better Stack collector, another vendor, a
	// second Alloy — and unset it to stop dual-export without code changes.
	SecondaryEndpoint, SecondaryHeaders string
	// UptimeURL is an optional GET heartbeat (Better Stack Heartbeat, or any
	// URL). Empty disables it. Grafana blackbox remains the in-stack probe.
	UptimeURL string
	// BrowserBetterStackToken is the public JavaScript-tag token. Empty keeps
	// GET /app/observability.json at {"providers":[]} so the SPA loads no tag.
	BrowserBetterStackToken string
	TrustedProxies          []netip.Prefix
	AllowHTTP               bool // explicit development/isolated Docker network override
}

func FromEnv() Config {
	return Config{
		Endpoint:                os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
		Protocol:                os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL"),
		Headers:                 os.Getenv("OTEL_EXPORTER_OTLP_HEADERS"),
		SecondaryEndpoint:       os.Getenv("TELEMETRY_OTLP_SECONDARY_ENDPOINT"),
		SecondaryHeaders:        os.Getenv("TELEMETRY_OTLP_SECONDARY_HEADERS"),
		UptimeURL:               strings.TrimSpace(os.Getenv("TELEMETRY_UPTIME_URL")),
		BrowserBetterStackToken: strings.TrimSpace(os.Getenv("BETTERSTACK_RUM_TOKEN")),
		HashKey:                 os.Getenv("TELEMETRY_HASH_KEY"),
		Level:                   os.Getenv("LOG_LEVEL"),
		Environment:             os.Getenv("TELEMETRY_ENVIRONMENT"),
		AllowHTTP:               os.Getenv("TELEMETRY_ALLOW_HTTP") == "1",
	}
}

type otlpSink struct {
	name, endpoint, headers string
}

func (c Config) otlpSinks() []otlpSink {
	var sinks []otlpSink
	if c.Endpoint != "" {
		sinks = append(sinks, otlpSink{name: "otlp", endpoint: c.Endpoint, headers: c.Headers})
	}
	if c.SecondaryEndpoint != "" {
		sinks = append(sinks, otlpSink{name: "otlp-secondary", endpoint: c.SecondaryEndpoint, headers: c.SecondaryHeaders})
	}
	return sinks
}

// BrowserConfig is the public SPA file at GET /app/observability.json.
func (c Config) BrowserConfig(release string) []byte {
	type provider struct {
		Type  string `json:"type"`
		Token string `json:"token"`
	}
	doc := struct {
		Environment string     `json:"environment,omitempty"`
		Release     string     `json:"release,omitempty"`
		Providers   []provider `json:"providers"`
	}{Environment: c.Environment, Release: release, Providers: []provider{}}
	if doc.Environment == "" {
		doc.Environment = "production"
	}
	if c.BrowserBetterStackToken != "" {
		doc.Providers = append(doc.Providers, provider{Type: "betterstack", Token: c.BrowserBetterStackToken})
	}
	b, _ := json.Marshal(doc)
	return b
}

// ParseTrustedProxies rejects typos rather than trusting all forwarders.
func ParseTrustedProxies(raw string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, s := range strings.Split(raw, ",") {
		if strings.TrimSpace(s) == "" {
			continue
		}
		p, err := netip.ParsePrefix(strings.TrimSpace(s))
		if err != nil {
			return nil, fmt.Errorf("invalid TELEMETRY_TRUSTED_PROXIES CIDR")
		}
		out = append(out, p)
	}
	return out, nil
}

func (c Config) Validate() error {
	var level slog.Level
	if c.Level != "" {
		if err := level.UnmarshalText([]byte(c.Level)); err != nil {
			return fmt.Errorf("invalid LOG_LEVEL")
		}
	}
	switch c.Environment {
	case "", "production", "development", "test":
	default:
		return fmt.Errorf("invalid TELEMETRY_ENVIRONMENT")
	}
	if err := validateOTLPEndpoint("OTEL_EXPORTER_OTLP_ENDPOINT", c.Endpoint, c.AllowHTTP); err != nil {
		return err
	}
	if err := validateOTLPEndpoint("TELEMETRY_OTLP_SECONDARY_ENDPOINT", c.SecondaryEndpoint, c.AllowHTTP); err != nil {
		return err
	}
	if c.UptimeURL != "" {
		if err := validateUptimeURL(c.UptimeURL, c.AllowHTTP); err != nil {
			return err
		}
	}
	if c.Protocol != "" && c.Protocol != "http/protobuf" {
		return fmt.Errorf("OTLP protocol must be http/protobuf")
	}
	if len(c.otlpSinks()) > 0 && len(c.HashKey) < 32 {
		return fmt.Errorf("TELEMETRY_HASH_KEY must contain at least 32 bytes")
	}
	if _, err := parseHeaders(c.Headers); err != nil {
		return err
	}
	_, err := parseHeaders(c.SecondaryHeaders)
	return err
}

func validateOTLPEndpoint(name, endpoint string, allowHTTP bool) error {
	if endpoint == "" {
		return nil
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && allowHTTP)) {
		return fmt.Errorf("invalid %s: HTTPS base URL required (TELEMETRY_ALLOW_HTTP=1 for isolated development)", name)
	}
	return nil
}

func validateUptimeURL(endpoint string, allowHTTP bool) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && allowHTTP)) {
		return fmt.Errorf("invalid TELEMETRY_UPTIME_URL")
	}
	return nil
}

func parseHeaders(raw string) (map[string]string, error) {
	out := map[string]string{}
	for _, part := range strings.Split(raw, ",") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		k = strings.TrimSpace(k)
		decoded, err := url.PathUnescape(strings.TrimSpace(v))
		if !ok || k == "" || err != nil || strings.ContainsAny(k+decoded, "\r\n") {
			return nil, fmt.Errorf("invalid OTLP headers")
		}
		out[k] = decoded
	}
	return out, nil
}
