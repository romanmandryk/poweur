package identity

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/poweur/cli/internal/config"
	idpkg "github.com/poweur/identity"
)

func decodeBase64(value string) ([]byte, error) {
	if decoded, err := base64.RawURLEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	if decoded, err := base64.URLEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	if decoded, err := base64.RawStdEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	return base64.StdEncoding.DecodeString(value)
}

// Resolver abstracts the DNS lookups the CLI performs to discover an
// identity's public key, encryption key, relay routing, and canonical name.
//
// The default implementation wraps net.DefaultResolver; integration tests
// swap in an in-memory fake via SetResolver so the CLI, relay, and DNS can
// all share one test zone inside a single process.
type Resolver interface {
	LookupTXT(ctx context.Context, name string) ([]string, error)
	LookupHost(ctx context.Context, name string) ([]string, error)
	LookupCNAME(ctx context.Context, name string) (string, error)
}

type netResolverAdapter struct {
	inner *net.Resolver
}

func (n netResolverAdapter) LookupTXT(ctx context.Context, name string) ([]string, error) {
	return n.inner.LookupTXT(ctx, name)
}

func (n netResolverAdapter) LookupHost(ctx context.Context, name string) ([]string, error) {
	return n.inner.LookupHost(ctx, name)
}

func (n netResolverAdapter) LookupCNAME(ctx context.Context, name string) (string, error) {
	return n.inner.LookupCNAME(ctx, name)
}

var defaultResolver Resolver = netResolverAdapter{inner: net.DefaultResolver}

// SetResolver replaces the package-level DNS resolver used by LookupDNS and
// LookupEncryptionKey. Intended for integration tests. Production code should
// never call this.
func SetResolver(r Resolver) {
	if r == nil {
		defaultResolver = netResolverAdapter{inner: net.DefaultResolver}
		return
	}
	defaultResolver = r
}

// ResetResolver restores the net.DefaultResolver-backed resolver. Tests should
// defer this after SetResolver to avoid leaking state into other tests.
func ResetResolver() {
	defaultResolver = netResolverAdapter{inner: net.DefaultResolver}
}

// CustomNetResolver returns a Resolver that routes all DNS queries through the
// given server (host[:port], defaulting to port 53) using Go's pure-Go DNS
// client. Useful when the local/system resolver drops TXT records, caches
// stale NXDOMAIN responses, or otherwise cannot see the authoritative answer.
// Wired up from the CLI entry point when DNS_SERVER is set.
func CustomNetResolver(server string) Resolver {
	server = strings.TrimSpace(server)
	if server == "" {
		return netResolverAdapter{inner: net.DefaultResolver}
	}
	if _, _, err := net.SplitHostPort(server); err != nil {
		server = net.JoinHostPort(server, "53")
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	return netResolverAdapter{inner: &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			switch network {
			case "udp", "udp4", "udp6":
				network = "udp"
			default:
				network = "tcp"
			}
			return dialer.DialContext(ctx, network, server)
		},
	}}
}

type DNSStatus struct {
	Identity         string   `json:"identity"`
	PublicKeyTXT     string   `json:"public_key_txt"`
	EncryptionKeyTXT string   `json:"encryption_key_txt,omitempty"`
	RelayHosts       []string `json:"relay_hosts"`
	CNAME            string   `json:"cname"`
}

func LookupDNS(ctx context.Context, identity string) (DNSStatus, error) {
	status := DNSStatus{Identity: identity}
	txtRecords, _ := defaultResolver.LookupTXT(ctx, "_poweur."+identity)
	for _, record := range txtRecords {
		record = strings.TrimSpace(record)
		if strings.HasPrefix(record, "poweur-pubkey=") {
			status.PublicKeyTXT = record
			break
		}
	}

	encRecords, _ := defaultResolver.LookupTXT(ctx, "_poweur-enc."+identity)
	for _, record := range encRecords {
		record = strings.TrimSpace(record)
		if strings.HasPrefix(record, "poweur-enckey=") {
			status.EncryptionKeyTXT = record
			break
		}
	}

	if cname, err := defaultResolver.LookupCNAME(ctx, identity); err == nil {
		status.CNAME = strings.TrimSuffix(cname, ".")
	}
	if hosts, err := defaultResolver.LookupHost(ctx, identity); err == nil {
		status.RelayHosts = hosts
	}
	return status, nil
}

