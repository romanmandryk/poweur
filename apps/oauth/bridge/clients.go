package bridge

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/poweur/identity/signin"
)

// Client sources.
const (
	ClientStatic     = "static"     // operator configuration
	ClientRegistered = "registered" // the developer console
	ClientURL        = "url"        // a URL client_id (IndieAuth)
)

// Registration postures and URL-client modes (Config).
const (
	RegistrationOpen      = "open"
	RegistrationAllowlist = "allowlist"
	RegistrationClosed    = "closed"

	URLClientsIndieAuth = "indieauth"
	URLClientsOn        = "on"
	URLClientsOff       = "off"
)

// Token endpoint authentication methods.
const (
	AuthSecretBasic = "client_secret_basic"
	AuthSecretPost  = "client_secret_post"
	AuthPrivateJWT  = "private_key_jwt"
	AuthNone        = "none"
)

const (
	maxRedirectURIs = 20
	maxClientName   = 80
)

var clientIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._~-]{1,127}$`)

// Client is an OAuth client, whatever its source.
type Client struct {
	ID           string   `json:"client_id"`
	Name         string   `json:"client_name"`
	RedirectURIs []string `json:"redirect_uris"`
	AuthMethod   string   `json:"token_endpoint_auth_method,omitempty"`

	// Secret is accepted only in the static-client file, and hashed on load.
	Secret string `json:"client_secret,omitempty"`
	// SecretSHA256 lets the static file avoid holding the plaintext.
	SecretSHA256 string         `json:"client_secret_sha256,omitempty"`
	Secrets      []ClientSecret `json:"secrets,omitempty"`

	JWKS    *JWKS  `json:"jwks,omitempty"`
	JWKSURI string `json:"jwks_uri,omitempty"`

	// Sector is the host pairwise subjects are derived from. Fixed at
	// creation: editing redirect URIs never changes a user's `sub`.
	Sector string `json:"sector,omitempty"`
	// FirstParty (static only) skips consent for `openid` alone.
	FirstParty bool `json:"first_party,omitempty"`

	ClientURI string `json:"client_uri,omitempty"`
	LogoURI   string `json:"logo_uri,omitempty"`

	Owners    []string  `json:"owners,omitempty"`
	CreatedBy string    `json:"created_by,omitempty"`
	CreatedAt time.Time `json:"created_at,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`

	Suspended       bool   `json:"suspended,omitempty"`
	SuspendedReason string `json:"suspended_reason,omitempty"`

	Source string `json:"source,omitempty"`

	// sameOriginRedirects: a URL client that published no redirect_uris may
	// return anywhere on its own origin (IndieAuth).
	sameOriginRedirects bool
}

