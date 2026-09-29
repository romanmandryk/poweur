package relay

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	idpkg "github.com/poweur/identity"
)

const identityPageTemplateVersion = "2"

//go:embed identitypage_assets/*
var identityPageAssets embed.FS

var identityPageTemplate = template.Must(template.ParseFS(identityPageAssets, "identitypage_assets/page.html"))

type identityPageLink struct {
	Label string
	URL   string
}

type identityPageView struct {
	Identity           string
	DisplayName        string
	Title              string
	Description        string
	Bio                string
	AvatarURL          string
	Initial            string
	CanonicalURL       string
	Indexable          bool
	AdvertiseAnonymous bool
	Links              []identityPageLink
	// ShortName is who the Message button names.
	ShortName string
	// HomeURL, MessageURL and ClaimURL point at the launcher; empty when the
	// relay has none. MessageURL carries `?to=` through a claim or an
	// "I already have an ID" hop; page.js swaps in a one-click link when the
	// browser already uses an ID under the same parent domain.
	HomeURL    string
	MessageURL string
	ClaimURL   string
}

func requestHost(r *http.Request) string {
	host := r.Host
	if h, _, err := splitHostPort(host); err == nil {
		host = h
	}
	return strings.ToLower(strings.TrimSuffix(host, "."))
}

func wantsHTML(r *http.Request) bool {
	accept := strings.ToLower(r.Header.Get("Accept"))
	return strings.Contains(accept, "text/html") && !strings.Contains(accept, "application/json")
}

// wantsJSON reports whether the caller asked for the root document itself.
// Every @poweur/client JSON request says so. A browser navigation does not,
// and neither does a client that sends no Accept header.
func wantsJSON(r *http.Request) bool {
	return strings.Contains(strings.ToLower(r.Header.Get("Accept")), "application/json")
}

func safeProfileLink(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return "", false
	}
	return u.String(), true
}

func firstInitial(displayName, identity string) string {
	value := strings.TrimSpace(displayName)
	if value == "" {
		value = identity
	}
	r, _ := utf8.DecodeRuneInString(value)
	if r == utf8.RuneError || r == 0 {
		return "P"
	}
	return strings.ToUpper(string(r))
}

func pageETag(identity string, profileRaw []byte) string {
	h := sha256.New()
	_, _ = h.Write([]byte(identityPageTemplateVersion))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(identity))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(profileRaw)
	return `"` + hex.EncodeToString(h.Sum(nil)[:12]) + `"`
}

func setIdentityPageHeaders(w http.ResponseWriter, etag string, indexable bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=60, stale-while-revalidate=300")
	w.Header().Set("ETag", etag)
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; style-src 'self'; script-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	w.Header().Set("Permissions-Policy", "publickey-credentials-create=(), publickey-credentials-get=()")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	if !indexable {
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	}
}

// serveIdentityPage renders one response from the identity's public profile.
// It does not retain the HTML: a map of pages would grow with registrations.
func (s *Server) serveIdentityPage(w http.ResponseWriter, r *http.Request, identity string) {
	profileRaw := s.readSysJSON(r.Context(), identity, profilePath)
	profile := idpkg.Profile{Version: 1}
	if len(profileRaw) > 0 {
		if parsed, err := idpkg.ParseProfile(profileRaw); err == nil {
			profile = parsed
		}
	}
	etag := pageETag(identity, profileRaw)
	indexable := profile.IdentityPage.IndexableOrDefault()
	setIdentityPageHeaders(w, etag, indexable)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if !profile.IdentityPage.EnabledOrDefault() {
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("<!doctype html><html lang=\"en\"><meta charset=\"utf-8\"><meta name=\"robots\" content=\"noindex,nofollow\"><title>Page unavailable</title><body><h1>Page unavailable</h1></body></html>"))
		return
	}

	links := make([]identityPageLink, 0, len(profile.Links))
	for _, link := range profile.Links {
		href, ok := safeProfileLink(link.URL)
		if !ok {
			continue
		}
		label := strings.TrimSpace(link.Label)
		if label == "" {
			label = href
		}
		links = append(links, identityPageLink{Label: label, URL: href})
	}
	avatarURL := ""
	if idpkg.ValidAvatarName(profile.Avatar) {
		avatarURL = "/.well-known/poweur/" + profile.Avatar
	}
	displayName := strings.TrimSpace(profile.DisplayName)
	title := identity + " — Poweur ID"
	if displayName != "" {
		title = displayName + " (" + identity + ")"
	}
	shortName := displayName
	if first, _, ok := strings.Cut(displayName, " "); ok && first != "" {
		shortName = first
	}
	if shortName == "" {
		shortName = identity
	}
	var homeURL, messageURL string
	if launcher := strings.TrimSpace(s.cfg.LauncherHost); launcher != "" {
		homeURL = "https://" + launcher + "/app/"
		messageURL = homeURL + "?to=" + url.QueryEscape(identity)
	}
	view := identityPageView{
		Identity:           identity,
		DisplayName:        displayName,
		Title:              title,
		Description:        "Public Poweur identity page for " + identity,
		Bio:                strings.TrimSpace(profile.Bio),
		AvatarURL:          avatarURL,
		Initial:            firstInitial(displayName, identity),
		CanonicalURL:       "https://" + identity + "/",
		Indexable:          indexable,
		AdvertiseAnonymous: profile.IdentityPage.AdvertisesAnonymousMessages(),
		Links:              links,
		ShortName:          shortName,
		HomeURL:            homeURL,
		MessageURL:         messageURL,
		ClaimURL:           messageURL,
	}
	w.WriteHeader(http.StatusOK)
	_ = identityPageTemplate.ExecuteTemplate(w, "page.html", view)
}

func serveIdentityPageAsset(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/identity-page/assets/")
	var contentType string
	switch name {
	case "page.css":
		contentType = "text/css; charset=utf-8"
	case "page.js":
		contentType = "text/javascript; charset=utf-8"
	case "logo.svg":
		contentType = "image/svg+xml"
	default:
		http.NotFound(w, r)
		return
	}
	raw, err := fs.ReadFile(identityPageAssets, "identitypage_assets/"+name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	sum := sha256.Sum256(raw)
	etag := `"` + hex.EncodeToString(sum[:12]) + `"`
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("ETag", etag)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = w.Write(raw)
}