// LookupEncryptionKey returns the raw X25519 public key bytes for the given identity,
// or nil if the identity has no published encryption key.
// Resolution is web-first (/.well-known/poweur/id.json) then DNS TXT for the
// full identity document; if that fails, falls back to `_poweur-enc` TXT only.
func LookupEncryptionKey(ctx context.Context, identityName string) ([]byte, error) {
	key, _, err := LookupEncryptionKeyCached(ctx, identityName)
	return key, err
}

// LookupEncryptionKeyCached is LookupEncryptionKey that also returns how the
// identity resolved, so a caller can tell the user when the key came from a
// cached document (res.Stale). When the identity can't be resolved at all and
// nothing is cached, the error says so, instead of the caller concluding
// that the recipient publishes no encryption key.
func LookupEncryptionKeyCached(ctx context.Context, identityName string) ([]byte, idpkg.Result, error) {
	res, resolveErr := ResolveIdentityCached(ctx, identityName)
	if resolveErr == nil && res.Document.EncryptionPublicKey != "" {
		key, err := idpkg.ParseX25519PublicKey(res.Document.EncryptionPublicKey)
		return key, res, err
	}
	key, err := lookupEncryptionKeyDNS(ctx, identityName)
	if resolveErr != nil && len(key) == 0 {
		return nil, idpkg.Result{}, fmt.Errorf("no cached identity for %s; try again when it resolves (%w)", identityName, resolveErr)
	}
	return key, res, err
}

func lookupEncryptionKeyDNS(ctx context.Context, identityName string) ([]byte, error) {
	records, err := defaultResolver.LookupTXT(ctx, "_poweur-enc."+identityName)
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		record = strings.TrimSpace(record)
		if !strings.HasPrefix(record, "poweur-enckey=") {
			continue
		}
		parts := strings.SplitN(record, "=", 2)
		if len(parts) != 2 {
			continue
		}
		value := strings.TrimPrefix(parts[1], "x25519:")
		decoded, err := decodeBase64(value)
		if err != nil {
			continue
		}
		if len(decoded) == 32 {
			return decoded, nil
		}
	}
	return nil, nil
}

// ResolveIdentity resolves via the shared web-first resolver chain. The
// answer is always fresh: pinning a contact, verifying a share or a group
// never acts on a cached document. Every verified document it fetches is
// written to the identity cache.
func ResolveIdentity(ctx context.Context, identityName string) (idpkg.Result, error) {
	opts := resolveOptions()
	opts.TXT = txtAdapter{}
	return idpkg.Resolve(ctx, identityName, opts)
}

// ResolveIdentityCached is ResolveIdentity for sending to someone already
// resolved before (EPIC-029 E29-T3): when their web and DNS can't be reached
// it returns the cached document, marked Stale, if it is no older than
// MaxStale. A "not found", a bad signature or a key mismatch is still an error.
func ResolveIdentityCached(ctx context.Context, identityName string) (idpkg.Result, error) {
	opts := resolveOptions()
	opts.TXT = txtAdapter{}
	opts.MaxStale = maxStale
	return idpkg.Resolve(ctx, identityName, opts)
}

// DefaultMaxStale is how old a cached identity document may be and still be
// used to send.
const DefaultMaxStale = 30 * 24 * time.Hour

var maxStale = DefaultMaxStale

// SetMaxStale sets how old a cached document ResolveIdentityCached may
// return; zero turns the fallback off.
func SetMaxStale(d time.Duration) {
	if d < 0 {
		d = 0
	}
	maxStale = d
}

