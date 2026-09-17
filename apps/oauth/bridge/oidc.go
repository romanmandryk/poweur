package bridge

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/poweur/identity"
	"github.com/poweur/identity/signin"
)

// Subject types.
const (
	SubjectPairwise = "pairwise"
	SubjectPublic   = "public"
)

// Scopes.
const (
	ScopeOpenID   = "openid"
	ScopePoweurID = "poweur_id"
	ScopeProfile  = "profile"
)

// optionalScopes are released only with consent, in this display order.
var optionalScopes = []string{ScopePoweurID, ScopeProfile}

// AuthorizeRequest is a validated authorization request.
type AuthorizeRequest struct {
	ClientID      string   `json:"client_id"`
	ClientName    string   `json:"client_name"`
	RedirectURI   string   `json:"redirect_uri"`
	Scopes        []string `json:"scopes"`
	State         string   `json:"state"`
	Nonce         string   `json:"nonce,omitempty"`
	CodeChallenge string   `json:"code_challenge"`
	Prompt        []string `json:"prompt,omitempty"`
	MaxAge        *int     `json:"max_age,omitempty"`
	LoginHint     string   `json:"login_hint,omitempty"`
	// Surface is "oidc" or "indieauth"; IndieAuth fields follow.
	Surface string `json:"surface"`
	Me      string `json:"me,omitempty"`
}

func (a *AuthorizeRequest) wants(scope string) bool { return slices.Contains(a.Scopes, scope) }

func (a *AuthorizeRequest) prompt(p string) bool { return slices.Contains(a.Prompt, p) }

// requestedOptional lists the consent-gated scopes the client asked for.
// IndieAuth has only `profile`: the ID itself is its answer.
func (a *AuthorizeRequest) requestedOptional() []string {
	var out []string
	for _, s := range optionalScopes {
		if a.Surface == surfaceIndieAuth && s == ScopePoweurID {
			continue
		}
		if a.wants(s) {
			out = append(out, s)
		}
	}
	return out
}

// --- Discovery ------------------------------------------------------------------

func (s *Server) handleDiscovery(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "public, max-age=300")
	iss := s.cfg.Issuer
	doc := map[string]any{
		"issuer":                                           iss,
		"authorization_endpoint":                           iss + "/authorize",
		"token_endpoint":                                   iss + "/token",
		"userinfo_endpoint":                                iss + "/userinfo",
		"jwks_uri":                                         iss + "/jwks.json",
		"revocation_endpoint":                              iss + "/revoke",
		"response_types_supported":                         []string{"code"},
		"response_modes_supported":                         []string{"query"},
		"grant_types_supported":                            []string{"authorization_code"},
		"subject_types_supported":                          []string{s.cfg.SubjectType},
		"id_token_signing_alg_values_supported":            []string{"RS256"},
		"token_endpoint_auth_methods_supported":            []string{AuthSecretBasic, AuthSecretPost, AuthPrivateJWT, AuthNone},
		"token_endpoint_auth_signing_alg_values_supported": []string{"RS256", "ES256", "EdDSA"},
		"revocation_endpoint_auth_methods_supported":       []string{AuthSecretBasic, AuthSecretPost, AuthPrivateJWT, AuthNone},
		"scopes_supported":                                 []string{ScopeOpenID, ScopePoweurID, ScopeProfile},
		"claims_supported":                                 []string{"iss", "sub", "aud", "exp", "iat", "auth_time", "nonce", "amr", "poweur_id", "poweur_key_fingerprint", "poweur_id_url", "name", "picture", "profile"},
		"code_challenge_methods_supported":                 []string{"S256"},
		"prompt_values_supported":                          []string{"none", "login", "consent"},
		"authorization_response_iss_parameter_supported":   true,
		"claims_parameter_supported":                       false,
		"request_parameter_supported":                      false,
		"request_uri_parameter_supported":                  false,
		"require_request_uri_registration":                 false,
		"client_id_metadata_document_supported":            s.cfg.URLClients != URLClientsOff,
		"service_documentation":                            iss + "/",
	}
	doc["introspection_endpoint"] = iss + "/introspect"
	doc["introspection_endpoint_auth_methods_supported"] = []string{AuthSecretBasic, AuthSecretPost, AuthPrivateJWT, AuthNone}
	writeJSON(w, http.StatusOK, doc)
}

