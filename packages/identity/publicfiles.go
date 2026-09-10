package identity

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Fetching an identity's world-readable self-description (EPIC-006 E06-T2).
//
// `poweur-sys/public/` is served at `/.well-known/poweur/<file>` on the
// identity's own host — the same route and the same host that already serve
// `id.json`, which is why this needs no new relay endpoint. `id.json` says
// which keys an identity has; `profile.json` says who they are and
// `capabilities.json` says what they speak, and a client that resolves the
// first two and not the last two can verify a stranger without ever being
// able to show the user a name.
//
// Everything here is deliberately *best effort*. These documents are
// optional, they live on a host the caller does not control, and an identity
// with no profile is completely normal — so a caller that cannot fetch them
// must still be able to do its job. The resolver's guarantees (no redirects,
// size and time caps, no private IPs unless a test flag says otherwise) are
// reused wholesale rather than re-derived: this is a fetch of attacker-
// influenced content from a name the user typed, which is precisely the SSRF
// shape the resolver was hardened against.

const (
	// WellKnownProfilePath is the world route to poweur-sys/public/profile.json.
	WellKnownProfilePath = "/.well-known/poweur/profile.json"
	// WellKnownCapabilitiesPath is the world route to capabilities.json.
	WellKnownCapabilitiesPath = "/.well-known/poweur/capabilities.json"

	// MaxPublicDocumentBytes caps a public self-description. Larger than an
	// identity document (a bio and a link list are not keys) and far smaller
	// than anything worth streaming.
	MaxPublicDocumentBytes = 64 * 1024
)

// ErrPublicFileAbsent reports a well-known document the identity does not
// publish (HTTP 404). Callers distinguish "no profile" — the common case,
// and not an error worth showing — from "could not reach the host".
var ErrPublicFileAbsent = errors.New("identity does not publish this document")

// FetchPublicFile GETs one world-readable document from an identity's home.
// wellKnownPath must be an absolute /.well-known/poweur/… path.
func FetchPublicFile(ctx context.Context, identity, wellKnownPath string, opts ResolveOptions) ([]byte, error) {
	identity = strings.ToLower(strings.TrimSpace(identity))
	if err := ValidateIdentityName(identity); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(wellKnownPath, "/.well-known/poweur/") {
		return nil, fmt.Errorf("refusing to fetch %q: not a well-known poweur path", wellKnownPath)
	}
	if opts.Scheme == "" {
		opts.Scheme = "https"
	}
	if opts.Timeout == 0 {
		opts.Timeout = DefaultTimeout
	}
	if !opts.AllowPrivate {
		if err := checkIdentityHostSafe(ctx, identity); err != nil {
			return nil, err
		}
	}

	client := opts.HTTPClient
	if client == nil {
		client = newSafeHTTPClient(opts)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s://%s%s", opts.Scheme, identity, wellKnownPath), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrPublicFileAbsent
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("well-known %s returned %d", wellKnownPath, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxPublicDocumentBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > MaxPublicDocumentBytes {
		return nil, fmt.Errorf("well-known %s exceeds %d bytes", wellKnownPath, MaxPublicDocumentBytes)
	}
	return body, nil
}

// FetchProfile reads and validates an identity's public profile.
func FetchProfile(ctx context.Context, identity string, opts ResolveOptions) (Profile, error) {
	raw, err := FetchPublicFile(ctx, identity, WellKnownProfilePath, opts)
	if err != nil {
		return Profile{}, err
	}
	return ParseProfile(raw)
}

// FetchCapabilities reads and validates an identity's published capabilities.
func FetchCapabilities(ctx context.Context, identity string, opts ResolveOptions) (Capabilities, error) {
	raw, err := FetchPublicFile(ctx, identity, WellKnownCapabilitiesPath, opts)
	if err != nil {
		return Capabilities{}, err
	}
	return ParseCapabilities(raw)
}

// CapabilitiesFromDocument is the floor under FetchCapabilities: the identity
// document carries a bare `capabilities` string list, which always resolves
// even when the well-known route does not (a shared dev relay, an identity
// that is not yet on its own host). Same shape as the web client's fallback,
// so both surfaces degrade to the same answer instead of to two.
func CapabilitiesFromDocument(doc IdentityDocument) Capabilities {
	if len(doc.Capabilities) == 0 {
		return Capabilities{}
	}
	features := make(map[string]string, len(doc.Capabilities))
	for _, c := range doc.Capabilities {
		if c = strings.TrimSpace(c); c != "" {
			features[c] = "1"
		}
	}
	if len(features) == 0 {
		return Capabilities{}
	}
	return Capabilities{Version: 1, Features: features}
}

// AvatarURL turns a profile's tree path ("public/avatar.png") into the URL
// that serves it. Returns "" for an absent or non-conforming path: the schema
// says a tree path under public/, never an arbitrary URL, so rendering
// somebody's profile can never become a request to a host they chose.
func AvatarURL(identity, avatarPath, scheme string) string {
	avatarPath = strings.TrimSpace(avatarPath)
	if !strings.HasPrefix(avatarPath, "public/") {
		return ""
	}
	rest := strings.TrimPrefix(avatarPath, "public/")
	if rest == "" || strings.Contains(rest, "..") {
		return ""
	}
	if scheme == "" {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s/pub/%s", scheme, strings.ToLower(strings.TrimSpace(identity)), rest)
}
