package bridge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/poweur/identity"
	"github.com/poweur/identity/signin"
)

// Config configures a Server.
type Config struct {
	// Issuer is the bridge's exact origin: "https://oauth.poweur.org". It is
	// the OIDC `iss` and the native Sign-In `audience`. http:// is accepted
	// only for local development.
	Issuer string
	// Name is what signers and pages show. Default "Poweur OAuth bridge".
	Name string
	// Store holds all state. Required.
	Store *Store

	// DefaultSigner is the web signer offered when an identity advertises
	// none, e.g. "https://poweur.net/app/". Optional.
	DefaultSigner string
	// LauncherURL is where someone without a Poweur ID creates one — a relay
	// origin serving the web app's claim flow, e.g. "https://poweur.net".
	// Empty hides the offer.
	LauncherURL string
	// AnalyticsToken is a Better Stack web-analytics token. When set, the public
	// information pages (home, privacy, security, abuse) load its script; the
	// sign-in, consent, account and developer pages never do. Optional.
	AnalyticsToken string
	// LauncherDomain is the hosted domain new names are created under.
	// Defaults to the launcher's host without a leading "id.".
	LauncherDomain string
	// ResolveOptions configures identity resolution (and the fetches of
	// capabilities and profiles, which use the same SSRF rules).
	ResolveOptions identity.ResolveOptions
	// Resolver overrides identity resolution for native verification. Tests.
	Resolver signin.Resolver
	// FetchCapabilities overrides the capabilities fetch. Tests.
	FetchCapabilities func(ctx context.Context, id string) (identity.Capabilities, error)
	// FetchProfile overrides the public-profile fetch. Tests.
	FetchProfile func(ctx context.Context, id string) (identity.Profile, error)

	// SessionTTL is how long a browser stays signed in to the bridge.
	SessionTTL time.Duration // default 12h
	// TxnTTL bounds a whole authorization journey.
	TxnTTL time.Duration // default 10m
	// RequestTTL is the native request's validity (≤ 5 minutes).
	RequestTTL time.Duration // default 3m

	// OIDC (E22-T3).

	// KeyEncryptionKey encrypts issuer signing keys at rest: 32 bytes.
	// Required unless Keys is set.
	KeyEncryptionKey []byte
	// Keys overrides the key ring. Tests.
	Keys *KeyRing
	// SubjectType is "pairwise" (default) or "public".
	SubjectType string
	// StaticClients are operator-configured clients (OAUTH_STATIC_CLIENTS).
	StaticClients []Client
	// ClientRegistration is "open" (default: any signed-in Poweur ID may
	// register applications), "allowlist" or "closed" — for deployments that
	// serve only their own applications.
	ClientRegistration string
	// RegistrationAllowlist lists identities (or "*.domain" suffixes) that
	// may register clients when ClientRegistration is "allowlist".
	RegistrationAllowlist []string
	// MaxClientsPerOwner caps console clients per owner. Default 10.
	MaxClientsPerOwner int
	// URLClients is "indieauth" (default), "on" or "off".
	URLClients string
	// FetchClientMetadata overrides URL-client document fetches. Tests.
	FetchClientMetadata func(ctx context.Context, clientID string) ([]byte, error)

	// Pusher delivers sign-in prompts to the user's app as `sys.auth.request`
	// messages (E22-T7). Nil disables the "Send to my Poweur app" button.
	Pusher Pusher
	// PushIdentity is the bridge's own Poweur ID, which users must list under
	// trusted sign-in services before prompts reach them.
	PushIdentity string

	// ContactURI is shown on consent pages for abuse reports.
	ContactURI string
	// AbuseContact is what /abuse tells people to write to (an email address
	// or URL).
	AbuseContact string
	// SecurityContact is where /security asks vulnerability reports to go.
	// Defaults to AbuseContact.
	SecurityContact string

	// RateLimits per client IP; zero values use the defaults.
	RateLimits RateLimits
	// TrustProxyHeaders believes the last X-Forwarded-For hop for rate
	// limiting. Set it only behind a proxy that appends that header.
	TrustProxyHeaders bool

	Retention Retention
	Logger    *slog.Logger
	Now       func() time.Time
}

