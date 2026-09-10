package signin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/poweur/identity"
)

// MetadataPath is where a relying party publishes its Sign-In metadata.
// It is the only "federation" document in the protocol, it is served by the
// RP itself, and nobody has to register it anywhere.
const MetadataPath = "/.well-known/poweur.json"

// MaxMetadataBytes caps what a signer will read from an RP.
const MaxMetadataBytes = 16 * 1024

// Metadata is the relying-party description a signer fetches over TLS from
// the origin named in a request's `audience`.
//
// This fetch is how a signer *verifies* an origin. Anyone can put any
// audience in a request; only the operator of that origin can serve a
// document at it over a valid TLS certificate. A signer that renders
// "guestbook.poweur.net wants to sign you in" without this check is showing
// the user an attacker-controlled string.
type Metadata struct {
	PoweurAuth string `json:"poweur_auth"`
	// Origin must equal the origin the document was fetched from.
	Origin string `json:"origin"`
	// Name is the human-facing application name for the consent screen.
	Name string `json:"name"`
	// LogoURI is optional and must be same-origin (a signer that loads a
	// third-party image leaks the approval to that third party).
	LogoURI string `json:"logo_uri,omitempty"`
	// ResponseURIs enumerates every URI this RP will accept an approval at.
	// A request whose response_uri is not listed here is refused by the
	// signer even though it is same-origin — this is the per-RP allowlist
	// that stops an open redirect on the RP from becoming an approval leak.
	ResponseURIs []string `json:"response_uris,omitempty"`
	// Scopes the RP may ask for. Advisory for the signer, informative for
	// the user; the relay enforces the namespace independently.
	Scopes []string `json:"scopes,omitempty"`
	// AppID is the tree namespace this RP writes into. It MUST equal the
	// reverse-DNS of Origin's host; it is published so a user reading the
	// document can see which folder the app will use.
	AppID string `json:"app_id,omitempty"`
	// Transports the RP supports: "redirect", "qr", "deeplink", "poll".
	Transports []string `json:"transports,omitempty"`
	// PollURI is where the RP's own front end polls for a cross-device
	// approval it is waiting on. Must be same-origin.
	PollURI string `json:"poll_uri,omitempty"`
	// ContactURI is where a user reports abuse by this RP.
	ContactURI string `json:"contact_uri,omitempty"`
}

// Validate checks a metadata document against the origin it was served from.
func (m Metadata) Validate(servedFrom string) error {
	if m.PoweurAuth != identity.SignInVersion {
		return fmt.Errorf("%w: metadata poweur_auth is %q", identity.ErrSignInVersion, m.PoweurAuth)
	}
	origin, err := identity.NormalizeOrigin(m.Origin)
	if err != nil {
		return fmt.Errorf("metadata origin: %w", err)
	}
	if servedFrom != "" {
		want, err := identity.NormalizeOrigin(servedFrom)
		if err != nil {
			return err
		}
		if origin != want {
			return fmt.Errorf("%w: metadata at %s claims origin %s", identity.ErrSignInAudience, want, origin)
		}
	}
	if strings.TrimSpace(m.Name) == "" {
		return errors.New("metadata name is required")
	}
	for _, u := range m.ResponseURIs {
		if !identity.SameOrigin(origin, u) {
			return fmt.Errorf("%w: response_uri %q is not same-origin with %s", identity.ErrSignInAudience, u, origin)
		}
	}
	if m.PollURI != "" && !identity.SameOrigin(origin, m.PollURI) {
		return fmt.Errorf("%w: poll_uri is not same-origin with %s", identity.ErrSignInAudience, origin)
	}
	if m.LogoURI != "" && !identity.SameOrigin(origin, m.LogoURI) {
		return fmt.Errorf("%w: logo_uri is not same-origin with %s", identity.ErrSignInAudience, origin)
	}
	if m.AppID != "" {
		want, err := identity.SignInAppID(origin)
		if err != nil {
			return err
		}
		if m.AppID != want {
			return fmt.Errorf("%w: app_id %q does not match the origin's namespace %q", identity.ErrSignInScope, m.AppID, want)
		}
	}
	for _, s := range m.Scopes {
		n, err := identity.NormalizeSignInScope(s)
		if err != nil {
			return err
		}
		appID, err := identity.SignInAppID(origin)
		if err != nil {
			return err
		}
		if err := identity.CheckSignInScopeNamespace(n, appID); err != nil {
			return err
		}
	}
	return nil
}

// AllowsResponseURI reports whether the RP has published uri as a delivery
// target. An RP that publishes no response_uris accepts any same-origin one
// (the permissive default for a demo); publishing the list is the hardened
// posture and is what the tutorial recommends.
func (m Metadata) AllowsResponseURI(uri string) bool {
	if strings.TrimSpace(uri) == "" {
		return true
	}
	if !identity.SameOrigin(m.Origin, uri) {
		return false
	}
	if len(m.ResponseURIs) == 0 {
		return true
	}
	for _, allowed := range m.ResponseURIs {
		if strings.EqualFold(strings.TrimRight(allowed, "/"), strings.TrimRight(uri, "/")) {
			return true
		}
	}
	return false
}

// FetchOptions configure FetchMetadata.
type FetchOptions struct {
	// HTTPClient overrides the default. The default refuses redirects: a
	// redirect would let one origin answer for another, which is exactly
	// the confusion this fetch exists to prevent.
	HTTPClient *http.Client
	Timeout    time.Duration
}

// FetchMetadata retrieves and validates the RP metadata published at origin.
// A signer calls this before showing the user any RP-supplied string.
func FetchMetadata(ctx context.Context, origin string, opts FetchOptions) (Metadata, error) {
	norm, err := identity.NormalizeOrigin(origin)
	if err != nil {
		return Metadata{}, err
	}
	client := opts.HTTPClient
	if client == nil {
		timeout := opts.Timeout
		if timeout == 0 {
			timeout = identity.DefaultTimeout
		}
		client = &http.Client{
			Timeout: timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return errors.New("redirects are not followed when fetching RP metadata")
			},
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, norm+MetadataPath, nil)
	if err != nil {
		return Metadata{}, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return Metadata{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Metadata{}, fmt.Errorf("relying-party metadata at %s returned %d", norm+MetadataPath, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxMetadataBytes+1))
	if err != nil {
		return Metadata{}, err
	}
	if len(raw) > MaxMetadataBytes {
		return Metadata{}, fmt.Errorf("relying-party metadata larger than %d bytes", MaxMetadataBytes)
	}
	var m Metadata
	if err := json.Unmarshal(raw, &m); err != nil {
		return Metadata{}, fmt.Errorf("relying-party metadata is not JSON: %w", err)
	}
	if err := m.Validate(norm); err != nil {
		return Metadata{}, err
	}
	return m, nil
}

// CheckRequestAgainstMetadata is the signer-side origin verification: the
// request's audience must be the origin the metadata came from, and its
// response_uri must be one the RP itself published.
//
// Callers pass the metadata they fetched from the request's audience. The
// returned metadata is the trusted source for names shown to the user —
// never the request's own `domain` or `statement` alone.
func CheckRequestAgainstMetadata(req identity.SignInRequest, meta Metadata) error {
	audience, err := identity.NormalizeOrigin(req.Audience)
	if err != nil {
		return err
	}
	if err := meta.Validate(audience); err != nil {
		return err
	}
	if !meta.AllowsResponseURI(req.ResponseURI) {
		return fmt.Errorf("%w: response_uri %q is not published by %s", identity.ErrSignInAudience, req.ResponseURI, audience)
	}
	return nil
}
