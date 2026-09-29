// Package cli is the public entry point of the Poweur ID CLI. It re-exports
// the internal command runner so external consumers (notably integration
// tests that live in a separate Go module) can invoke the CLI in-process
// without shelling out.
package cli

import (
	"io"

	internalcli "github.com/poweur/cli/internal/cli"
	"github.com/poweur/cli/internal/identity"
)

// Run executes a single CLI invocation and returns its exit code.
// It is a thin pass-through to the internal implementation.
func Run(args []string, stdout, stderr io.Writer) int {
	return internalcli.Run(args, stdout, stderr)
}

// Resolver is the DNS contract the CLI consults for TXT/host/CNAME lookups.
// Integration tests implement this with an in-memory zone.
type Resolver = identity.Resolver

// SetDNSResolver replaces the DNS resolver used by the CLI. Intended for
// integration tests. Production callers should not touch this.
func SetDNSResolver(r Resolver) { identity.SetResolver(r) }

// ResetDNSResolver restores the default net.DefaultResolver-backed resolver.
func ResetDNSResolver() { identity.ResetResolver() }

// ConfigureIdentityResolver sets web-first resolve options for integration tests.
func ConfigureIdentityResolver(scheme string, allowPrivate bool, dialAddr string) {
	identity.ConfigureResolver(scheme, allowPrivate, dialAddr, nil)
}

// ConfigureIdentityResolverHosts is ConfigureIdentityResolver for tests with
// several relays: dialHost maps an identity host to the relay address serving
// it (an empty answer dials the host itself).
func ConfigureIdentityResolverHosts(scheme string, allowPrivate bool, dialHost func(host string) string) {
	identity.ConfigureResolverHosts(scheme, allowPrivate, dialHost)
}