// Server is the bridge.
type Server struct {
	cfg          Config
	store        *Store
	verifier     *signin.Verifier
	keys         *KeyRing
	pairwise     []byte
	mux          *http.ServeMux
	log          *slog.Logger
	secure       bool
	limits       limiters
	csp          string
	cspAnalytics string // csp plus Better Stack, for analyticsPages; empty when analytics is off
	metrics      *metrics
}

// New validates cfg and builds a Server.
func New(ctx context.Context, cfg Config) (*Server, error) {
	if cfg.AnalyticsToken != "" && !analyticsTokenRE.MatchString(cfg.AnalyticsToken) {
		return nil, errors.New("bridge: AnalyticsToken must be 8-64 letters and digits")
	}
	issuer, err := identity.NormalizeOrigin(cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("bridge: issuer: %w", err)
	}
	cfg.Issuer = issuer
	// The native sign-in derives an app namespace from the audience host, so
	// a single-label host (plain "localhost") can never sign anyone in.
	if _, err := identity.SignInAppID(issuer); err != nil {
		return nil, fmt.Errorf("bridge: issuer %s cannot be a sign-in audience (%v); use a name with a dot, e.g. http://oauth.localhost:8090", issuer, err)
	}
	if cfg.Store == nil {
		return nil, errors.New("bridge: a store is required")
	}
	if strings.TrimSpace(cfg.Name) == "" {
		cfg.Name = "Poweur OAuth bridge"
	}
	if cfg.SessionTTL == 0 {
		cfg.SessionTTL = 12 * time.Hour
	}
	if cfg.TxnTTL == 0 {
		cfg.TxnTTL = 10 * time.Minute
	}
	if cfg.RequestTTL == 0 {
		cfg.RequestTTL = 3 * time.Minute
	}
	if cfg.RequestTTL > identity.SignInMaxTTL {
		return nil, fmt.Errorf("bridge: RequestTTL exceeds %s", identity.SignInMaxTTL)
	}
	switch cfg.SubjectType {
	case "":
		cfg.SubjectType = SubjectPairwise
	case SubjectPairwise, SubjectPublic:
	default:
		return nil, fmt.Errorf("bridge: SubjectType must be %q or %q", SubjectPairwise, SubjectPublic)
	}
	switch cfg.ClientRegistration {
	case "":
		cfg.ClientRegistration = RegistrationOpen
	case RegistrationOpen, RegistrationAllowlist, RegistrationClosed:
	default:
		return nil, fmt.Errorf("bridge: ClientRegistration must be open, allowlist or closed")
	}
	switch cfg.URLClients {
	case "":
		cfg.URLClients = URLClientsIndieAuth
	case URLClientsIndieAuth, URLClientsOn, URLClientsOff:
	default:
		return nil, fmt.Errorf("bridge: URLClients must be indieauth, on or off")
	}
	if cfg.MaxClientsPerOwner == 0 {
		cfg.MaxClientsPerOwner = 10
	}
	if cfg.DefaultSigner != "" {
		if u, err := url.Parse(cfg.DefaultSigner); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return nil, fmt.Errorf("bridge: DefaultSigner must be an absolute http(s) URL")
		}
	}
	if cfg.LauncherURL != "" {
		launcher, err := identity.NormalizeOrigin(cfg.LauncherURL)
		if err != nil {
			return nil, fmt.Errorf("bridge: LauncherURL: %w", err)
		}
		cfg.LauncherURL = launcher
		if cfg.LauncherDomain == "" {
			u, _ := url.Parse(launcher)
			cfg.LauncherDomain = strings.TrimPrefix(u.Hostname(), "id.")
		}
		cfg.LauncherDomain = strings.ToLower(strings.Trim(cfg.LauncherDomain, ". "))
		if cfg.LauncherDomain == "" || strings.ContainsAny(cfg.LauncherDomain, "/:@ ") {
			return nil, fmt.Errorf("bridge: LauncherDomain %q is not a domain", cfg.LauncherDomain)
		}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	cfg.Store.now = cfg.Now
	if cfg.SecurityContact == "" {
		cfg.SecurityContact = cfg.AbuseContact
	}
	cfg.Retention = cfg.Retention.withDefaults()
	if cfg.ContactURI == "" {
		cfg.ContactURI = issuer + "/abuse"
	}
	for i := range cfg.StaticClients {
		c := &cfg.StaticClients[i]
		c.Source = ClientStatic
		if err := c.validate(strings.HasPrefix(issuer, "http://")); err != nil {
			return nil, fmt.Errorf("bridge: static client %q: %w", c.ID, err)
		}
	}

	if cfg.ResolveOptions.HTTPClient == nil {
		// Identity documents are fetched from hosts a stranger typed in.
		cfg.ResolveOptions.HTTPClient = newSafeHTTPClient(cfg.ResolveOptions.AllowPrivate)
	}
	if cfg.ResolveOptions.Cache == nil {
		cfg.ResolveOptions.Cache = identity.NewCache()
	}

	v, err := signin.NewVerifier(issuer)
	if err != nil {
		return nil, err
	}
	v.Nonces = cfg.Store
	v.Resolver = cfg.Resolver
	v.ResolveOptions = cfg.ResolveOptions
	v.RequestTTL = cfg.RequestTTL
	v.Now = cfg.Now

	s := &Server{
		cfg:      cfg,
		store:    cfg.Store,
		verifier: v,
		log:      cfg.Logger,
		secure:   strings.HasPrefix(issuer, "https://"),
		limits:   newLimiters(cfg.RateLimits, cfg.Now),
		metrics:  newMetrics(cfg.Now()),
	}
	// No third-party content, no framing (consent is a clickjacking target),
	// no inline script. form-action is deliberately absent: Chrome applies it
	// to the redirect that follows a consent POST, which must reach the client.
	// The launcher is the one other origin pages talk to: the create-an-ID
	// offer checks name availability there, from the visitor's browser.
	connect := "'self'"
	if cfg.LauncherURL != "" {
		connect += " " + cfg.LauncherURL
	}
	s.csp = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
		"connect-src " + connect + "; frame-ancestors 'none'; base-uri 'none'"
	// Analytics widens the policy only on the pages that load it (analyticsPages).
	if cfg.AnalyticsToken != "" {
		s.cspAnalytics = "default-src 'none'; script-src 'self' " + analyticsScriptOrigin + "; style-src 'self'; " +
			"img-src 'self' data: " + analyticsDataOrigin + "; connect-src " + connect + " " + analyticsDataOrigin +
			"; worker-src blob:; frame-ancestors 'none'; base-uri 'none'"
	}
	s.keys = cfg.Keys
	if s.keys == nil {
		if len(cfg.KeyEncryptionKey) != 32 {
			return nil, errors.New("bridge: KeyEncryptionKey must be 32 bytes")
		}
		s.keys, err = LoadKeyRing(ctx, cfg.Store, cfg.KeyEncryptionKey, cfg.Now)
		if err != nil {
			return nil, err
		}
	}
	s.pairwise, err = cfg.Store.getOrCreateSecret(ctx, "pairwise_subject", func() ([]byte, error) {
		return randomBytes(32)
	})
	if err != nil {
		return nil, fmt.Errorf("bridge: pairwise secret: %w", err)
	}
	s.routes()
	return s, nil
}