// MaxStale is the current limit.
func MaxStale() time.Duration { return maxStale }

// identityStore is the on-disk cache under ~/.poweur. It is looked up on
// every call because tests move HOME between runs.
func identityStore() idpkg.Store {
	dir, err := config.IdentityCacheDir()
	if err != nil {
		return nil
	}
	return idpkg.NewFileStore(dir, 0)
}

// FetchProfile reads an identity's world-readable profile.json (E06-T2),
// served at /.well-known/poweur/ from their own host — the same route and
// the same resolver hardening as id.json, so this needs no new endpoint and
// no new SSRF story. Absent profiles return idpkg.ErrPublicFileAbsent.
func FetchProfile(ctx context.Context, identityName string) (idpkg.Profile, error) {
	return idpkg.FetchProfile(ctx, identityName, resolveOptions())
}

// FetchCapabilities reads an identity's capabilities.json (E06-T2).
func FetchCapabilities(ctx context.Context, identityName string) (idpkg.Capabilities, error) {
	return idpkg.FetchCapabilities(ctx, identityName, resolveOptions())
}

// ResolveScheme is the scheme the resolver is configured for ("https", or
// "http" under local/integration overrides). Callers building a URL into an
// identity's own host — an avatar, say — must use the same one.
func ResolveScheme() string { return resolveScheme }

type txtAdapter struct{}

func (txtAdapter) LookupTXT(ctx context.Context, name string) ([]string, error) {
	return defaultResolver.LookupTXT(ctx, name)
}

var (
	resolveScheme       = "https"
	resolveAllowPrivate bool
	resolveHTTPClient   *http.Client
	resolveDialAddr     string
	// resolveDialHost, when set, picks the address per host (tests with
	// several relays); an empty answer falls back to resolveDialAddr.
	resolveDialHost func(host string) string
)

// ConfigureResolverHosts is ConfigureResolver with a per-host dial address.
func ConfigureResolverHosts(scheme string, allowPrivate bool, dialHost func(host string) string) {
	ConfigureResolver(scheme, allowPrivate, "", nil)
	resolveDialHost = dialHost
}

// ConfigureResolver sets web-first resolve options (used by CLI entrypoint and tests).
func ConfigureResolver(scheme string, allowPrivate bool, dialAddr string, client *http.Client) {
	if scheme != "" {
		resolveScheme = scheme
	}
	resolveAllowPrivate = allowPrivate
	resolveDialAddr = dialAddr
	resolveDialHost = nil
	resolveHTTPClient = client
}

func resolveOptions() idpkg.ResolveOptions {
	opts := idpkg.ResolveOptions{
		Scheme:       resolveScheme,
		AllowPrivate: resolveAllowPrivate,
		HTTPClient:   resolveHTTPClient,
		Store:        identityStore(),
	}
	if (resolveDialAddr != "" || resolveDialHost != nil) && opts.HTTPClient == nil {
		fixed, perHost := resolveDialAddr, resolveDialHost
		opts.HTTPClient = &http.Client{
			Timeout: idpkg.DefaultTimeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return errors.New("redirects not allowed")
			},
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
					target := fixed
					if perHost != nil {
						host, _, err := net.SplitHostPort(addr)
						if err != nil {
							host = addr
						}
						if picked := perHost(host); picked != "" {
							target = picked
						}
					}
					if target == "" {
						target = addr
					}
					return (&net.Dialer{Timeout: idpkg.DefaultTimeout}).DialContext(ctx, network, target)
				},
			},
		}
	}
	return opts
}

// FetchGroupPublicKey reads a group identity's current public key (EPIC-024
// E24-T3), verified against the group's own signing key.
func FetchGroupPublicKey(ctx context.Context, group string) (idpkg.GroupPublicKey, error) {
	return idpkg.FetchGroupPublicKey(ctx, group, resolveOptions())
}
