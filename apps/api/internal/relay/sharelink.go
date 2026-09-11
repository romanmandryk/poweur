package relay

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"os"
	gopath "path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/poweur/api/internal/files"
	idpkg "github.com/poweur/identity"
)

// Public-link shares (EPIC-005 E05-T4) — capability URLs.
//
// `https://<identity>/s/<token>` serves a read-only browse/download view of
// one signed link grant to anyone holding the token, with no Poweur
// identity and no authentication. The token IS the credential, which sets
// the whole shape of this file:
//
//   - It is compared in constant time and never logged, echoed into an
//     error, or put in a page the browser might send onward.
//   - Every page here sends `Referrer-Policy: no-referrer` so the token does
//     not leak to whatever a document links to, and `noindex` so a crawler
//     that gets hold of one does not publish it.
//   - The endpoint is read-only at the protocol level (the grant format
//     refuses `write` on a link grant) so a leaked URL can never mutate the
//     owner's tree.
//   - Grants are re-read per request like every other grant, so revocation
//     is a file delete that takes effect on the next click. There is no
//     cache window.
//
// The threat model, and why capability URLs are still worth having, is
// written down in apps/docs/docs/files/sharing.md.

const (
	// linkRateCost charges a link request several units of the per-IP
	// budget: the endpoint reads files for an unauthenticated caller, so it
	// gets a tighter cap than messaging without a second set of tunables.
	linkRateCost = 4
	// linkPasswordRateCost is what one password attempt costs. Guessing a
	// link password should run out of budget long before it runs out of
	// candidates.
	linkPasswordRateCost = 20
	// linkSessionTTL is how long a correct password is remembered, capped
	// again by the grant's own expiry.
	linkSessionTTL = 12 * time.Hour
	// maxLinkListing bounds a directory page so a huge folder cannot be
	// turned into a bandwidth amplifier.
	maxLinkListing = 2000
)

// shareLinkRequest is one parsed /s/ request.
type shareLinkRequest struct {
	owner string // tree owner
	token string // capability token
	rest  string // path *within* the share, "" for its root
	base  string // URL prefix that addresses this share, e.g. "/s/<token>"
}

// parseShareLinkPath splits a /s/ URL into owner, token and sub-path.
//
// Two forms are accepted, and they cannot be confused for one another: a
// token is exactly 26 characters of [a-z2-7], and a Poweur identity is a
// domain name, so it always contains a dot the token alphabet has no room
// for.
//
//	/s/<token>[/<path>]             Host-routed: owner comes from the Host
//	/s/<identity>/<token>[/<path>]  explicit, for relays with no vanity host
func parseShareLinkPath(urlPath, hostOwner string) (shareLinkRequest, bool) {
	rest := strings.TrimPrefix(urlPath, "/s")
	rest = strings.TrimPrefix(rest, "/")
	segments := strings.Split(rest, "/")
	if len(segments) == 0 || segments[0] == "" {
		return shareLinkRequest{}, false
	}
	if idpkg.ValidateLinkToken(strings.ToLower(segments[0])) == nil {
		token := strings.ToLower(segments[0])
		return shareLinkRequest{
			owner: hostOwner,
			token: token,
			rest:  strings.Join(segments[1:], "/"),
			base:  "/s/" + token,
		}, hostOwner != ""
	}
	if len(segments) < 2 {
		return shareLinkRequest{}, false
	}
	owner := strings.ToLower(strings.TrimSuffix(segments[0], "."))
	token := strings.ToLower(segments[1])
	if idpkg.ValidateLinkToken(token) != nil {
		return shareLinkRequest{}, false
	}
	return shareLinkRequest{
		owner: owner,
		token: token,
		rest:  strings.Join(segments[2:], "/"),
		base:  "/s/" + owner + "/" + token,
	}, true
}

