package identity

import (
	"context"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

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
	res, err := ResolveIdentity(ctx, identityName)
	if err == nil && res.Document.EncryptionPublicKey != "" {
		return idpkg.ParseX25519PublicKey(res.Document.EncryptionPublicKey)
	}
	return lookupEncryptionKeyDNS(ctx, identityName)
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

// ResolveIdentity resolves via the shared web-first resolver chain.
func ResolveIdentity(ctx context.Context, identityName string) (idpkg.Result, error) {
	opts := resolveOptions()
	opts.TXT = txtAdapter{}
	return idpkg.Resolve(ctx, identityName, opts)
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
