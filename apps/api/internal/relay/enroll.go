package relay

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/poweur/api/internal/crypto"
	"github.com/poweur/api/internal/storage"
)

// New-device enrollment ceremony (EPIC-011 E11-T3).
//
// Moving a seed to a new device needs an authentic channel, not a secret one.
// The new device generates an ephemeral X25519 keypair and shows a short
// code; the user types that code on a device that already holds the identity,
// which encrypts the seed to the ephemeral key and posts the ciphertext here.
//
// **Why there is no PAKE.** An earlier design had the code act as a password
// protecting the payload, which would have required a PAKE so the relay could
// not brute-force a six-digit secret offline. Having the *new* device generate
// the keypair removes the requirement entirely: the code authenticates a
// public key rather than encrypting anything, so there is no offline target.
// Forging it means finding a colliding SAS on the first and only try. That is
// the numeric-comparison model used by Bluetooth pairing and Signal safety
// numbers, and it needs no exotic primitive.
//
// The relay is a blind letterbox throughout: it sees an ephemeral public key
// and a sealed blob, and can open neither.

// enrollTTL bounds an offer's life. Long enough to type a code, short enough
// that an abandoned offer disappears quickly.
const enrollTTL = 10 * time.Minute

// sasDigits is the length of the short authentication string.
const sasDigits = 6

// EnrollOfferRequest is posted by the *new* device. It is unauthenticated —
// the device has no key yet, which is the whole point — so it is rate-limited
// and capped per identity instead.
type EnrollOfferRequest struct {
	EphemeralPublicKey string `json:"ephemeral_public_key"`
	// Label is shown to the approving user ("Firefox on Linux").
	Label string `json:"label,omitempty"`
}

type EnrollOfferResponse struct {
	RendezvousID string `json:"rendezvous_id"`
	SAS          string `json:"sas"`
	ExpiresAt    string `json:"expires_at"`
}

// EnrollFetchRequest is the approving device asking what it is approving.
type EnrollFetchRequest struct {
	IssuedAt          string `json:"issued_at"`
	Nonce             string `json:"nonce"`
	IdentitySignature string `json:"identity_signature"`
}

type EnrollFetchResponse struct {
	RendezvousID       string `json:"rendezvous_id"`
	EphemeralPublicKey string `json:"ephemeral_public_key"`
	SAS                string `json:"sas"`
	Label              string `json:"label,omitempty"`
	ExpiresAt          string `json:"expires_at"`
}

// EnrollDeliverRequest carries the sealed seed from the approving device.
type EnrollDeliverRequest struct {
	Sealed            string `json:"sealed"`
	IssuedAt          string `json:"issued_at"`
	Nonce             string `json:"nonce"`
	IdentitySignature string `json:"identity_signature"`
}

// EnrollClaimResponse is polled by the new device.
type EnrollClaimResponse struct {
	Ready  bool   `json:"ready"`
	Sealed string `json:"sealed,omitempty"`
}

// ComputeSAS derives the short authentication string both devices display.
// Both compute it independently from the ephemeral public key, so the relay is
// never trusted to report it honestly — it is echoed only for convenience.
func ComputeSAS(ephemeralPublicKey string) string {
	sum := sha256.Sum256([]byte("poweur/v1/enroll-sas\n" + ephemeralPublicKey))
	n := binary.BigEndian.Uint32(sum[:4]) % 1000000
	return fmt.Sprintf("%0*d", sasDigits, n)
}

func (s *Server) handleEnrollOffer(w http.ResponseWriter, r *http.Request) {
	identity := r.PathValue("identity")
	var req EnrollOfferRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return
	}
	if _, ok := s.identities.Get(identity); !ok {
		writeError(w, http.StatusNotFound, "not_found", "identity not found")
		return
	}
	key := strings.TrimSpace(req.EphemeralPublicKey)
	raw, err := decodeB64(key)
	if err != nil || len(raw) != 32 {
		writeError(w, http.StatusBadRequest, "invalid_request",
			"ephemeral_public_key must be a 32-byte base64url x25519 key")
		return
	}
	id, err := randomToken(16)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "rendezvous_failed", "failed to allocate rendezvous")
		return
	}
	expiresAt := time.Now().UTC().Add(enrollTTL)
	if err := s.rendezvous.Offer(storage.Rendezvous{
		ID:                 id,
		Identity:           strings.ToLower(identity),
		EphemeralPublicKey: key,
		Label:              trimLabel(req.Label),
		CreatedAt:          time.Now().UTC(),
		ExpiresAt:          expiresAt,
	}); err != nil {
		if errors.Is(err, storage.ErrTooManyOffers) {
			writeError(w, http.StatusTooManyRequests, "too_many_offers",
				"this identity already has the maximum number of open enrollment offers")
			return
		}
		writeError(w, http.StatusInternalServerError, "rendezvous_failed", "failed to store offer")
		return
	}
	writeJSON(w, http.StatusCreated, EnrollOfferResponse{
		RendezvousID: id,
		SAS:          ComputeSAS(key),
		ExpiresAt:    expiresAt.Format(time.RFC3339),
	})
}