func (s *Server) handleJWKS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "public, max-age=300")
	writeJSON(w, http.StatusOK, s.keys.JWKS())
}

// --- /authorize -----------------------------------------------------------------

// authorizeError is an error to show on a page because the redirect URI
// cannot be trusted yet.
type authorizeError struct{ msg string }

func (e *authorizeError) Error() string { return e.msg }

// redirectableError is an OAuth error the client should receive.
type redirectableError struct{ code, desc string }

func (e *redirectableError) Error() string { return e.code + ": " + e.desc }

// parseAuthorize validates an authorization request. Errors before the
// client and redirect URI are known are authorizeErrors; afterwards they are
// redirectableErrors.
func (s *Server) parseAuthorize(ctx context.Context, q url.Values, surface string) (*AuthorizeRequest, *Client, error) {
	for key, vals := range q {
		if len(vals) > 1 {
			return nil, nil, &authorizeError{"The parameter " + key + " was sent more than once."}
		}
	}
	clientID := q.Get("client_id")
	if clientID == "" {
		return nil, nil, &authorizeError{"The application did not say who it is (client_id is missing)."}
	}
	c, err := s.lookupClient(ctx, clientID, surface)
	if err != nil {
		if errors.Is(err, errUnknownClient) {
			return nil, nil, &authorizeError{"This application is not registered with " + s.cfg.Name + "."}
		}
		return nil, nil, &authorizeError{err.Error()}
	}
	redirectURI := q.Get("redirect_uri")
	if redirectURI == "" && surface == surfaceOIDC {
		return nil, nil, &authorizeError{"The application did not say where to return (redirect_uri is missing)."}
	}
	if !c.MatchRedirectURI(redirectURI) {
		return nil, nil, &authorizeError{"The application asked to return to an address it has not registered. Nothing was sent to it."}
	}
	a := &AuthorizeRequest{
		ClientID:    c.ID,
		ClientName:  c.Name,
		RedirectURI: redirectURI,
		State:       q.Get("state"),
		Surface:     surface,
	}
	fail := func(code, desc string) (*AuthorizeRequest, *Client, error) {
		return a, c, &redirectableError{code, desc}
	}
	if q.Get("request") != "" {
		return fail("request_not_supported", "request objects are not supported")
	}
	if q.Get("request_uri") != "" {
		return fail("request_uri_not_supported", "request_uri is not supported")
	}
	if q.Get("response_type") != "code" {
		return fail("unsupported_response_type", "only response_type=code is supported")
	}
	if rm := q.Get("response_mode"); rm != "" && rm != "query" {
		return fail("invalid_request", "only response_mode=query is supported")
	}
	if a.State == "" {
		return fail("invalid_request", "state is required")
	}
	if len(a.State) > 512 {
		return fail("invalid_request", "state is too long")
	}
	for _, sc := range strings.Fields(q.Get("scope")) {
		switch sc {
		case ScopeOpenID, ScopePoweurID, ScopeProfile:
			if !a.wants(sc) {
				a.Scopes = append(a.Scopes, sc)
			}
		}
	}
	if surface == surfaceOIDC {
		if !a.wants(ScopeOpenID) {
			return fail("invalid_scope", "the openid scope is required")
		}
		// OIDC Core makes nonce optional for the code flow, and PKCE — which is
		// mandatory here — already binds the code to the client's session.
		// Echoed when sent (oauth2-proxy, for one, does not send it by default).
		a.Nonce = q.Get("nonce")
		if len(a.Nonce) > 512 {
			return fail("invalid_request", "nonce is too long")
		}
	}
	a.CodeChallenge = q.Get("code_challenge")
	if a.CodeChallenge == "" {
		return fail("invalid_request", "PKCE is required: send code_challenge with code_challenge_method=S256")
	}
	if q.Get("code_challenge_method") != "S256" {
		return fail("invalid_request", "code_challenge_method must be S256")
	}
	if len(a.CodeChallenge) != 43 {
		return fail("invalid_request", "code_challenge must be a base64url SHA-256 digest")
	}
	if p := strings.Fields(q.Get("prompt")); len(p) > 0 {
		for _, v := range p {
			switch v {
			case "none", "login", "consent":
			case "select_account":
				// Nothing to select: one identity per sign-in.
				continue
			default:
				return fail("invalid_request", "unsupported prompt value "+v)
			}
			a.Prompt = append(a.Prompt, v)
		}
		if a.prompt("none") && len(a.Prompt) > 1 {
			return fail("invalid_request", "prompt=none cannot be combined with other values")
		}
	}
	if ma := q.Get("max_age"); ma != "" {
		n, err := strconv.Atoi(ma)
		if err != nil || n < 0 {
			return fail("invalid_request", "max_age must be a non-negative integer")
		}
		a.MaxAge = &n
	}
	if surface == surfaceIndieAuth {
		// In IndieAuth the identity is the whole point of signing in: `me` is
		// always returned, so it is not an optional release.
		if me := q.Get("me"); me != "" {
			if id, err := identity.NormalizeIDInput(me, !s.secure); err == nil {
				a.Me = id
				a.LoginHint = id
			}
		}
	}
	if hint := q.Get("login_hint"); hint != "" {
		if id, err := identity.NormalizeIDInput(hint, !s.secure); err == nil {
			a.LoginHint = id
		}
	}
	return a, c, nil
}

