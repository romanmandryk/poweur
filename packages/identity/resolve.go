package identity

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"
)

const (
	MaxDocumentBytes = 16 * 1024
	DefaultTimeout   = 5 * time.Second
	WellKnownPath    = "/.well-known/poweur/id.json"
)

// Source indicates where an identity was resolved from.
type Source string

const (
	SourceWeb  Source = "web"
	SourceDNS  Source = "dns"
	SourceBoth Source = "both"
)

// TXTLookup looks up DNS TXT records (injectable for tests).
type TXTLookup interface {
	LookupTXT(ctx context.Context, name string) ([]string, error)
}

// ResolveOptions configure Resolve.
type ResolveOptions struct {
	// Scheme is "https" (default) or "http" for local/integration tests.
	Scheme string
	// AllowPrivate permits loopback/private IPs (tests/dev only).
	AllowPrivate bool
	// Timeout for HTTP fetch; default DefaultTimeout.
	Timeout time.Duration
	// HTTPClient overrides the client; if nil a safe default is built that
	// refuses redirects and non-public dial addresses (unless AllowPrivate).
	// A caller that supplies its own client owns that address policy.
	HTTPClient *http.Client
	// TXT is optional DNS fallback.
	TXT TXTLookup
	// SkipWeb skips HTTPS well-known (DNS-only).
	SkipWeb bool
	// SkipDNS skips DNS fallback.
	SkipDNS bool
	// Cache, when set, stores successful results for CacheTTL (default 5m,
	// matching well-known Cache-Control max-age=300).
	Cache *Cache
	// CacheTTL overrides the default cache duration.
	CacheTTL time.Duration
	// SkipMovedTo disables following moved_to (used when already following once).
	SkipMovedTo bool
}

// Result is a verified identity document plus resolution metadata.
type Result struct {
	Document IdentityDocument
	Source   Source
}

// Resolve fetches and verifies an identity document (web-first, then DNS).
func Resolve(ctx context.Context, identity string, opts ResolveOptions) (Result, error) {
	identity = strings.ToLower(strings.TrimSpace(identity))
	if err := ValidateIdentityName(identity); err != nil {
		return Result{}, err
	}
	if opts.Scheme == "" {
		opts.Scheme = "https"
	}
	if opts.Timeout == 0 {
		opts.Timeout = DefaultTimeout
	}
	if opts.CacheTTL == 0 {
		opts.CacheTTL = 5 * time.Minute
	}
	if cached, ok := opts.Cache.Get(identity); ok {
		return cached, nil
	}

	var webDoc *IdentityDocument
	var webErr error
	if !opts.SkipWeb {
		doc, err := resolveWeb(ctx, identity, opts)
		if err == nil {
			webDoc = &doc
		} else {
			webErr = err
		}
	}

	var dnsDoc *IdentityDocument
	var dnsErr error
	if !opts.SkipDNS && opts.TXT != nil {
		doc, err := resolveDNS(ctx, identity, opts.TXT)
		if err == nil {
			dnsDoc = &doc
		} else {
			dnsErr = err
		}
	}

	var result Result
	switch {
	case webDoc != nil && dnsDoc != nil:
		if NormalizePublicKeyKey(webDoc.PublicKey) != NormalizePublicKeyKey(dnsDoc.PublicKey) {
			return Result{}, errors.New("identity key mismatch between web and DNS sources")
		}
		result = Result{Document: *webDoc, Source: SourceBoth}
	case webDoc != nil:
		result = Result{Document: *webDoc, Source: SourceWeb}
	case dnsDoc != nil:
		result = Result{Document: *dnsDoc, Source: SourceDNS}
	default:
		if webErr != nil && dnsErr != nil {
			return Result{}, fmt.Errorf("identity not found: web: %v; dns: %v", webErr, dnsErr)
		}
		if webErr != nil {
			return Result{}, fmt.Errorf("identity not found via web: %w", webErr)
		}
		if dnsErr != nil {
			return Result{}, fmt.Errorf("identity not found via dns: %w", dnsErr)
		}
		return Result{}, errors.New("identity not found")
	}
	// Follow hosted migration tombstone once (no chains).
	if !opts.SkipMovedTo && result.Document.MovedTo != "" {
		target := strings.ToLower(strings.TrimSpace(result.Document.MovedTo))
		if target != "" && target != identity {
			followed, err := Resolve(ctx, target, ResolveOptions{
				Scheme:       opts.Scheme,
				AllowPrivate: opts.AllowPrivate,
				Timeout:      opts.Timeout,
				HTTPClient:   opts.HTTPClient,
				TXT:          opts.TXT,
				SkipWeb:      opts.SkipWeb,
				SkipDNS:      opts.SkipDNS,
				Cache:        opts.Cache,
				CacheTTL:     opts.CacheTTL,
				SkipMovedTo:  true,
			})
			if err == nil {
				result = followed
			}
		}
	}

	opts.Cache.Put(identity, result, opts.CacheTTL)
	return result, nil
}

