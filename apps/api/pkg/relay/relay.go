// Package relay is the public entry point of the Poweur ID relay server.
// It re-exports just enough of the internal implementation so external
// consumers (notably integration tests living in a separate Go module) can
// spin up an in-process relay server.
package relay

import (
	"net/http"

	"github.com/poweur/api/internal/config"
	"github.com/poweur/api/internal/dns"
	irelay "github.com/poweur/api/internal/relay"
	"github.com/poweur/api/internal/telemetry"
)

type (
	// Config matches the production relay config shape.
	Config          = config.Config
	TelemetryConfig = telemetry.Config
	// RateLimits configures the relay's per-sender rate limits.
	RateLimits = config.RateLimits
	// Resolver is the DNS read contract used by the relay.
	Resolver = dns.Resolver
	// Provider is the DNS write contract used by the relay when handling
	// identity registration.
	Provider = dns.Provider
	// ProviderFactory decides which Provider backs a given DNS provider name.
	// Use RegisterProvider to inject a Provider in tests.
	ProviderFactory = dns.ProviderFactory
	// Server is the relay's HTTP handler holder. Build one with NewServer.
	Server = irelay.Server
)

// NewProviderFactory returns a factory seeded with the real providers
// (cloudflare, hetzner, mock). Use RegisterProvider to shadow them in tests.
func NewProviderFactory(cfg Config) *ProviderFactory {
	return dns.NewProviderFactory(cfg)
}

// RegisterProvider installs a Provider under name in the given factory,
// shadowing the built-in implementation. Typically used from tests so that
// POST /identities writes records into a fake zone.
func RegisterProvider(f *ProviderFactory, name string, provider Provider) {
	f.Register(name, provider)
}

// NewServer builds a relay server from a config, resolver, and provider factory.
// The returned server exposes its HTTP routes via Router.
func NewServer(cfg Config, resolver Resolver, providers *ProviderFactory) *Server {
	return irelay.NewServer(cfg, resolver, providers)
}

// Router returns the HTTP handler tree served by the relay, suitable for
// passing to httptest.NewServer.
func Router(s *Server) http.Handler {
	return s.Router()
}
