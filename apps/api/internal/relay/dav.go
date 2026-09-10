package relay

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/poweur/api/internal/files"
	idpkg "github.com/poweur/identity"
	"golang.org/x/net/webdav"
)

// davEnabled reports whether the file layer is available (needs durable storage).
func (s *Server) davEnabled() bool {
	return s.filesProvider != nil
}

// grantSnapshot loads the owner's verified grants once for this request
// (EPIC-005). Revocation is a grant-file delete: the next request reloads.
func (s *Server) grantSnapshot(r *http.Request, owner string) files.GrantChecker {
	if s.grants == nil {
		return files.DenyAllGrants
	}
	return s.grants.Snapshot(r.Context(), owner)
}

// lockSystem returns the per-identity DAV lock system (Class 2 locks are
// in-memory: relay restart drops locks, which vanilla clients re-acquire).
func (s *Server) lockSystem(identity string) webdav.LockSystem {
	s.locksMu.Lock()
	defer s.locksMu.Unlock()
	key := strings.ToLower(identity)
	if ls, ok := s.davLocks[key]; ok {
		return ls
	}
	ls := webdav.NewMemLS()
	s.davLocks[key] = ls
	return ls
}

// resolveDAVTree extracts (owner, prefix) from a /dav request. Two forms:
//
//	/dav/<identity>/<path>   canonical (what clients always send)
//	Host: <identity> /dav/<path>   vanity alias via Host-routing
//
// On a shared wildcard relay the SPA origin *is* the identity Host, and the
// client still emits the canonical path. When both apply, the path wins:
// a first segment that names a hosted identity is canonical; otherwise Host
// selects the owner (vanity, for Finder mounts at https://<identity>/dav/).
func (s *Server) resolveDAVTree(r *http.Request) (owner, prefix string, ok bool) {
	host := r.Host
	if h, _, err := splitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	seg := firstDavSegment(r.URL.Path)
	if host != "" && s.identities.Exists(host) {
		if seg != "" && s.identities.Exists(seg) {
			return seg, "/dav/" + seg, true
		}
		return host, "/dav", true
	}
	if seg == "" {
		return "", "", false
	}
	return seg, "/dav/" + seg, true
}

func firstDavSegment(path string) string {
	rest := strings.TrimPrefix(path, "/dav")
	rest = strings.TrimPrefix(rest, "/")
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		rest = rest[:i]
	}
	return strings.ToLower(rest)
}

// davMethodAccess classifies WebDAV methods into read/write for the
// permission pre-check (the filesystem layer enforces again as a backstop).
func davMethodAccess(method string) (files.Access, bool) {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, "PROPFIND":
		return files.AccessRead, true
	case http.MethodPut, http.MethodDelete, "MKCOL", "PROPPATCH", "COPY", "MOVE", "LOCK", "UNLOCK":
		return files.AccessWrite, true
	default:
		return files.AccessRead, false
	}
}

// authenticateDAV resolves the caller's principal for owner's tree from
// Bearer (DAV token) or Basic (app password) credentials. A missing header
// yields the anonymous principal with ok=true; a *bad* credential fails.
func (s *Server) authenticateDAV(r *http.Request, owner string) (files.Principal, bool, string) {
	authz := strings.TrimSpace(r.Header.Get("Authorization"))
	if authz == "" {
		return files.Anonymous, true, ""
	}
	switch {
	case strings.HasPrefix(authz, "Bearer "):
		tok, ok := s.davTokens.Get(strings.TrimSpace(strings.TrimPrefix(authz, "Bearer ")))
		if !ok {
			return files.Principal{}, false, "invalid or expired token"
		}
		if !strings.EqualFold(tok.Audience, owner) {
			return files.Principal{}, false, "token audience does not match tree"
		}
		verifiedActor(r, tok.Identity)
		return files.Principal{
			Identity: tok.Identity,
			Owner:    strings.EqualFold(tok.Identity, owner),
			Scope:    tok.Scope,
		}, true, ""
	case strings.HasPrefix(authz, "Basic "):
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(strings.TrimPrefix(authz, "Basic ")))
		if err != nil {
			return files.Principal{}, false, "malformed Basic credentials"
		}
		user, pass, found := strings.Cut(string(raw), ":")
		if !found {
			return files.Principal{}, false, "malformed Basic credentials"
		}
		if !strings.EqualFold(strings.TrimSpace(user), owner) {
			return files.Principal{}, false, "Basic username must be the tree owner identity"
		}
		entry, ok := s.verifyAppPassword(r, owner, pass)
		if !ok {
			return files.Principal{}, false, "invalid app password"
		}
		scope := files.Scope{Write: true}
		if entry.Scope != "" {
			if parsed, err := files.ParseScope(entry.Scope); err == nil {
				scope = parsed
			}
		}
		verifiedActor(r, owner)
		return files.Principal{Identity: owner, Owner: true, Scope: scope}, true, ""
	default:
		return files.Principal{}, false, "unsupported Authorization scheme"
	}
}

