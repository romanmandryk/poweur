package relay

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	idpkg "github.com/poweur/identity"
)

// Owner API for system files (EPIC-020 E20-T6):
//
//	GET    /identities/{identity}/system/{path...}
//	PUT    /identities/{identity}/system/{path...}
//	DELETE /identities/{identity}/system/{path...}
//
// Authenticated like an inbox pickup (a one-shot challenge signed by the
// identity key or a live session key). The owner writes `.poweur/public/`
// and `.poweur/relay/`; `.poweur/state/` is the relay's own record and is
// read-only here. Known documents are schema-checked on write so a malformed
// contacts.json can never take down policy enforcement; unknown names in the
// owner zones are stored untouched (forward compatibility).
//
// Writes accept `If-Match` with the ETag a read returned, so two devices
// editing the same document learn about each other instead of silently
// overwriting.

// sysValidators maps exact system paths to their validators.
var sysValidators = map[string]func([]byte) error{
	analyticsPath: func(b []byte) error { _, err := parseAnalytics(b); return err },
	contactsPath: func(b []byte) error {
		_, err := idpkg.ParseContactsFile(b)
		return err
	},
	inboxPolicyPath: func(b []byte) error {
		_, err := idpkg.ParseInboxPolicy(b)
		return err
	},
	idpkg.ConnectedAppsPath: func(b []byte) error {
		_, err := idpkg.ParseConnectedApps(b)
		return err
	},
	profilePath: func(b []byte) error {
		_, err := idpkg.ParseProfile(b)
		return err
	},
	capabilitiesPath: func(b []byte) error {
		_, err := idpkg.ParseCapabilities(b)
		return err
	},
}

// sysFileETag is the ETag of a system document: a truncated SHA-256.
func sysFileETag(raw []byte) string {
	sum := sha256.Sum256(raw)
	return `"` + hex.EncodeToString(sum[:8]) + `"`
}

// sysAPIPath authenticates the owner and returns the requested system path.
func (s *Server) sysAPIPath(w http.ResponseWriter, r *http.Request) (identity, path string, ok bool) {
	identity = strings.ToLower(strings.TrimSpace(r.PathValue("identity")))
	path = r.PathValue("path")
	if !validSysPath(path) {
		writeError(w, http.StatusBadRequest, "invalid_path", "not a system file path (.poweur/{public,relay,state}/<name>)")
		return "", "", false
	}
	if !s.identities.Exists(identity) {
		writeError(w, http.StatusNotFound, "not_found", "identity not hosted on this relay")
		return "", "", false
	}
	if !s.authorizeInboxRead(w, r, identity) {
		return "", "", false
	}
	return identity, path, true
}

func (s *Server) handleSystemFileGet(w http.ResponseWriter, r *http.Request) {
	identity, path, ok := s.sysAPIPath(w, r)
	if !ok {
		return
	}
	raw, err := s.sysFiles.Read(r.Context(), identity, path)
	if errors.Is(err, errSysFileNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "no such system file")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "failed to read system file")
		return
	}
	w.Header().Set("ETag", sysFileETag(raw))
	w.Header().Set("Content-Type", sysFileContentType(path))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(raw)
}

func (s *Server) handleSystemFilePut(w http.ResponseWriter, r *http.Request) {
	identity, path, ok := s.sysAPIPath(w, r)
	if !ok {
		return
	}
	limit, validate, err := s.sysWriteRule(identity, path)
	if err != nil {
		writeError(w, http.StatusForbidden, "forbidden", err.Error())
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, int64(limit)+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "failed to read body")
		return
	}
	if len(body) > limit {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", fmt.Sprintf("system file exceeds %d bytes", limit))
		return
	}
	if err := validate(body); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_document", err.Error())
		return
	}
	old, readErr := s.sysFiles.Read(r.Context(), identity, path)
	if readErr != nil && !errors.Is(readErr, errSysFileNotFound) {
		writeError(w, http.StatusInternalServerError, "storage_error", "failed to read system file")
		return
	}
	if !sysPreconditionOK(r, old, readErr == nil) {
		writeError(w, http.StatusPreconditionFailed, "conflict", "the system file changed since you read it")
		return
	}
	if err := s.sysFiles.Write(r.Context(), identity, path, body); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "failed to write system file")
		return
	}
	if fields := settingsChanges(path, old, body); len(fields) > 0 {
		settingsChanged(r, fields)
	}
	w.Header().Set("ETag", sysFileETag(body))
	writeJSON(w, http.StatusOK, map[string]string{"path": path, "etag": sysFileETag(body)})
}

