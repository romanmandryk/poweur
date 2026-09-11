// Package guestbook is the reference relying party for "Sign in with Poweur
// ID" (EPIC-008 E08-T2). It is a real, deployable site — `go run
// ./cmd/guestbook` — and at the same time the integration fixture the suite
// in apps/integration drives end to end.
//
// It exists to make one claim checkable: a site accepts Poweur identities
// with a verifier value and one Verify call, no account with anybody, no
// registration, no shared secret. Everything below that is not sign-in is
// deliberately boring — entries live in a map, the login session is a cookie.
//
// The whole protocol surface is these three moments:
//
//	req  := srv.verifier.NewRequest(...)          // challenge
//	res  := srv.verifier.Verify(ctx, encoded)     // approval → identity
//	tok  := srv.exchangeGrant(ctx, res)           // optional: the user's home
package guestbook

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/poweur/identity"
	"github.com/poweur/identity/signin"
)

// AppID is the tree namespace this RP owns in a user's home. It is derived
// from the origin at runtime (identity.SignInAppID) — the constant here is
// only the production value, for documentation and the deploy manifest.
const AppID = "net.poweur.guestbook"

// Config configures a Server.
type Config struct {
	// Origin is the RP's own origin ("https://guestbook.poweur.net"). During
	// local development an http:// origin is accepted; a signer warns.
	Origin string
	// Name is what a signer shows the user. Comes back out of
	// /.well-known/poweur.json, which is the only string the signer trusts.
	Name string
	// Scopes requested at sign-in. Empty means login only. The worked
	// example (E08-T4) asks for dav:rw:apps/<app id>, which is the whole
	// point: the guestbook keeps its entries in the *user's* home.
	Scopes []string
	// Resolver overrides identity resolution. Tests inject a fake zone;
	// production leaves it nil and gets the published resolver chain.
	Resolver signin.Resolver
	// ResolveOptions is passed to the default resolver.
	ResolveOptions identity.ResolveOptions
	// HTTPClient is used for the relay grant exchange. Tests inject one that
	// reaches their in-process relay.
	HTTPClient *http.Client
	// Now is injectable for tests.
	Now func() time.Time
}

// Entry is one signed line in the guestbook.
type Entry struct {
	Identity string `json:"identity"`
	Message  string `json:"message"`
	At       string `json:"at"`
	// StoredAt is the path in the *user's own home* this entry was written
	// to, when the user granted storage. Empty when the guestbook is keeping
	// the entry itself.
	StoredAt string `json:"stored_at,omitempty"`
}

// Server is the reference relying party.
type Server struct {
	cfg      Config
	verifier *signin.Verifier
	mux      *http.ServeMux

	mu sync.Mutex
	// pending maps a request_id to the cross-device login waiting on it. The
	// *protocol* is stateless for the verifier; a cross-device UX is not, and
	// this is the only state the RP adds.
	pending map[string]*pendingLogin
	// sessions are the RP's own login cookies. Nothing to do with Poweur
	// session keys — this is an ordinary web session.
	sessions map[string]*Session
	entries  []Entry
}

// Session is a signed-in browser at the RP.
type Session struct {
	Identity  string    `json:"identity"`
	Relay     string    `json:"relay"`
	Scopes    []string  `json:"scopes"`
	ExpiresAt time.Time `json:"-"`
	// Grant is the relay-minted, path-scoped credential for the user's home,
	// when they approved a dav: scope (E08-T4). Never leaves the server.
	Grant    *Grant `json:"-"`
	grantErr string
}

type pendingLogin struct {
	requestID string
	expiresAt time.Time
	token     string // set once an approval lands
	identity  string
	err       string
}

// New builds a Server. It fails rather than starting with an origin the
// protocol cannot bind a response to.
func New(cfg Config) (*Server, error) {
	origin, err := identity.NormalizeOrigin(cfg.Origin)
	if err != nil {
		return nil, fmt.Errorf("guestbook: %w", err)
	}
	cfg.Origin = origin
	if strings.TrimSpace(cfg.Name) == "" {
		cfg.Name = "Poweur Guestbook"
	}
	scopes, err := identity.NormalizeSignInScopes(cfg.Scopes)
	if err != nil {
		return nil, fmt.Errorf("guestbook: %w", err)
	}
	appID, err := identity.SignInAppID(origin)
	if err != nil {
		return nil, fmt.Errorf("guestbook: %w", err)
	}
	for _, s := range scopes {
		if err := identity.CheckSignInScopeNamespace(s, appID); err != nil {
			return nil, fmt.Errorf("guestbook: %w", err)
		}
	}
	cfg.Scopes = scopes

	v, err := signin.NewVerifier(origin)
	if err != nil {
		return nil, err
	}
	v.Resolver = cfg.Resolver
	v.ResolveOptions = cfg.ResolveOptions
	if cfg.Now != nil {
		v.Now = cfg.Now
	}

	s := &Server{
		cfg:      cfg,
		verifier: v,
		pending:  make(map[string]*pendingLogin),
		sessions: make(map[string]*Session),
	}
	s.routes()
	return s, nil
}

