package dns

import (
	"context"
	"encoding/json"
	"os"
	"strings"
)

// HostsFileResolver answers LookupHost from a JSON file of name → address
// ({"alice.poweur.net": "127.0.0.1:5001"}) before asking Next. It exists so
// several local relays (tests, development) can reach each other's hosted
// identities without real DNS; main wires it only with RESOLVER_ALLOW_PRIVATE.
// The file is read on every lookup, so identities can be added while relays run.
type HostsFileResolver struct {
	Path string
	Next Resolver
}

func (r *HostsFileResolver) LookupTXT(ctx context.Context, name string) ([]string, error) {
	return r.Next.LookupTXT(ctx, name)
}

func (r *HostsFileResolver) LookupHost(ctx context.Context, name string) ([]string, error) {
	if raw, err := os.ReadFile(r.Path); err == nil {
		var hosts map[string]string
		if json.Unmarshal(raw, &hosts) == nil {
			if addr := hosts[strings.ToLower(strings.TrimSuffix(name, "."))]; addr != "" {
				return []string{addr}, nil
			}
		}
	}
	return r.Next.LookupHost(ctx, name)
}