// handleShareLink serves the capability-URL endpoint (GET browse/download,
// POST password submission).
func (s *Server) handleShareLink(w http.ResponseWriter, r *http.Request) {
	// Nothing on this path is worth caching or indexing, and the token must
	// not ride along in a Referer to wherever a shared document points.
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	if !s.davEnabled() {
		writeError(w, http.StatusNotImplemented, "storage_disabled", "file storage requires POWEUR_DATA")
		return
	}
	if decision := s.rateLimit.AllowCost("link:"+clientIPOf(r), linkRateCost); !decision.Allowed {
		s.writeLinkPage(w, http.StatusTooManyRequests, "Slow down",
			"Too many link requests from this address. Try again shortly.")
		return
	}

	req, ok := parseShareLinkPath(r.URL.Path, ownerFromHost(r.Host))
	if !ok {
		s.writeLinkMissing(w)
		return
	}
	if !s.identities.Exists(req.owner) {
		// Same page as a bad token: whether an identity is hosted here is
		// not something a link visitor gets to probe.
		s.writeLinkMissing(w)
		return
	}

	grant, found, expired := s.grants.Snapshot(r.Context(), req.owner).LinkGrant(req.token)
	if !found {
		// A revoked link and a token that never existed are the same page.
		s.writeLinkMissing(w)
		return
	}
	if expired {
		// Whoever holds the token already knows the link existed, so saying
		// "expired" leaks nothing and beats a mystery 404.
		s.writeLinkPage(w, http.StatusGone, "This link has expired",
			"The person who shared it can issue a new one.")
		return
	}

	if grant.RequiresPassword() && !s.linkSessionValid(r, req, grant) {
		if r.Method == http.MethodPost {
			s.handleLinkPassword(w, r, req, grant)
			return
		}
		s.writeLinkPasswordForm(w, req, http.StatusUnauthorized, "")
		return
	}
	if r.Method == http.MethodPost {
		// A password POST to a link that needs none: nothing to do but show
		// the share.
		http.Redirect(w, r, req.base+"/"+req.rest, http.StatusSeeOther)
		return
	}

	s.serveLinkTarget(w, r, req, grant)
}

// serveLinkTarget resolves the requested path inside the share and serves
// either a listing or a file.
func (s *Server) serveLinkTarget(w http.ResponseWriter, r *http.Request, req shareLinkRequest, grant idpkg.ShareGrant) {
	clean, ok := resolveLinkPath(grant.Path, req.rest)
	if !ok {
		// Anything that escapes the granted subtree is simply not part of
		// this share, so it gets the share's own not-found page.
		s.writeLinkPage(w, http.StatusNotFound, "Not in this share",
			"That path is not part of the shared folder.")
		return
	}

	fi, err := s.filesProvider.Stat(r.Context(), req.owner, clean)
	if err != nil {
		s.writeLinkPage(w, http.StatusNotFound, "Not found",
			"That file is no longer in the shared folder.")
		return
	}
	if fi.IsDir() {
		s.serveLinkListing(w, r, req, grant, clean)
		return
	}
	s.serveLinkFile(w, r, req, grant, clean, fi.Size())
}

// resolveLinkPath maps a sub-path inside the share onto a tree path,
// refusing anything that leaves the granted subtree.
func resolveLinkPath(grantPath, rest string) (string, bool) {
	joined := grantPath
	if rest = strings.Trim(rest, "/"); rest != "" {
		joined = grantPath + "/" + rest
	}
	clean, err := files.CleanPath(joined)
	if err != nil {
		return "", false
	}
	// Under() is the same containment check the grant engine uses, so a
	// link can never reach further than the grant that created it — /private
	// and poweur-sys included, which grant paths cannot name anyway.
	if !files.Under(clean, grantPath) {
		return "", false
	}
	return clean, true
}

