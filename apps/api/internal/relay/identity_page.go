package relay

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	idpkg "github.com/poweur/identity"
)

const identityPageTemplateVersion = "3"

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
	// Links are the owner's own profile links and linked handles, all
	// rendered rel="me": the profile and handles.json are owner-signed.
	Links []identityPageLink
	// ActorURL is the linked Fediverse actor, a second rel="me" in <head>
	// when it differs from the profile link shown (Mastodon checks either).
	ActorURL string
	// IndieAuthMetadataURL, AuthorizationEndpoint and TokenEndpoint repeat
	// the Link header in HTML for IndieAuth clients that only parse the page;
	// empty when the relay advertises no bridge.
	IndieAuthMetadataURL  string
	AuthorizationEndpoint string
	TokenEndpoint         string
	// ShortName is who the Message button names.
	ShortName string
	// HomeURL, MessageURL and ClaimURL point at the launcher; empty when the
	// relay has none. MessageURL carries `?to=` through a claim or an
	// "I already have an ID" hop; page.js swaps in a one-click link when the
	// browser already uses an ID under the same parent domain.
	HomeURL    string
	MessageURL string
	ClaimURL   string
	// SignInURL is the identity's own web app, for its owner on a browser
	// that has never used it (EPIC-028): unlock, add this device, or
	// restore. Empty when this relay serves no web app.
	SignInURL string
}

func requestHost(r *http.Request) string {
	host := r.Host
	if h, _, err := splitHostPort(host); err == nil {
		host = h
	}
	return strings.ToLower(strings.TrimSuffix(host, "."))
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

// appendHandleLinks adds the linked Bluesky and Fediverse accounts as profile
// links, skipping any the owner already lists. Bluesky links by DID, which
// survives a handle change; the Fediverse link is the `/@user` page Mastodon
// shows, and the actor URL is returned for a second rel="me".
func appendHandleLinks(links []identityPageLink, h idpkg.Handles) ([]identityPageLink, string) {
	add := func(label, href string) {
		for _, l := range links {
			if strings.EqualFold(strings.TrimSuffix(l.URL, "/"), strings.TrimSuffix(href, "/")) {
				return
			}
		}
		links = append(links, identityPageLink{Label: label, URL: href})
	}
	if h.ATProtoDID != "" {
		add("Bluesky", "https://bsky.app/profile/"+h.ATProtoDID)
	}
	actorURL := ""
	if user, server, ok := strings.Cut(h.FediverseAcct, "@"); ok {
		profileURL := "https://" + server + "/@" + user
		add("@"+h.FediverseAcct, profileURL)
		if h.FediverseActor != "" && h.FediverseActor != profileURL {
			actorURL = h.FediverseActor
		}
	}
	return links, actorURL
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

// pageETag covers everything the page renders from: the profile and the
// linked handles, so linking a Bluesky or Fediverse account changes it.
func pageETag(identity string, profileRaw, handlesRaw []byte) string {
	h := sha256.New()
	_, _ = h.Write([]byte(identityPageTemplateVersion))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(identity))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(profileRaw)
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(handlesRaw)
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
	handles, _ := s.readHandles(r.Context(), identity)
	handlesRaw, _ := json.Marshal(handles)
	etag := pageETag(identity, profileRaw, handlesRaw)
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
	links, actorURL := appendHandleLinks(links, handles)
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
		homeURL = "https://" + launcher + "/id/"
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
		ActorURL:           actorURL,
		ShortName:          shortName,
		HomeURL:            homeURL,
		MessageURL:         messageURL,
		ClaimURL:           messageURL,
	}
	if s.cfg.WebStaticDir != "" {
		view.SignInURL = appPrefix + "/"
	}
	if bridge := s.cfg.OAuthBridgeURL; bridge != "" {
		view.IndieAuthMetadataURL = bridge + "/.well-known/oauth-authorization-server"
		view.AuthorizationEndpoint = bridge + "/authorize"
		view.TokenEndpoint = bridge + "/token"
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
