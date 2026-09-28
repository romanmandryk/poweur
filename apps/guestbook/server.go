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
// The whole protocol surface is these two moments:
//
//	req  := srv.verifier.NewRequest(...)          // challenge
//	res  := srv.verifier.Verify(ctx, encoded)     // approval → identity
//
// Storage v1 also let the guestbook write entries into the user's own home
// through a path-scoped grant. That returns on storage v2 as a scoped drive
// handle (EPIC-020, EPIC-029); until then entries live here only.
package guestbook

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
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

// AppID identifies this RP. It is derived from the origin at runtime
// (identity.SignInAppID) — the constant here is only the production value,
// for documentation and the deploy manifest.
const AppID = "net.poweur.guestbook"

// Config configures a Server.
type Config struct {
	// Origin is the RP's own origin ("https://guestbook.poweur.net"). During
	// local development an http:// origin is accepted; a signer warns.
	Origin string
	// Name is what a signer shows the user. Comes back out of
	// /.well-known/poweur.json, which is the only string the signer trusts.
	Name string
	// Scopes requested at sign-in. Empty means login only.
	Scopes []string
	// Resolver overrides identity resolution. Tests inject a fake zone;
	// production leaves it nil and gets the published resolver chain.
	Resolver signin.Resolver
	// ResolveOptions is passed to the default resolver.
	ResolveOptions identity.ResolveOptions
	// Now is injectable for tests.
	Now func() time.Time
}

// Entry is one signed line in the guestbook.
type Entry struct {
	Identity string `json:"identity"`
	Message  string `json:"message"`
	At       string `json:"at"`
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
}

// pendingLogin is one sign-in in progress. Every value whose possession is
// the proof is stored hashed.
type pendingLogin struct {
	requestID   string
	expiresAt   time.Time
	bindingHash string // the starting browser's binding cookie
	startedAt   time.Time
	browser     string // coarse label for the signer's context line
	pollHash    string // the starting page's poll secret
	match       string // shown on the starting screen
	code        string // short code: the request by reference at /auth/r/{code}
	encoded     string // the request /auth/r/{code} serves
	signer      string // the web signer a browser opening the short link is sent to
	claimed     bool   // an approval has been received; no second one is processed

	finish        string // finishSameDevice or finishCrossDevice, once approved
	token         string // the prepared session, not yet handed to anyone
	identity      string
	resumeHash    string
	resumeExpires time.Time
	resumed       bool
	err           string
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
	if _, err := identity.SignInAppID(origin); err != nil {
		return nil, fmt.Errorf("guestbook: %w", err)
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
	mux.HandleFunc("GET /auth/callback", s.handleAuthCallbackGet)
	mux.HandleFunc("GET /auth/resume", s.handleAuthResume)
	mux.HandleFunc("GET /auth/poll", s.handleAuthPoll)
	mux.HandleFunc("GET /auth/context", s.handleAuthContext)
	mux.HandleFunc("GET /auth/r/{code}", s.handleAuthByReference)
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
		ContextURI:   s.cfg.Origin + "/auth/context",
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
// Poweur ID": one request rendered into every transport it might use, plus
// the two values only this page may know.
type StartResponse struct {
	RequestID string `json:"request_id"`
	Request   string `json:"request"`
	// RequestLink is the request by reference (E08-T6): a short link for
	// another device, which a signer fetches and a browser follows.
	RequestLink string   `json:"request_link"`
	DeepLink    string   `json:"deep_link"`
	WebLink     string   `json:"web_link"`
	ExpiresAt   string   `json:"expires_at"`
	Scopes      []string `json:"scopes,omitempty"`
	Consent     []string `json:"consent,omitempty"`
	// PollSecret authenticates this page's polling. It is deliberately not
	// the request_id, which travels inside the request to whoever approves.
	PollSecret string `json:"poll_secret"`
	// MatchCode is shown on this screen. A signer on another device sends it
	// back, which is the proof that the approver was looking at this screen.
	MatchCode string `json:"match_code"`
}

// How a sign-in may finish. The approval decides, never the page that started
// it: see "Who may complete a sign-in" in apps/docs/docs/auth/sign-in.md.
const (
	// finishSameDevice: through /auth/resume, in the browser that holds the
	// binding cookie set by /auth/start.
	finishSameDevice = "same-device"
	// finishCrossDevice: through /auth/poll, because the signer sent back the
	// match code the starting screen showed.
	finishCrossDevice = "cross-device"
)

// resumeWindow is how long a same-device approval waits for its browser.
const resumeWindow = time.Minute

func (s *Server) handleAuthStart(w http.ResponseWriter, r *http.Request) {
	binding, err := s.browserBinding(w, r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
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
	pollSecret, err := signin.NewSecret()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	match, err := signin.NewMatchCode()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	deep, _ := identity.SignInDeepLink(req)
	web, _ := identity.SignInWebLink(signerBase(r), req)
	code, err := identity.NewShortCode()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	expires, _ := time.Parse(time.RFC3339, req.ExpiresAt)
	s.mu.Lock()
	s.pending[req.RequestID] = &pendingLogin{
		requestID:   req.RequestID,
		expiresAt:   expires,
		bindingHash: signin.HashSecret(binding),
		startedAt:   s.now(),
		browser:     coarseBrowser(r.UserAgent()),
		pollHash:    signin.HashSecret(pollSecret),
		match:       match,
		code:        code,
		encoded:     encoded,
		signer:      signerBase(r),
	}
	s.prunePendingLocked()
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, StartResponse{
		RequestID:   req.RequestID,
		Request:     encoded,
		RequestLink: s.cfg.Origin + "/auth/r/" + code,
		DeepLink:    deep,
		WebLink:     web,
		ExpiresAt:   req.ExpiresAt,
		Scopes:      req.Scopes,
		Consent:     signin.DescribeScopes(req.Scopes, s.cfg.Name),
		PollSecret:  pollSecret,
		MatchCode:   match,
	})
}

// handleAuthByReference serves a pending request by its short code: JSON to a
// signer (which checks the request names this origin as its audience), and a
// redirect into the web signer for a browser that opened the link.
func (s *Server) handleAuthByReference(w http.ResponseWriter, r *http.Request) {
	code, err := identity.NormalizeShortCode(r.PathValue("code"))
	var p *pendingLogin
	if err == nil {
		s.mu.Lock()
		for _, candidate := range s.pending {
			if candidate.code == code && !candidate.claimed && s.now().Before(candidate.expiresAt) {
				p = candidate
				break
			}
		}
		s.mu.Unlock()
	}
	if p == nil {
		writeError(w, http.StatusGone, "this sign-in code expired or was already used")
		return
	}
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, map[string]string{"request": p.encoded})
		return
	}
	http.Redirect(w, r, p.signer+"?auth="+url.QueryEscape(s.cfg.Origin+"/auth/r/"+code), http.StatusSeeOther)
}