func (s *Server) now() time.Time {
	if s.cfg.Now != nil {
		return s.cfg.Now().UTC()
	}
	return time.Now().UTC()
}

// AppID is the tree namespace this server owns, derived from its origin.
func (s *Server) AppID() string {
	id, _ := identity.SignInAppID(s.cfg.Origin)
	return id
}

func (s *Server) routes() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/poweur.json", s.handleMetadata)
	mux.HandleFunc("POST /auth/start", s.handleAuthStart)
	mux.HandleFunc("POST /auth/callback", s.handleAuthCallback)
	mux.HandleFunc("GET /auth/callback", s.handleAuthCallbackRedirect)
	mux.HandleFunc("GET /auth/poll", s.handleAuthPoll)
	mux.HandleFunc("POST /auth/logout", s.handleLogout)
	mux.HandleFunc("GET /api/session", s.handleSession)
	mux.HandleFunc("GET /api/entries", s.handleEntriesGet)
	mux.HandleFunc("POST /api/entries", s.handleEntriesPost)
	mux.HandleFunc("GET /", s.handleIndex)
	s.mux = mux
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

// --- Relying-party metadata --------------------------------------------------

// Metadata is what this RP publishes at /.well-known/poweur.json. A signer
// fetches it over TLS from the audience in the request, which is how it
// verifies that the origin naming itself in a request really is that origin.
func (s *Server) Metadata() signin.Metadata {
	return signin.Metadata{
		PoweurAuth: identity.SignInVersion,
		Origin:     s.cfg.Origin,
		Name:       s.cfg.Name,
		AppID:      s.AppID(),
		// Publishing the list is the hardened posture: it stops an open
		// redirect elsewhere on this site from becoming an approval leak.
		ResponseURIs: []string{s.cfg.Origin + "/auth/callback"},
		PollURI:      s.cfg.Origin + "/auth/poll",
		Scopes:       s.cfg.Scopes,
		Transports:   []string{"redirect", "qr", "deeplink", "poll"},
		ContactURI:   s.cfg.Origin + "/",
	}
}

func (s *Server) handleMetadata(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	writeJSON(w, http.StatusOK, s.Metadata())
}

// --- Sign-in -----------------------------------------------------------------

// StartResponse is what the browser gets when it presses "Sign in with
// Poweur ID": one request rendered into every transport it might use.
type StartResponse struct {
	RequestID string   `json:"request_id"`
	Request   string   `json:"request"`
	DeepLink  string   `json:"deep_link"`
	WebLink   string   `json:"web_link"`
	ExpiresAt string   `json:"expires_at"`
	Scopes    []string `json:"scopes,omitempty"`
	Consent   []string `json:"consent,omitempty"`
}