func (s *Server) handleEnrollFetch(w http.ResponseWriter, r *http.Request) {
	identity := r.PathValue("identity")
	rid := strings.TrimSpace(r.PathValue("rendezvous"))
	var req EnrollFetchRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return
	}
	ident, ok := s.verifyEnrollAdmin(w, identity, "enroll-fetch", rid, req.IssuedAt, req.Nonce, req.IdentitySignature)
	if !ok {
		return
	}
	_ = ident
	entry, err := s.rendezvous.Get(strings.ToLower(identity), rid)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "rendezvous not found or expired")
		return
	}
	writeJSON(w, http.StatusOK, EnrollFetchResponse{
		RendezvousID:       entry.ID,
		EphemeralPublicKey: entry.EphemeralPublicKey,
		SAS:                ComputeSAS(entry.EphemeralPublicKey),
		Label:              entry.Label,
		ExpiresAt:          entry.ExpiresAt.Format(time.RFC3339),
	})
}

func (s *Server) handleEnrollDeliver(w http.ResponseWriter, r *http.Request) {
	identity := r.PathValue("identity")
	rid := strings.TrimSpace(r.PathValue("rendezvous"))
	var req EnrollDeliverRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return
	}
	if strings.TrimSpace(req.Sealed) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "sealed payload is required")
		return
	}
	if _, ok := s.verifyEnrollAdmin(w, identity, "enroll-deliver", rid, req.IssuedAt, req.Nonce, req.IdentitySignature); !ok {
		return
	}
	if err := s.rendezvous.Deliver(strings.ToLower(identity), rid, req.Sealed); err != nil {
		if errors.Is(err, storage.ErrRendezvousNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "rendezvous not found or expired")
			return
		}
		writeError(w, http.StatusConflict, "already_delivered", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleEnrollClaim is polled by the new device. The rendezvous id is a bearer
// token, which is safe: it releases only ciphertext that requires the ephemeral
// private key the new device never shared.
func (s *Server) handleEnrollClaim(w http.ResponseWriter, r *http.Request) {
	identity := r.PathValue("identity")
	rid := strings.TrimSpace(r.PathValue("rendezvous"))
	if _, ok := s.identities.Get(identity); !ok {
		writeError(w, http.StatusNotFound, "not_found", "identity not found")
		return
	}
	sealed, err := s.rendezvous.Claim(strings.ToLower(identity), rid)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "rendezvous not found or expired")
		return
	}
	if sealed == "" {
		writeJSON(w, http.StatusOK, EnrollClaimResponse{Ready: false})
		return
	}
	writeJSON(w, http.StatusOK, EnrollClaimResponse{Ready: true, Sealed: sealed})
}

// handleEnrollCancel abandons an offer. Authenticated by the rendezvous id as
// a bearer token — the same reasoning as claim: it is 16 random bytes known
// only to the new device and whoever the user showed the code to. Without
// this an abandoned ceremony would occupy a slot until it expired, and a user
// who mistyped once would be locked out for the TTL.
func (s *Server) handleEnrollCancel(w http.ResponseWriter, r *http.Request) {
	identity := r.PathValue("identity")
	rid := strings.TrimSpace(r.PathValue("rendezvous"))
	if _, ok := s.identities.Get(identity); !ok {
		writeError(w, http.StatusNotFound, "not_found", "identity not found")
		return
	}
	s.rendezvous.Cancel(strings.ToLower(identity), rid)
	w.WriteHeader(http.StatusNoContent)
}

// verifyEnrollAdmin checks an identity-signed envelope binding the action to
// this rendezvous, so an approval for one offer cannot be replayed onto another.
func (s *Server) verifyEnrollAdmin(
	w http.ResponseWriter, identity, action, rid, issuedAt, nonce, signature string,
) (storage.Identity, bool) {
	if issuedAt == "" || nonce == "" || signature == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "missing identity-signed envelope")
		return storage.Identity{}, false
	}
	if err := requireRecentTimestamp(issuedAt); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return storage.Identity{}, false
	}
	ident, ok := s.identities.Get(identity)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "identity not found")
		return storage.Identity{}, false
	}
	canonical := crypto.CanonicalEnrollAction(action, strings.ToLower(identity), rid, issuedAt, nonce)
	if err := crypto.VerifySignature(ident.PublicKeyBytes, canonical, signature); err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "signature invalid")
		return storage.Identity{}, false
	}
	if tw, ok := w.(*responseTelemetry); ok {
		tw.state.actor = strings.ToLower(identity)
		tw.state.direct = true
	}
	return ident, true
}

func trimLabel(label string) string {
	label = strings.TrimSpace(label)
	if len(label) > 64 {
		return label[:64]
	}
	return label
}