// browserBinding returns this browser's login-binding value, setting the
// cookie on first use. One value per browser rather than per attempt, so two
// tabs signing in at once do not orphan each other.
func (s *Server) browserBinding(w http.ResponseWriter, r *http.Request) (string, error) {
	if c, err := r.Cookie(bindingCookieName); err == nil && len(c.Value) >= 43 {
		return c.Value, nil
	}
	v, err := signin.NewSecret()
	if err != nil {
		return "", err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     bindingCookieName,
		Value:    v,
		Path:     "/auth/",
		HttpOnly: true,
		// Lax, not Strict: the resume is a top-level navigation arriving from
		// the signer's origin, and must carry this cookie.
		SameSite: http.SameSiteLaxMode,
		Secure:   strings.HasPrefix(s.cfg.Origin, "https://"),
	})
	return v, nil
}

// signerBase is where a browser without a native handler is sent to approve.
// Overridable so a local deployment can point at its own web client.
func signerBase(r *http.Request) string {
	if v := strings.TrimSpace(r.URL.Query().Get("signer")); v != "" {
		return v
	}
	return "https://poweur.net/app/"
}

// handleAuthCallback is the response_uri: a signer POSTs the approval here.
//
// It also decides how the sign-in may finish, and only it can: a delivery
// carrying the match code came from someone looking at the starting screen,
// and one without it must finish in the browser that started it.
func (s *Server) handleAuthCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	d, err := signin.ParseDelivery(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	requestID := ""
	if resp, err := identity.DecodeSignInResponse(d.Response); err == nil {
		requestID = resp.RequestID
	}
	crossDevice := d.Match != ""

	// Claim the transaction before verifying: one approval per sign-in is
	// ever processed, so a second approver — or a second guess at the match
	// code — has nothing to race for.
	s.mu.Lock()
	p := s.pending[requestID]
	switch {
	case p == nil || s.now().After(p.expiresAt):
		s.mu.Unlock()
		writeError(w, http.StatusNotFound, "no sign-in is waiting for this approval")
		return
	case p.claimed:
		s.mu.Unlock()
		writeError(w, http.StatusConflict, "this sign-in has already been answered")
		return
	}
	p.claimed = true
	if crossDevice && !signin.MatchCodesEqual(p.match, d.Match) {
		p.err = signin.ErrMatchCode.Error()
		s.mu.Unlock()
		writeError(w, http.StatusForbidden, p.err)
		return
	}
	s.mu.Unlock()

	session, err := s.completeSignIn(r.Context(), d.Response)
	if err != nil {
		s.failPending(requestID, err.Error())
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}

	receipt := signin.DeliveryReceipt{Status: "ok", Identity: session.Identity}
	var resumeCode string
	if !crossDevice {
		if resumeCode, err = signin.NewSecret(); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		receipt.ResumeURI = s.cfg.Origin + "/auth/resume?code=" + url.QueryEscape(resumeCode)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if p = s.pending[requestID]; p == nil {
		delete(s.sessions, session.token)
		writeError(w, http.StatusGone, "the sign-in expired while it was being verified")
		return
	}
	p.token = session.token
	p.identity = session.Identity
	if crossDevice {
		p.finish = finishCrossDevice
	} else {
		p.finish = finishSameDevice
		p.resumeHash = signin.HashSecret(resumeCode)
		p.resumeExpires = s.now().Add(resumeWindow)
	}
	writeJSON(w, http.StatusOK, receipt)
}

// handleAuthCallbackGet refuses approvals carried in a URL. History, server
// logs and Referer headers all keep URLs, and an approval belongs in none of
// them. Signers POST, then send the browser to the resume_uri they get back.
func (s *Server) handleAuthCallbackGet(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	writeError(w, http.StatusBadRequest,
		"approvals are accepted only by POST to this URL; the browser continues at the resume_uri the POST returns")
}

// handleAuthResume finishes a same-device sign-in. Two halves must meet in one
// browser: the resume code, which only the approving signer received, and the
// binding cookie, which only the browser that pressed "Sign in" holds. A
// forwarded sign-in link splits them between attacker and victim, and then
// nobody is signed in.
func (s *Server) handleAuthResume(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	binding := ""
	if c, err := r.Cookie(bindingCookieName); err == nil {
		binding = c.Value
	}

	s.mu.Lock()
	var p *pendingLogin
	if code != "" {
		for _, candidate := range s.pending {
			if candidate.finish == finishSameDevice && signin.SecretMatches(candidate.resumeHash, code) {
				p = candidate
				break
			}
		}
	}
	if p == nil || s.now().After(p.resumeExpires) {
		s.mu.Unlock()
		resumeFailed(w, http.StatusGone, "This sign-in link has expired or was already used.")
		return
	}
	// Single use, whatever happens next.
	p.resumeHash = ""
	if !signin.SecretMatches(p.bindingHash, binding) {
		delete(s.sessions, p.token)
		p.token = ""
		p.err = "the sign-in was approved for a different browser from the one that started it"
		s.mu.Unlock()
		resumeFailed(w, http.StatusForbidden,
			"This sign-in was started in a different browser. If you did not start it yourself, someone may have sent you their sign-in link — nothing was signed in.")
		return
	}
	token := p.token
	p.resumed = true
	s.mu.Unlock()

	s.setCookie(w, token)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func resumeFailed(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = resumeFailedTemplate.Execute(w, message)
}

func (s *Server) failPending(requestID, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.pending[requestID]; p != nil {
		p.err = message
	}
}

// sessionWithToken carries the cookie value alongside the session.
type sessionWithToken struct {
	*Session
	token string
}

// completeSignIn is the whole protocol obligation of a relying party. The
// session it creates is not handed to anyone: the caller decides which
// browser may have it.
func (s *Server) completeSignIn(ctx context.Context, encoded string) (sessionWithToken, error) {
	result, err := s.verifier.Verify(ctx, encoded)
	if err != nil {
		return sessionWithToken{}, err
	}

	session := &Session{
		Identity:  result.Identity,
		Relay:     result.Relay,
		Scopes:    result.Scopes,
		ExpiresAt: s.now().Add(12 * time.Hour),
	}
	token, err := randomToken(24)
	if err != nil {
		return sessionWithToken{}, err
	}
	s.mu.Lock()
	s.sessions[token] = session
	s.mu.Unlock()
	return sessionWithToken{Session: session, token: token}, nil
}

// handleAuthPoll is what the tab that started the login watches. It needs
// the poll secret only that tab was given; a request_id alone — which anyone
// who saw the QR code knows — gets the same answer as a request that never
// existed.
func (s *Server) handleAuthPoll(w http.ResponseWriter, r *http.Request) {
	requestID := strings.TrimSpace(r.URL.Query().Get("request_id"))
	secret := strings.TrimSpace(r.URL.Query().Get("poll_secret"))
	if requestID == "" || secret == "" {
		writeError(w, http.StatusBadRequest, "missing request_id or poll_secret")
		return
	}
	s.mu.Lock()
	p := s.pending[requestID]
	if p != nil && !signin.SecretMatches(p.pollHash, secret) {
		p = nil
	}
	now := s.now()
	var out map[string]any
	switch {
	case p == nil:
		out = map[string]any{"status": "unknown"}
	case p.err != "":
		out = map[string]any{"status": "failed", "error": p.err}
		s.dropPendingLocked(p)
	case p.finish == finishCrossDevice:
		out = map[string]any{"status": "complete", "identity": p.identity}
		// One-shot: the poll hands the cookie over exactly once.
		s.setCookie(w, p.token)
		delete(s.pending, requestID)
	case p.finish == finishSameDevice && p.resumed:
		// The resume already set the cookie in this browser.
		out = map[string]any{"status": "complete", "identity": p.identity}
		delete(s.pending, requestID)
	case p.finish == finishSameDevice && now.After(p.resumeExpires):
		out = map[string]any{"status": "expired"}
		s.dropPendingLocked(p)
	case p.finish == finishSameDevice:
		// Approved; the signer is sending this browser to /auth/resume.
		out = map[string]any{"status": "approved"}
	case now.After(p.expiresAt):
		out = map[string]any{"status": "expired"}
		s.dropPendingLocked(p)
	default:
		out = map[string]any{"status": "pending"}
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, out)
}

// handleAuthContext tells a signer on another device where this sign-in was
// started, while it can still be approved.
func (s *Server) handleAuthContext(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	requestID := strings.TrimSpace(r.URL.Query().Get("request_id"))
	s.mu.Lock()
	p := s.pending[requestID]
	var out *signin.SignInContext
	if p != nil && !p.claimed && p.err == "" && !s.now().After(p.expiresAt) {
		out = &signin.SignInContext{RequestID: requestID, StartedAt: p.startedAt, Browser: p.browser}
	}
	s.mu.Unlock()
	if out == nil {
		writeError(w, http.StatusNotFound, "no sign-in is waiting for this request")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// coarseBrowser keeps a browser and OS name, never the full user agent.
func coarseBrowser(ua string) string {
	lower := strings.ToLower(ua)
	browser := "a browser"
	for _, b := range []struct{ token, name string }{
		{"edg/", "Edge"}, {"firefox/", "Firefox"}, {"chrome/", "Chrome"}, {"safari/", "Safari"},
	} {
		if strings.Contains(lower, b.token) {
			browser = b.name
			break
		}
	}
	for _, o := range []struct{ token, name string }{
		{"iphone", "iOS"}, {"ipad", "iOS"}, {"android", "Android"}, {"mac os", "macOS"}, {"windows", "Windows"}, {"linux", "Linux"},
	} {
		if strings.Contains(lower, o.token) {
			return browser + " on " + o.name
		}
	}
	return browser
}

// dropPendingLocked forgets a transaction and any session prepared for it but
// never handed out.
func (s *Server) dropPendingLocked(p *pendingLogin) {
	if p.token != "" && !p.resumed {
		delete(s.sessions, p.token)
	}
	delete(s.pending, p.requestID)
}

func (s *Server) prunePendingLocked() {
	now := s.now()
	for _, p := range s.pending {
		deadline := p.expiresAt
		if p.resumeExpires.After(deadline) {
			deadline = p.resumeExpires
		}
		if now.After(deadline.Add(time.Minute)) {
			s.dropPendingLocked(p)
		}
	}
}

// --- The RP's own session ----------------------------------------------------

const (
	cookieName = "guestbook_session"
	// bindingCookieName ties a sign-in to the browser that started it.
	bindingCookieName = "guestbook_login"
)

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
