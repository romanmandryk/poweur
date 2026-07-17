package relay

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"mime"
	"net/http"
	"os"
	gopath "path"
	"sort"
	"strings"

	"github.com/poweur/api/internal/files"
)

// webPublicMarkerConfig is the optional JSON content of a .poweur-web-public
// marker file. An empty marker serves files only.
type webPublicMarkerConfig struct {
	Listings bool `json:"listings"`
}

// handlePub serves owner-marked folders under /public to the plain web,
// without Poweur authentication (E03-T6). Routed by Host header:
// https://<identity>/pub/<path>. Only folders carrying a
// `.poweur-web-public` marker (the folder itself or an ancestor within
// /public) are exposed; everything else stays ID-auth only.
func (s *Server) handlePub(w http.ResponseWriter, r *http.Request) {
	if !s.davEnabled() {
		writeError(w, http.StatusNotImplemented, "storage_disabled", "file storage requires POWEUR_DATA")
		return
	}
	host := r.Host
	if h, _, err := splitHostPort(host); err == nil {
		host = h
	}
	owner := strings.ToLower(strings.TrimSuffix(host, "."))
	if owner == "" || !s.identities.Exists(owner) {
		writeError(w, http.StatusNotFound, "not_found", "identity not hosted on this relay")
		return
	}

	rel := strings.TrimPrefix(r.URL.Path, "/pub")
	rel = strings.TrimPrefix(rel, "/")
	clean, err := files.CleanPath(files.RootPublic + "/" + rel)
	if err != nil || !files.Under(clean, files.RootPublic) {
		writeError(w, http.StatusBadRequest, "invalid_path", "invalid path")
		return
	}

	marker, ok := s.findWebPublicMarker(r, owner, clean)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "not web-public")
		return
	}

	fi, err := s.filesProvider.Stat(r.Context(), owner, clean)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "no such file")
		return
	}

	if fi.IsDir() {
		if !marker.Listings {
			writeError(w, http.StatusNotFound, "not_found", "directory listings are disabled")
			return
		}
		s.servePubListing(w, r, owner, clean)
		return
	}

	if gopath.Base(clean) == files.WebPublicMarker {
		writeError(w, http.StatusNotFound, "not_found", "no such file")
		return
	}

	f, err := s.filesProvider.OpenFile(r.Context(), owner, clean, os.O_RDONLY, 0)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "no such file")
		return
	}
	defer f.Close()

	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", pubContentType(clean))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", fi.Size()))
	if meta, ok := s.filesIndex.Get(owner, clean); ok {
		if etag := files.FormatETag(meta); etag != "" {
			w.Header().Set("ETag", etag)
			if r.Header.Get("If-None-Match") == etag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
	}
	if isDownloadType(clean) {
		w.Header().Set("Content-Disposition", `attachment; filename="`+gopath.Base(clean)+`"`)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f)
}

// findWebPublicMarker walks from the requested path up to /public looking
// for a .poweur-web-public marker; returns its parsed config.
func (s *Server) findWebPublicMarker(r *http.Request, owner, clean string) (webPublicMarkerConfig, bool) {
	dir := clean
	if fi, err := s.filesProvider.Stat(r.Context(), owner, clean); err != nil || !fi.IsDir() {
		dir = gopath.Dir(clean)
	}
	for {
		if !files.Under(dir, files.RootPublic) {
			return webPublicMarkerConfig{}, false
		}
		markerPath := dir + "/" + files.WebPublicMarker
		if f, err := s.filesProvider.OpenFile(r.Context(), owner, markerPath, os.O_RDONLY, 0); err == nil {
			var cfg webPublicMarkerConfig
			_ = json.NewDecoder(io.LimitReader(f, 4096)).Decode(&cfg) // empty file = defaults
			_ = f.Close()
			return cfg, true
		}
		if dir == files.RootPublic {
			return webPublicMarkerConfig{}, false
		}
		dir = gopath.Dir(dir)
	}
}

func (s *Server) servePubListing(w http.ResponseWriter, r *http.Request, owner, clean string) {
	f, err := s.filesProvider.OpenFile(r.Context(), owner, clean, os.O_RDONLY, 0)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "no such directory")
		return
	}
	defer f.Close()
	entries, err := f.Readdir(-1)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "listing_failed", err.Error())
		return
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	rel := strings.TrimPrefix(clean, files.RootPublic)
	fmt.Fprintf(w, "<!doctype html><meta charset=\"utf-8\"><title>%s</title><h1>%s</h1><ul>",
		html.EscapeString("/pub"+rel), html.EscapeString("/pub"+rel))
	for _, e := range entries {
		name := e.Name()
		if name == files.WebPublicMarker {
			continue
		}
		suffix := ""
		if e.IsDir() {
			suffix = "/"
		}
		fmt.Fprintf(w, `<li><a href="%s%s">%s%s</a></li>`,
			html.EscapeString(gopath.Join("/pub", rel, name)), suffix,
			html.EscapeString(name), suffix)
	}
	fmt.Fprint(w, "</ul>")
}

// pubContentType maps extensions to safe content types. Active content
// (HTML/SVG/XML/JS) is served as text/plain in v1 — /pub is a file share,
// not a web host; that keeps stored-XSS off identity origins.
func pubContentType(path string) string {
	ext := strings.ToLower(gopath.Ext(path))
	switch ext {
	case ".html", ".htm", ".svg", ".xml", ".xhtml", ".js", ".mjs":
		return "text/plain; charset=utf-8"
	}
	if t := mime.TypeByExtension(ext); t != "" {
		return t
	}
	return "application/octet-stream"
}

func isDownloadType(path string) bool {
	switch strings.ToLower(gopath.Ext(path)) {
	case ".zip", ".gz", ".tar", ".dmg", ".exe", ".bin", ".apk":
		return true
	}
	return false
}
