package relay

import (
	"bufio"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
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
// `https://<identity>/s/<token>` serves either a browse/download view or an
// upload-only file request from one signed link grant. No Poweur identity is
// required. The token IS the credential, which sets the whole shape here:
//
//   - It is compared in constant time and never logged, echoed into an
//     error, or put in a page the browser might send onward.
//   - Every page here sends `Referrer-Policy: no-referrer` so the token does
//     not leak to whatever a document links to, and `noindex` so a crawler
//     that gets hold of one does not publish it.
//   - Ordinary links are read-only. A file request can only create fresh,
//     uniquely named objects and can never list/read/replace/delete.
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
	// maxFileRequestBytes is the relay safety ceiling even when an owner sets
	// no per-request limit. Larger transfers belong on resumable uploads.
	maxFileRequestBytes int64 = 64 << 20
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
	if req.rest == "claim" && r.Method == http.MethodGet {
		s.handleShareClaimRedirect(w, r, req, grant)
		return
	}
	if grant.IsFileRequest() {
		if req.rest != "" {
			s.writeLinkPage(w, http.StatusNotFound, "Not in this request",
				"File requests do not expose uploaded files.")
			return
		}
		if r.Method == http.MethodPost {
			s.handleFileRequestUpload(w, r, req, grant)
			return
		}
		s.linkStats.RecordOpen(req.owner, grant.ShareID)
		s.writeFileRequestForm(w, req, grant, http.StatusOK, "")
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

func (s *Server) writeFileRequestForm(w http.ResponseWriter, req shareLinkRequest, grant idpkg.ShareGrant, status int, problem string) {
	var body strings.Builder
	fmt.Fprintf(&body, `<p class="crumb">Requested by %s</p>`, html.EscapeString(req.owner))
	body.WriteString(s.shareLinkDetails(req, grant))
	body.WriteString(`<p class="muted">You can upload a new file. You cannot see, replace, or remove anyone else's uploads.</p>`)
	if problem != "" {
		fmt.Fprintf(&body, `<p class="bad">%s</p>`, html.EscapeString(problem))
	}
	accept := ""
	if request := grant.Link.FileRequest; len(request.AllowedTypes) > 0 {
		accept = ` accept="` + html.EscapeString(strings.Join(request.AllowedTypes, ",")) + `"`
	}
	fmt.Fprintf(&body, `<form class="upload" method="post" enctype="multipart/form-data" action="%s">`+
		`<input type="file" name="file" required%s><button type="submit">Upload</button></form>`,
		html.EscapeString(req.base), accept)
	body.WriteString(s.shareClaimCTA(req, "viewed"))
	s.writeLinkHTML(w, status, "Send a file", body.String(), false)
}

func (s *Server) handleFileRequestUpload(w http.ResponseWriter, r *http.Request, req shareLinkRequest, grant idpkg.ShareGrant) {
	options := grant.Link.FileRequest
	objectLimit := maxFileRequestBytes
	if options.MaxObjectBytes > 0 && options.MaxObjectBytes < objectLimit {
		objectLimit = options.MaxObjectBytes
	}
	limit := objectLimit
	if s.cfg.MaxFileBytes > 0 && s.cfg.MaxFileBytes < limit {
		limit = s.cfg.MaxFileBytes
	}
	// Leave bounded room for multipart headers while enforcing the file's
	// exact size below.
	r.Body = http.MaxBytesReader(w, r.Body, limit+(1<<20))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		s.writeFileRequestForm(w, req, grant, http.StatusRequestEntityTooLarge, "That upload is too large or malformed.")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		s.writeFileRequestForm(w, req, grant, http.StatusBadRequest, "Choose one file to upload.")
		return
	}
	defer file.Close()
	if s.cfg.MaxFileBytes > 0 && header.Size > s.cfg.MaxFileBytes {
		s.writeFileRequestForm(w, req, grant, http.StatusRequestEntityTooLarge, "That file is larger than this relay allows.")
		return
	}
	if header.Size < 0 || header.Size > objectLimit {
		s.writeFileRequestForm(w, req, grant, http.StatusRequestEntityTooLarge, "That file is larger than this request allows.")
		return
	}
	reader := bufio.NewReader(file)
	prefixBytes, _ := reader.Peek(512)
	if !validFileRequestType(http.DetectContentType(prefixBytes), options.AllowedTypes) {
		s.writeFileRequestForm(w, req, grant, http.StatusUnsupportedMediaType, "That file type is not accepted.")
		return
	}
	name, err := fileRequestName(header.Filename)
	if err != nil {
		s.writeFileRequestForm(w, req, grant, http.StatusBadRequest, err.Error())
		return
	}
	if s.cfg.MaxIdentityBytes > 0 {
		used, usedErr := s.filesProvider.UsedBytes(r.Context(), req.owner)
		if usedErr == nil && used+header.Size > s.cfg.MaxIdentityBytes {
			s.writeLinkPage(w, http.StatusInsufficientStorage, "Storage is full", "The owner has no room for this upload.")
			return
		}
	}
	if fi, err := s.filesProvider.Stat(r.Context(), req.owner, grant.Path); err != nil || !fi.IsDir() {
		s.writeLinkPage(w, http.StatusConflict, "Upload folder unavailable", "The owner needs to recreate the destination folder.")
		return
	}
	prefix, err := idpkg.GenerateLinkToken()
	if err != nil {
		s.writeLinkPage(w, http.StatusInternalServerError, "Upload failed", "Please try again.")
		return
	}
	destination, err := files.CleanPath(grant.Path + "/" + prefix[:12] + "-" + name)
	if err != nil {
		s.writeFileRequestForm(w, req, grant, http.StatusBadRequest, "That filename cannot be stored.")
		return
	}
	dst, err := s.filesProvider.OpenFile(r.Context(), req.owner, destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		s.writeLinkPage(w, http.StatusConflict, "Upload failed", "Please retry; no existing file was replaced.")
		return
	}
	if _, ok := s.linkStats.ReserveUpload(req.owner, grant.ShareID, header.Size, options.MaxUploads, options.MaxBytes); !ok {
		_ = dst.Close()
		_ = s.filesProvider.RemoveAll(r.Context(), req.owner, destination)
		s.writeLinkPage(w, http.StatusGone, "This request is full", "It reached the upload limit its owner set.")
		return
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(dst, hash), io.LimitReader(reader, limit+1))
	closeErr := dst.Close()
	if copyErr != nil || closeErr != nil || written != header.Size || written > limit {
		_ = s.filesProvider.RemoveAll(r.Context(), req.owner, destination)
		s.writeLinkPage(w, http.StatusBadRequest, "Upload failed", "The file was incomplete; please try again.")
		return
	}
	s.filesIndex.RecordWrite(req.owner, destination, hex.EncodeToString(hash.Sum(nil)), written, time.Now().UTC(), "public-link:"+grant.ShareID)
	if options.Notify {
		// This is a wake-up hint, never a forged owner message: the signed
		// grant opted in, while the files tree and counters remain truth.
		s.notify(req.owner, "file_request", grant.ShareID)
	}
	body := `<p>Your file <strong>` + html.EscapeString(name) + `</strong> was delivered.</p>` +
		`<p class="muted">Other submissions remain private.</p>` + s.shareClaimCTA(req, "uploaded")
	s.writeLinkHTML(w, http.StatusCreated, "Upload complete", body, false)
}

func (s *Server) handleShareClaimRedirect(w http.ResponseWriter, r *http.Request, req shareLinkRequest, grant idpkg.ShareGrant) {
	if s.claimURL() == "" {
		s.writeLinkPage(w, http.StatusNotFound, "No ID claim here", "This relay does not offer Poweur IDs.")
		return
	}
	action := claimHandoffAction(r.URL.Query().Get("action"), grant.IsFileRequest())
	s.linkStats.RecordClaimStarted(req.owner, grant.ShareID)
	http.Redirect(w, r, s.shareClaimURL(req, grant, action), http.StatusSeeOther)
}

func claimHandoffAction(raw string, fileRequest bool) string {
	switch raw {
	case "uploaded":
		if fileRequest {
			return "uploaded"
		}
	case "downloaded":
		if !fileRequest {
			return "downloaded"
		}
	}
	return "viewed"
}

func (s *Server) shareClaimCTA(req shareLinkRequest, action string) string {
	if s.claimURL() == "" {
		return ""
	}
	href := req.base + "/claim?action=" + action
	note := "The files stay available through this link."
	if action == "uploaded" {
		note = "Your upload stays complete."
	}
	return `<p class="upsell"><a href="` + html.EscapeString(href) + `" rel="noreferrer">Get a Poweur ID</a> and ask the owner for ongoing access. ` + note + `</p>`
}

func (s *Server) shareClaimURL(req shareLinkRequest, grant idpkg.ShareGrant, action string) string {
	payload, _ := json.Marshal(map[string]string{
		"share_id": grant.ShareID, "owner": req.owner, "token": req.token, "action": action,
	})
	return s.claimURL() + "#share=" + base64.RawURLEncoding.EncodeToString(payload)
}

func fileRequestName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\\`) {
		return "", fmt.Errorf("that filename is not valid")
	}
	if strings.HasPrefix(strings.ToLower(name), ".poweur-") || len([]byte(name)) > 180 {
		return "", fmt.Errorf("that filename is reserved or too long")
	}
	if _, err := files.CleanPath("shared/" + name); err != nil {
		return "", fmt.Errorf("that filename is not valid")
	}
	return name, nil
}

func validFileRequestType(got string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	got = strings.ToLower(strings.TrimSpace(strings.Split(got, ";")[0]))
	for _, want := range allowed {
		want = strings.ToLower(strings.TrimSpace(want))
		if got == want || (strings.HasSuffix(want, "/*") && strings.HasPrefix(got, strings.TrimSuffix(want, "*"))) {
			return true
		}
	}
	return false
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
	body.WriteString(s.shareLinkDetails(req, grant))
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
	body.WriteString(s.shareClaimCTA(req, "viewed"))
	s.writeLinkHTML(w, http.StatusOK, title, body.String(), false)
}

// shareLinkDetails gives a capability holder enough public, non-secret context
// to verify who issued it and understand the boundary before acting. It never
// includes the token, storage path, filenames or visitor data.
func (s *Server) shareLinkDetails(req shareLinkRequest, grant idpkg.ShareGrant) string {
	var details []string
	if entry, ok := s.identities.Get(req.owner); ok && len(entry.PublicKeyBytes) == ed25519.PublicKeySize {
		details = append(details, "Identity key <code>"+html.EscapeString(idpkg.KeyFingerprintBytes(entry.PublicKeyBytes))+"</code>")
	}
	if grant.ExpiresAt != "" {
		if expiry, err := time.Parse(time.RFC3339, grant.ExpiresAt); err == nil {
			details = append(details, "Expires <time datetime=\""+html.EscapeString(grant.ExpiresAt)+"\">"+
				html.EscapeString(expiry.UTC().Format("2 Jan 2006, 15:04 UTC"))+"</time>")
		}
	}
	if grant.Link != nil && grant.Link.Password != "" {
		details = append(details, "Password protected")
	}
	if grant.Link != nil && grant.Link.FileRequest != nil {
		request := grant.Link.FileRequest
		stat := s.linkStats.Get(req.owner, grant.ShareID)
		if request.MaxUploads > 0 {
			remaining := int64(request.MaxUploads) - stat.Uploads
			if remaining < 0 {
				remaining = 0
			}
			details = append(details, fmt.Sprintf("%d of %d uploads remaining", remaining, request.MaxUploads))
		}
		if request.MaxBytes > 0 {
			remaining := request.MaxBytes - stat.UploadBytes
			if remaining < 0 {
				remaining = 0
			}
			details = append(details, formatLinkSize(remaining)+" upload capacity remaining")
		}
	}
	if len(details) == 0 {
		return ""
	}
	return `<p class="muted link-details">` + strings.Join(details, " · ") + `</p>`
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
			fmt.Fprintf(w, `<p class="upsell">`+
				`<a href="%s" rel="noreferrer noopener">Get a Poweur ID</a> to keep sharing and collaborating.</p>`,
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