func (s *Server) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "The authorization request could not be read.")
		return
	}
	// IndieAuth redeems profile-only codes at the authorization endpoint.
	if r.Method == http.MethodPost && r.PostForm.Get("grant_type") == "authorization_code" {
		s.handleIndieAuthRedeem(w, r)
		return
	}
	surface := surfaceOIDC
	// A URL client_id without `openid` is an IndieAuth client. With `openid`
	// it is an OIDC client using a Client ID Metadata Document, which only
	// OAUTH_URL_CLIENTS=on admits.
	if strings.HasPrefix(r.Form.Get("client_id"), "http") &&
		!slices.Contains(strings.Fields(r.Form.Get("scope")), ScopeOpenID) {
		surface = surfaceIndieAuth
	}
	s.authorize(w, r, r.Form, surface)
}

// authorize is shared by the OIDC and IndieAuth surfaces.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request, q url.Values, surface string) {
	ctx := r.Context()
	a, c, err := s.parseAuthorize(ctx, q, surface)
	var ae *authorizeError
	if errors.As(err, &ae) {
		s.audit(ctx, "authorize.refused", map[string]any{"client_id": q.Get("client_id"), "reason": ae.msg})
		s.renderError(w, r, http.StatusBadRequest, ae.msg)
		return
	}
	var re *redirectableError
	if errors.As(err, &re) {
		s.redirectError(w, r, a, re.code, re.desc)
		return
	}
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "The authorization request could not be processed.")
		return
	}

	t, err := s.newTxn(w, r, KindAuthorize)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "Could not start signing in.")
		return
	}
	t.Authorize = a
	t.LoginHint = a.LoginHint

	// An existing bridge session may answer without a new signature.
	sess, _ := s.currentSession(r)
	reuse := sess != nil && !a.prompt("login") &&
		(a.LoginHint == "" || a.LoginHint == sess.Identity) &&
		(a.MaxAge == nil || s.now().Sub(sess.AuthTime) <= time.Duration(*a.MaxAge)*time.Second)
	if a.prompt("none") && !reuse {
		s.redirectError(w, r, a, "login_required", "the user is not signed in")
		return
	}
	if reuse {
		t.Identity = sess.Identity
		t.AuthTime = sess.AuthTime
		t.SessionDelegated = sess.SessionDelegated
		t.Resumed = true
		t.Finish = "session"
	}
	if err := s.store.CreateTxn(ctx, t); err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "Could not start signing in.")
		return
	}
	s.audit(ctx, "authorize.started", map[string]any{
		"txn": shortID(t.ID), "client_id": c.ID, "surface": surface, "session_reused": reuse,
	})
	if reuse {
		s.continueAuthorize(w, r, t)
		return
	}
	http.Redirect(w, r, "/t/"+t.ID, http.StatusSeeOther)
}

