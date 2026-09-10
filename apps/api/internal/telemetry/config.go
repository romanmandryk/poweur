// Package telemetry owns optional OTLP export. It never discovers user data from
// HTTP payloads: callers must explicitly mark a verified actor.
package telemetry

import (
	"fmt"
	"log/slog"
	"net/netip"
	"net/url"
	"os"
	"strings"
)

type Config struct {
	Endpoint, Protocol, Headers, HashKey, Level, Environment string
	TrustedProxies                                           []netip.Prefix
	AllowHTTP                                                bool // explicit development/isolated Docker network override
}

func FromEnv() Config {
	return Config{Endpoint: os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"), Protocol: os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL"), Headers: os.Getenv("OTEL_EXPORTER_OTLP_HEADERS"), HashKey: os.Getenv("TELEMETRY_HASH_KEY"), Level: os.Getenv("LOG_LEVEL"), Environment: os.Getenv("TELEMETRY_ENVIRONMENT"), AllowHTTP: os.Getenv("TELEMETRY_ALLOW_HTTP") == "1"}
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
	if c.Endpoint == "" {
		return nil
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && c.AllowHTTP)) {
		return fmt.Errorf("invalid OTLP endpoint: HTTPS base URL required (TELEMETRY_ALLOW_HTTP=1 for isolated development)")
	}
	if c.Protocol != "" && c.Protocol != "http/protobuf" {
		return fmt.Errorf("OTLP protocol must be http/protobuf")
	}
	if len(c.HashKey) < 32 {
		return fmt.Errorf("TELEMETRY_HASH_KEY must contain at least 32 bytes")
	}
	_, err = parseHeaders(c.Headers)
	return err
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