// verifyAppPassword checks pass against poweur-sys/relay/app-passwords.json.
// Reading the file per attempt keeps revocation immediate (file edit = revoke).
func (s *Server) verifyAppPassword(r *http.Request, owner, pass string) (idpkg.AppPassword, bool) {
	if decision := s.rateLimit.Allow("davauth:" + strings.ToLower(owner)); !decision.Allowed {
		return idpkg.AppPassword{}, false
	}
	f, err := s.filesProvider.OpenFile(r.Context(), owner, files.SysRelay+"/app-passwords.json", os.O_RDONLY, 0)
	if err != nil {
		return idpkg.AppPassword{}, false
	}
	defer f.Close()
	var parsed idpkg.AppPasswordsFile
	if err := json.NewDecoder(f).Decode(&parsed); err != nil {
		return idpkg.AppPassword{}, false
	}
	for _, entry := range parsed.Passwords {
		if idpkg.VerifyAppPassword(pass, entry.Hash) {
			return entry, true
		}
	}
	return idpkg.AppPassword{}, false
}

// handleDAV serves the WebDAV tree (E03-T2/T4).
func (s *Server) handleDAV(w http.ResponseWriter, r *http.Request) {
	if !s.davEnabled() {
		writeError(w, http.StatusNotImplemented, "storage_disabled", "file storage requires POWEUR_DATA")
		return
	}
	owner, prefix, ok := s.resolveDAVTree(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_request", "missing identity in /dav path")
		return
	}
	if !s.identities.Exists(owner) {
		writeError(w, http.StatusNotFound, "not_found", "identity not hosted on this relay")
		return
	}

	principal, ok, reason := s.authenticateDAV(r, owner)
	if !ok {
		s.noteDAVAuthFailure(r, owner, principal.Identity, reason)
		w.Header().Set("WWW-Authenticate", `Basic realm="poweur-dav", Bearer`)
		writeError(w, http.StatusUnauthorized, "unauthorized", reason)
		return
	}

	access, known := davMethodAccess(r.Method)
	if !known {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "unsupported method")
		return
	}

	// Pre-check permissions on the request path (and MOVE/COPY destination)
	// so denials produce clean 401/403 instead of generic DAV errors.
	treePath := strings.TrimPrefix(r.URL.Path, prefix)
	clean, err := files.CleanPath(treePath)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_path", err.Error())
		return
	}
	if clean != "" {
		if _, err := files.ValidateTreePath(clean); err != nil {
			writeError(w, http.StatusForbidden, "invalid_path", err.Error())
			return
		}
	}
	perms := files.Permissions{Grants: s.grantSnapshot(r, owner)}
	if !perms.Allowed(owner, principal, clean, access) {
		if principal == files.Anonymous {
			w.Header().Set("WWW-Authenticate", `Basic realm="poweur-dav", Bearer`)
			writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		writeError(w, http.StatusForbidden, "forbidden", "path not permitted for this principal")
		return
	}
	if dest := r.Header.Get("Destination"); dest != "" && (r.Method == "MOVE" || r.Method == "COPY") {
		destPath, derr := davDestinationPath(dest, prefix)
		if derr != nil {
			writeError(w, http.StatusBadRequest, "invalid_destination", derr.Error())
			return
		}
		if !perms.Allowed(owner, principal, destPath, files.AccessWrite) {
			writeError(w, http.StatusForbidden, "forbidden", "destination not permitted for this principal")
			return
		}
	}

	// Quota enforcement on PUT (507 per RFC 4918); Content-Length required
	// so the check happens before the body is consumed.
	if r.Method == http.MethodPut {
		if r.ContentLength < 0 {
			writeError(w, http.StatusLengthRequired, "length_required", "PUT requires Content-Length")
			return
		}
		if s.cfg.MaxFileBytes > 0 && r.ContentLength > s.cfg.MaxFileBytes {
			writeError(w, http.StatusRequestEntityTooLarge, "file_too_large", "file exceeds max file size")
			return
		}
		if s.cfg.MaxIdentityBytes > 0 {
			used, err := s.filesProvider.UsedBytes(r.Context(), owner)
			if err == nil && used+r.ContentLength > s.cfg.MaxIdentityBytes {
				writeError(w, http.StatusInsufficientStorage, "quota_exceeded", "identity storage quota exceeded")
				return
			}
		}
	}

	// Schema validation for known system documents (E06-T1): a malformed
	// contacts.json / inbox-policy.json / grant / manifest never lands.
	if !s.checkSysWrite(w, r, clean) {
		return
	}

	if access == files.AccessWrite {
		requestAction(r, systemAction(clean, r.Method))
	}

	// Audit cross-identity access (E03-T4): any authenticated non-owner
	// touching the tree is logged to poweur-sys/relay/logs/access.log.
	if principal.Identity != "" && !principal.Owner {
		s.appendAccessLog(r, owner, map[string]any{
			"time":    time.Now().UTC().Format(time.RFC3339),
			"event":   "cross_identity_access",
			"visitor": principal.Identity,
			"method":  r.Method,
			"path":    "/" + clean,
		})
	}

	h := &webdav.Handler{
		Prefix:     prefix,
		FileSystem: &files.DavFS{Owner: owner, Provider: s.filesProvider, Index: s.filesIndex, Perms: perms},
		LockSystem: s.lockSystem(owner),
	}
	h.ServeHTTP(w, r.WithContext(files.WithPrincipal(r.Context(), principal)))
}

