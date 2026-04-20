package relay

import (
	"net/http"
	"strings"
	"time"

	"github.com/eurything/api/internal/crypto"
	"github.com/eurything/api/internal/storage"
)

// maxSessionTTL caps how long a session can be valid. Mobile passkey flows can
// only re-prove the long-lived identity key periodically, so keep the window short.
const maxSessionTTL = 24 * time.Hour

func (s *Server) handleSessionCreate(w http.ResponseWriter, r *http.Request) {
	var req SessionCreateRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return
	}

	req.Identity = strings.TrimSpace(req.Identity)
	req.SessionPublicKey = strings.TrimSpace(req.SessionPublicKey)
	req.IssuedAt = strings.TrimSpace(req.IssuedAt)
	req.ExpiresAt = strings.TrimSpace(req.ExpiresAt)
	req.Nonce = strings.TrimSpace(req.Nonce)
	req.IdentitySignature = strings.TrimSpace(req.IdentitySignature)

	if req.Identity == "" || req.SessionPublicKey == "" || req.IssuedAt == "" ||
		req.ExpiresAt == "" || req.Nonce == "" || req.IdentitySignature == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "missing required session fields")
		return
	}

	issuedAt, err := time.Parse(time.RFC3339, req.IssuedAt)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "issued_at must be RFC3339")
		return
	}
	expiresAt, err := time.Parse(time.RFC3339, req.ExpiresAt)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "expires_at must be RFC3339")
		return
	}
	now := time.Now().UTC()
	if expiresAt.Before(now) {
		writeError(w, http.StatusBadRequest, "invalid_request", "session already expired")
		return
	}
	if !expiresAt.After(issuedAt) {
		writeError(w, http.StatusBadRequest, "invalid_request", "expires_at must be after issued_at")
		return
	}
	if expiresAt.Sub(issuedAt) > maxSessionTTL {
		writeError(w, http.StatusBadRequest, "invalid_request", "session exceeds max TTL (24h)")
		return
	}
	if issuedAt.After(now.Add(5 * time.Minute)) {
		writeError(w, http.StatusBadRequest, "invalid_request", "issued_at is too far in the future")
		return
	}

	sessionPubNormalized, sessionPubBytes, err := crypto.NormalizePublicKey(req.SessionPublicKey)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_session_key", err.Error())
		return
	}

	identityPubKey, err := s.resolveIdentityPublicKey(r.Context(), req.Identity)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "cannot resolve identity public key: "+err.Error())
		return
	}

	canonical := crypto.CanonicalSessionRegistration(
		req.Identity,
		sessionPubNormalized,
		req.IssuedAt,
		req.ExpiresAt,
		req.Nonce,
	)
	if err := crypto.VerifySignature(identityPubKey, canonical, req.IdentitySignature); err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "identity signature invalid")
		return
	}

	token, err := randomToken(18)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server_error", "could not generate session id")
		return
	}
	sessionID := "sess_" + token

	session := storage.Session{
		ID:                sessionID,
		Identity:          req.Identity,
		PublicKey:         sessionPubNormalized,
		PublicKeyBytes:    sessionPubBytes,
		IssuedAt:          issuedAt.UTC(),
		ExpiresAt:         expiresAt.UTC(),
		DeviceFingerprint: strings.TrimSpace(req.DeviceFingerprint),
		IssuedAtRaw:       req.IssuedAt,
		ExpiresAtRaw:      req.ExpiresAt,
		Nonce:             req.Nonce,
		IdentitySignature: req.IdentitySignature,
	}
	s.sessions.Put(session)

	writeJSON(w, http.StatusCreated, SessionResponse{
		SessionID:        sessionID,
		Identity:         req.Identity,
		SessionPublicKey: sessionPubNormalized,
		IssuedAt:         issuedAt.UTC().Format(time.RFC3339),
		ExpiresAt:        expiresAt.UTC().Format(time.RFC3339),
	})
}

func (s *Server) handleSessionDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "session id required")
		return
	}
	s.sessions.Delete(id)
	w.WriteHeader(http.StatusNoContent)
}