// Keys exposes the key ring to the operator CLI.
func (s *Server) Keys() *KeyRing { return s.keys }

// Run does the periodic housekeeping until ctx ends: pruning expired state and
// picking up a key rotation done by another process.
func (s *Server) Run(ctx context.Context) {
	prune := time.NewTicker(time.Hour)
	reload := time.NewTicker(5 * time.Minute)
	defer prune.Stop()
	defer reload.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-prune.C:
			if err := s.store.Prune(ctx, s.cfg.Retention); err != nil {
				s.log.Warn("prune", "err", err)
			}
		case <-reload.C:
			if err := s.keys.Reload(ctx); err != nil {
				s.log.Warn("reload keys", "err", err)
			}
		}
	}
}

// Issuer is the bridge's origin.
func (s *Server) Issuer() string { return s.cfg.Issuer }

func (s *Server) now() time.Time { return s.cfg.Now().UTC() }

func (s *Server) routes() {
	mux := http.NewServeMux()

	// Native relying party (E22-T2).
	mux.HandleFunc("GET /.well-known/poweur.json", s.handleNativeMetadata)
	mux.HandleFunc("POST /poweur/callback", s.handleNativeCallback)
	mux.HandleFunc("GET /poweur/callback", s.handleNativeCallbackGet)
	mux.HandleFunc("GET /poweur/resume", s.handleNativeResume)
	mux.HandleFunc("GET /poweur/context", s.handleNativeContext)
	mux.HandleFunc("GET /login", s.handleLogin)
	mux.HandleFunc("POST /logout", s.handleLogout)
	mux.HandleFunc("GET /t/{id}", s.handleTxnPage)
	mux.HandleFunc("POST /t/{id}/identify", s.handleIdentify)
	mux.HandleFunc("GET /t/{id}/status", s.handleTxnStatus)
	mux.HandleFunc("GET /t/{id}/continue", s.handleTxnContinue)
	mux.HandleFunc("POST /t/{id}/cancel", s.handleTxnCancel)
	mux.HandleFunc("POST /t/{id}/push", s.handlePush)
	mux.HandleFunc("POST /t/{id}/creating", s.handleCreating)
	mux.HandleFunc("GET /r/{code}", s.handleRequestByReference)

	// OIDC provider (E22-T3).
	mux.HandleFunc("GET /.well-known/openid-configuration", s.handleDiscovery)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", s.handleDiscovery)
	mux.HandleFunc("GET /jwks.json", s.handleJWKS)
	mux.HandleFunc("GET /authorize", s.handleAuthorize)
	mux.HandleFunc("POST /authorize", s.handleAuthorize)
	mux.HandleFunc("POST /t/{id}/consent", s.handleConsent)
	mux.HandleFunc("POST /token", s.handleToken)
	mux.HandleFunc("GET /userinfo", s.handleUserInfo)
	mux.HandleFunc("POST /userinfo", s.handleUserInfo)
	mux.HandleFunc("POST /revoke", s.handleRevoke)
	mux.HandleFunc("POST /introspect", s.handleIntrospect)

	// Pages.
	mux.HandleFunc("GET /assets/{file}", s.handleAsset)
	mux.HandleFunc("GET /analytics.js", s.handleAnalytics)
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /{$}", s.handleHome)
	s.extraRoutes(mux)
	s.mux = mux
}

