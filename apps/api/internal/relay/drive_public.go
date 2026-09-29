package relay

import (
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/poweur/api/internal/drive/engine"
	"github.com/poweur/identity/drive"
)

// Public folders (EPIC-020 E20-T5): https://<identity>/pub/<folder>/<path>
// serves the owner's public nodes. Their names and content keys are in the
// signed manifests on purpose, so the relay decrypts on the way out and any
// cache may keep the result. Nothing private resolves here: a path that is
// not public is simply not found.
func (s *Server) handlePublic(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if h, _, err := splitHostPort(host); err == nil {
		host = h
	}
	driveID := strings.ToLower(strings.TrimSuffix(host, "."))
	if s.engine == nil || !s.identities.Exists(driveID) {
		http.NotFound(w, r)
		return
	}
	raw := strings.TrimPrefix(r.URL.EscapedPath(), "/pub")
	raw = strings.TrimPrefix(raw, "/")
	var segments []string
	for _, part := range strings.Split(raw, "/") {
		if part == "" {
			continue
		}
		name, err := url.PathUnescape(part)
		if err != nil || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
			http.NotFound(w, r)
			return
		}
		segments = append(segments, name)
	}
	publicHeaders(w)
	if len(segments) == 0 {
		children, err := s.engine.PublicChildren(r.Context(), driveID, "")
		if err != nil {
			s.writePublicError(w, r, err)
			return
		}
		s.writePublicListing(w, r, driveID, "/pub/", children)
		return
	}
	node, err := s.engine.PublicPath(r.Context(), driveID, segments)
	if err != nil {
		s.writePublicError(w, r, err)
		return
	}
	etag := `"` + node.Head + `"`
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if node.Kind == drive.KindFolder {
		if !strings.HasSuffix(r.URL.Path, "/") {
			http.Redirect(w, r, r.URL.EscapedPath()+"/", http.StatusMovedPermanently)
			return
		}
		children, err := s.engine.PublicChildren(r.Context(), driveID, node.ID)
		if err != nil {
			s.writePublicError(w, r, err)
			return
		}
		s.writePublicListing(w, r, driveID, r.URL.EscapedPath(), children)
		return
	}
	if node.Mode != drive.ModeReplace || len(node.Versions) == 0 {
		http.NotFound(w, r)
		return
	}
	key, err := base64.RawURLEncoding.DecodeString(engine.PublicContentKey(node))
	if err != nil || len(key) != 32 {
		http.Error(w, "public file has no content key", http.StatusInternalServerError)
		return
	}
	head := node.Versions[0]
	context, err := drive.Context(head.Drive, head.Node, drive.PurposeContent, head.Generation)
	if err != nil {
		http.Error(w, "invalid public file", http.StatusInternalServerError)
		return
	}
	var refs []drive.ChunkRef
	for _, hash := range head.Pages {
		page, err := s.engine.Page(r.Context(), driveID, node.ID, head.Version, hash)
		if err != nil {
			s.writePublicError(w, r, err)
			return
		}
		refs = append(refs, page.Chunks...)
	}
	contentType := mime.TypeByExtension(path.Ext(segments[len(segments)-1]))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	if r.Method == http.MethodHead {
		return
	}
	// One chunk at a time: memory stays bounded by the chunk size.
	for i, ref := range refs {
		objectKey, err := s.engine.ChunkKey(r.Context(), driveID, node.ID, head.Version, ref.ID)
		if err != nil {
			s.publicAbort(w, r, i, err)
			return
		}
		obj, err := s.drive.Get(r.Context(), objectKey, nil)
		if err != nil {
			s.publicAbort(w, r, i, err)
			return
		}
		plain, err := drive.DecryptChunk(key, obj.Data, context)
		if err != nil {
			s.publicAbort(w, r, i, err)
			return
		}
		if _, err := w.Write(plain); err != nil {
			return
		}
	}
}

// publicHeaders: public content may be cached by anyone for a minute. It is
// sandboxed: scripts on a published page run, but in an opaque origin that
// cannot touch the identity host's viewer, storage or cookies.
func publicHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "public, max-age=60")
	w.Header().Set("Content-Security-Policy", "sandbox allow-scripts allow-forms allow-popups allow-downloads")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Access-Control-Allow-Origin", "*")
}

func (s *Server) writePublicError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, engine.ErrNotFound) {
		w.Header().Set("Cache-Control", "no-store")
		http.NotFound(w, r)
		return
	}
	s.writeDriveError(w, err)
}

// publicAbort fails a public download: cleanly before any byte was sent,
// otherwise by cutting the connection so a client never keeps a truncated file.
func (s *Server) publicAbort(w http.ResponseWriter, r *http.Request, sent int, err error) {
	if sent == 0 {
		w.Header().Del("Content-Type")
		s.writePublicError(w, r, err)
		return
	}
	panic(http.ErrAbortHandler)
}

// writePublicListing renders a public folder: JSON for programs, a plain
// index page for browsers. Names are escaped; nothing else is shown.
func (s *Server) writePublicListing(w http.ResponseWriter, r *http.Request, driveID, base string, children []engine.ListedNode) {
	type entry struct {
		Name     string `json:"name"`
		Kind     string `json:"kind"`
		Mode     string `json:"mode,omitempty"`
		Version  string `json:"version"`
		Position uint64 `json:"position,omitempty"`
	}
	entries := make([]entry, 0, len(children))
	for _, c := range children {
		entries = append(entries, entry{Name: engine.PublicName(c), Kind: c.Kind, Mode: c.Mode, Version: c.Head, Position: c.Position})
	}
	if !strings.Contains(r.Header.Get("Accept"), "text/html") {
		writeJSON(w, http.StatusOK, map[string]any{"drive": driveID, "path": base, "entries": entries})
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	var b strings.Builder
	fmt.Fprintf(&b, "<!doctype html><meta charset=utf-8><meta name=viewport content=\"width=device-width\"><title>%s</title>", html.EscapeString(driveID+base))
	fmt.Fprintf(&b, "<body style=\"font:16px system-ui;max-width:40em;margin:2em auto;padding:0 1em\"><h1 style=\"font-size:1.2em\">%s</h1><ul>", html.EscapeString(driveID+base))
	for _, e := range entries {
		href := url.PathEscape(e.Name)
		label := e.Name
		if e.Kind == drive.KindFolder {
			href += "/"
			label += "/"
		}
		fmt.Fprintf(&b, "<li><a href=\"%s\">%s</a></li>", html.EscapeString(href), html.EscapeString(label))
	}
	b.WriteString("</ul></body>")
	_, _ = w.Write([]byte(b.String()))
}
