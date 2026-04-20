// Package fakedns provides an in-memory DNS zone used to close the loop
// between the CLI, the relay, and "DNS" in integration tests that run
// entirely in-process.
//
// A Zone implements three contracts at once:
//
//   - github.com/eurything/api/internal/dns.Resolver   (read path used by the relay)
//   - github.com/eurything/cli/internal/identity.Resolver (read path used by the CLI)
//   - github.com/eurything/api/internal/dns.Provider via Provider() (write path
//     used by the relay when handling POST /identities)
//
// Because the same Zone object backs both read and write paths, writes the
// relay performs on behalf of "identity create" become immediately visible to
// any subsequent lookup from either the relay or the CLI.
package fakedns

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	relaypkg "github.com/eurything/api/pkg/relay"
)

// Zone is a tiny DNS stub keyed by fully qualified name.
// TXT records are stored as a slice (a domain can host multiple TXT strings),
// hosts are stored as a slice of host:port or IP strings,
// and CNAMEs are stored as a single target string per name.
//
// The Zone is safe for concurrent use.
type Zone struct {
	mu    sync.RWMutex
	txt   map[string][]string
	hosts map[string][]string
	cname map[string]string
}

// NewZone returns an empty zone.
func NewZone() *Zone {
	return &Zone{
		txt:   map[string][]string{},
		hosts: map[string][]string{},
		cname: map[string]string{},
	}
}

// SetTXT replaces the TXT records for a name.
func (z *Zone) SetTXT(name string, values ...string) {
	z.mu.Lock()
	defer z.mu.Unlock()
	z.txt[name] = append([]string{}, values...)
}

// AppendTXT appends TXT records without replacing existing ones.
func (z *Zone) AppendTXT(name string, values ...string) {
	z.mu.Lock()
	defer z.mu.Unlock()
	z.txt[name] = append(z.txt[name], values...)
}

// SetHost replaces the host records for a name.
func (z *Zone) SetHost(name string, hosts ...string) {
	z.mu.Lock()
	defer z.mu.Unlock()
	z.hosts[name] = append([]string{}, hosts...)
}

// SetCNAME sets (or replaces) the CNAME target for a name.
func (z *Zone) SetCNAME(name, target string) {
	z.mu.Lock()
	defer z.mu.Unlock()
	z.cname[name] = target
}

// Snapshot returns a copy of the zone for debugging.
func (z *Zone) Snapshot() map[string][]string {
	z.mu.RLock()
	defer z.mu.RUnlock()
	out := map[string][]string{}
	for k, v := range z.txt {
		out["TXT:"+k] = append([]string{}, v...)
	}
	for k, v := range z.hosts {
		out["HOST:"+k] = append([]string{}, v...)
	}
	for k, v := range z.cname {
		out["CNAME:"+k] = []string{v}
	}
	return out
}

// LookupTXT satisfies both api/internal/dns.Resolver and
// cli/internal/identity.Resolver.
func (z *Zone) LookupTXT(_ context.Context, name string) ([]string, error) {
	z.mu.RLock()
	defer z.mu.RUnlock()
	if values, ok := z.txt[name]; ok {
		return append([]string{}, values...), nil
	}
	return nil, fmt.Errorf("fakedns: no TXT records for %q", name)
}

// LookupHost satisfies both api/internal/dns.Resolver and
// cli/internal/identity.Resolver.
func (z *Zone) LookupHost(_ context.Context, name string) ([]string, error) {
	z.mu.RLock()
	hosts, ok := z.hosts[name]
	target, hasCNAME := z.cname[name]
	z.mu.RUnlock()
	if ok {
		return append([]string{}, hosts...), nil
	}
	if hasCNAME {
		return z.LookupHost(context.Background(), target)
	}
	return nil, fmt.Errorf("fakedns: no host records for %q", name)
}

// LookupCNAME is required by cli/internal/identity.Resolver.
func (z *Zone) LookupCNAME(_ context.Context, name string) (string, error) {
	z.mu.RLock()
	defer z.mu.RUnlock()
	if target, ok := z.cname[name]; ok {
		if !strings.HasSuffix(target, ".") {
			target += "."
		}
		return target, nil
	}
	return "", errors.New("fakedns: no CNAME")
}

// Provider returns a relay.Provider implementation that writes into this
// zone. Hand this to the relay's ProviderFactory via relay.RegisterProvider.
func (z *Zone) Provider() relaypkg.Provider {
	return zoneProvider{zone: z}
}

type zoneProvider struct {
	zone *Zone
}

func (p zoneProvider) WriteIdentityRecords(_ context.Context, _, identity, publicKey, encryptionPublicKey, relayAddress string) error {
	// Record the long-lived Ed25519 identity key.
	p.zone.SetTXT("_eurything."+identity, "eurything-pubkey=ed25519:"+publicKey)

	if encryptionPublicKey != "" {
		p.zone.SetTXT("_eurything-enc."+identity, "eurything-enckey=x25519:"+encryptionPublicKey)
	}

	// Deliberately keep the raw relay address (including port) so
	// httptest-based relays remain reachable. The production Cloudflare /
	// Hetzner providers strip ports because real DNS can't carry them; here
	// we want the loop to stay closed.
	p.zone.SetHost(identity, relayAddress)
	return nil
}

func (p zoneProvider) WriteEncryptionKey(_ context.Context, _, identity, encryptionPublicKey string) error {
	if encryptionPublicKey == "" {
		return errors.New("fakedns: missing encryption public key")
	}
	p.zone.SetTXT("_eurything-enc."+identity, "eurything-enckey=x25519:"+encryptionPublicKey)
	return nil
}