// ClientSecret is a stored secret: its hash and, during rotation, when it
// stops working.
type ClientSecret struct {
	ID        string    `json:"id"`
	Hash      string    `json:"hash"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
}

// Public reports whether the client authenticates with nothing but PKCE.
func (c *Client) Public() bool { return c.AuthMethod == AuthNone }

// validate normalizes and checks a client definition.
func (c *Client) validate(allowHTTP bool) error {
	c.ID = strings.TrimSpace(c.ID)
	if c.Source != ClientURL && !clientIDPattern.MatchString(c.ID) {
		return errors.New("client_id must be 2–128 characters of letters, digits, . _ ~ -")
	}
	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" || len(c.Name) > maxClientName || strings.ContainsAny(c.Name, "\r\n<>") {
		return fmt.Errorf("client_name must be 1–%d characters on one line", maxClientName)
	}
	if len(c.RedirectURIs) == 0 {
		return errors.New("at least one redirect URI is required")
	}
	if len(c.RedirectURIs) > maxRedirectURIs {
		return fmt.Errorf("at most %d redirect URIs", maxRedirectURIs)
	}
	seen := map[string]bool{}
	var clean []string
	for _, raw := range c.RedirectURIs {
		u, err := checkRedirectURI(raw, allowHTTP)
		if err != nil {
			return fmt.Errorf("redirect URI %q: %w", raw, err)
		}
		if !seen[u] {
			seen[u] = true
			clean = append(clean, u)
		}
	}
	c.RedirectURIs = clean

	if c.Secret != "" {
		if c.Source != ClientStatic {
			return errors.New("client_secret may only be set in the static client file")
		}
		c.Secrets = append(c.Secrets, ClientSecret{ID: "static", Hash: signin.HashSecret(c.Secret)})
		c.Secret = ""
	}
	if c.SecretSHA256 != "" {
		c.Secrets = append(c.Secrets, ClientSecret{ID: "static-sha256", Hash: c.SecretSHA256})
		c.SecretSHA256 = ""
	}
	if c.AuthMethod == "" {
		switch {
		case len(c.Secrets) > 0:
			c.AuthMethod = AuthSecretBasic
		case c.JWKS != nil || c.JWKSURI != "":
			c.AuthMethod = AuthPrivateJWT
		default:
			c.AuthMethod = AuthNone
		}
	}
	switch c.AuthMethod {
	case AuthSecretBasic, AuthSecretPost:
		if len(c.Secrets) == 0 {
			return errors.New("a secret-authenticated client needs a secret")
		}
	case AuthPrivateJWT:
		if c.JWKS == nil && c.JWKSURI == "" {
			return errors.New("private_key_jwt needs jwks or jwks_uri")
		}
		if c.JWKSURI != "" {
			if u, err := url.Parse(c.JWKSURI); err != nil || u.Scheme != "https" || u.Host == "" {
				if !(allowHTTP && err == nil && u.Scheme == "http") {
					return errors.New("jwks_uri must be an https URL")
				}
			}
		}
	case AuthNone:
		if len(c.Secrets) > 0 {
			return errors.New("a public client has no secret")
		}
	default:
		return fmt.Errorf("unsupported token_endpoint_auth_method %q", c.AuthMethod)
	}
	if c.FirstParty && c.Source != ClientStatic {
		return errors.New("only static clients may be first-party")
	}

	c.Sector = strings.ToLower(strings.TrimSpace(c.Sector))
	if c.Sector == "" {
		c.Sector = defaultSector(c)
	}
	if c.Sector == "" || strings.ContainsAny(c.Sector, "/:@ ") {
		return errors.New("sector must be a host name")
	}
	return nil
}

// defaultSector is the host of the first non-loopback redirect URI (or of a
// URL client_id), else the first redirect host.
func defaultSector(c *Client) string {
	if c.Source == ClientURL {
		if u, err := url.Parse(c.ID); err == nil {
			return strings.ToLower(u.Hostname())
		}
	}
	first := ""
	for _, raw := range c.RedirectURIs {
		u, err := url.Parse(raw)
		if err != nil {
			continue
		}
		host := strings.ToLower(u.Hostname())
		if first == "" {
			first = host
		}
		if !isLoopbackHost(host) {
			return host
		}
	}
	return first
}

// checkRedirectURI enforces what a registered redirect URI may be.
func checkRedirectURI(raw string, allowHTTP bool) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" {
		return "", errors.New("must be an absolute URL")
	}
	if u.Fragment != "" || strings.Contains(raw, "#") {
		return "", errors.New("must not contain a fragment")
	}
	if u.User != nil {
		return "", errors.New("must not contain credentials")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !isLoopbackHost(u.Hostname()) && !allowHTTP {
			return "", errors.New("must use https (http only for loopback addresses)")
		}
	default:
		return "", errors.New("must use https")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	return u.String(), nil
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// MatchRedirectURI reports whether uri is one the client registered: exact
// match, or — for public clients only, per RFC 8252 — a loopback URI that
// differs from a registered one only in its port.
func (c *Client) MatchRedirectURI(uri string) bool {
	if c.Source == ClientURL {
		return c.matchURLClientRedirect(uri)
	}
	for _, registered := range c.RedirectURIs {
		if uri == registered {
			return true
		}
	}
	if !c.Public() {
		return false
	}
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "http" || !isLoopbackIP(u.Hostname()) {
		return false
	}
	for _, registered := range c.RedirectURIs {
		r, err := url.Parse(registered)
		if err != nil || r.Scheme != "http" || !isLoopbackIP(r.Hostname()) {
			continue
		}
		if r.Hostname() == u.Hostname() && r.Path == u.Path && r.RawQuery == u.RawQuery {
			return true
		}
	}
	return false
}

// isLoopbackIP excludes "localhost": RFC 8252 §8.3 prefers literal addresses,
// and a name can be pointed elsewhere.
func isLoopbackIP(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// checkSecret compares a presented secret with every live stored one.
func (c *Client) checkSecret(presented string, now time.Time) bool {
	if presented == "" {
		return false
	}
	hash := signin.HashSecret(presented)
	ok := false
	for _, s := range c.Secrets {
		if !s.ExpiresAt.IsZero() && now.After(s.ExpiresAt) {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(s.Hash), []byte(hash)) == 1 {
			ok = true
		}
	}
	return ok
}

// --- Lookup ------------------------------------------------------------------------

var errUnknownClient = errors.New("unknown client")

// lookupClient finds a client by id in the static list, the registry, or —
// when enabled for the caller's surface — at its URL.
func (s *Server) lookupClient(ctx context.Context, id string, surface string) (*Client, error) {
	for i := range s.cfg.StaticClients {
		if s.cfg.StaticClients[i].ID == id {
			c := s.cfg.StaticClients[i]
			return &c, nil
		}
	}
	if strings.HasPrefix(id, "https://") || strings.HasPrefix(id, "http://") {
		if !s.urlClientsAllowed(surface) {
			return nil, errUnknownClient
		}
		return s.fetchURLClient(ctx, id)
	}
	c, err := s.store.GetClient(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return nil, errUnknownClient
	}
	if err != nil {
		return nil, err
	}
	if c.Suspended {
		return nil, fmt.Errorf("this application has been suspended by the operator")
	}
	return c, nil
}

// Surfaces a client may arrive through.
const (
	surfaceOIDC      = "oidc"
	surfaceIndieAuth = "indieauth"
)

func (s *Server) urlClientsAllowed(surface string) bool {
	switch s.cfg.URLClients {
	case URLClientsOn:
		return true
	case URLClientsIndieAuth:
		return surface == surfaceIndieAuth
	default:
		return false
	}
}

// --- Client authentication at the token endpoint -------------------------------------

const clientAssertionType = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"

type clientAuthError struct{ msg string }

func (e *clientAuthError) Error() string { return e.msg }

// authenticateClient identifies and authenticates the caller of /token or
// /revoke. The registered method is enforced; the two secret transports are
// interchangeable because servers commonly are.
func (s *Server) authenticateClient(r *http.Request, surface string) (*Client, error) {
	ctx := r.Context()
	basicID, basicSecret, hasBasic := r.BasicAuth()
	if hasBasic {
		// RFC 6749 §2.3.1: both parts are form-urlencoded inside Basic.
		if v, err := url.QueryUnescape(basicID); err == nil {
			basicID = v
		}
		if v, err := url.QueryUnescape(basicSecret); err == nil {
			basicSecret = v
		}
	}
	formID := r.PostForm.Get("client_id")
	formSecret := r.PostForm.Get("client_secret")
	assertion := r.PostForm.Get("client_assertion")
	assertionType := r.PostForm.Get("client_assertion_type")

	methods := 0
	for _, used := range []bool{hasBasic, formSecret != "", assertion != ""} {
		if used {
			methods++
		}
	}
	if methods > 1 {
		return nil, &clientAuthError{"use exactly one client authentication method"}
	}

	id := formID
	switch {
	case hasBasic:
		if formID != "" && formID != basicID {
			return nil, &clientAuthError{"client_id does not match the Authorization header"}
		}
		id = basicID
	case assertion != "":
		if assertionType != clientAssertionType {
			return nil, &clientAuthError{"unsupported client_assertion_type"}
		}
		if id == "" {
			// The assertion names its client in iss/sub.
			_, payload, _, _, err := parseJWS(assertion)
			if err != nil {
				return nil, &clientAuthError{"malformed client_assertion"}
			}
			var claims struct {
				Sub string `json:"sub"`
			}
			_ = json.Unmarshal(payload, &claims)
			id = claims.Sub
		}
	}
	if id == "" {
		return nil, &clientAuthError{"client authentication is required"}
	}
	c, err := s.lookupClient(ctx, id, surface)
	if err != nil {
		return nil, &clientAuthError{"unknown client or bad credentials"}
	}
	now := s.now()
	switch c.AuthMethod {
	case AuthSecretBasic, AuthSecretPost:
		secret := formSecret
		if hasBasic {
			secret = basicSecret
		}
		if !c.checkSecret(secret, now) {
			return nil, &clientAuthError{"unknown client or bad credentials"}
		}
	case AuthPrivateJWT:
		if assertion == "" {
			return nil, &clientAuthError{"this client authenticates with private_key_jwt"}
		}
		if err := s.checkClientAssertion(ctx, c, assertion); err != nil {
			return nil, &clientAuthError{"client_assertion rejected: " + err.Error()}
		}
	case AuthNone:
		if hasBasic || formSecret != "" || assertion != "" {
			return nil, &clientAuthError{"this is a public client; send client_id only"}
		}
	default:
		return nil, &clientAuthError{"client has no usable authentication method"}
	}
	return c, nil
}

// checkClientAssertion verifies an RFC 7523 client assertion.
func (s *Server) checkClientAssertion(ctx context.Context, c *Client, assertion string) error {
	set, err := s.clientJWKS(ctx, c)
	if err != nil {
		return err
	}
	payload, err := verifyJWS(assertion, set)
	if err != nil {
		return err
	}
	var claims struct {
		Iss string          `json:"iss"`
		Sub string          `json:"sub"`
		Aud json.RawMessage `json:"aud"`
		Exp int64           `json:"exp"`
		Iat int64           `json:"iat"`
		Jti string          `json:"jti"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return errors.New("claims are not JSON")
	}
	if claims.Iss != c.ID || claims.Sub != c.ID {
		return errors.New("iss and sub must both be the client_id")
	}
	if !audienceContains(claims.Aud, s.cfg.Issuer, s.cfg.Issuer+"/token", s.cfg.Issuer+"/revoke") {
		return errors.New("aud must name this issuer")
	}
	now := s.now()
	exp := time.Unix(claims.Exp, 0)
	if claims.Exp == 0 || now.After(exp.Add(30*time.Second)) {
		return errors.New("assertion expired")
	}
	if exp.Sub(now) > 10*time.Minute {
		return errors.New("assertion lifetime exceeds 10 minutes")
	}
	if claims.Jti == "" {
		return errors.New("jti is required")
	}
	fresh, err := s.store.Use(ctx, "client-assertion|"+c.ID+"|"+claims.Jti, exp.Add(time.Minute))
	if err != nil {
		return err
	}
	if !fresh {
		return errors.New("assertion replayed")
	}
	return nil
}

func audienceContains(raw json.RawMessage, want ...string) bool {
	var single string
	if json.Unmarshal(raw, &single) == nil {
		for _, w := range want {
			if single == w {
				return true
			}
		}
		return false
	}
	var many []string
	if json.Unmarshal(raw, &many) == nil {
		for _, a := range many {
			for _, w := range want {
				if a == w {
					return true
				}
			}
		}
	}
	return false
}

func (s *Server) clientJWKS(ctx context.Context, c *Client) (JWKS, error) {
	if c.JWKS != nil {
		return *c.JWKS, nil
	}
	if c.JWKSURI == "" {
		return JWKS{}, errors.New("client has no keys")
	}
	raw, err := s.fetchPublicJSON(ctx, c.JWKSURI)
	if err != nil {
		return JWKS{}, fmt.Errorf("fetch jwks_uri: %w", err)
	}
	var set JWKS
	if err := json.Unmarshal(raw, &set); err != nil {
		return JWKS{}, errors.New("jwks_uri is not a JWK set")
	}
	return set, nil
}
