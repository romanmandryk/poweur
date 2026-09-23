package bridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/poweur/identity"
	"github.com/poweur/identity/signin"
)

// Console limits.
const (
	secretRotationOverlap = 7 * 24 * time.Hour
	clientCreatesPerHour  = 5
	maxCoOwners           = 10
)

// extraRoutes registers the pages that need a signed-in bridge session: the
// developer console and the user's account (E22-T9).
func (s *Server) extraRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /account", s.handleAccount)
	mux.HandleFunc("POST /account/revoke", s.handleAccountRevoke)
	mux.HandleFunc("GET /developers", s.handleDevelopers)
	mux.HandleFunc("GET /developers/new", s.handleClientNewForm)
	mux.HandleFunc("POST /developers/new", s.handleClientCreate)
	mux.HandleFunc("GET /developers/clients/{id}", s.handleClientPage)
	mux.HandleFunc("POST /developers/clients/{id}", s.handleClientUpdate)
	mux.HandleFunc("POST /developers/clients/{id}/secrets", s.handleClientRotate)
	mux.HandleFunc("POST /developers/clients/{id}/secrets/{sid}/retire", s.handleClientRetireSecret)
	mux.HandleFunc("POST /developers/clients/{id}/delete", s.handleClientDelete)
	mux.HandleFunc("GET /abuse", s.handleAbuse)
	mux.HandleFunc("GET /privacy", s.handlePrivacy)
	mux.HandleFunc("GET /security", s.handleSecurity)
}

// requireSession returns the signed-in browser session, or sends the browser
// to sign in and returns nil.
func (s *Server) requireSession(w http.ResponseWriter, r *http.Request) *BrowserSession {
	sess, _ := s.currentSession(r)
	if sess != nil {
		return sess
	}
	http.Redirect(w, r, "/login?return_to="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
	return nil
}

// requirePost checks origin and session for a console form.
func (s *Server) requirePost(w http.ResponseWriter, r *http.Request) *BrowserSession {
	if !s.sameOriginPost(r) {
		s.renderError(w, r, http.StatusForbidden, "That request did not come from this site.")
		return nil
	}
	sess, _ := s.currentSession(r)
	if sess == nil {
		s.renderError(w, r, http.StatusUnauthorized, "Your session has ended. Sign in again and retry.")
		return nil
	}
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "That form could not be read.")
		return nil
	}
	return sess
}

// --- Account --------------------------------------------------------------------

type accountView struct {
	Consents []Consent
	SignIns  []SignInRecord
	Notice   string
}

func (s *Server) handleAccount(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSession(w, r)
	if sess == nil {
		return
	}
	s.renderAccount(w, r, sess, r.URL.Query().Get("notice"))
}

func (s *Server) renderAccount(w http.ResponseWriter, r *http.Request, sess *BrowserSession, notice string) {
	ctx := r.Context()
	consents, err := s.store.ListConsents(ctx, sess.Identity)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "Could not load your authorizations.")
		return
	}
	signins, err := s.store.RecentSignIns(ctx, sess.Identity, 20)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "Could not load your sign-ins.")
		return
	}
	s.render(w, r, http.StatusOK, "account.html", "Your authorized apps", accountView{
		Consents: consents, SignIns: signins, Notice: notice,
	})
}

func (s *Server) handleAccountRevoke(w http.ResponseWriter, r *http.Request) {
	sess := s.requirePost(w, r)
	if sess == nil {
		return
	}
	ctx := r.Context()
	clientID := r.PostForm.Get("client_id")
	if err := s.store.DeleteConsent(ctx, sess.Identity, clientID); err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "Could not revoke.")
		return
	}
	if err := s.store.RevokeGrant(ctx, sess.Identity, clientID); err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "Could not revoke.")
		return
	}
	s.audit(ctx, "consent.revoked", map[string]any{"client_id": clientID})
	http.Redirect(w, r, "/account?notice=revoked", http.StatusSeeOther)
}

// --- Developer console -----------------------------------------------------------

type developersView struct {
	Clients  []*Client
	CanWrite bool
	Why      string
	Max      int
}

