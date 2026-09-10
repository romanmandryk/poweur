package relay

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/poweur/api/internal/files"
)

// Sync API (EPIC-004): delta discovery over the changes journal, tree
// manifest for full resync, and chunked resumable uploads. File bodies keep
// flowing over WebDAV (E03); these endpoints only carry metadata + chunks.

const (
	syncChangesDefaultLimit = 1000
	syncChangesMaxLimit     = 5000
)

// syncAuth authenticates the caller for owner's tree and returns the
// principal, mirroring DAV semantics (anonymous allowed, bad creds fail).
func (s *Server) syncAuth(w http.ResponseWriter, r *http.Request) (owner string, principal files.Principal, ok bool) {
	if !s.davEnabled() {
		writeError(w, http.StatusNotImplemented, "storage_disabled", "file storage requires POWEUR_DATA")
		return "", files.Principal{}, false
	}
	owner = strings.ToLower(r.PathValue("identity"))
	if !s.identities.Exists(owner) {
		writeError(w, http.StatusNotFound, "not_found", "identity not hosted on this relay")
		return "", files.Principal{}, false
	}
	principal, authOK, reason := s.authenticateDAV(r, owner)
	if !authOK {
		s.noteDAVAuthFailure(r, owner, principal.Identity, reason)
		w.Header().Set("WWW-Authenticate", `Basic realm="poweur-dav", Bearer`)
		writeError(w, http.StatusUnauthorized, "unauthorized", reason)
		return "", files.Principal{}, false
	}
	return owner, principal, true
}

// syncPathFilters parses the ?paths= query (repeatable, comma-separable)
// into clean tree-path prefixes.
func syncPathFilters(r *http.Request) ([]string, error) {
	var out []string
	for _, raw := range r.URL.Query()["paths"] {
		for _, one := range strings.Split(raw, ",") {
			one = strings.TrimSpace(one)
			if one == "" {
				continue
			}
			clean, err := files.ValidateTreePath(one)
			if err != nil {
				return nil, fmt.Errorf("invalid paths filter %q: %w", one, err)
			}
			out = append(out, clean)
		}
	}
	return out, nil
}

// syncVisible builds the per-record visibility predicate: layout/grant
// permissions for the principal plus any ?paths= prefixes.
func syncVisible(owner string, principal files.Principal, prefixes []string, grants files.GrantChecker) func(string) bool {
	perms := files.Permissions{Grants: grants}
	return func(path string) bool {
		if !perms.Allowed(owner, principal, path, files.AccessRead) {
			return false
		}
		if len(prefixes) == 0 {
			return true
		}
		for _, p := range prefixes {
			if files.Under(path, p) || files.Under(p, path) {
				return true
			}
		}
		return false
	}
}

// parseCursor decodes an opaque changes cursor ("" = from the beginning).
func parseCursor(raw string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0, errors.New("invalid cursor")
	}
	return n, nil
}

func formatCursor(n int64) string { return strconv.FormatInt(n, 10) }

// handleSyncChanges serves GET /sync/{identity}/changes?since=&limit=&paths=.
func (s *Server) handleSyncChanges(w http.ResponseWriter, r *http.Request) {
	owner, principal, ok := s.syncAuth(w, r)
	if !ok {
		return
	}
	since, err := parseCursor(r.URL.Query().Get("since"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_cursor", "since must be a cursor previously returned by this endpoint")
		return
	}
	limit := syncChangesDefaultLimit
	if rawLimit := r.URL.Query().Get("limit"); rawLimit != "" {
		n, err := strconv.Atoi(rawLimit)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "invalid_request", "limit must be a positive integer")
			return
		}
		limit = min(n, syncChangesMaxLimit)
	}
	prefixes, err := syncPathFilters(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_path", err.Error())
		return
	}

	recs, latest, gap := s.filesIndex.Changes(owner, since, limit, syncVisible(owner, principal, prefixes, s.grantSnapshot(r, owner)))
	next := since
	if len(recs) > 0 {
		next = recs[len(recs)-1].ChangeID
	} else if !gap {
		// Nothing visible beyond the cursor: fast-forward so clients don't
		// re-scan filtered-out records forever.
		next = latest
	}
	if recs == nil {
		recs = []files.JournalRecord{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"identity":    owner,
		"since":       formatCursor(since),
		"next":        formatCursor(next),
		"latest":      formatCursor(latest),
		"full_resync": gap,
		"changes":     recs,
	})
}