// serveLinkFile streams one file, charging it to the share's download cap
// and bandwidth counters.
func (s *Server) serveLinkFile(w http.ResponseWriter, r *http.Request, req shareLinkRequest, grant idpkg.ShareGrant, clean string, size int64) {
	// The slot is claimed before any bytes move, so the cap holds even when
	// several visitors click at the same moment.
	if _, ok := s.linkStats.Reserve(req.owner, grant.ShareID, grant.MaxDownloads()); !ok {
		s.writeLinkPage(w, http.StatusGone, "This link is used up",
			"It reached the download limit its owner set.")
		return
	}

	f, err := s.filesProvider.OpenFile(r.Context(), req.owner, clean, os.O_RDONLY, 0)
	if err != nil {
		s.writeLinkPage(w, http.StatusNotFound, "Not found",
			"That file is no longer in the shared folder.")
		return
	}
	defer f.Close()

	// Active content is served as text/plain, exactly as /pub does it: a
	// share is a file handout, not a web host, and identity origins must
	// not host stored XSS.
	w.Header().Set("Content-Type", pubContentType(clean))
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.Header().Set("Content-Disposition", `attachment; filename="`+sanitizeFilename(gopath.Base(clean))+`"`)
	w.WriteHeader(http.StatusOK)
	n, _ := io.Copy(w, f)
	// Bandwidth is charged after the copy, so it records what actually left
	// the relay rather than what was asked for.
	s.linkStats.AddBytes(req.owner, grant.ShareID, n)
}

// serveLinkListing renders the minimal browse page for a folder.
func (s *Server) serveLinkListing(w http.ResponseWriter, r *http.Request, req shareLinkRequest, grant idpkg.ShareGrant, clean string) {
	d, err := s.filesProvider.OpenFile(r.Context(), req.owner, clean, os.O_RDONLY, 0)
	if err != nil {
		s.writeLinkPage(w, http.StatusNotFound, "Not found", "That folder is no longer shared.")
		return
	}
	entries, err := d.Readdir(maxLinkListing)
	_ = d.Close()
	if err != nil && len(entries) == 0 {
		s.writeLinkPage(w, http.StatusInternalServerError, "Cannot list", "The folder could not be read.")
		return
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir() != entries[j].IsDir() {
			return entries[i].IsDir()
		}
		return entries[i].Name() < entries[j].Name()
	})

	rel := strings.Trim(strings.TrimPrefix(clean, grant.Path), "/")
	title := gopath.Base(grant.Path)
	if rel != "" {
		title = gopath.Base(clean)
	}

	var body strings.Builder
	fmt.Fprintf(&body, `<p class="crumb">Shared by %s</p>`, html.EscapeString(req.owner))
	body.WriteString(`<ul class="ls">`)
	if rel != "" {
		parent := req.base
		if up := gopath.Dir(rel); up != "." {
			parent = req.base + "/" + up
		}
		fmt.Fprintf(&body, `<li><a href="%s">../</a></li>`, html.EscapeString(parent))
	}
	for _, e := range entries {
		href := req.base + "/" + strings.TrimPrefix(rel+"/"+e.Name(), "/")
		name := e.Name()
		meta := formatLinkSize(e.Size())
		if e.IsDir() {
			name += "/"
			meta = ""
		}
		fmt.Fprintf(&body, `<li><a href="%s">%s</a><span class="sz">%s</span></li>`,
			html.EscapeString(href), html.EscapeString(name), html.EscapeString(meta))
	}
	body.WriteString(`</ul>`)
	if len(entries) == 0 {
		body.WriteString(`<p class="muted">This folder is empty.</p>`)
	}
	s.writeLinkHTML(w, http.StatusOK, title, body.String(), true)
}

// --- password gate -------------------------------------------------------

// handleLinkPassword checks a submitted password and, on success, sets the
// session cookie and redirects back to the share.
func (s *Server) handleLinkPassword(w http.ResponseWriter, r *http.Request, req shareLinkRequest, grant idpkg.ShareGrant) {
	// Password attempts are charged far more than a plain view: this is the
	// one place on the endpoint that is worth grinding.
	if decision := s.rateLimit.AllowCost("linkpw:"+clientIPOf(r), linkPasswordRateCost); !decision.Allowed {
		s.writeLinkPage(w, http.StatusTooManyRequests, "Too many attempts",
			"Too many password attempts from this address. Try again later.")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.writeLinkPasswordForm(w, req, http.StatusBadRequest, "That form could not be read.")
		return
	}
	if !grant.CheckLinkPassword(r.PostFormValue("password")) {
		s.writeLinkPasswordForm(w, req, http.StatusUnauthorized, "That password is not right.")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     linkCookieName(grant.ShareID),
		Value:    s.mintLinkSession(req, grant),
		Path:     req.base,
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(linkSessionTTL / time.Second),
	})
	http.Redirect(w, r, req.base+"/"+req.rest, http.StatusSeeOther)
}

