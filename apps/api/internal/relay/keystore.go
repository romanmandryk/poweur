package relay

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/poweur/api/internal/crypto"
	"github.com/poweur/api/internal/storage"
	idpkg "github.com/poweur/identity"
)

// Keystore endpoints (EPIC-011 E11-T1).
//
// Writes are authenticated by the identity key — you are unlocked when you
// enroll a device. The bootstrap read is authenticated by a WebAuthn
// assertion instead, because its whole purpose is recovering an identity whose
// key you no longer hold. The relay stores ciphertext and never unwraps it.

// KeystoreEnrollRequest stores one authenticator's wrapped copy of the seed.
type KeystoreEnrollRequest struct {
	EnrollmentID        string          `json:"enrollment_id"`
	Kind                string          `json:"kind"`
	Wrap                string          `json:"wrap"`
	Payload             string          `json:"payload"`
	CredentialID        string          `json:"credential_id,omitempty"`
	CredentialPublicKey string          `json:"credential_public_key,omitempty"`
	CredentialAlg       int             `json:"credential_alg,omitempty"`
	Wrapped             json.RawMessage `json:"wrapped"`
	Label               string          `json:"label,omitempty"`
	Role                string          `json:"role,omitempty"`
	IssuedAt            string          `json:"issued_at"`
	Nonce               string          `json:"nonce"`
	IdentitySignature   string          `json:"identity_signature"`
}

// KeystoreFetchRequest is the bootstrap read: a WebAuthn assertion over a
// relay-issued challenge, from a caller who has no identity key.
type KeystoreFetchRequest struct {
	Assertion WebAuthnAssertion `json:"assertion"`
	// RelyingPartyID the assertion was scoped to. Must be the identity itself
	// or its registrable domain (EPIC-018 E18-T4).
	RelyingPartyID string `json:"rp_id"`
}

// KeystoreFetchResponse returns ciphertext only.
type KeystoreFetchResponse struct {
	Identity string                  `json:"identity"`
	Entries  []storage.KeystoreEntry `json:"entries"`
}

// KeystoreRemoveRequest deletes an enrollment.
//
// ActorAssertion is what makes the recovery-master role *enforceable* rather
// than advisory. The identity key is shared by every enrollment, so an
// identity signature alone says nothing about which device is asking. Once an
// identity designates a recovery-master, removals must additionally prove
// possession of a recovery-master authenticator — that is the "I lost my
// phone, kill it" path, and it is why a stolen phone cannot evict the YubiKey
// that will revoke it.
type KeystoreRemoveRequest struct {
	IssuedAt          string             `json:"issued_at"`
	Nonce             string             `json:"nonce"`
	IdentitySignature string             `json:"identity_signature"`
	ActorAssertion    *WebAuthnAssertion `json:"actor_assertion,omitempty"`
	RelyingPartyID    string             `json:"rp_id,omitempty"`
	// AllowLast permits removing the final enrollment. Refused by default:
	// silently stranding recovery is worse than an error the caller must
	// acknowledge.
	AllowLast bool `json:"allow_last,omitempty"`
	// RevokeSessions ends the removed device's live access as well. Removal
	// alone only denies it a future bootstrap read.
	RevokeSessions bool `json:"revoke_sessions,omitempty"`
}

var validKeystoreKinds = map[string]bool{
	"passkey": true, "hardware-key": true, "cli-passphrase": true,
	"recovery-kit": true, "native": true,
}

var validKeystoreWraps = map[string]bool{
	"prf": true, "passphrase": true, "native": true,
}