func (s *Server) handleAuthStart(w http.ResponseWriter, r *http.Request) {
	req, err := s.verifier.NewRequest(signin.RequestOptions{
		Statement:   "Sign the Poweur Guestbook",
		ResponseURI: s.cfg.Origin + "/auth/callback",
		Scopes:      s.cfg.Scopes,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	encoded, err := identity.EncodeSignInRequest(req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	deep, _ := identity.SignInDeepLink(req)
	web, _ := identity.SignInWebLink(signerBase(r), req)

	expires, _ := time.Parse(time.RFC3339, req.ExpiresAt)
	s.mu.Lock()
	s.pending[req.RequestID] = &pendingLogin{requestID: req.RequestID, expiresAt: expires}
	s.prunePendingLocked()
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, StartResponse{
		RequestID: req.RequestID,
		Request:   encoded,
		DeepLink:  deep,
		WebLink:   web,
		ExpiresAt: req.ExpiresAt,
		Scopes:    req.Scopes,
		Consent:   signin.DescribeScopes(req.Scopes, s.cfg.Name),
	})
}

// signerBase is where a browser without a native handler is sent to approve.
// Overridable so a local deployment can point at its own web client.
func signerBase(r *http.Request) string {
	if v := strings.TrimSpace(r.URL.Query().Get("signer")); v != "" {
		return v
	}
	return "https://poweur.net/app/"
}

// handleAuthCallback is the response_uri: the signer POSTs the approval here.
// It accepts a JSON body, a form field, or the raw encoded string, because a
// signer, a CLI and a paste box all end up here.
func (s *Server) handleAuthCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	encoded, err := readApproval(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	session, requestID, err := s.completeSignIn(r.Context(), encoded)
	if err != nil {
		// Record the failure against the pending login so the waiting tab is
		// told what happened instead of spinning until the request expires.
		if requestID != "" {
			s.mu.Lock()
			if p := s.pending[requestID]; p != nil {
				p.err = err.Error()
			}
			s.mu.Unlock()
		}
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"identity": session.Identity,
	})
}

// handleAuthCallbackRedirect is the same delivery over a browser navigation
// (`GET /auth/callback?response=…`), which is how a same-device redirect flow
// lands. It sets the login cookie and sends the user back to the page.
func (s *Server) handleAuthCallbackRedirect(w http.ResponseWriter, r *http.Request) {
	encoded := strings.TrimSpace(r.URL.Query().Get("response"))
	if encoded == "" {
		writeError(w, http.StatusBadRequest, "missing response parameter")
		return
	}
	session, _, err := s.completeSignIn(r.Context(), encoded)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	s.setCookie(w, session.token)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// sessionWithToken carries the cookie value alongside the session.
type sessionWithToken struct {
	*Session
	token string
}

// completeSignIn is the whole protocol obligation of a relying party.
func (s *Server) completeSignIn(ctx context.Context, encoded string) (sessionWithToken, string, error) {
	// Decoded first only so a failure can be reported against the right
	// pending login; Verify re-decodes and trusts nothing from this.
	requestID := ""
	if resp, err := identity.DecodeSignInResponse(encoded); err == nil {
		requestID = resp.RequestID
	}

	result, err := s.verifier.Verify(ctx, encoded)
	if err != nil {
		return sessionWithToken{}, requestID, err
	}

	session := &Session{
		Identity:  result.Identity,
		Relay:     result.Relay,
		Scopes:    result.Scopes,
		ExpiresAt: s.now().Add(12 * time.Hour),
	}
	// The second step, against a different server: swap the same approval for
	// a path-scoped token at the *user's* relay. A failure here is not a
	// failed login — the user is signed in either way, the guestbook just
	// keeps their entry itself.
	if hasDAVScope(result.Scopes) {
		if grant, gerr := s.exchangeGrant(ctx, result, encoded); gerr == nil {
			session.Grant = grant
		} else {
			session.grantErr = gerr.Error()
		}
	}

	token, err := randomToken(24)
	if err != nil {
		return sessionWithToken{}, requestID, err
	}

	s.mu.Lock()
	s.sessions[token] = session
	if p := s.pending[result.RequestID]; p != nil {
		p.token = token
		p.identity = result.Identity
	}
	s.mu.Unlock()

	return sessionWithToken{Session: session, token: token}, result.RequestID, nil
}

// handleAuthPoll is what the tab that started the login watches while the
// user approves on their phone.
func (s *Server) handleAuthPoll(w http.ResponseWriter, r *http.Request) {
	requestID := strings.TrimSpace(r.URL.Query().Get("request_id"))
	if requestID == "" {
		writeError(w, http.StatusBadRequest, "missing request_id")
		return
	}
	s.mu.Lock()
	p := s.pending[requestID]
	var out map[string]any
	switch {
	case p == nil:
		out = map[string]any{"status": "unknown"}
	case p.err != "":
		out = map[string]any{"status": "failed", "error": p.err}
		delete(s.pending, requestID)
	case p.token != "":
		out = map[string]any{"status": "complete", "identity": p.identity}
		// One-shot: the poll hands the cookie over exactly once.
		s.setCookie(w, p.token)
		delete(s.pending, requestID)
	case s.now().After(p.expiresAt):
		out = map[string]any{"status": "expired"}
		delete(s.pending, requestID)
	default:
		out = map[string]any{"status": "pending"}
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) prunePendingLocked() {
	now := s.now()
	for k, p := range s.pending {
		if now.After(p.expiresAt.Add(time.Minute)) {
			delete(s.pending, k)
		}
	}
}

// --- The RP's own session ----------------------------------------------------

const cookieName = "guestbook_session"

func (s *Server) setCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   strings.HasPrefix(s.cfg.Origin, "https://"),
	})
}