func linkCookieName(shareID string) string {
	// Share ids are `shr_<hex>`, so this is always a valid cookie name.
	return "poweur_link_" + shareID
}

// mintLinkSession returns `<expiry>.<mac>`, where the MAC covers the owner,
// the token, the password hash and the expiry.
//
// Binding the password hash in is what makes "change the password" mean
// something: a new hash invalidates every cookie handed out under the old
// one. The secret is per-process, so a relay restart also ends every link
// session — which costs a visitor one re-entry and is the safe default.
func (s *Server) mintLinkSession(req shareLinkRequest, grant idpkg.ShareGrant) string {
	expiry := time.Now().Add(linkSessionTTL)
	// A session never outlives the grant it belongs to.
	if grant.ExpiresAt != "" {
		if t, err := time.Parse(time.RFC3339, grant.ExpiresAt); err == nil && t.Before(expiry) {
			expiry = t
		}
	}
	unix := strconv.FormatInt(expiry.Unix(), 10)
	return unix + "." + s.linkSessionMAC(req, grant, unix)
}

func (s *Server) linkSessionMAC(req shareLinkRequest, grant idpkg.ShareGrant, unix string) string {
	var passwordHash string
	if grant.Link != nil {
		passwordHash = grant.Link.Password
	}
	mac := hmac.New(sha256.New, s.linkSecret)
	// Length-prefixed so no two different tuples can produce one string.
	for _, field := range []string{req.owner, req.token, grant.ShareID, passwordHash, unix} {
		fmt.Fprintf(mac, "%d:%s\n", len(field), field)
	}
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// linkSessionValid reports whether the request carries an unexpired cookie
// this relay issued for this exact grant.
func (s *Server) linkSessionValid(r *http.Request, req shareLinkRequest, grant idpkg.ShareGrant) bool {
	cookie, err := r.Cookie(linkCookieName(grant.ShareID))
	if err != nil || cookie.Value == "" {
		return false
	}
	unix, mac, found := strings.Cut(cookie.Value, ".")
	if !found {
		return false
	}
	expiry, err := strconv.ParseInt(unix, 10, 64)
	if err != nil || time.Now().After(time.Unix(expiry, 0)) {
		return false
	}
	want := s.linkSessionMAC(req, grant, unix)
	return subtle.ConstantTimeCompare([]byte(want), []byte(mac)) == 1
}

// --- rendering -----------------------------------------------------------

func (s *Server) writeLinkMissing(w http.ResponseWriter) {
	s.writeLinkPage(w, http.StatusNotFound, "This link is not available",
		"It may have been revoked, or the address may be mistyped.")
}

func (s *Server) writeLinkPage(w http.ResponseWriter, status int, title, message string) {
	s.writeLinkHTML(w, status, title,
		`<p class="muted">`+html.EscapeString(message)+`</p>`, false)
}

func (s *Server) writeLinkPasswordForm(w http.ResponseWriter, req shareLinkRequest, status int, problem string) {
	var body strings.Builder
	body.WriteString(`<p class="muted">This share is password-protected.</p>`)
	if problem != "" {
		fmt.Fprintf(&body, `<p class="bad">%s</p>`, html.EscapeString(problem))
	}
	// The form posts to the *current* URL, so the token never has to be
	// repeated into a hidden field where a page save would keep it.
	fmt.Fprintf(&body, `<form method="post" action="%s">`+
		`<input type="password" name="password" autocomplete="current-password" `+
		`aria-label="Password" autofocus>`+
		`<button type="submit">Open</button></form>`,
		html.EscapeString(req.base+"/"+req.rest))
	s.writeLinkHTML(w, status, "Password required", body.String(), false)
}

// writeLinkHTML renders the minimal viewer page. It is deliberately one
// self-contained document with no scripts and no external references:
// anything it fetched would carry the capability URL in a Referer or a
// third-party log.
func (s *Server) writeLinkHTML(w http.ResponseWriter, status int, title, body string, upsell bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// No inline or external scripts at all, so the strictest CSP that still
	// renders is the right one.
	w.Header().Set("Content-Security-Policy",
		"default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	w.WriteHeader(status)

	fmt.Fprintf(w, `<!doctype html><html lang="en"><head><meta charset="utf-8">`+
		`<meta name="viewport" content="width=device-width,initial-scale=1">`+
		`<meta name="referrer" content="no-referrer">`+
		`<meta name="robots" content="noindex,nofollow">`+
		`<title>%s</title><style>%s</style></head><body><main>`+
		`<h1>%s</h1>%s`,
		html.EscapeString(title), linkPageCSS, html.EscapeString(title), body)

	if upsell {
		// The on-ramp funnel from the epic: a link is often somebody's first
		// contact with Poweur, and the only thing worth selling them at this
		// moment is that an ID of their own gets them more than read-only.
		claim := s.claimURL()
		if claim != "" {
			fmt.Fprintf(w, `<p class="upsell">Read-only view. `+
				`<a href="%s" rel="noreferrer noopener">Get a Poweur ID</a> to share and edit files of your own.</p>`,
				html.EscapeString(claim))
		}
	}
	fmt.Fprint(w, `</main></body></html>`)
}

// claimURL is where "get a Poweur ID" points, or "" when this relay hosts
// no claim flow.
func (s *Server) claimURL() string {
	if s.cfg.LauncherHost == "" {
		return ""
	}
	return "https://" + s.cfg.LauncherHost + "/app/"
}

const linkPageCSS = `:root{color-scheme:light dark}` +
	`body{margin:0;font:16px/1.5 system-ui,-apple-system,"Segoe UI",sans-serif}` +
	`main{max-width:40rem;margin:0 auto;padding:2rem 1.25rem}` +
	`h1{font-size:1.35rem;margin:0 0 .25rem;word-break:break-word}` +
	`.crumb{margin:0 0 1.25rem;font-size:.85rem;opacity:.7}` +
	`.muted{opacity:.7}.bad{color:#b3261e}` +
	`ul.ls{list-style:none;margin:0;padding:0;border-top:1px solid rgba(128,128,128,.3)}` +
	`ul.ls li{display:flex;gap:1rem;justify-content:space-between;align-items:baseline;` +
	`padding:.6rem .25rem;border-bottom:1px solid rgba(128,128,128,.3)}` +
	`ul.ls a{text-decoration:none;word-break:break-word}` +
	`ul.ls a:hover{text-decoration:underline}` +
	`.sz{font-variant-numeric:tabular-nums;font-size:.85rem;opacity:.6;white-space:nowrap}` +
	`form{display:flex;gap:.5rem;margin:1rem 0}` +
	`input,button{font:inherit;padding:.5rem .75rem;border-radius:.4rem;border:1px solid rgba(128,128,128,.5)}` +
	`input{flex:1;min-width:0}button{cursor:pointer}` +
	`.upsell{margin-top:2rem;padding-top:1rem;border-top:1px solid rgba(128,128,128,.3);font-size:.9rem;opacity:.8}`

// --- small helpers -------------------------------------------------------

// ownerFromHost extracts the identity a vanity host addresses.
func ownerFromHost(hostport string) string {
	host := hostport
	if h, _, err := splitHostPort(hostport); err == nil {
		host = h
	}
	return strings.ToLower(strings.TrimSuffix(host, "."))
}

// clientIPOf is the rate-limit key for an unauthenticated caller.
func clientIPOf(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// sanitizeFilename keeps a Content-Disposition filename on one line and
// inside its quotes.
func sanitizeFilename(name string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == '"' || r == '\\' || r == 0x7f {
			return '_'
		}
		return r
	}, name)
}

func formatLinkSize(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	div, exp := int64(unit), 0
	for size := n / unit; size >= unit && exp < 3; size /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}
