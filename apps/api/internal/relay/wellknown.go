package relay

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

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

func (s *Server) handleDIDWeb(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if h, _, err := splitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	raw, ok := s.identities.DocumentJSON(host)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "identity document not found")
		return
	}
	var doc idpkg.IdentityDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		writeError(w, http.StatusInternalServerError, "invalid_identity", "stored identity document is invalid")
		return
	}
	did, err := idpkg.DIDWeb(doc)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "invalid_identity", err.Error())
		return
	}
	body, err := json.Marshal(did)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to encode DID document")
		return
	}
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:8]) + `"`
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/did+json")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("ETag", etag)
	_, _ = w.Write(body)
}

// maxPublicFileBytes caps a public system file (profile, capabilities,
// avatar). Avatars are the largest; clients downscale before upload.
const maxPublicFileBytes = 2 << 20

// publicFileTypes is the closed set of public file types the relay serves,
// by extension. Anything else is refused rather than served as an unknown
// type from the identity's origin.
var publicFileTypes = map[string]string{
	".json": "application/json",
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".webp": "image/webp",
	".gif":  "image/gif",
}

// publicFileName accepts one flat file name under .poweur/public: lowercase
// letters, digits, '-', '_' and a single extension from publicFileTypes.
func publicFileName(sub string) (name, contentType string, ok bool) {
	if sub == "" || len(sub) > 64 || strings.HasPrefix(sub, ".") {
		return "", "", false
	}
	for _, c := range sub {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return "", "", false
		}
	}
	dot := strings.LastIndexByte(sub, '.')
	if dot <= 0 || strings.Count(sub, ".") != 1 {
		return "", "", false
	}
	contentType, ok = publicFileTypes[sub[dot:]]
	return sub, contentType, ok
}

// serveSysPublicFile serves .poweur/public/<name> for a hosted identity.
func (s *Server) serveSysPublicFile(w http.ResponseWriter, r *http.Request, identity, sub string) {
	name, contentType, ok := publicFileName(sub)
	if !ok || !s.identities.Exists(identity) {
		writeError(w, http.StatusNotFound, "not_found", "unknown well-known path")
		return
	}
	if name == "capabilities.json" {
		s.serveCapabilities(w, r, identity)
		return
	}
	raw, err := s.sysFiles.Read(r.Context(), identity, sysPublicDir+"/"+name)
	if err != nil || len(raw) > maxPublicFileBytes {
		writeError(w, http.StatusNotFound, "not_found", "unknown well-known path")
		return
	}
	sum := sha256.Sum256(raw)
	etag := `"` + hex.EncodeToString(sum[:8]) + `"`
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("ETag", etag)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
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

// serveCapabilities serves the identity's capabilities.json with the
// endpoints this relay provides filled in where the file is silent: the web
// signer the relay serves beside the identity, and the operator's OAuth
// bridge (EPIC-022). The user's own values always win, and a missing file
// still answers with those defaults.
func (s *Server) serveCapabilities(w http.ResponseWriter, r *http.Request, identity string) {
	var caps map[string]any
	if raw, err := s.sysFiles.Read(r.Context(), identity, capabilitiesPath); err == nil {
		if len(raw) > idpkg.MaxDocumentBytes || json.Unmarshal(raw, &caps) != nil {
			writeError(w, http.StatusInternalServerError, "invalid_capabilities", "stored capabilities.json is unreadable")
			return
		}
	}
	defaults := s.capabilityEndpoints(identity)
	if caps == nil && len(defaults) == 0 {
		writeError(w, http.StatusNotFound, "not_found", "unknown well-known path")
		return
	}
	if caps == nil {
		caps = map[string]any{"version": 1}
	}
	endpoints, _ := caps["endpoints"].(map[string]any)
	if endpoints == nil {
		endpoints = map[string]any{}
	}
	for k, v := range defaults {
		if _, set := endpoints[k]; !set {
			endpoints[k] = v
		}
	}
	if len(endpoints) > 0 {
		caps["endpoints"] = endpoints
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "public, max-age=300")
	writeJSON(w, http.StatusOK, caps)
}

// capabilityEndpoints are the endpoints this relay provides for identity.
func (s *Server) capabilityEndpoints(identity string) map[string]string {
	out := map[string]string{}
	if s.cfg.WebStaticDir != "" {
		scheme := s.cfg.RelayScheme
		if scheme == "" {
			scheme = "https"
		}
		out["web_signer"] = scheme + "://" + identity + "/app/"
	}
	if s.cfg.OAuthBridgeURL != "" {
		out["oauth_bridge"] = s.cfg.OAuthBridgeURL
	}
	return out
}
