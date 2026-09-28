package relay

import (
	"net/http"
	"strings"
	"time"

	"github.com/poweur/api/internal/crypto"
	"github.com/poweur/api/internal/storage"
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
	verifiedActor(r, req.Identity)

	token, err := randomToken(18)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server_error", "could not generate session id")
		return
	}
	sessionID := "sess_" + token

	// The device this session belongs to (EPIC-004 E04-T6). The body field
	// has always existed; the header is what every other endpoint uses, so a
	// client that sets only the header still gets a registry row — and, more
	// importantly, a session that device revocation can find and delete.
	obs := deviceFromRequest(r)
	if fp := strings.TrimSpace(req.DeviceFingerprint); fp != "" {
		obs.Fingerprint = fp
	}
	// A revoked device may not quietly re-arm itself under the same
	// fingerprint; it has to re-enrol as a new device the owner can see.
	if s.deviceRevoked(r.Context(), req.Identity, obs.Fingerprint) {
		writeError(w, http.StatusForbidden, "device_revoked", "this device has been revoked by the owner")
		return
	}

	session := storage.Session{
		ID:                sessionID,
		Identity:          req.Identity,
		PublicKey:         sessionPubNormalized,
		PublicKeyBytes:    sessionPubBytes,
		IssuedAt:          issuedAt.UTC(),
		ExpiresAt:         expiresAt.UTC(),
		DeviceFingerprint: obs.Fingerprint,
		IssuedAtRaw:       req.IssuedAt,
		ExpiresAtRaw:      req.ExpiresAt,
		Nonce:             req.Nonce,
		IdentitySignature: req.IdentitySignature,
	}
	s.sessions.Put(session)

	s.touchDevice(r.Context(), req.Identity, obs)

	writeJSON(w, http.StatusCreated, SessionResponse{
		SessionID:        sessionID,
		Identity:         req.Identity,
		SessionPublicKey: sessionPubNormalized,
		IssuedAt:         issuedAt.UTC().Format(time.RFC3339),
		ExpiresAt:        expiresAt.UTC().Format(time.RFC3339),
	})
}

// handleSessionDelete is owner-only. The body must include an
// identity-signed envelope (canonical: session-revocation/identity/
// session_id/issued_at/nonce) verified against the long-lived signing
// key of the identity that owns the session. Idempotent: deleting an
// unknown session still returns 204, but only after auth succeeds.
func (s *Server) handleSessionDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "session id required")
		return
	}
	var req SessionRevokeRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return
	}
	req.Identity = strings.TrimSpace(req.Identity)
	req.IssuedAt = strings.TrimSpace(req.IssuedAt)
	req.Nonce = strings.TrimSpace(req.Nonce)
	req.IdentitySignature = strings.TrimSpace(req.IdentitySignature)
	if req.Identity == "" || req.IssuedAt == "" || req.Nonce == "" || req.IdentitySignature == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "missing identity-signed admin envelope (identity, issued_at, nonce, identity_signature)")
		return
	}
	if err := requireRecentTimestamp(req.IssuedAt); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	// If the session is known, ensure the caller's claimed identity
	// matches it. If unknown, fall through to identity verification on the
	// claimed identity — revoking by id alone is fine as long as the
	// caller proves they own *some* identity that DNS associates with
	// this relay; the relay does not leak whether the id existed.
	if existing, ok := s.sessions.Get(id); ok {
		if existing.Identity != req.Identity {
			writeError(w, http.StatusUnauthorized, "unauthorized", "session does not belong to identity")
			return
		}
	}

	identityPub, err := s.resolveIdentityPublicKey(r.Context(), req.Identity)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "cannot resolve identity public key: "+err.Error())
		return
	}
	canonical := crypto.CanonicalSessionRevocation(req.Identity, id, req.IssuedAt, req.Nonce)
	if err := crypto.VerifySignature(identityPub, canonical, req.IdentitySignature); err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "identity signature invalid")
		return
	}
	verifiedActor(r, req.Identity)

	s.sessions.Delete(id)
	w.WriteHeader(http.StatusNoContent)
}
