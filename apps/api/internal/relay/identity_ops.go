package relay

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/poweur/api/internal/crypto"
	"github.com/poweur/api/internal/storage"
	idpkg "github.com/poweur/identity"
)

func (s *Server) storageHealth() *StorageHealth {
	path := strings.TrimSpace(s.cfg.DataDir)
	if path == "" {
		return &StorageHealth{Configured: false}
	}
	h := &StorageHealth{Configured: true, Path: path}
	if err := os.MkdirAll(path, 0o700); err != nil {
		h.Writable = false
		h.Error = err.Error()
		return h
	}
	probe := filepath.Join(path, ".poweur-write-probe")
	if err := os.WriteFile(probe, []byte("ok"), 0o600); err != nil {
		h.Writable = false
		h.Error = err.Error()
		return h
	}
	_ = os.Remove(probe)
	h.Writable = true
	if free, err := freeBytes(path); err == nil {
		h.FreeBytes = free
	}
	return h
}

func (s *Server) handleIdentityExport(w http.ResponseWriter, r *http.Request) {
	identity := r.PathValue("identity")
	var req ExportRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return
	}
	if req.IssuedAt == "" || req.Nonce == "" || req.IdentitySignature == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "missing export signature envelope")
		return
	}
	if err := requireRecentTimestamp(req.IssuedAt); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	ident, ok := s.identities.Get(identity)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "identity not found")
		return
	}
	canonical := crypto.CanonicalIdentityExport(identity, req.IssuedAt, req.Nonce)
	if err := crypto.VerifySignature(ident.PublicKeyBytes, canonical, req.IdentitySignature); err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "export signature invalid")
		return
	}

	home, err := s.identities.IdentityHomeDir(identity)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "export_failed", err.Error())
		return
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if home == "" {
		// Memory-only: export just the document.
		raw := ident.DocumentJSON
		if len(raw) == 0 {
			writeError(w, http.StatusNotFound, "not_found", "no durable document to export")
			return
		}
		hdr := &tar.Header{Name: "poweur-sys/public/id.json", Mode: 0o600, Size: int64(len(raw)), ModTime: time.Now()}
		if err := tw.WriteHeader(hdr); err != nil {
			writeError(w, http.StatusInternalServerError, "export_failed", err.Error())
			return
		}
		if _, err := tw.Write(raw); err != nil {
			writeError(w, http.StatusInternalServerError, "export_failed", err.Error())
			return
		}
	} else {
		err := filepath.WalkDir(home, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			rel, err := filepath.Rel(home, path)
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			hdr, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}
			hdr.Name = filepath.ToSlash(rel)
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			_, err = io.Copy(tw, f)
			return err
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "export_failed", err.Error())
			return
		}
	}
	if err := tw.Close(); err != nil {
		writeError(w, http.StatusInternalServerError, "export_failed", err.Error())
		return
	}
	if err := gz.Close(); err != nil {
		writeError(w, http.StatusInternalServerError, "export_failed", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+identity+`.tar.gz"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

func (s *Server) handleIdentityRotate(w http.ResponseWriter, r *http.Request) {
	identity := r.PathValue("identity")
	var req RotateRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return
	}
	if len(req.IdentityDocument) == 0 || req.NewPublicKey == "" || req.IssuedAt == "" || req.Nonce == "" || req.RotationSignature == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "missing rotation fields")
		return
	}
	if err := requireRecentTimestamp(req.IssuedAt); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	ident, ok := s.identities.Get(identity)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "identity not found")
		return
	}

	newNorm, newPubBytes, err := crypto.NormalizePublicKey(req.NewPublicKey)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_public_key", err.Error())
		return
	}
	oldNorm := ident.PublicKey
	canonical := crypto.CanonicalIdentityRotation(identity, oldNorm, newNorm, req.IssuedAt, req.Nonce)
	if err := crypto.VerifySignature(ident.PublicKeyBytes, canonical, req.RotationSignature); err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "rotation signature invalid")
		return
	}

	doc, err := idpkg.ParseDocument(req.IdentityDocument, true)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_identity_document", err.Error())
		return
	}
	if !strings.EqualFold(doc.Identity, identity) {
		writeError(w, http.StatusBadRequest, "invalid_identity_document", "document identity mismatch")
		return
	}
	if idpkg.NormalizePublicKeyKey(doc.PublicKey) != newNorm {
		writeError(w, http.StatusBadRequest, "invalid_identity_document", "document public key mismatch")
		return
	}
	// New document must list the old key in previous_keys.
	foundOld := false
	for _, pk := range doc.PreviousKeys {
		if idpkg.NormalizePublicKeyKey(pk.PublicKey) == oldNorm {
			foundOld = true
			break
		}
	}
	if !foundOld {
		writeError(w, http.StatusBadRequest, "invalid_identity_document", "previous_keys must include the old public key")
		return
	}

	enc := req.EncryptionPublicKey
	if enc == "" {
		enc = ident.EncryptionPublicKey
	} else {
		enc, _, err = crypto.NormalizeX25519PublicKey(enc)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_encryption_key", err.Error())
			return
		}
	}

	updated := storage.Identity{
		Identity:            strings.ToLower(identity),
		PublicKey:           newNorm,
		PublicKeyBytes:      newPubBytes,
		EncryptionPublicKey: enc,
		Relay:               doc.Relay,
		DocumentJSON:        req.IdentityDocument,
		CreatedAt:           ident.CreatedAt,
	}
	if err := s.identities.Put(updated); err != nil {
		writeError(w, http.StatusInternalServerError, "persist_failed", err.Error())
		return
	}
	s.idCache.Invalidate(identity)

	writeJSON(w, http.StatusOK, IdentityResponse{
		Identity:            identity,
		PublicKey:           newNorm,
		EncryptionPublicKey: enc,
		Relay:               doc.Relay,
		CreatedAt:           ident.CreatedAt.UTC().Format(time.RFC3339),
		IdentityDocument:    req.IdentityDocument,
	})
}