func (s *Server) canRegister(id string) (bool, string) {
	switch s.cfg.ClientRegistration {
	case RegistrationOpen:
		return true, ""
	case RegistrationAllowlist:
		for _, entry := range s.cfg.RegistrationAllowlist {
			entry = strings.ToLower(strings.TrimSpace(entry))
			if entry == id || (strings.HasPrefix(entry, "*.") && strings.HasSuffix(id, entry[1:])) {
				return true, ""
			}
		}
		return false, "This service registers applications only for approved Poweur IDs. Ask the operator."
	default:
		return false, "This service does not accept new applications. Ask the operator."
	}
}

func (s *Server) handleDevelopers(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSession(w, r)
	if sess == nil {
		return
	}
	clients, err := s.store.ClientsOwnedBy(r.Context(), sess.Identity)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "Could not load your applications.")
		return
	}
	can, why := s.canRegister(sess.Identity)
	s.render(w, r, http.StatusOK, "developers.html", "Your applications", developersView{
		Clients: clients, CanWrite: can, Why: why, Max: s.cfg.MaxClientsPerOwner,
	})
}

// clientForm is the create/edit form's state.
type clientForm struct {
	Name         string
	RedirectURIs string
	AuthMethod   string
	JWKS         string
	JWKSURI      string
	Sector       string
	CoOwners     string
	Error        string
}

type clientView struct {
	Client    *Client
	Form      clientForm
	Secret    string
	Issuer    string
	Now       time.Time
	CanDelete bool
	Notice    string
}

func (s *Server) handleClientNewForm(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSession(w, r)
	if sess == nil {
		return
	}
	if ok, why := s.canRegister(sess.Identity); !ok {
		s.renderError(w, r, http.StatusForbidden, why)
		return
	}
	s.render(w, r, http.StatusOK, "client_new.html", "Register an application", clientView{
		Form: clientForm{AuthMethod: AuthSecretBasic}, Issuer: s.cfg.Issuer,
	})
}

func formFrom(r *http.Request) clientForm {
	return clientForm{
		Name:         strings.TrimSpace(r.PostForm.Get("name")),
		RedirectURIs: r.PostForm.Get("redirect_uris"),
		AuthMethod:   r.PostForm.Get("auth_method"),
		JWKS:         strings.TrimSpace(r.PostForm.Get("jwks")),
		JWKSURI:      strings.TrimSpace(r.PostForm.Get("jwks_uri")),
		Sector:       strings.TrimSpace(r.PostForm.Get("sector")),
		CoOwners:     r.PostForm.Get("co_owners"),
	}
}