// davDestinationPath extracts the tree path from a Destination header value.
func davDestinationPath(dest, prefix string) (string, error) {
	p := dest
	if strings.Contains(dest, "://") {
		// absolute URL — strip scheme://host
		if i := strings.Index(dest, "://"); i >= 0 {
			rest := dest[i+3:]
			if j := strings.IndexByte(rest, '/'); j >= 0 {
				p = rest[j:]
			} else {
				p = "/"
			}
		}
	}
	p = strings.TrimPrefix(p, prefix)
	return files.ValidateTreePath(p)
}

// appendAccessLog appends a JSON line to the owner's relay-readable access
// log. Best-effort: logging never blocks the request.
func (s *Server) appendAccessLog(r *http.Request, owner string, record map[string]any) {
	if !s.davEnabled() {
		return
	}
	dir := files.SysRelay + "/logs"
	_ = s.filesProvider.Mkdir(r.Context(), owner, dir)
	f, err := s.filesProvider.OpenFile(r.Context(), owner, dir+"/access.log", os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	line, err := json.Marshal(record)
	if err != nil {
		return
	}
	_, _ = f.Write(append(line, '\n'))
}

// noteDAVAuthFailure rate-limits and audit-logs failed DAV authentications.
func (s *Server) noteDAVAuthFailure(r *http.Request, owner, identity, reason string) {
	if owner == "" || !s.davEnabled() || !s.identities.Exists(owner) {
		return
	}
	s.appendAccessLog(r, owner, map[string]any{
		"time":     time.Now().UTC().Format(time.RFC3339),
		"event":    "auth_failure",
		"identity": identity,
		"reason":   reason,
	})
}

// handleFilesQuota reports storage usage for an identity (owner-only).
func (s *Server) handleFilesQuota(w http.ResponseWriter, r *http.Request) {
	if !s.davEnabled() {
		writeError(w, http.StatusNotImplemented, "storage_disabled", "file storage requires POWEUR_DATA")
		return
	}
	owner := strings.ToLower(r.PathValue("identity"))
	if !s.identities.Exists(owner) {
		writeError(w, http.StatusNotFound, "not_found", "identity not hosted on this relay")
		return
	}
	principal, ok, reason := s.authenticateDAV(r, owner)
	if !ok || !principal.Owner {
		if reason == "" {
			reason = "owner credentials required"
		}
		writeError(w, http.StatusUnauthorized, "unauthorized", reason)
		return
	}
	used, err := s.filesProvider.UsedBytes(r.Context(), owner)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"identity":    owner,
		"provider":    s.filesProvider.Name(),
		"used_bytes":  used,
		"quota_bytes": s.cfg.MaxIdentityBytes,
		"change_id":   s.filesIndex.ChangeID(owner),
	})
}