func resolveWeb(ctx context.Context, identity string, opts ResolveOptions) (IdentityDocument, error) {
	url := fmt.Sprintf("%s://%s%s", opts.Scheme, identity, WellKnownPath)
	client := opts.HTTPClient
	if client == nil {
		client = newSafeHTTPClient(opts)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return IdentityDocument{}, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return IdentityDocument{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return IdentityDocument{}, fmt.Errorf("well-known returned %d", resp.StatusCode)
	}
	limited := io.LimitReader(resp.Body, MaxDocumentBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return IdentityDocument{}, err
	}
	if len(body) > MaxDocumentBytes {
		return IdentityDocument{}, errors.New("identity document exceeds 16KB")
	}
	doc, err := ParseDocument(body, true)
	if err != nil {
		return IdentityDocument{}, err
	}
	if !strings.EqualFold(doc.Identity, identity) {
		return IdentityDocument{}, errors.New("document identity does not match requested identity")
	}
	return doc, nil
}

func resolveDNS(ctx context.Context, identity string, txt TXTLookup) (IdentityDocument, error) {
	records, err := txt.LookupTXT(ctx, "_poweur."+identity)
	if err != nil {
		return IdentityDocument{}, err
	}
	pub := ""
	for _, r := range records {
		r = strings.TrimSpace(r)
		if strings.HasPrefix(r, "poweur-pubkey=") {
			pub = strings.TrimPrefix(r, "poweur-pubkey=")
			break
		}
	}
	if pub == "" {
		return IdentityDocument{}, errors.New("no poweur-pubkey TXT record")
	}
	// Normalize to ed25519: prefix for document consistency
	if !strings.HasPrefix(pub, "ed25519:") {
		pub = "ed25519:" + pub
	}

	enc := ""
	encRecords, _ := txt.LookupTXT(ctx, "_poweur-enc."+identity)
	for _, r := range encRecords {
		r = strings.TrimSpace(r)
		if strings.HasPrefix(r, "poweur-enckey=") {
			enc = strings.TrimPrefix(r, "poweur-enckey=")
			break
		}
	}

	doc := IdentityDocument{
		Version:             1,
		Identity:            identity,
		PublicKey:           pub,
		EncryptionPublicKey: enc,
		Relay:               identity, // DNS path: A/CNAME on identity is the relay
		UpdatedAt:           time.Now().UTC().Format(time.RFC3339),
		// DNS-derived docs are not signed; Verify is skipped for SourceDNS-only
		// when built here — callers that need signatures use web. We mark
		// Signature empty and do not call Verify.
	}
	return doc, nil
}

// errPrivateAddress is returned when the dialled address is not on the public
// internet and the caller did not set AllowPrivate.
var errPrivateAddress = errors.New("refusing to fetch identity document from a private, loopback or reserved address")

// newSafeHTTPClient is the default client for hosts that someone else chose.
// The address check runs in the dialer's Control hook, on the socket actually
// being dialled, so a DNS answer that changes between a check and the
// connection (DNS rebinding) cannot slip a private address through.
func newSafeHTTPClient(opts ResolveOptions) *http.Client {
	allowPrivate := opts.AllowPrivate
	dialer := &net.Dialer{
		Timeout: opts.Timeout,
		Control: func(network, address string, _ syscall.RawConn) error {
			if allowPrivate {
				return nil
			}
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip := net.ParseIP(host)
			if ip == nil || !publicIP(ip) {
				return errPrivateAddress
			}
			return nil
		},
	}
	transport := &http.Transport{
		Proxy:                  nil,
		DialContext:            dialer.DialContext,
		TLSHandshakeTimeout:    opts.Timeout,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
		DisableKeepAlives:      true,
		MaxResponseHeaderBytes: 16 * 1024,
	}
	return &http.Client{
		Timeout:   opts.Timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return errors.New("redirects are not allowed when resolving identity documents")
		},
	}
}

// publicIP reports whether ip is a routable address on the public internet.
func publicIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return false
	}
	// "This network", carrier-grade NAT, benchmarking, documentation and
	// reserved ranges are not the public internet either. (IPv4-mapped
	// addresses need no range of their own: net.IP holds every IPv4 address
	// in that form, and the checks above already see through it.)
	for _, cidr := range nonPublicRanges {
		if cidr.Contains(ip) {
			return false
		}
	}
	return true
}

var nonPublicRanges = func() []*net.IPNet {
	var out []*net.IPNet
	for _, s := range []string{
		"0.0.0.0/8",       // "this network"
		"100.64.0.0/10",   // carrier-grade NAT
		"192.0.0.0/24",    // IETF protocol assignments
		"192.0.2.0/24",    // TEST-NET-1
		"198.18.0.0/15",   // benchmarking
		"198.51.100.0/24", // TEST-NET-2
		"203.0.113.0/24",  // TEST-NET-3
		"240.0.0.0/4",     // reserved, incl. 255.255.255.255
		"64:ff9b::/96",    // NAT64 (embeds an IPv4 address)
		"64:ff9b:1::/48",  // local-use NAT64
		"2001:db8::/32",   // documentation
	} {
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			panic(err)
		}
		out = append(out, n)
	}
	return out
}()