func lines(s string) []string {
	var out []string
	for _, l := range strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '\r' || r == ',' || r == ' ' }) {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// owners returns the creator plus normalized co-owners.
func (s *Server) parseOwners(creator, raw string) ([]string, error) {
	owners := []string{creator}
	for _, v := range lines(raw) {
		id, err := identity.NormalizeIDInput(v, !s.secure)
		if err != nil {
			return nil, fmt.Errorf("co-owner %q: %v", v, err)
		}
		if !slices.Contains(owners, id) {
			owners = append(owners, id)
		}
	}
	if len(owners) > maxCoOwners+1 {
		return nil, fmt.Errorf("at most %d co-owners", maxCoOwners)
	}
	return owners, nil
}

func parseJWKSInput(raw string) (*JWKS, error) {
	if raw == "" {
		return nil, nil
	}
	var set JWKS
	if err := json.Unmarshal([]byte(raw), &set); err != nil || len(set.Keys) == 0 {
		var single JWK
		if err2 := json.Unmarshal([]byte(raw), &single); err2 != nil || single.Kty == "" {
			return nil, errors.New("keys must be a JWK or a JWK set")
		}
		set = JWKS{Keys: []JWK{single}}
	}
	for _, k := range set.Keys {
		var err error
		switch k.Kty {
		case "RSA":
			_, err = rsaPublicKey(k)
		case "EC":
			_, err = ecPublicKey(k)
		case "OKP":
			if k.Crv != "Ed25519" {
				err = errors.New("only Ed25519 OKP keys are accepted")
			}
		default:
			err = fmt.Errorf("unsupported key type %q", k.Kty)
		}
		if err != nil {
			return nil, err
		}
		if k.D != "" || k.P != "" {
			return nil, errors.New("paste public keys only — that key includes private material")
		}
	}
	return &set, nil
}

func (s *Server) handleClientCreate(w http.ResponseWriter, r *http.Request) {
	sess := s.requirePost(w, r)
	if sess == nil {
		return
	}
	ctx := r.Context()
	if ok, why := s.canRegister(sess.Identity); !ok {
		s.renderError(w, r, http.StatusForbidden, why)
		return
	}
	form := formFrom(r)
	fail := func(msg string) {
		form.Error = msg
		s.render(w, r, http.StatusBadRequest, "client_new.html", "Register an application", clientView{Form: form, Issuer: s.cfg.Issuer})
	}
	now := s.now()
	if n, err := s.store.CountClientsCreatedBy(ctx, sess.Identity); err != nil || n >= s.cfg.MaxClientsPerOwner {
		fail(fmt.Sprintf("You can register at most %d applications.", s.cfg.MaxClientsPerOwner))
		return
	}
	if n, err := s.store.CountClientsCreatedSince(ctx, sess.Identity, now.Add(-time.Hour)); err != nil || n >= clientCreatesPerHour {
		fail("You are registering applications too quickly. Try again later.")
		return
	}
	owners, err := s.parseOwners(sess.Identity, form.CoOwners)
	if err != nil {
		fail(capitalize(err.Error()) + ".")
		return
	}
	idPart, err := randomToken(12)
	if err != nil {
		fail("Could not create the application.")
		return
	}
	c := &Client{
		ID:           "pwc_" + strings.NewReplacer("-", "x", "_", "y").Replace(idPart),
		Name:         form.Name,
		RedirectURIs: lines(form.RedirectURIs),
		Sector:       form.Sector,
		Owners:       owners,
		CreatedBy:    sess.Identity,
		CreatedAt:    now,
		UpdatedAt:    now,
		Source:       ClientRegistered,
	}
	var secret string
	switch form.AuthMethod {
	case AuthSecretBasic, AuthSecretPost:
		c.AuthMethod = form.AuthMethod
		secret, err = newClientSecret()
		if err != nil {
			fail("Could not create the application.")
			return
		}
		c.Secrets = []ClientSecret{{ID: shortID(signin.HashSecret(secret)), Hash: signin.HashSecret(secret), CreatedAt: now}}
	case AuthPrivateJWT:
		c.AuthMethod = AuthPrivateJWT
		if c.JWKS, err = parseJWKSInput(form.JWKS); err != nil {
			fail(capitalize(err.Error()) + ".")
			return
		}
		c.JWKSURI = form.JWKSURI
	case AuthNone:
		c.AuthMethod = AuthNone
	default:
		fail("Choose how the application authenticates.")
		return
	}
	if err := c.validate(!s.secure); err != nil {
		fail(capitalize(err.Error()) + ".")
		return
	}
	if err := s.store.PutClient(ctx, c); err != nil {
		fail("Could not save the application.")
		return
	}
	s.audit(ctx, "client.created", map[string]any{
		"client_id": c.ID, "by": sess.Identity, "auth_method": c.AuthMethod, "redirect_uris": len(c.RedirectURIs),
	})
	// The secret is shown in this response only; it is never stored.
	s.renderClient(w, r, c, secret, "Application registered.")
}

func newClientSecret() (string, error) {
	v, err := signin.NewSecret()
	if err != nil {
		return "", err
	}
	return "pws_" + v, nil
}

// ownedClient loads a registered client the session may manage.
func (s *Server) ownedClient(w http.ResponseWriter, r *http.Request, sess *BrowserSession) *Client {
	c, err := s.store.GetClient(r.Context(), r.PathValue("id"))
	if err != nil || !slices.Contains(c.Owners, sess.Identity) {
		s.renderError(w, r, http.StatusNotFound, "No application with that id is registered to you.")
		return nil
	}
	return c
}

func (s *Server) renderClient(w http.ResponseWriter, r *http.Request, c *Client, secret, notice string) {
	var co []string
	for _, o := range c.Owners {
		if o != c.CreatedBy {
			co = append(co, o)
		}
	}
	s.render(w, r, http.StatusOK, "client.html", c.Name, clientView{
		Client: c,
		Form: clientForm{
			Name:         c.Name,
			RedirectURIs: strings.Join(c.RedirectURIs, "\n"),
			CoOwners:     strings.Join(co, "\n"),
		},
		Secret: secret,
		Issuer: s.cfg.Issuer,
		Now:    s.now(),
		Notice: notice,
	})
}

func (s *Server) handleClientPage(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSession(w, r)
	if sess == nil {
		return
	}
	c := s.ownedClient(w, r, sess)
	if c == nil {
		return
	}
	notice := ""
	if r.URL.Query().Get("notice") == "saved" {
		notice = "Saved."
	}
	s.renderClient(w, r, c, "", notice)
}

func (s *Server) handleClientUpdate(w http.ResponseWriter, r *http.Request) {
	sess := s.requirePost(w, r)
	if sess == nil {
		return
	}
	c := s.ownedClient(w, r, sess)
	if c == nil {
		return
	}
	ctx := r.Context()
	form := formFrom(r)
	next := *c
	next.Name = form.Name
	next.RedirectURIs = lines(form.RedirectURIs)
	owners, err := s.parseOwners(c.CreatedBy, form.CoOwners)
	if err != nil {
		s.renderClientError(w, r, c, form, capitalize(err.Error())+".")
		return
	}
	if !slices.Contains(owners, sess.Identity) {
		s.renderClientError(w, r, c, form, "You cannot remove yourself; ask another owner to.")
		return
	}
	next.Owners = owners
	next.UpdatedAt = s.now()
	// The sector is fixed at creation: validate must not re-derive it.
	if err := next.validate(!s.secure); err != nil {
		s.renderClientError(w, r, c, form, capitalize(err.Error())+".")
		return
	}
	if err := s.store.PutClient(ctx, &next); err != nil {
		s.renderClientError(w, r, c, form, "Could not save.")
		return
	}
	s.audit(ctx, "client.updated", map[string]any{"client_id": c.ID, "by": sess.Identity})
	http.Redirect(w, r, "/developers/clients/"+url.PathEscape(c.ID)+"?notice=saved", http.StatusSeeOther)
}

func (s *Server) renderClientError(w http.ResponseWriter, r *http.Request, c *Client, form clientForm, msg string) {
	form.Error = msg
	s.render(w, r, http.StatusBadRequest, "client.html", c.Name, clientView{
		Client: c, Form: form, Issuer: s.cfg.Issuer, Now: s.now(),
	})
}

func (s *Server) handleClientRotate(w http.ResponseWriter, r *http.Request) {
	sess := s.requirePost(w, r)
	if sess == nil {
		return
	}
	c := s.ownedClient(w, r, sess)
	if c == nil {
		return
	}
	if c.AuthMethod != AuthSecretBasic && c.AuthMethod != AuthSecretPost {
		s.renderError(w, r, http.StatusBadRequest, "This application does not use a secret.")
		return
	}
	ctx := r.Context()
	now := s.now()
	secret, err := newClientSecret()
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "Could not create a secret.")
		return
	}
	var kept []ClientSecret
	for _, old := range c.Secrets {
		if !old.ExpiresAt.IsZero() && now.After(old.ExpiresAt) {
			continue
		}
		if old.ExpiresAt.IsZero() {
			old.ExpiresAt = now.Add(secretRotationOverlap)
		}
		kept = append(kept, old)
	}
	hash := signin.HashSecret(secret)
	c.Secrets = append(kept, ClientSecret{ID: shortID(hash), Hash: hash, CreatedAt: now})
	c.UpdatedAt = now
	if err := s.store.PutClient(ctx, c); err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "Could not save the secret.")
		return
	}
	s.audit(ctx, "client.secret_rotated", map[string]any{"client_id": c.ID, "by": sess.Identity})
	s.renderClient(w, r, c, secret, "New secret created. Earlier secrets keep working for 7 days unless you retire them.")
}

