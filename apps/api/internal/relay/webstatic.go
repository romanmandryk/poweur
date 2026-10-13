package relay

import (
	"net/http"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"
)

// corsMiddleware allows browser clients (web UI, third-party sites) to call the
// relay API cross-origin. Signatures and rate limits remain the trust layer.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		// Every header the protocol sends must be listed, or the browser's
		// preflight fails and the request never happens. X-Poweur-Challenge was
		// missing, which nothing same-origin ever noticed — the web app is
		// served *by* the relay, so it sends no preflight — while a Capacitor
		// shell on capacitor://localhost, or any third-party site using the
		// SDK, had every authenticated call blocked before it left the browser.
		w.Header().Set("Access-Control-Allow-Headers",
			"Content-Type, Authorization, If-Match, "+
				"X-Poweur-Identity, X-Poweur-Challenge, X-Poweur-Signature, X-Poweur-Session-Id, X-Poweur-Timestamp, X-Poweur-Nonce, "+
				"X-Poweur-Link, X-Poweur-Link-Verifier, X-Poweur-PoW-Token, X-Poweur-PoW-Solution, X-Poweur-Group-Roster, "+
				"X-Poweur-Device, X-Poweur-Device-Name, X-Poweur-Device-Kind, X-Poweur-Device-Client, X-Poweur-Device-Platform, X-Poweur-Device-Browser, X-Poweur-Device-Enrollment")
		// ETag is how a system-file read tells the writer what to put in
		// If-Match; without exposing it a browser client cannot see it.
		w.Header().Set("Access-Control-Expose-Headers", "ETag, Date")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// mountWebStatic serves the web client SPA from dir under prefix ("/id" →
// GET /id/): real files as themselves, every other path as index.html so the
// app's own routes survive a reload. With pwa set, a hosted identity's host
// gets its own app name and icon (pwa.go).
func mountWebStatic(mux *http.ServeMux, prefix, dir string, browserConfig []byte, pwa func(*http.Request) *pwaIdentity) {
	if dir == "" {
		return
	}
	root := filepath.Clean(dir)
	base := prefix + "/"
	mux.HandleFunc("GET "+prefix, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, base, http.StatusMovedPermanently)
	})
	mux.HandleFunc("GET "+base, func(w http.ResponseWriter, r *http.Request) {
		rel := strings.TrimPrefix(r.URL.Path, base)
		rel = strings.TrimPrefix(rel, "/")
		if rel == "observability.json" {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			if len(browserConfig) == 0 {
				_, _ = w.Write([]byte(`{"providers":[]}`))
				return
			}
			_, _ = w.Write(browserConfig)
			return
		}
		if rel == "" {
			serveAppPage(w, r, root, pwa)
			return
		}
		if rel == "manifest.webmanifest" {
			serveAppManifest(w, r, root, pwa)
			return
		}
		if rel == "sw.js" {
			// The browser re-checks the worker on navigation; never let a
			// cache hand it a stale one, and keep its scope at /id/.
			w.Header().Set("Cache-Control", "no-cache")
		}
		candidate := filepath.Join(root, filepath.FromSlash(pathpkg.Clean("/"+rel)))
		cleanCand := filepath.Clean(candidate)
		if cleanCand != root && !strings.HasPrefix(cleanCand+string(os.PathSeparator), root+string(os.PathSeparator)) {
			http.Error(w, "invalid path", http.StatusBadRequest)
			return
		}
		if st, err := os.Stat(cleanCand); err != nil || st.IsDir() {
			serveAppPage(w, r, root, pwa)
			return
		}
		http.ServeFile(w, r, cleanCand)
	})
}

// appPrefix is where the web client lives on every host (EPIC-028 E28-T13).
const appPrefix = "/id"

// legacyAppPrefix is where it lived until web 0.2.25.
const legacyAppPrefix = "/app"

