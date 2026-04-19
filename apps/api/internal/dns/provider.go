package dns

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/eurything/api/internal/config"
)

type Provider interface {
	WriteIdentityRecords(ctx context.Context, token, identity, publicKey, relayAddress string) error
}

type ProviderFactory struct {
	cfg config.Config
}

func NewProviderFactory(cfg config.Config) *ProviderFactory {
	return &ProviderFactory{cfg: cfg}
}

func (f *ProviderFactory) Provider(name string) (Provider, error) {
	switch strings.ToLower(name) {
	case "cloudflare":
		return NewCloudflareProvider(f.cfg), nil
	case "hetzner":
		return NewHetznerProvider(f.cfg), nil
	case "mock":
		return NewMemoryProvider(f.cfg), nil
	default:
		return nil, errors.New("unsupported dns provider")
	}
}

func relayRecord(relayAddress string) (string, string) {
	host := normalizeRelayHost(relayAddress)
	if net.ParseIP(host) != nil {
		return "A", host
	}
	return "CNAME", host
}

func normalizeRelayHost(relayAddress string) string {
	if strings.Contains(relayAddress, "://") {
		if parsed, err := url.Parse(relayAddress); err == nil {
			if host := parsed.Hostname(); host != "" {
				return host
			}
		}
	}
	if host, _, err := net.SplitHostPort(relayAddress); err == nil && host != "" {
		return host
	}
	return relayAddress
}

func defaultTTL(cfg config.Config) time.Duration {
	if cfg.DNSTTL > 0 {
		return cfg.DNSTTL
	}
	return config.DefaultDNSTTL
}