func (s *Server) handleSystemFileDelete(w http.ResponseWriter, r *http.Request) {
	identity, path, ok := s.sysAPIPath(w, r)
	if !ok {
		return
	}
	if _, _, err := s.sysWriteRule(identity, path); err != nil {
		writeError(w, http.StatusForbidden, "forbidden", err.Error())
		return
	}
	old, readErr := s.sysFiles.Read(r.Context(), identity, path)
	if errors.Is(readErr, errSysFileNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "no such system file")
		return
	}
	if readErr != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "failed to read system file")
		return
	}
	if !sysPreconditionOK(r, old, true) {
		writeError(w, http.StatusPreconditionFailed, "conflict", "the system file changed since you read it")
		return
	}
	if err := s.sysFiles.Delete(r.Context(), identity, path); err != nil && !errors.Is(err, errSysFileNotFound) {
		writeError(w, http.StatusInternalServerError, "storage_error", "failed to delete system file")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// sysPreconditionOK applies If-Match: "*" needs the file to exist, an ETag
// needs it to be unchanged, and no header means last write wins.
func sysPreconditionOK(r *http.Request, current []byte, exists bool) bool {
	match := strings.TrimSpace(r.Header.Get("If-Match"))
	switch {
	case match == "":
		return true
	case match == "*":
		return exists
	default:
		return exists && match == sysFileETag(current)
	}
}

// sysWriteRule says whether the owner may write path and how: the size limit
// and the validator. `.poweur/state/` is relay-written; id.json is served
// from the identity store and changes only through registration and rotation.
func (s *Server) sysWriteRule(identity, path string) (int, func([]byte) error, error) {
	name := path[strings.LastIndexByte(path, '/')+1:]
	switch {
	case strings.HasPrefix(path, sysStateDir+"/"):
		return 0, nil, errors.New(".poweur/state is written by the relay")
	case path == sysPublicDir+"/id.json":
		return 0, nil, errors.New("id.json changes through registration and key rotation")
	case path == groupRosterPath:
		return maxSysDocBytes, func(b []byte) error { return s.validateOwnGroupRoster(identity, b) }, nil
	}
	if v, ok := sysValidators[path]; ok {
		return maxSysDocBytes, v, nil
	}
	if strings.HasPrefix(path, sysPublicDir+"/") {
		if _, contentType, ok := publicFileName(name); ok && strings.HasPrefix(contentType, "image/") {
			return maxPublicFileBytes, func(b []byte) error { return validateImage(b, contentType) }, nil
		}
	}
	return maxSysDocBytes, func([]byte) error { return nil }, nil
}

// validateOwnGroupRoster accepts a roster only for a group identity naming
// itself and signed with its own key — the same checks groupIdentity applies
// when reading, enforced before the document is stored.
func (s *Server) validateOwnGroupRoster(identity string, raw []byte) error {
	gr, err := idpkg.ParseShareGroup(raw)
	if err != nil {
		return err
	}
	if !gr.IsGroupIdentity() || !strings.EqualFold(gr.Group, identity) || !strings.EqualFold(gr.Owner, identity) {
		return errors.New("group.json must describe this identity as a group identity")
	}
	id, ok := s.identities.Get(identity)
	if !ok {
		return errors.New("identity not hosted here")
	}
	return gr.VerifySignature(id.PublicKeyBytes)
}

// validateImage checks that the bytes really are the image type the file
// name claims, so a public avatar cannot be HTML served from the identity's
// origin.
func validateImage(raw []byte, contentType string) error {
	sniffed := http.DetectContentType(raw)
	if contentType == "image/jpeg" && sniffed == "image/jpeg" || sniffed == contentType {
		return nil
	}
	return fmt.Errorf("content is %s, not %s", sniffed, contentType)
}

// sysFileContentType is the Content-Type a system file is returned with.
func sysFileContentType(path string) string {
	if _, contentType, ok := publicFileName(path[strings.LastIndexByte(path, '/')+1:]); ok {
		return contentType
	}
	return "application/octet-stream"
}