// continueAuthorize runs once the transaction's browser is authenticated:
// consent if needed, then the code.
func (s *Server) continueAuthorize(w http.ResponseWriter, r *http.Request, t *Txn) {
	ctx := r.Context()
	a := t.Authorize
	c, err := s.lookupClient(ctx, a.ClientID, a.Surface)
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, "This application is no longer available.")
		return
	}
	if a.LoginHint != "" && a.LoginHint != t.Identity && a.Surface != surfaceIndieAuth {
		s.failAuthorize(w, r, t, "access_denied", "the user signed in as a different identity than login_hint")
		return
	}
	requested := a.requestedOptional()
	consent, _ := s.store.GetConsent(ctx, t.Identity, c.ID)
	needConsent := a.prompt("consent") || consent == nil || !subset(requested, consent.Decided)
	if c.FirstParty && len(requested) == 0 && !a.prompt("consent") {
		needConsent = false
	}
	if needConsent {
		if a.prompt("none") {
			s.failAuthorize(w, r, t, "consent_required", "the user has not approved this application")
			return
		}
		s.renderConsent(w, r, t, c, consent)
		return
	}
	var granted []string
	if consent != nil {
		granted = intersect(requested, consent.Granted)
		consent.LastUsed = s.now()
		_ = s.store.PutConsent(ctx, t.Identity, *consent)
	}
	s.issueCode(w, r, t, c, granted)
}

// handleConsent records the user's decision.
func (s *Server) handleConsent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !s.sameOriginPost(r) {
		s.renderError(w, r, http.StatusForbidden, "That request did not come from this site.")
		return
	}
	t, err := s.loadTxnForBrowser(r, r.PathValue("id"))
	if err != nil {
		s.txnError(w, r, err)
		return
	}
	if err := t.usable(); err != nil {
		s.renderFailed(w, r, t)
		return
	}
	if t.Kind != KindAuthorize || !t.Resumed {
		http.Redirect(w, r, "/t/"+t.ID, http.StatusSeeOther)
		return
	}
	// The consent is the browser session's, and it must still be this user.
	if sess, _ := s.currentSession(r); sess == nil || sess.Identity != t.Identity {
		s.renderError(w, r, http.StatusForbidden, "Your session changed while you were deciding. Start again from the application.")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "That form could not be read.")
		return
	}
	a := t.Authorize
	if r.PostForm.Get("decision") != "allow" {
		s.audit(ctx, "consent.denied", map[string]any{"txn": shortID(t.ID), "client_id": a.ClientID})
		s.failAuthorize(w, r, t, "access_denied", "the user denied the request")
		return
	}
	c, err := s.lookupClient(ctx, a.ClientID, a.Surface)
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, "This application is no longer available.")
		return
	}
	requested := a.requestedOptional()
	var granted []string
	for _, sc := range requested {
		if r.PostForm.Get("release_"+sc) == "on" {
			granted = append(granted, sc)
		}
	}
	now := s.now()
	prev, _ := s.store.GetConsent(ctx, t.Identity, c.ID)
	consent := Consent{
		ClientID:   c.ID,
		ClientName: c.Name,
		ClientHost: clientHost(c),
		Decided:    requested,
		Granted:    granted,
		FirstAt:    now,
		LastUsed:   now,
	}
	if prev != nil {
		consent.FirstAt = prev.FirstAt
		// Keep earlier decisions about scopes this request did not mention.
		for _, sc := range prev.Decided {
			if !slices.Contains(requested, sc) {
				consent.Decided = append(consent.Decided, sc)
				if slices.Contains(prev.Granted, sc) {
					consent.Granted = append(consent.Granted, sc)
				}
			}
		}
	}
	if err := s.store.PutConsent(ctx, t.Identity, consent); err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "Could not save your decision.")
		return
	}
	s.audit(ctx, "consent.granted", map[string]any{"txn": shortID(t.ID), "client_id": c.ID, "released": granted})
	s.issueCode(w, r, t, c, granted)
}