// ServeHTTP applies the headers every response carries, then routes.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Content-Security-Policy", s.csp)
	if s.secure {
		h.Set("Strict-Transport-Security", "max-age=31536000")
	}
	if s.rateLimited(w, r) {
		return
	}
	start := time.Now()
	rec := &statusRecorder{ResponseWriter: w}
	s.mux.ServeHTTP(rec, r)
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	s.metrics.observe(routeOf(r), rec.status, time.Since(start))
}

// handleHealth answers ok only when the bridge could sign someone in: the
// database reads and a signing key is loaded.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{
		"status":  "ok",
		"service": "poweur-oauth",
		"version": Version,
		"issuer":  s.cfg.Issuer,
	}
	if VersionHash != "" {
		out["versionHash"] = VersionHash
	}
	if BuildTime != "" {
		out["buildTime"] = BuildTime
	}
	status := http.StatusOK
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.Check(ctx); err != nil {
		s.log.Error("health: database", "err", err)
		out["status"], out["error"], status = "unavailable", "database", http.StatusServiceUnavailable
	} else if _, err := s.keys.signer(); err != nil {
		out["status"], out["error"], status = "unavailable", "signing key", http.StatusServiceUnavailable
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, out)
}

// --- Cookies -------------------------------------------------------------------

const (
	bindingCookie = "poweur_bind"
	sessionCookie = "poweur_session"
)

// cookieName adds the __Host- prefix when the issuer is https. Browsers refuse
// __Host- cookies without Secure, so a local http issuer uses the bare name.
func (s *Server) cookieName(base string) string {
	if s.secure {
		return "__Host-" + base
	}
	return base
}