func (s *Server) sessionFor(r *http.Request) (*Session, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[c.Value]
	if !ok || s.now().After(sess.ExpiresAt) {
		delete(s.sessions, c.Value)
		return nil, false
	}
	return sess, true
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionFor(r)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"signed_in": false})
		return
	}
	out := map[string]any{
		"signed_in": true,
		"identity":  sess.Identity,
		"relay":     sess.Relay,
		"scopes":    sess.Scopes,
		"app_id":    s.AppID(),
	}
	if sess.Grant != nil {
		out["home_storage"] = sess.Grant.Path
		out["grant_expires_at"] = sess.Grant.ExpiresAt.UTC().Format(time.RFC3339)
	} else if sess.grantErr != "" {
		out["home_storage_error"] = sess.grantErr
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		s.mu.Lock()
		delete(s.sessions, c.Value)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, http.StatusOK, map[string]string{"status": "signed out"})
}

// --- Entries -----------------------------------------------------------------

func (s *Server) handleEntriesGet(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	entries := append([]Entry(nil), s.entries...)
	s.mu.Unlock()
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].At > entries[j].At })
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

func (s *Server) handleEntriesPost(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionFor(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "sign in first")
		return
	}
	var body struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8*1024)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "body must be JSON")
		return
	}
	message := strings.TrimSpace(body.Message)
	if message == "" {
		writeError(w, http.StatusBadRequest, "message is required")
		return
	}
	if len(message) > 500 {
		message = message[:500]
	}
	entry := Entry{Identity: sess.Identity, Message: message, At: s.now().Format(time.RFC3339)}

	// The data-portability demo: when the user granted storage, the entry is
	// written into *their* home, in this app's namespace, and the guestbook's
	// own copy is a cache. Revoke the grant and the app loses the writing,
	// not the user.
	if sess.Grant != nil {
		path, err := s.writeToHome(r.Context(), sess, entry)
		if err != nil {
			writeError(w, http.StatusBadGateway, "could not write to your home: "+err.Error())
			return
		}
		entry.StoredAt = path
	}

	s.mu.Lock()
	s.entries = append(s.entries, entry)
	s.mu.Unlock()
	writeJSON(w, http.StatusCreated, entry)
}

// Entries returns the guestbook's contents (tests, and the CLI demo).
func (s *Server) Entries() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Entry(nil), s.entries...)
}

// --- helpers -----------------------------------------------------------------

func hasDAVScope(scopes []string) bool {
	for _, s := range scopes {
		if _, _, ok := identity.SignInScopePath(s); ok {
			return true
		}
	}
	return false
}

// readApproval accepts the three shapes an approval arrives in: a JSON body
// {"response": "..."} or the response object itself, a form field, or the raw
// encoded string as the whole body.
func readApproval(r *http.Request) (string, error) {
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "application/x-www-form-urlencoded") {
		if err := r.ParseForm(); err != nil {
			return "", errors.New("malformed form body")
		}
		if v := strings.TrimSpace(r.PostForm.Get("response")); v != "" {
			return v, nil
		}
		return "", errors.New("missing response field")
	}
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	body := http.MaxBytesReader(nil, r.Body, 64*1024)
	for {
		n, err := body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			break
		}
	}
	raw := strings.TrimSpace(string(buf))
	if raw == "" {
		return "", errors.New("empty body")
	}
	if strings.HasPrefix(raw, "{") {
		var envelope struct {
			Response json.RawMessage `json:"response"`
		}
		if err := json.Unmarshal([]byte(raw), &envelope); err == nil && len(envelope.Response) > 0 {
			var asString string
			if err := json.Unmarshal(envelope.Response, &asString); err == nil {
				return asString, nil
			}
			return string(envelope.Response), nil
		}
		// A bare response object.
		return raw, nil
	}
	return raw, nil
}

func randomToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func joinURL(base, path string) string {
	u, err := url.Parse(base)
	if err != nil {
		return strings.TrimRight(base, "/") + path
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	return u.String()
}