// issueCode finishes the transaction with an authorization code.
func (s *Server) issueCode(w http.ResponseWriter, r *http.Request, t *Txn, c *Client, granted []string) {
	ctx := r.Context()
	a := t.Authorize
	claims, err := s.buildClaims(ctx, t.Identity, granted)
	if err != nil {
		s.failAuthorize(w, r, t, "server_error", "could not read the identity's public details")
		return
	}
	code, err := randomToken(32)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "Could not finish signing in.")
		return
	}
	amr := []string{"poweur"}
	if t.SessionDelegated {
		amr = append(amr, "session")
	}
	now := s.now()
	scopes := append([]string{}, granted...)
	if a.wants(ScopeOpenID) {
		scopes = append([]string{ScopeOpenID}, scopes...)
	}
	grant := CodeGrant{
		ClientID:      c.ID,
		RedirectURI:   a.RedirectURI,
		CodeChallenge: a.CodeChallenge,
		Nonce:         a.Nonce,
		Identity:      t.Identity,
		Scopes:        scopes,
		Claims:        claims,
		AuthTime:      t.AuthTime,
		AMR:           amr,
		IssuedAt:      now,
		Surface:       a.Surface,
	}
	// Mark the transaction done first: it produces exactly one code.
	if _, err := s.store.UpdateTxn(ctx, t.ID, func(t *Txn) error {
		if err := t.usable(); err != nil {
			return err
		}
		t.Done = true
		return nil
	}); err != nil {
		s.txnError(w, r, err)
		return
	}
	if err := s.store.PutCode(ctx, signin.HashSecret(code), grant, now.Add(codeTTL)); err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "Could not finish signing in.")
		return
	}
	s.recordSignIn(r, t, c)
	s.audit(ctx, "authorize.code_issued", map[string]any{
		"txn": shortID(t.ID), "client_id": c.ID, "released": granted, "surface": a.Surface,
	})
	s.redirectToClient(w, r, a, url.Values{"code": {code}})
}

func (s *Server) failAuthorize(w http.ResponseWriter, r *http.Request, t *Txn, code, desc string) {
	_, _ = s.store.UpdateTxn(r.Context(), t.ID, func(t *Txn) error {
		t.Done = true
		if t.Err == "" {
			t.Err = desc
		}
		return nil
	})
	s.redirectError(w, r, t.Authorize, code, desc)
}

func (s *Server) redirectError(w http.ResponseWriter, r *http.Request, a *AuthorizeRequest, code, desc string) {
	s.redirectToClient(w, r, a, url.Values{"error": {code}, "error_description": {desc}})
}

// redirectToClient sends the browser to the registered redirect URI with
// state and iss (RFC 9207) added.
func (s *Server) redirectToClient(w http.ResponseWriter, r *http.Request, a *AuthorizeRequest, params url.Values) {
	u, err := url.Parse(a.RedirectURI)
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, "The application's return address is invalid.")
		return
	}
	q := u.Query()
	for k, v := range params {
		q[k] = v
	}
	if a.State != "" {
		q.Set("state", a.State)
	}
	q.Set("iss", s.cfg.Issuer)
	u.RawQuery = q.Encode()
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, u.String(), http.StatusSeeOther)
}