// wrappedDigest is the base64url SHA-256 of the raw wrapped ciphertext, bound
// into the enrollment signature so the blob cannot be swapped.
func wrappedDigest(wrapped json.RawMessage) string {
	sum := sha256.Sum256(wrapped)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// acceptableRPIDs lists the relying-party ids a credential for this identity
// may legitimately be scoped to: the identity host itself, or its registrable
// domain when the identity is hosted under one of the relay's domains.
func (s *Server) acceptableRPIDs(identity string) []string {
	identity = strings.ToLower(strings.TrimSuffix(identity, "."))
	rpIDs := []string{identity}
	for _, domain := range s.cfg.HostedDomains {
		domain = strings.ToLower(strings.TrimSpace(domain))
		if domain != "" && idpkg.IsUnderDomain(identity, domain) {
			rpIDs = append(rpIDs, domain)
		}
	}
	return rpIDs
}

func (s *Server) handleKeystorePut(w http.ResponseWriter, r *http.Request) {
	identity := r.PathValue("identity")
	var req KeystoreEnrollRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return
	}
	if req.EnrollmentID == "" || req.Kind == "" || req.Wrap == "" ||
		len(req.Wrapped) == 0 || req.IssuedAt == "" || req.Nonce == "" || req.IdentitySignature == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "missing keystore enrollment fields")
		return
	}
	if !validKeystoreKinds[req.Kind] {
		writeError(w, http.StatusBadRequest, "invalid_request", "unknown enrollment kind")
		return
	}
	if !validKeystoreWraps[req.Wrap] {
		writeError(w, http.StatusBadRequest, "invalid_request", "unknown wrap method")
		return
	}
	if req.Payload == "" {
		req.Payload = "seed"
	}
	if req.Payload != "seed" && req.Payload != "legacy-keypair" {
		writeError(w, http.StatusBadRequest, "invalid_request", "payload must be seed or legacy-keypair")
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

	// A passkey enrollment without a verifiable public key could never satisfy
	// the bootstrap read, so refuse it rather than storing a dead entry.
	if req.CredentialID != "" {
		if req.CredentialPublicKey == "" {
			writeError(w, http.StatusBadRequest, "invalid_request",
				"credential_public_key is required with credential_id")
			return
		}
		if _, err := parseSPKIPublicKey(req.CredentialPublicKey); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		switch req.CredentialAlg {
		case coseAlgES256, coseAlgEdDSA, coseAlgRS256:
		default:
			writeError(w, http.StatusBadRequest, "invalid_request",
				"credential_alg must be -7, -8 or -257")
			return
		}
	}

	canonical := crypto.CanonicalKeystoreEnroll(
		strings.ToLower(identity), req.EnrollmentID, req.Kind, req.CredentialID,
		wrappedDigest(req.Wrapped), req.IssuedAt, req.Nonce,
	)
	if err := crypto.VerifySignature(ident.PublicKeyBytes, canonical, req.IdentitySignature); err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "enrollment signature invalid")
		return
	}
	verifiedActor(r, identity)

	role := req.Role
	if role == "" {
		role = "device"
	}
	entry := storage.KeystoreEntry{
		EnrollmentID:        req.EnrollmentID,
		Kind:                req.Kind,
		Wrap:                req.Wrap,
		Payload:             req.Payload,
		CredentialID:        req.CredentialID,
		CredentialPublicKey: req.CredentialPublicKey,
		CredentialAlg:       req.CredentialAlg,
		Wrapped:             req.Wrapped,
		Label:               req.Label,
		Role:                role,
		CreatedAt:           time.Now().UTC().Format(time.RFC3339),
	}
	if existing, ok := s.keystore.Get(identity, req.EnrollmentID); ok {
		entry.CreatedAt = existing.CreatedAt
		entry.LastUsedAt = existing.LastUsedAt
	}
	if err := s.keystore.Put(identity, entry); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "failed to store enrollment")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"identity":      strings.ToLower(identity),
		"enrollment_id": entry.EnrollmentID,
		"created_at":    entry.CreatedAt,
	})
}

// KeystoreListRequest enumerates enrollments for the owner. Identity-signed,
// because the owner is unlocked when managing devices.
type KeystoreListRequest struct {
	IssuedAt          string `json:"issued_at"`
	Nonce             string `json:"nonce"`
	IdentitySignature string `json:"identity_signature"`
}

// KeystoreSummary is one enrollment without its ciphertext.
type KeystoreSummary struct {
	EnrollmentID string `json:"enrollment_id"`
	Kind         string `json:"kind"`
	Wrap         string `json:"wrap"`
	Payload      string `json:"payload"`
	Label        string `json:"label,omitempty"`
	Role         string `json:"role,omitempty"`
	HasPasskey   bool   `json:"has_passkey"`
	CreatedAt    string `json:"created_at"`
	LastUsedAt   string `json:"last_used_at,omitempty"`
}

func (s *Server) handleKeystoreList(w http.ResponseWriter, r *http.Request) {
	identity := r.PathValue("identity")
	var req KeystoreListRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return
	}
	if req.IssuedAt == "" || req.Nonce == "" || req.IdentitySignature == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "missing list fields")
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
	canonical := crypto.CanonicalKeystoreList(strings.ToLower(identity), req.IssuedAt, req.Nonce)
	if err := crypto.VerifySignature(ident.PublicKeyBytes, canonical, req.IdentitySignature); err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "list signature invalid")
		return
	}
	verifiedActor(r, identity)
	entries := s.keystore.List(identity)
	out := make([]KeystoreSummary, 0, len(entries))
	for _, e := range entries {
		out = append(out, KeystoreSummary{
			EnrollmentID: e.EnrollmentID,
			Kind:         e.Kind,
			Wrap:         e.Wrap,
			Payload:      e.Payload,
			Label:        e.Label,
			Role:         e.Role,
			HasPasskey:   e.CredentialID != "",
			CreatedAt:    e.CreatedAt,
			LastUsedAt:   e.LastUsedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"identity":    strings.ToLower(identity),
		"enrollments": out,
	})
}