// manifestEntry is one NDJSON line of the manifest stream.
type manifestEntry struct {
	Path    string    `json:"path"`
	ETag    string    `json:"etag,omitempty"`
	Size    int64     `json:"size,omitempty"`
	ModTime time.Time `json:"mtime"`
	Dir     bool      `json:"dir,omitempty"`
}

// handleSyncManifest serves GET /sync/{identity}/manifest?paths= as streamed
// NDJSON: a header line {"manifest":1,...,"cursor":...} then one line per
// visible index entry, path-sorted (E04-T1 full-resync path).
func (s *Server) handleSyncManifest(w http.ResponseWriter, r *http.Request) {
	owner, principal, ok := s.syncAuth(w, r)
	if !ok {
		return
	}
	prefixes, err := syncPathFilters(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_path", err.Error())
		return
	}
	visible := syncVisible(owner, principal, prefixes, s.grantSnapshot(r, owner))
	snapshot, changeID := s.filesIndex.Snapshot(owner)
	paths := make([]string, 0, len(snapshot))
	for p := range snapshot {
		if visible(p) {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)

	w.Header().Set("Content-Type", "application/x-ndjson")
	enc := json.NewEncoder(w)
	_ = enc.Encode(map[string]any{"manifest": 1, "identity": owner, "cursor": formatCursor(changeID)})
	for _, p := range paths {
		m := snapshot[p]
		dir := m.Dir || (m.ETag == "" && m.Size == 0) // pre-Dir-flag index entries
		_ = enc.Encode(manifestEntry{Path: p, ETag: m.ETag, Size: m.Size, ModTime: m.ModTime, Dir: dir})
	}
}

// --- chunked resumable upload (E04-T3) ---

// uploadResponse is the JSON body for upload create/status.
type uploadResponse struct {
	ID        string `json:"id"`
	Path      string `json:"path"`
	Offset    int64  `json:"offset"`
	Length    int64  `json:"length"`
	ExpiresAt string `json:"expires_at"`
}

func uploadJSON(info files.UploadInfo) uploadResponse {
	return uploadResponse{
		ID:     info.ID,
		Path:   info.Path,
		Offset: info.Offset,
		Length: info.Length,
		ExpiresAt: info.CreatedAt.Add(files.UploadTTL).
			Format(time.RFC3339),
	}
}

// handleUploadCreate serves POST /sync/{identity}/upload?path=. The total
// size arrives in Upload-Length (tus header names; see sync-protocol.md).
// Quota is enforced here, at upload start — not just at assembly.
func (s *Server) handleUploadCreate(w http.ResponseWriter, r *http.Request) {
	owner, principal, ok := s.syncAuth(w, r)
	if !ok {
		return
	}
	clean, err := files.ValidateTreePath(r.URL.Query().Get("path"))
	if err != nil || clean == "" || files.IsRoot(clean) {
		writeError(w, http.StatusBadRequest, "invalid_path", "path query parameter must name a file inside the tree")
		return
	}
	perms := files.Permissions{Grants: s.grantSnapshot(r, owner)}
	if !perms.Allowed(owner, principal, clean, files.AccessWrite) {
		writeError(w, http.StatusForbidden, "forbidden", "path not permitted for this principal")
		return
	}
	length, err := strconv.ParseInt(r.Header.Get("Upload-Length"), 10, 64)
	if err != nil || length < 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "Upload-Length header required")
		return
	}
	if s.cfg.MaxFileBytes > 0 && length > s.cfg.MaxFileBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "file_too_large", "file exceeds max file size")
		return
	}
	if s.cfg.MaxIdentityBytes > 0 {
		used, err := s.filesProvider.UsedBytes(r.Context(), owner)
		if err == nil && used+length > s.cfg.MaxIdentityBytes {
			writeError(w, http.StatusInsufficientStorage, "quota_exceeded", "identity storage quota exceeded")
			return
		}
	}
	info, err := s.uploads.Create(owner, clean, length, principal.Identity)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "upload_failed", err.Error())
		return
	}
	w.Header().Set("Location", "/sync/"+owner+"/upload/"+info.ID)
	writeJSON(w, http.StatusCreated, uploadJSON(info))
}

