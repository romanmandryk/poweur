package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/poweur/identity"
	"github.com/poweur/identity/signin"
)

// --- URL client IDs --------------------------------------------------------------

// urlClientDoc is the client metadata an IndieAuth client (or a Client ID
// Metadata Document client) serves at its client_id URL.
type urlClientDoc struct {
	ClientID                string   `json:"client_id"`
	ClientName              string   `json:"client_name"`
	ClientURI               string   `json:"client_uri"`
	LogoURI                 string   `json:"logo_uri"`
	RedirectURIs            []string `json:"redirect_uris"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	JWKSURI                 string   `json:"jwks_uri"`
}

type cachedURLClient struct {
	client  *Client
	expires time.Time
}

var urlClientCache sync.Map // client_id → cachedURLClient

const (
	urlClientMinCache = 5 * time.Minute
)

// checkURLClientID applies IndieAuth's client identifier rules.
func (s *Server) checkURLClientID(id string) (*url.URL, error) {
	u, err := url.Parse(id)
	if err != nil || !u.IsAbs() {
		return nil, errors.New("client_id must be an absolute URL")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if s.secure && !isLoopbackIP(u.Hostname()) && u.Hostname() != "localhost" {
			return nil, errors.New("client_id must use https")
		}
	default:
		return nil, errors.New("client_id must use https")
	}
	if u.User != nil || u.Fragment != "" || strings.Contains(id, "#") {
		return nil, errors.New("client_id must not contain credentials or a fragment")
	}
	if u.Path == "" {
		return nil, errors.New("client_id must have a path")
	}
	for _, seg := range strings.Split(u.Path, "/") {
		if seg == "." || seg == ".." {
			return nil, errors.New("client_id must not contain dot path segments")
		}
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && !ip.IsLoopback() {
		return nil, errors.New("client_id must be a domain name")
	}
	return u, nil
}

// fetchURLClient builds a Client from a URL client_id, fetching its metadata.
// A client whose document cannot be fetched may still sign in, but only to
// redirect URIs on its own origin, and is named by its host.
func (s *Server) fetchURLClient(ctx context.Context, id string) (*Client, error) {
	u, err := s.checkURLClientID(id)
	if err != nil {
		return nil, err
	}
	now := s.now()
	if v, ok := urlClientCache.Load(id); ok {
		cached := v.(cachedURLClient)
		if now.Before(cached.expires) {
			c := *cached.client
			return &c, nil
		}
	}
	c := &Client{
		ID:         id,
		Name:       u.Hostname(),
		AuthMethod: AuthNone,
		Source:     ClientURL,
	}
	origin := u.Scheme + "://" + u.Host
	var raw []byte
	if s.cfg.FetchClientMetadata != nil {
		raw, err = s.cfg.FetchClientMetadata(ctx, id)
	} else {
		raw, err = s.fetchPublicJSON(ctx, id)
	}
	if err == nil {
		var doc urlClientDoc
		if jerr := json.Unmarshal(raw, &doc); jerr == nil {
			if doc.ClientID != id {
				return nil, errors.New("the client's metadata names a different client_id")
			}
			if name := strings.TrimSpace(doc.ClientName); name != "" && len(name) <= maxClientName && !strings.ContainsAny(name, "\r\n<>") {
				c.Name = name
			}
			if doc.LogoURI != "" && identity.SameOrigin(origin, doc.LogoURI) {
				c.LogoURI = doc.LogoURI
			}
			if doc.ClientURI != "" && identity.SameOrigin(origin, doc.ClientURI) {
				c.ClientURI = doc.ClientURI
			}
			for _, r := range doc.RedirectURIs {
				if clean, rerr := checkRedirectURI(r, !s.secure); rerr == nil {
					if identity.SameOrigin(origin, clean) || isLoopbackRedirect(clean) {
						c.RedirectURIs = append(c.RedirectURIs, clean)
					}
				}
			}
			switch doc.TokenEndpointAuthMethod {
			case "", AuthNone:
			case AuthPrivateJWT:
				if doc.JWKSURI == "" || !identity.SameOrigin(origin, doc.JWKSURI) {
					return nil, errors.New("private_key_jwt needs a same-origin jwks_uri")
				}
				c.AuthMethod = AuthPrivateJWT
				c.JWKSURI = doc.JWKSURI
			default:
				return nil, errors.New("a URL client cannot use a shared secret")
			}
		}
	}
	c.Sector = strings.ToLower(u.Hostname())
	// With no published list, the client's own origin is where it may return.
	c.sameOriginRedirects = len(c.RedirectURIs) == 0
	if c.sameOriginRedirects {
		c.RedirectURIs = []string{origin + "/"}
	}
	urlClientCache.Store(id, cachedURLClient{client: c, expires: now.Add(urlClientMinCache)})
	out := *c
	return &out, nil
}

func isLoopbackRedirect(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "http" && isLoopbackIP(u.Hostname())
}

// matchURLClientRedirect is IndieAuth's rule: same scheme, host and port as
// the client_id, or listed in the client's metadata.
func (c *Client) matchURLClientRedirect(uri string) bool {
	if c.sameOriginRedirects {
		clean, err := checkRedirectURI(uri, true)
		return err == nil && identity.SameOrigin(c.ID, clean) && strings.HasPrefix(clean, strings.SplitN(c.ID, "://", 2)[0]+"://")
	}
	if identity.SameOrigin(c.ID, uri) {
		if _, err := checkRedirectURI(uri, true); err == nil {
			return true
		}
	}
	return slices.Contains(c.RedirectURIs, uri)
}

// --- IndieAuth code redemption ----------------------------------------------------

// handleIndieAuthRedeem redeems a profile-only code at the authorization
// endpoint, which IndieAuth allows for clients that only need `me`.
func (s *Server) handleIndieAuthRedeem(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	clientID := r.PostForm.Get("client_id")
	c, err := s.lookupClient(r.Context(), clientID, surfaceIndieAuth)
	if err != nil || c.Source != ClientURL {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client", "unknown client")
		return
	}
	code := r.PostForm.Get("code")
	grant, err := s.store.RedeemCode(r.Context(), signin.HashSecret(code))
	if err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "unknown, expired or already used authorization code")
		return
	}
	if grant.Surface != surfaceIndieAuth || grant.ClientID != c.ID ||
		r.PostForm.Get("redirect_uri") != grant.RedirectURI ||
		!verifyPKCE(r.PostForm.Get("code_verifier"), grant.CodeChallenge) {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "the code does not match this client, redirect_uri or code_verifier")
		return
	}
	s.writeIndieAuthProfile(w, grant, nil)
}

// finishIndieAuthToken answers a token-endpoint exchange for an IndieAuth
// code. v1 grants only login and profile, so no access token is issued: the
// response is the canonical `me` (and profile) and nothing that grants access
// to anything.
func (s *Server) finishIndieAuthToken(w http.ResponseWriter, r *http.Request, c *Client, grant *CodeGrant) {
	s.writeIndieAuthProfile(w, grant, nil)
}

func (s *Server) writeIndieAuthProfile(w http.ResponseWriter, grant *CodeGrant, extra map[string]any) {
	out := map[string]any{"me": identity.IndieAuthProfileURL(grant.Identity)}
	if slices.Contains(grant.Scopes, ScopeProfile) {
		p := map[string]any{"url": identity.IndieAuthProfileURL(grant.Identity)}
		if grant.Claims.Name != "" {
			p["name"] = grant.Claims.Name
		}
		if grant.Claims.Picture != "" {
			p["photo"] = grant.Claims.Picture
		}
		out["profile"] = p
	}
	for k, v := range extra {
		out[k] = v
	}
	s.audit(context.Background(), "indieauth.redeemed", map[string]any{"client_id": grant.ClientID})
	writeJSON(w, http.StatusOK, out)
}

// --- Introspection ------------------------------------------------------------------

// handleIntrospect is RFC 7662 for authenticated clients, about their own
// tokens only.
func (s *Server) handleIntrospect(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "malformed form body")
		return
	}
	c, err := s.authenticateClient(r, surfaceForClientID(r.PostForm.Get("client_id")))
	if err != nil {
		writeOAuthError(w, http.StatusUnauthorized, "invalid_client", err.Error())
		return
	}
	g, err := s.store.GetAccessToken(r.Context(), signin.HashSecret(r.PostForm.Get("token")))
	if err != nil || g.ClientID != c.ID {
		writeJSON(w, http.StatusOK, map[string]any{"active": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"active":    true,
		"client_id": g.ClientID,
		"sub":       g.Subject,
		"scope":     strings.Join(g.Scopes, " "),
		"iat":       g.IssuedAt.Unix(),
		"exp":       g.IssuedAt.Add(accessTokenTTL).Unix(),
		"iss":       s.cfg.Issuer,
		"me":        identity.IndieAuthProfileURL(g.Identity),
	})
}
