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
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Depth, Destination, Overwrite, If, Lock-Token, X-Poweur-Identity, X-Poweur-Signature, X-Poweur-Session-Id")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// mountWebStatic serves the Vite-built SPA from dir under /app/ (base URL /app/).
func mountWebStatic(mux *http.ServeMux, dir string) {
	if dir == "" {
		return
	}
	root := filepath.Clean(dir)
	mux.HandleFunc("GET /app", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/app/", http.StatusMovedPermanently)
	})
	mux.HandleFunc("GET /app/", func(w http.ResponseWriter, r *http.Request) {
		rel := strings.TrimPrefix(r.URL.Path, "/app/")
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