func (s *Server) handleClientRetireSecret(w http.ResponseWriter, r *http.Request) {
	sess := s.requirePost(w, r)
	if sess == nil {
		return
	}
	c := s.ownedClient(w, r, sess)
	if c == nil {
		return
	}
	sid := r.PathValue("sid")
	now := s.now()
	var kept []ClientSecret
	live := 0
	for _, sec := range c.Secrets {
		if sec.ID == sid {
			continue
		}
		kept = append(kept, sec)
		if sec.ExpiresAt.IsZero() || now.Before(sec.ExpiresAt) {
			live++
		}
	}
	if len(kept) == len(c.Secrets) {
		s.renderError(w, r, http.StatusNotFound, "No such secret.")
		return
	}
	if live == 0 {
		s.renderError(w, r, http.StatusBadRequest, "That is the application's only working secret. Create a new one first.")
		return
	}
	c.Secrets = kept
	c.UpdatedAt = now
	if err := s.store.PutClient(r.Context(), c); err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "Could not save.")
		return
	}
	s.audit(r.Context(), "client.secret_retired", map[string]any{"client_id": c.ID, "by": sess.Identity})
	http.Redirect(w, r, "/developers/clients/"+url.PathEscape(c.ID)+"?notice=saved", http.StatusSeeOther)
}

