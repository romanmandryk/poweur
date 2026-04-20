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
	// WriteIdentityRecords publishes the full set of DNS records for a new
	// identity: its signing TXT, its optional encryption TXT, and its
	// relay host record.
	WriteIdentityRecords(ctx context.Context, token, identity, publicKey, encryptionPublicKey, relayAddress string) error

	// WriteEncryptionKey publishes (or overwrites) just the
	// `_eurything-enc.<identity>` TXT record. It exists so that an identity
	// created before the encryption-key support landed can be retro-fitted
	// with an X25519 key, and so existing identities can rotate their
	// encryption key without touching the signing key or the relay host
	// record.
	WriteEncryptionKey(ctx context.Context, token, identity, encryptionPublicKey string) error
}

type ProviderFactory struct {
	cfg       config.Config
	overrides map[string]Provider
}

func NewProviderFactory(cfg config.Config) *ProviderFactory {
	return &ProviderFactory{cfg: cfg, overrides: map[string]Provider{}}
}

// Register installs a Provider under the given name, shadowing the built-in
// implementation. Primarily used by integration tests that want a specific
// in-memory provider instance (e.g. backed by a shared DNS zone) to be
// returned when the relay handles POST /identities.
func (f *ProviderFactory) Register(name string, provider Provider) {
	if f.overrides == nil {
		f.overrides = map[string]Provider{}
	}
	f.overrides[strings.ToLower(name)] = provider
}

func (f *ProviderFactory) Provider(name string) (Provider, error) {
	if f.overrides != nil {
		if p, ok := f.overrides[strings.ToLower(name)]; ok {
			return p, nil
		}
	}
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
