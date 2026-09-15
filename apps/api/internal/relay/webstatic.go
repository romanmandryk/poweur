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
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS, PROPFIND, PROPPATCH, MKCOL, COPY, MOVE, LOCK, UNLOCK")
		// Every header the protocol sends must be listed, or the browser's
		// preflight fails and the request never happens. X-Poweur-Challenge was
		// missing, which nothing same-origin ever noticed — the web app is
		// served *by* the relay, so it sends no preflight — while a Capacitor
		// shell on capacitor://localhost, or any third-party site using the
		// SDK, had every authenticated call blocked before it left the browser.
		w.Header().Set("Access-Control-Allow-Headers",
			"Content-Type, Authorization, Depth, Destination, Overwrite, If, Lock-Token, "+
				"X-Poweur-Identity, X-Poweur-Challenge, X-Poweur-Signature, X-Poweur-Session-Id")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// mountWebStatic serves the web client SPA from dir under prefix ("/app" →
// GET /app/). The same handler serves the EPIC-021 preview build at /newapp/,
// on the same origin, so both apps share one identity store while they are
// compared side by side.
func mountWebStatic(mux *http.ServeMux, prefix, dir string) {
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
		if rel == "" {
			http.ServeFile(w, r, filepath.Join(root, "index.html"))
			return
		}
		candidate := filepath.Join(root, filepath.FromSlash(pathpkg.Clean("/"+rel)))
		cleanCand := filepath.Clean(candidate)
		if cleanCand != root && !strings.HasPrefix(cleanCand+string(os.PathSeparator), root+string(os.PathSeparator)) {
			http.Error(w, "invalid path", http.StatusBadRequest)
			return
		}
		if st, err := os.Stat(cleanCand); err != nil || st.IsDir() {
			http.ServeFile(w, r, filepath.Join(root, "index.html"))
			return
		}
		http.ServeFile(w, r, cleanCand)
	})
}