// buildClaims freezes the claims a grant releases.
func (s *Server) buildClaims(ctx context.Context, id string, granted []string) (Claims, error) {
	var c Claims
	if slices.Contains(granted, ScopePoweurID) {
		res, err := s.resolve(ctx, id)
		if err != nil {
			return c, err
		}
		fp, err := identity.KeyFingerprint(res.Document.PublicKey)
		if err != nil {
			return c, err
		}
		c.PoweurID = id
		c.KeyFingerprint = fp
		c.PoweurIDURL = identity.IndieAuthProfileURL(id)
	}
	if slices.Contains(granted, ScopeProfile) {
		c.Profile = identity.IndieAuthProfileURL(id)
		if p, err := s.fetchProfile(ctx, id); err == nil {
			c.Name = p.DisplayName
			scheme := s.cfg.ResolveOptions.Scheme
			c.Picture = identity.AvatarURL(id, p.Avatar, scheme)
		}
	}
	return c, nil
}

func (s *Server) fetchProfile(ctx context.Context, id string) (identity.Profile, error) {
	if s.cfg.FetchProfile != nil {
		return s.cfg.FetchProfile(ctx, id)
	}
	return identity.FetchProfile(ctx, id, s.cfg.ResolveOptions)
}

// subject is the `sub` a client sees for an identity.
func (s *Server) subject(c *Client, id string) string {
	if s.cfg.SubjectType == SubjectPublic {
		return id
	}
	return pairwiseSubject(s.pairwise, c.Sector, id)
}

func pairwiseSubject(secret []byte, sector, id string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("poweur-oidc-sub-v1\n" + sector + "\n" + id))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// --- /token ------------------------------------------------------------------------

func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Pragma", "no-cache")
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "malformed form body")
		return
	}
	for key, vals := range r.PostForm {
		if len(vals) > 1 {
			writeOAuthError(w, http.StatusBadRequest, "invalid_request", key+" was sent more than once")
			return
		}
	}
	surface := surfaceOIDC
	if strings.HasPrefix(r.PostForm.Get("client_id"), "http") {
		surface = surfaceIndieAuth
	}
	c, err := s.authenticateClient(r, surface)
	if err != nil {
		w.Header().Set("WWW-Authenticate", `Basic realm="`+s.cfg.Issuer+`"`)
		writeOAuthError(w, http.StatusUnauthorized, "invalid_client", err.Error())
		return
	}
	if gt := r.PostForm.Get("grant_type"); gt != "authorization_code" {
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type", "only authorization_code is supported")
		return
	}
	code := r.PostForm.Get("code")
	grant, err := s.store.RedeemCode(ctx, signin.HashSecret(code))
	switch {
	case errors.Is(err, errCodeReused):
		s.audit(ctx, "token.code_reused", map[string]any{"client_id": c.ID})
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "the authorization code was already used; its tokens are revoked")
		return
	case err != nil:
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "unknown or expired authorization code")
		return
	}
	if grant.ClientID != c.ID {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "the code was issued to another client")
		return
	}
	if r.PostForm.Get("redirect_uri") != grant.RedirectURI {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "redirect_uri does not match the authorization request")
		return
	}
	if !verifyPKCE(r.PostForm.Get("code_verifier"), grant.CodeChallenge) {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "code_verifier does not match code_challenge")
		return
	}

	if grant.Surface == surfaceIndieAuth {
		s.finishIndieAuthToken(w, r, c, grant)
		return
	}

	now := s.now()
	sub := s.subject(c, grant.Identity)
	idClaims := map[string]any{
		"iss":       s.cfg.Issuer,
		"sub":       sub,
		"aud":       c.ID,
		"exp":       now.Add(idTokenTTL).Unix(),
		"iat":       now.Unix(),
		"auth_time": grant.AuthTime.Unix(),
		"amr":       grant.AMR,
	}
	if grant.Nonce != "" {
		idClaims["nonce"] = grant.Nonce
	}
	addClaims(idClaims, grant.Claims)
	idToken, err := s.keys.Sign(idClaims)
	if err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "could not sign the ID token")
		return
	}
	access, err := randomToken(32)
	if err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "could not issue a token")
		return
	}
	if err := s.store.PutAccessToken(ctx, signin.HashSecret(access), signin.HashSecret(code), AccessGrant{
		ClientID: c.ID, Identity: grant.Identity, Subject: sub,
		Scopes: grant.Scopes, Claims: grant.Claims, IssuedAt: now,
	}, now.Add(accessTokenTTL)); err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "could not issue a token")
		return
	}
	s.audit(ctx, "token.issued", map[string]any{"client_id": c.ID})
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": access,
		"token_type":   "Bearer",
		"expires_in":   int(accessTokenTTL / time.Second),
		"id_token":     idToken,
		"scope":        strings.Join(grant.Scopes, " "),
	})
}