// uploadForRequest loads the upload and re-checks the principal's write
// permission on its target path.
func (s *Server) uploadForRequest(w http.ResponseWriter, r *http.Request) (owner string, info files.UploadInfo, ok bool) {
	owner, principal, ok := s.syncAuth(w, r)
	if !ok {
		return "", files.UploadInfo{}, false
	}
	info, err := s.uploads.Get(owner, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "upload not found or expired")
		return "", files.UploadInfo{}, false
	}
	perms := files.Permissions{Grants: s.grantSnapshot(r, owner)}
	if !perms.Allowed(owner, principal, info.Path, files.AccessWrite) {
		writeError(w, http.StatusForbidden, "forbidden", "path not permitted for this principal")
		return "", files.UploadInfo{}, false
	}
	return owner, info, true
}

// handleUploadStatus serves HEAD /sync/{identity}/upload/{id} — the resume
// probe: Upload-Offset says where to continue.
func (s *Server) handleUploadStatus(w http.ResponseWriter, r *http.Request) {
	_, info, ok := s.uploadForRequest(w, r)
	if !ok {
		return
	}
	w.Header().Set("Upload-Offset", strconv.FormatInt(info.Offset, 10))
	w.Header().Set("Upload-Length", strconv.FormatInt(info.Length, 10))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
}

// handleUploadPatch serves PATCH /sync/{identity}/upload/{id}: appends the
// request body at Upload-Offset; when the spool reaches Upload-Length the
// file is assembled atomically into the tree and journaled.
func (s *Server) handleUploadPatch(w http.ResponseWriter, r *http.Request) {
	owner, info, ok := s.uploadForRequest(w, r)
	if !ok {
		return
	}
	offset, err := strconv.ParseInt(r.Header.Get("Upload-Offset"), 10, 64)
	if err != nil || offset < 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "Upload-Offset header required")
		return
	}
	newOffset, err := s.uploads.Append(owner, info.ID, offset, r.Body)
	if err != nil {
		w.Header().Set("Upload-Offset", strconv.FormatInt(newOffset, 10))
		switch {
		case errors.Is(err, files.ErrUploadOffset):
			writeError(w, http.StatusConflict, "offset_mismatch", "Upload-Offset does not match spooled bytes; HEAD to resume")
		case errors.Is(err, files.ErrUploadOverflow):
			writeError(w, http.StatusRequestEntityTooLarge, "upload_overflow", "chunk exceeds declared Upload-Length")
		default:
			writeError(w, http.StatusInternalServerError, "upload_failed", err.Error())
		}
		return
	}
	w.Header().Set("Upload-Offset", strconv.FormatInt(newOffset, 10))
	if newOffset < info.Length {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	meta, err := s.uploads.Assemble(r.Context(), s.filesProvider, s.filesIndex, owner, info.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "assembly_failed", err.Error())
		return
	}
	requestAction(r, "upload.complete")
	w.Header().Set("ETag", files.FormatETag(meta))
	writeJSON(w, http.StatusOK, map[string]any{
		"path": info.Path, "etag": meta.ETag, "size": meta.Size,
		"change_id": meta.ChangeID,
	})
}

// handleUploadDelete cancels a partial upload.
func (s *Server) handleUploadDelete(w http.ResponseWriter, r *http.Request) {
	owner, info, ok := s.uploadForRequest(w, r)
	if !ok {
		return
	}
	if err := s.uploads.Cancel(owner, info.ID); err != nil {
		writeError(w, http.StatusNotFound, "not_found", "upload not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}