func (s *Server) setCookie(w http.ResponseWriter, base, value string, maxAge time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     s.cookieName(base),
		Value:    value,
		Path:     "/",
		MaxAge:   int(maxAge / time.Second),
		HttpOnly: true,
		Secure:   s.secure,
		// Lax: the resume arrives as a top-level navigation from the signer.
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearCookie(w http.ResponseWriter, base string) {
	http.SetCookie(w, &http.Cookie{
		Name: s.cookieName(base), Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) cookieValue(r *http.Request, base string) string {
	c, err := r.Cookie(s.cookieName(base))
	if err != nil {
		return ""
	}
	return c.Value
}

// browserBinding returns this browser's binding value, setting it on first
// use. One per browser rather than per transaction, so two tabs do not orphan
// each other.
func (s *Server) browserBinding(w http.ResponseWriter, r *http.Request) (string, error) {
	if v := s.cookieValue(r, bindingCookie); len(v) >= 43 {
		return v, nil
	}
	v, err := signin.NewSecret()
	if err != nil {
		return "", err
	}
	s.setCookie(w, bindingCookie, v, 0)
	return v, nil
}

// sameOriginPost refuses a state-changing browser POST that did not come from
// a bridge page. SameSite=Lax already withholds cookies from cross-site
// POSTs; this also covers same-site siblings.
func (s *Server) sameOriginPost(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
		return site == "same-origin"
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		if ref := r.Header.Get("Referer"); ref != "" {
			return identity.SameOrigin(s.cfg.Issuer, ref)
		}
		// No browser sent this; the cookie checks still apply.
		return true
	}
	o, err := identity.NormalizeOrigin(origin)
	return err == nil && o == s.cfg.Issuer
}

// --- Browser sessions ------------------------------------------------------------

// currentSession returns the browser's bridge session, if any.
func (s *Server) currentSession(r *http.Request) (*BrowserSession, string) {
	v := s.cookieValue(r, sessionCookie)
	if v == "" {
		return nil, ""
	}
	hash := signin.HashSecret(v)
	sess, err := s.store.GetSession(r.Context(), hash)
	if err != nil {
		return nil, ""
	}
	return sess, hash
}

// startSession signs the browser in to the bridge as t's identity.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, t *Txn) error {
	v, err := signin.NewSecret()
	if err != nil {
		return err
	}
	now := s.now()
	sess := BrowserSession{
		Identity:         t.Identity,
		AuthTime:         t.AuthTime,
		SessionDelegated: t.SessionDelegated,
		CreatedAt:        now,
		ExpiresAt:        now.Add(s.cfg.SessionTTL),
		UserAgent:        summarizeUserAgent(r.UserAgent()),
	}
	if err := s.store.CreateSession(r.Context(), signin.HashSecret(v), sess); err != nil {
		return err
	}
	s.setCookie(w, sessionCookie, v, s.cfg.SessionTTL)
	return nil
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if !s.sameOriginPost(r) {
		s.renderError(w, r, http.StatusForbidden, "That request did not come from this site.")
		return
	}
	if _, hash := s.currentSession(r); hash != "" {
		_ = s.store.DeleteSession(r.Context(), hash)
	}
	s.clearCookie(w, sessionCookie)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// summarizeUserAgent keeps a coarse browser/OS label, never the full string.
func summarizeUserAgent(ua string) string {
	lower := strings.ToLower(ua)
	browser := "Browser"
	switch {
	case strings.Contains(lower, "edg/"):
		browser = "Edge"
	case strings.Contains(lower, "firefox/"):
		browser = "Firefox"
	case strings.Contains(lower, "chrome/"):
		browser = "Chrome"
	case strings.Contains(lower, "safari/"):
		browser = "Safari"
	case strings.Contains(lower, "curl/"), strings.Contains(lower, "go-http-client"):
		browser = "Script"
	}
	osName := ""
	switch {
	case strings.Contains(lower, "iphone"), strings.Contains(lower, "ipad"):
		osName = "iOS"
	case strings.Contains(lower, "android"):
		osName = "Android"
	case strings.Contains(lower, "mac os"):
		osName = "macOS"
	case strings.Contains(lower, "windows"):
		osName = "Windows"
	case strings.Contains(lower, "linux"):
		osName = "Linux"
	}
	if osName == "" {
		return browser
	}
	return browser + " on " + osName
}
