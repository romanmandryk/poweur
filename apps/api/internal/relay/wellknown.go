package relay

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/poweur/api/internal/files"
	idpkg "github.com/poweur/identity"
)

func (s *Server) handleWellKnown(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if h, _, err := splitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" {
		writeError(w, http.StatusBadRequest, "invalid_host", "missing Host")
		return
	}

	path := r.URL.Path
	switch {
	case strings.HasSuffix(path, "/id.json") || path == "/.well-known/poweur/id.json":
		s.serveIdentityDocument(w, r, host)
	case strings.HasSuffix(path, "/pubkey") || path == "/.well-known/poweur/pubkey":
		s.serveIdentityPubkey(w, host)
	case strings.HasSuffix(path, "/enckey") || path == "/.well-known/poweur/enckey":
		s.serveIdentityEnckey(w, host)
	default:
		// Any other path serves the identity's poweur-sys/public/ tree
		// (E06-T1: /.well-known/poweur/ is backed by that world-readable
		// directory — profile.json, capabilities.json, future documents).
		s.serveSysPublicFile(w, r, host, strings.TrimPrefix(path, "/.well-known/poweur/"))
	}
}

// serveSysPublicFile serves poweur-sys/public/<sub> for a hosted identity.
func (s *Server) serveSysPublicFile(w http.ResponseWriter, r *http.Request, identity, sub string) {
	if !s.davEnabled() || !s.identities.Exists(identity) {
		writeError(w, http.StatusNotFound, "not_found", "unknown well-known path")
		return
	}
	clean, err := files.CleanPath(sub)
	if err != nil || clean == "" || strings.Contains(clean, "..") {
		writeError(w, http.StatusNotFound, "not_found", "unknown well-known path")
		return
	}
	f, err := s.filesProvider.OpenFile(r.Context(), identity, files.SysPublic+"/"+clean, os.O_RDONLY, 0)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "unknown well-known path")
		return
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || fi.IsDir() {
		writeError(w, http.StatusNotFound, "not_found", "unknown well-known path")
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if strings.HasSuffix(clean, ".json") {
		w.Header().Set("Content-Type", "application/json")
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f)
}

func (s *Server) serveIdentityDocument(w http.ResponseWriter, r *http.Request, identity string) {
	raw, ok := s.identities.DocumentJSON(identity)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "identity document not found")
		return
	}
	sum := sha256.Sum256(raw)
	etag := `"` + hex.EncodeToString(sum[:8]) + `"`
	if match := r.Header.Get("If-None-Match"); match == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("ETag", etag)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

func (s *Server) serveIdentityPubkey(w http.ResponseWriter, identity string) {
	entry, ok := s.identities.Get(identity)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "identity not found")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write([]byte(idpkg.FormatEd25519PublicKey(entry.PublicKeyBytes) + "\n"))
}

func (s *Server) serveIdentityEnckey(w http.ResponseWriter, identity string) {
	entry, ok := s.identities.Get(identity)
	if !ok || entry.EncryptionPublicKey == "" {
		writeError(w, http.StatusNotFound, "not_found", "encryption key not found")
		return
	}
	enc := entry.EncryptionPublicKey
	if !strings.HasPrefix(enc, "x25519:") {
		enc = "x25519:" + enc
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write([]byte(enc + "\n"))
}

func splitHostPort(hostport string) (host, port string, err error) {
	// net.SplitHostPort requires brackets for IPv6; for simple host:port use last colon.
	if i := strings.LastIndex(hostport, ":"); i >= 0 {
		// avoid treating bare hostnames with no port incorrectly when no colon
		if strings.Count(hostport, ":") == 1 || strings.HasPrefix(hostport, "[") {
			return hostport[:i], hostport[i+1:], nil
		}
	}
	return hostport, "", nil
}

func documentPublicKeyBytes(doc idpkg.IdentityDocument) (string, []byte, error) {
	pub, err := idpkg.ParseEd25519PublicKey(doc.PublicKey)
	if err != nil {
		return "", nil, err
	}
	return idpkg.NormalizePublicKeyKey(doc.PublicKey), pub, nil
}

func marshalDocRaw(doc idpkg.IdentityDocument) (json.RawMessage, error) {
	b, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(b), nil
}

func encodePubB64(pub []byte) string {
	return base64.RawURLEncoding.EncodeToString(pub)
}
