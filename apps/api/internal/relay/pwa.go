package relay

import (
	"bytes"
	"encoding/json"
	"html"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	idpkg "github.com/poweur/identity"
)

// Installable per-identity app (EPIC-009 E09-T12).
//
// The web client keeps its identity in per-origin storage, so each hosted
// identity's /app/ is already a separate app to the browser. These helpers
// make it *look* like one: the manifest and the page carry the identity's
// host as the app name, and its avatar as the icon when it has one, so
// alice.poweur.net and bob.poweur.net install as two recognisable apps
// instead of two identical "Poweur" icons. Every other host (the launcher,
// a self-hosted static server) gets the build's files untouched.

// pwaIdentity is what the app is named after on one host.
type pwaIdentity struct {
	Host string
	// ShortName is the home-screen label: the whole host when it fits the
	// ~12 characters launchers show, otherwise the handle.
	ShortName string
	// AvatarURL is the identity's public avatar (a 256px square JPEG from
	// the web client), or "".
	AvatarURL string
}

// homeScreenLabelMax is about what iOS and Android launchers show before
// truncating.
const homeScreenLabelMax = 12

// pwaIdentity returns the identity served on this request's host, or nil.
func (s *Server) pwaIdentity(r *http.Request) *pwaIdentity {
	host := requestHost(r)
	if host == "" || !s.identities.Exists(host) {
		return nil
	}
	short := host
	if len(short) > homeScreenLabelMax {
		short, _, _ = strings.Cut(host, ".")
	}
	out := &pwaIdentity{Host: host, ShortName: short}
	if raw := s.readSysJSON(r.Context(), host, profilePath); len(raw) > 0 {
		if profile, err := idpkg.ParseProfile(raw); err == nil && idpkg.ValidAvatarName(profile.Avatar) {
			out.AvatarURL = "/.well-known/poweur/" + profile.Avatar
		}
	}
	return out
}

var (
	titleTag       = regexp.MustCompile(`<title>[^<]*</title>`)
	appleTouchIcon = regexp.MustCompile(`<link rel="apple-touch-icon" href="[^"]*" */?>`)
)

// serveAppPage serves index.html, named after the identity on its host.
func serveAppPage(w http.ResponseWriter, r *http.Request, root string, pwa func(*http.Request) *pwaIdentity) {
	path := filepath.Join(root, "index.html")
	var id *pwaIdentity
	if pwa != nil {
		id = pwa(r)
	}
	if id == nil {
		http.ServeFile(w, r, path)
		return
	}
	page, err := os.ReadFile(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(personalizePage(page, id))
}

// personalizePage sets the tab title, iOS's default home-screen name and,
// with an avatar, iOS's home-screen icon (iOS reads apple-touch-icon, not
// the manifest).
func personalizePage(page []byte, id *pwaIdentity) []byte {
	head := `<title>` + html.EscapeString(id.Host) + `</title>` + "\n  " +
		`<meta name="apple-mobile-web-app-title" content="` + html.EscapeString(id.ShortName) + `" />`
	if loc := titleTag.FindIndex(page); loc != nil {
		page = append(append(append([]byte{}, page[:loc[0]]...), head...), page[loc[1]:]...)
	} else {
		page = bytes.Replace(page, []byte("</head>"), []byte(head+"\n</head>"), 1)
	}
	if id.AvatarURL != "" {
		icon := []byte(`<link rel="apple-touch-icon" href="` + html.EscapeString(id.AvatarURL) + `" />`)
		page = appleTouchIcon.ReplaceAllLiteral(page, icon)
	}
	return page
}

// serveAppManifest serves manifest.webmanifest, named after the identity on
// its host. `id`, `start_url` and `scope` stay origin-relative, so each
// identity's install is its own app and opens only its own origin.
func serveAppManifest(w http.ResponseWriter, r *http.Request, root string, pwa func(*http.Request) *pwaIdentity) {
	path := filepath.Join(root, "manifest.webmanifest")
	w.Header().Set("Content-Type", "application/manifest+json")
	var id *pwaIdentity
	if pwa != nil {
		id = pwa(r)
	}
	if id == nil {
		http.ServeFile(w, r, path)
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		http.ServeFile(w, r, path)
		return
	}
	personalizeManifest(manifest, id)
	w.Header().Set("Cache-Control", "no-cache")
	_ = json.NewEncoder(w).Encode(manifest)
}

func personalizeManifest(manifest map[string]any, id *pwaIdentity) {
	manifest["name"] = id.Host
	manifest["short_name"] = id.ShortName
	if id.AvatarURL != "" {
		// The avatar alone: launchers pick the closest size and prefer a
		// maskable icon, so leaving the brand icons in would hide it.
		manifest["icons"] = []map[string]string{{
			"src":     id.AvatarURL,
			"sizes":   "256x256",
			"type":    avatarType(id.AvatarURL),
			"purpose": "any",
		}}
	}
}

func avatarType(url string) string {
	switch {
	case strings.HasSuffix(url, ".png"):
		return "image/png"
	case strings.HasSuffix(url, ".webp"):
		return "image/webp"
	case strings.HasSuffix(url, ".gif"):
		return "image/gif"
	default:
		return "image/jpeg"
	}
}