func (s *Server) handleKeystoreFetch(w http.ResponseWriter, r *http.Request) {
	identity := r.PathValue("identity")
	var req KeystoreFetchRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return
	}
	if _, ok := s.identities.Get(identity); !ok {
		writeError(w, http.StatusNotFound, "not_found", "identity not found")
		return
	}
	if req.Assertion.CredentialID == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "assertion credential_id is required")
		return
	}

	// The challenge is consumed whatever the outcome: one assertion, one
	// challenge, so a failed attempt cannot be retried against the same nonce.
	challenge, ok := s.challenges.Consume(strings.ToLower(identity))
	if !ok || challenge.ExpiresAt.Before(time.Now().UTC()) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "no valid challenge; request one first")
		return
	}

	entry, found := s.keystore.FindByCredential(identity, req.Assertion.CredentialID)
	if !found {
		// Deliberately indistinguishable from a bad signature: the caller must
		// not learn which credential ids are enrolled.
		writeError(w, http.StatusUnauthorized, "unauthorized", "assertion rejected")
		return
	}

	rpIDs := s.acceptableRPIDs(identity)
	if req.RelyingPartyID != "" {
		allowed := false
		for _, rp := range rpIDs {
			if strings.EqualFold(rp, req.RelyingPartyID) {
				allowed = true
				break
			}
		}
		if !allowed {
			writeError(w, http.StatusUnauthorized, "unauthorized", "assertion rejected")
			return
		}
		rpIDs = []string{req.RelyingPartyID}
	}

	if err := verifyWebAuthnAssertion(
		req.Assertion, entry.CredentialPublicKey, entry.CredentialAlg, challenge.Value, rpIDs,
	); err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "assertion rejected")
		return
	}
	verifiedActor(r, identity)

	s.keystore.TouchLastUsed(identity, entry.EnrollmentID, time.Now())
	writeJSON(w, http.StatusOK, KeystoreFetchResponse{
		Identity: strings.ToLower(identity),
		Entries:  s.keystore.List(identity),
	})
}

func (s *Server) handleKeystoreDelete(w http.ResponseWriter, r *http.Request) {
	identity := r.PathValue("identity")
	enrollmentID := r.PathValue("enrollment")
	var req KeystoreRemoveRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return
	}
	if req.IssuedAt == "" || req.Nonce == "" || req.IdentitySignature == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "missing removal fields")
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
	canonical := crypto.CanonicalKeystoreRemove(
		strings.ToLower(identity), enrollmentID, req.IssuedAt, req.Nonce)
	if err := crypto.VerifySignature(ident.PublicKeyBytes, canonical, req.IdentitySignature); err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "removal signature invalid")
		return
	}
	verifiedActor(r, identity)
	entries := s.keystore.List(identity)
	if _, ok := s.keystore.Get(identity, enrollmentID); !ok {
		writeError(w, http.StatusNotFound, "not_found", "enrollment not found")
		return
	}
	if len(entries) == 1 && !req.AllowLast {
		writeError(w, http.StatusConflict, "last_enrollment",
			"refusing to remove the only enrollment; recovery would be impossible. "+
				"Set allow_last to override.")
		return
	}

	// Recovery-master gating (E11-T2): only enforced once such a role exists,
	// so identities that never designate one keep the simpler flow.
	if hasRecoveryMaster(entries) {
		if req.ActorAssertion == nil {
			writeError(w, http.StatusForbidden, "recovery_master_required",
				"this identity has a recovery-master; removals need an actor assertion from it")
			return
		}
		if err := s.verifyActorIsRecoveryMaster(identity, *req.ActorAssertion, req.RelyingPartyID); err != nil {
			writeError(w, http.StatusForbidden, "recovery_master_required", "actor assertion rejected")
			return
		}
	}

	if err := s.keystore.Remove(identity, enrollmentID); err != nil {
		if errors.Is(err, storage.ErrEnrollmentNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "enrollment not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "storage_error", "failed to remove enrollment")
		return
	}
	revoked := 0
	if req.RevokeSessions {
		for _, sess := range s.sessions.ListForIdentity(strings.ToLower(identity)) {
			s.sessions.Delete(sess.ID)
			revoked++
		}
	}
	if revoked > 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"enrollment_id":    enrollmentID,
			"sessions_revoked": revoked,
		})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func hasRecoveryMaster(entries []storage.KeystoreEntry) bool {
	for _, e := range entries {
		if e.Role == "recovery-master" {
			return true
		}
	}
	return false
}

// verifyActorIsRecoveryMaster consumes a challenge and checks the assertion
// belongs to an enrollment carrying the recovery-master role.
func (s *Server) verifyActorIsRecoveryMaster(
	identity string, assertion WebAuthnAssertion, rpID string,
) error {
	challenge, ok := s.challenges.Consume(strings.ToLower(identity))
	if !ok || challenge.ExpiresAt.Before(time.Now().UTC()) {
		return errors.New("no valid challenge")
	}
	actor, found := s.keystore.FindByCredential(identity, assertion.CredentialID)
	if !found || actor.Role != "recovery-master" {
		return errors.New("actor is not a recovery-master")
	}
	rpIDs := s.acceptableRPIDs(identity)
	if rpID != "" {
		allowed := false
		for _, candidate := range rpIDs {
			if strings.EqualFold(candidate, rpID) {
				allowed = true
				break
			}
		}
		if !allowed {
			return errors.New("relying party not acceptable")
		}
		rpIDs = []string{rpID}
	}
	return verifyWebAuthnAssertion(
		assertion, actor.CredentialPublicKey, actor.CredentialAlg, challenge.Value, rpIDs)
}