// legacyServiceWorker replaces the worker an installed /app/ web app
// registered. A browser re-checks its worker without following redirects, so
// /app/sw.js must answer itself: this one drops its push subscription (the
// relay then gets 410 and removes the registration), unregisters, and moves
// any open window to /id/?moved=1, where the current worker takes over and
// the app says the home-screen icon has to be added again.
const legacyServiceWorker = `// Retired: the web app moved from /app/ to /id/.
self.addEventListener("install", () => self.skipWaiting());
self.addEventListener("activate", (event) => {
  event.waitUntil((async () => {
    try {
      const sub = await self.registration.pushManager.getSubscription();
      if (sub) await sub.unsubscribe();
    } catch {}
    await self.registration.unregister();
    const windows = await self.clients.matchAll({ type: "window" });
    for (const client of windows) {
      const url = new URL(client.url);
      if (!url.pathname.startsWith("/app/")) continue;
      url.pathname = "/id/" + url.pathname.slice(5);
      url.searchParams.set("moved", "1");
      client.navigate(url.href);
    }
  })());
});
`

// mountLegacyApp keeps old links working after the move: /app and /app/...
// answer 308 to the same path under /id/, query included (browsers carry the
// fragment across a redirect themselves, so #pair= links survive).
func mountLegacyApp(mux *http.ServeMux, dir string) {
	if dir == "" {
		return
	}
	redirect := func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, legacyAppPrefix)
		rest = strings.TrimPrefix(rest, "/")
		target := appPrefix + "/" + rest
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusPermanentRedirect)
	}
	mux.HandleFunc("GET "+legacyAppPrefix, redirect)
	mux.HandleFunc("GET "+legacyAppPrefix+"/", redirect)
	mux.HandleFunc("GET "+legacyAppPrefix+"/sw.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write([]byte(legacyServiceWorker))
	})
}

// rootIcons are the brand icons a browser requests at the origin, not under
// /id/. The SPA links them from its own HTML, which Vite rewrites to sit next
// to the page; a tab on alice.poweur.net still asks for /favicon.ico on that
// host, and GET / would otherwise answer with the JSON service banner. Every
// host this process serves — launcher and identity — shares one router, so
// these routes put the same icon on any poweur.net id.
var rootIcons = []struct {
	path        string
	file        string
	contentType string
}{
	{"/favicon.ico", "favicon.ico", "image/x-icon"},
	{"/favicon.svg", "favicon.svg", "image/svg+xml"},
	{"/apple-touch-icon.png", "apple-touch-icon.png", "image/png"},
}

func mountRootIcons(mux *http.ServeMux, dir string) {
	if dir == "" {
		return
	}
	root := filepath.Clean(dir)
	for _, icon := range rootIcons {
		mux.HandleFunc("GET "+icon.path, func(w http.ResponseWriter, r *http.Request) {
			serveRootIcon(w, r, root, icon.file, icon.contentType)
		})
	}
}

func serveRootIcon(w http.ResponseWriter, r *http.Request, root, name, contentType string) {
	path := filepath.Join(root, name)
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=3600")
	http.ServeFile(w, r, path)
}

// linkViewerCSP is the drive-link viewer's policy: its own scripts and
// styles only, no framing, no forms, no referrer; it may fetch this relay
// and collaborators' public identity documents to verify authors.
const linkViewerCSP = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' blob: data:; " +
	"connect-src 'self' https:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// handleLinkViewer serves the standalone link viewer (web build viewer.html)
// at https://<identity>/s/<link>. The decryption key stays in the URL
// fragment, which browsers never send.
func (s *Server) handleLinkViewer(w http.ResponseWriter, r *http.Request) {
	root := s.cfg.WebStaticDir
	host := r.Host
	if h, _, err := splitHostPort(host); err == nil {
		host = h
	}
	link := r.PathValue("link")
	if root == "" || len(link) != 32 || !isHexString(link) || !s.identities.Exists(strings.ToLower(strings.TrimSuffix(host, "."))) {
		http.NotFound(w, r)
		return
	}
	viewerHeaders(w)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, filepath.Join(filepath.Clean(root), "viewer.html"))
}

// handleLinkViewerAsset serves the viewer's hashed assets beside it.
func (s *Server) handleLinkViewerAsset(w http.ResponseWriter, r *http.Request) {
	root := s.cfg.WebStaticDir
	name := r.PathValue("file")
	if root == "" || name == "" || strings.ContainsAny(name, "/\\") || strings.HasPrefix(name, ".") {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(filepath.Clean(root), "assets", name)
	if st, err := os.Stat(path); err != nil || st.IsDir() {
		http.NotFound(w, r)
		return
	}
	viewerHeaders(w)
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeFile(w, r, path)
}

func viewerHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", linkViewerCSP)
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
}

func isHexString(s string) bool {
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
