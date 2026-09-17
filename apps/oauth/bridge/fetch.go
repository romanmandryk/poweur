package bridge

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"syscall"
	"time"
)

const (
	maxFetchBytes = 16 * 1024
	fetchTimeout  = 5 * time.Second
)

var errPrivateAddress = errors.New("refusing to connect to a private or loopback address")

// newSafeHTTPClient is the only client the bridge uses for URLs that someone
// else chose: identity documents, capabilities, profiles, client documents
// and JWKS. The address check runs on the socket actually being dialled, so a
// DNS answer that changes between a check and the connection cannot slip a
// private address through.
func newSafeHTTPClient(allowPrivate bool) *http.Client {
	dialer := &net.Dialer{
		Timeout: fetchTimeout,
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
	return &http.Client{
		Timeout: fetchTimeout,
		Transport: &http.Transport{
			Proxy:                  nil,
			DialContext:            dialer.DialContext,
			TLSHandshakeTimeout:    fetchTimeout,
			TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
			DisableKeepAlives:      true,
			MaxResponseHeaderBytes: 16 * 1024,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("redirects are not followed")
		},
	}
}

func publicIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return false
	}
	// Carrier-grade NAT, benchmarking, and documentation ranges are not the
	// public internet either. (IPv4-mapped addresses need no range of their
	// own: net.IP holds every IPv4 address in that form, and the checks above
	// already see through it.)
	for _, cidr := range nonPublicRanges {
		if cidr.Contains(ip) {
			return false
		}
	}
	return true
}

var nonPublicRanges = func() []*net.IPNet {
	var out []*net.IPNet
	for _, s := range []string{"100.64.0.0/10", "198.18.0.0/15", "192.0.0.0/24", "192.0.2.0/24",
		"198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "64:ff9b::/96", "2001:db8::/32"} {
		_, n, err := net.ParseCIDR(s)
		if err == nil {
			out = append(out, n)
		}
	}
	return out
}()

// fetchPublicJSON GETs a small JSON document from a URL someone else chose.
func (s *Server) fetchPublicJSON(ctx context.Context, rawURL string) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return nil, errors.New("not an absolute URL")
	}
	if u.Scheme != "https" && (s.secure || u.Scheme != "http") {
		return nil, errors.New("only https URLs are fetched")
	}
	if u.User != nil {
		return nil, errors.New("URLs with credentials are not fetched")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "poweur-oauth/"+Version)
	resp, err := s.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxFetchBytes {
		return nil, fmt.Errorf("document exceeds %d bytes", maxFetchBytes)
	}
	return body, nil
}

func (s *Server) httpClient() *http.Client {
	if s.cfg.ResolveOptions.HTTPClient != nil {
		return s.cfg.ResolveOptions.HTTPClient
	}
	return newSafeHTTPClient(s.cfg.ResolveOptions.AllowPrivate)
}