func (s *Server) handleClientDelete(w http.ResponseWriter, r *http.Request) {
	sess := s.requirePost(w, r)
	if sess == nil {
		return
	}
	c := s.ownedClient(w, r, sess)
	if c == nil {
		return
	}
	if strings.TrimSpace(r.PostForm.Get("confirm")) != c.ID {
		s.renderClientError(w, r, c, clientForm{Name: c.Name, RedirectURIs: strings.Join(c.RedirectURIs, "\n")},
			"Type the client ID to confirm deletion.")
		return
	}
	if err := s.store.DeleteClient(r.Context(), c.ID); err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "Could not delete.")
		return
	}
	s.audit(r.Context(), "client.deleted", map[string]any{"client_id": c.ID, "by": sess.Identity})
	http.Redirect(w, r, "/developers", http.StatusSeeOther)
}

func (s *Server) handleAbuse(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, "abuse.html", "Report an application", s.cfg.AbuseContact)
}

// policyView is what /privacy and /security state, from the running config,
// so the page cannot drift from what the bridge does.
type policyView struct {
	Contact         string `json:"contact,omitempty"`
	SecurityContact string `json:"securityContact,omitempty"`
	Pairwise        bool   `json:"pairwise"`
	Audit           string `json:"audit"`
	SignIns         string `json:"signIns"`
	Consents        string `json:"consents"`
	Session         string `json:"session"`
	Txn             string `json:"txn"`
	Code            string `json:"code"`
	AccessToken     string `json:"accessToken"`
	Registration    string `json:"registration"`
}

func (s *Server) policyView() policyView {
	r := s.cfg.Retention
	return policyView{
		Contact:         s.cfg.AbuseContact,
		SecurityContact: s.cfg.SecurityContact,
		Pairwise:        s.cfg.SubjectType == SubjectPairwise,
		Audit:           humanDuration(r.Audit),
		SignIns:         humanDuration(r.SignIns),
		Consents:        humanDuration(r.Consents),
		Session:         humanDuration(s.cfg.SessionTTL),
		Txn:             humanDuration(s.cfg.TxnTTL),
		Code:            humanDuration(codeTTL),
		AccessToken:     humanDuration(accessTokenTTL),
		Registration:    s.cfg.ClientRegistration,
	}
}

func (s *Server) handlePrivacy(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, "privacy.html", "Privacy and retention", s.policyView())
}

func (s *Server) handleSecurity(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, "security.html", "Security and incidents", s.policyView())
}

func humanDuration(d time.Duration) string {
	switch {
	case d >= 24*time.Hour && d%(24*time.Hour) == 0:
		return plural(int(d/(24*time.Hour)), "day")
	case d >= time.Hour && d%time.Hour == 0:
		return plural(int(d/time.Hour), "hour")
	case d >= time.Minute && d%time.Minute == 0:
		return plural(int(d/time.Minute), "minute")
	}
	return d.String()
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}