func addClaims(m map[string]any, c Claims) {
	set := func(k, v string) {
		if v != "" {
			m[k] = v
		}
	}
	set("poweur_id", c.PoweurID)
	set("poweur_key_fingerprint", c.KeyFingerprint)
	set("poweur_id_url", c.PoweurIDURL)
	set("name", c.Name)
	set("picture", c.Picture)
	set("profile", c.Profile)
}

func verifyPKCE(verifier, challenge string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	for _, ch := range verifier {
		if !(ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || strings.ContainsRune("-._~", ch)) {
			return false
		}
	}
	sum := sha256.Sum256([]byte(verifier))
	got := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(got), []byte(challenge)) == 1
}

// --- /userinfo and /revoke ----------------------------------------------------------

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	if r.Method == http.MethodPost {
		_ = r.ParseForm()
		return r.PostForm.Get("access_token")
	}
	return ""
}

func (s *Server) handleUserInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	token := bearerToken(r)
	if token == "" {
		w.Header().Set("WWW-Authenticate", `Bearer realm="`+s.cfg.Issuer+`"`)
		writeOAuthError(w, http.StatusUnauthorized, "invalid_token", "an access token is required")
		return
	}
	g, err := s.store.GetAccessToken(r.Context(), signin.HashSecret(token))
	if err != nil {
		w.Header().Set("WWW-Authenticate", `Bearer realm="`+s.cfg.Issuer+`", error="invalid_token"`)
		writeOAuthError(w, http.StatusUnauthorized, "invalid_token", "the access token is invalid or expired")
		return
	}
	out := map[string]any{"sub": g.Subject}
	addClaims(out, g.Claims)
	writeJSON(w, http.StatusOK, out)
}

// handleRevoke is RFC 7009: always 200 for an authenticated client.
func (s *Server) handleRevoke(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
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
	if tok := r.PostForm.Get("token"); tok != "" {
		if err := s.store.RevokeAccessToken(r.Context(), signin.HashSecret(tok), c.ID); err != nil {
			writeOAuthError(w, http.StatusInternalServerError, "server_error", "could not revoke")
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

func surfaceForClientID(id string) string {
	if strings.HasPrefix(id, "http") {
		return surfaceIndieAuth
	}
	return surfaceOIDC
}

// --- helpers ------------------------------------------------------------------------

func subset(a, b []string) bool {
	for _, x := range a {
		if !slices.Contains(b, x) {
			return false
		}
	}
	return true
}

func intersect(a, b []string) []string {
	var out []string
	for _, x := range a {
		if slices.Contains(b, x) {
			out = append(out, x)
		}
	}
	return out
}

// clientHost is the host a consent page names for a client.
func clientHost(c *Client) string {
	if c.Source == ClientURL {
		if u, err := url.Parse(c.ID); err == nil {
			return u.Hostname()
		}
	}
	if len(c.RedirectURIs) > 0 {
		if u, err := url.Parse(c.RedirectURIs[0]); err == nil {
			return u.Hostname()
		}
	}
	return ""
}
