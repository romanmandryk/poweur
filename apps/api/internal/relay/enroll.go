package relay

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/poweur/api/internal/crypto"
	"github.com/poweur/api/internal/storage"
	idpkg "github.com/poweur/identity"
)

// New-device pairing (EPIC-011 E11-T8, v2).
//
// Moving a seed to a new device needs an authentic channel, not a secret one,
// and the relay is not trusted for either: it must never read the seed, and a
// compromised relay must not be able to pair a device of its own.
//
// v1 had the new device publish an ephemeral key and both screens show
// SHA-256(key) mod 10^6. The relay sees the key, so it could grind one of its
// own with the same six digits in seconds and receive the seed. v2 is
// commit-then-reveal (packages/identity/pairing.go):
//
//	new device  → offer {commitment}           ← code, claim token
//	approver    → fetch {approver_nonce}       (identity-signed)
//	new device  ← poll  {state: nonce}  → reveal {key, commit_nonce}
//	approver    → fetch                        ← key, commit_nonce; checks the commitment
//	approver    → deliver {sealed seed}        (identity-signed)
//	new device  ← poll  {sealed}
//
// The digits depend on the approver's nonce, which did not exist when any key
// was committed; a scanned QR carries the commitment itself and needs no
// digits. This relay computes no digits: it could only be believed if trusted.
//
// The relay is a blind letterbox throughout: it holds a commitment, two
// nonces, an ephemeral public key and a sealed blob, and can open none.

// enrollTTL bounds an offer's life. Long enough to type a code, short enough
// that an abandoned offer disappears quickly.
const enrollTTL = 10 * time.Minute

// EnrollOfferRequest is posted by the *new* device. It is unauthenticated —
// the device has no key yet, which is the whole point — so it is rate-limited
// and capped per identity instead.
type EnrollOfferRequest struct {
	Commitment string `json:"commitment"`
	// Label is shown to the approving user ("Firefox on Linux").
	Label string `json:"label,omitempty"`
	// EphemeralPublicKey is v1's field, refused with an explanation.
	EphemeralPublicKey string `json:"ephemeral_public_key,omitempty"`
}

type EnrollOfferResponse struct {
	RendezvousID string `json:"rendezvous_id"`
	// ClaimToken authenticates the new device's reveal, poll and cancel.
	ClaimToken string `json:"claim_token"`
	ExpiresAt  string `json:"expires_at"`
}

// EnrollFetchRequest is the approving device asking what it is approving,
// and contributing its nonce (first call; later calls repeat it).
type EnrollFetchRequest struct {
	ApproverNonce     string `json:"approver_nonce"`
	Mode              string `json:"mode,omitempty"` // scan | compare
	IssuedAt          string `json:"issued_at"`
	Nonce             string `json:"nonce"`
	IdentitySignature string `json:"identity_signature"`
}

type EnrollFetchResponse struct {
	RendezvousID       string `json:"rendezvous_id"`
	State              string `json:"state"`
	Commitment         string `json:"commitment"`
	Label              string `json:"label,omitempty"`
	ExpiresAt          string `json:"expires_at"`
	EphemeralPublicKey string `json:"ephemeral_public_key,omitempty"`
	CommitNonce        string `json:"commit_nonce,omitempty"`
}

// EnrollRevealRequest opens the commitment, from the new device.
type EnrollRevealRequest struct {
	EphemeralPublicKey string `json:"ephemeral_public_key"`
	CommitNonce        string `json:"commit_nonce"`
}

// EnrollDeliverRequest carries the sealed seed from the approving device.
type EnrollDeliverRequest struct {
	Sealed            string `json:"sealed"`
	IssuedAt          string `json:"issued_at"`
	Nonce             string `json:"nonce"`
	IdentitySignature string `json:"identity_signature"`
}

// EnrollPollResponse is what the new device polls.
type EnrollPollResponse struct {
	State         string `json:"state"`
	ApproverNonce string `json:"approver_nonce,omitempty"`
	Mode          string `json:"mode,omitempty"`
	Sealed        string `json:"sealed,omitempty"`
	// Ready is v1's field, kept true once sealed is present.
	Ready bool `json:"ready"`
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
	if req.Commitment == "" && req.EphemeralPublicKey != "" {
		writeError(w, http.StatusBadRequest, "pairing_v1",
			"this app pairs devices the old way, which a compromised relay could intercept; update it")
		return
	}
	if err := idpkg.CheckPairingCommitment(strings.TrimSpace(req.Commitment)); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "commitment must be a 32-byte base64url SHA-256")
		return
	}
	token, err := randomToken(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "rendezvous_failed", "failed to allocate rendezvous")
		return
	}
	expiresAt := time.Now().UTC().Add(enrollTTL)
	for attempt := 0; ; attempt++ {
		code, err := idpkg.NewShortCode()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "rendezvous_failed", "failed to allocate rendezvous")
			return
		}
		err = s.rendezvous.Offer(storage.Rendezvous{
			ID:             code,
			Identity:       strings.ToLower(identity),
			Commitment:     strings.TrimSpace(req.Commitment),
			ClaimTokenHash: storage.HashClaimToken(token),
			Label:          trimLabel(req.Label),
			CreatedAt:      time.Now().UTC(),
			ExpiresAt:      expiresAt,
		})
		switch {
		case err == nil:
			writeJSON(w, http.StatusCreated, EnrollOfferResponse{
				RendezvousID: code, ClaimToken: token, ExpiresAt: expiresAt.Format(time.RFC3339),
			})
			return
		case errors.Is(err, storage.ErrCodeTaken) && attempt < 3:
			continue
		case errors.Is(err, storage.ErrTooManyOffers):
			writeError(w, http.StatusTooManyRequests, "too_many_offers",
				"this identity already has the maximum number of open enrollment offers")
			return
		default:
			writeError(w, http.StatusInternalServerError, "rendezvous_failed", "failed to store offer")
			return
		}
	}
}

// rendezvousCode reads the path's code the way a person typed it.
func rendezvousCode(r *http.Request) string {
	code, err := idpkg.NormalizeShortCode(r.PathValue("rendezvous"))
	if err != nil {
		return ""
	}
	return code
}

func (s *Server) handleEnrollFetch(w http.ResponseWriter, r *http.Request) {
	identity := r.PathValue("identity")
	rid := rendezvousCode(r)
	var req EnrollFetchRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return
	}
	if rid == "" {
		writeError(w, http.StatusNotFound, "not_found", "rendezvous not found or expired")
		return
	}
	if _, ok := s.verifyEnrollAdmin(w, identity, "enroll-fetch", rid, req.IssuedAt, req.Nonce, req.IdentitySignature); !ok {
		return
	}
	if err := checkB64Bytes(req.ApproverNonce, 32); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "approver_nonce must be 32 random bytes, base64url")
		return
	}
	mode := req.Mode
	if mode != "scan" {
		mode = "compare"
	}
	entry, err := s.rendezvous.Approve(strings.ToLower(identity), rid, req.ApproverNonce, mode)
	switch {
	case errors.Is(err, storage.ErrRendezvousState):
		writeError(w, http.StatusConflict, "already_approving", "another device is already approving this request")
		return
	case err != nil:
		writeError(w, http.StatusNotFound, "not_found", "rendezvous not found or expired")
		return
	}
	writeJSON(w, http.StatusOK, EnrollFetchResponse{
		RendezvousID:       entry.ID,
		State:              entry.State(),
		Commitment:         entry.Commitment,
		Label:              entry.Label,
		ExpiresAt:          entry.ExpiresAt.Format(time.RFC3339),
		EphemeralPublicKey: entry.EphemeralPublicKey,
		CommitNonce:        entry.CommitNonce,
	})
}

func claimToken(r *http.Request) string {
	return strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
}

func (s *Server) handleEnrollReveal(w http.ResponseWriter, r *http.Request) {
	identity := strings.ToLower(r.PathValue("identity"))
	rid := rendezvousCode(r)
	var req EnrollRevealRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return
	}
	entry, err := s.rendezvous.Get(identity, rid)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "rendezvous not found or expired")
		return
	}
	// Checking here only saves an honest client a round trip: the approver
	// checks again, because it cannot trust this relay to have.
	if err := idpkg.VerifyPairingReveal(entry.Commitment, req.EphemeralPublicKey, req.CommitNonce); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	switch err := s.rendezvous.Reveal(identity, rid, claimToken(r), req.EphemeralPublicKey, req.CommitNonce); {
	case errors.Is(err, storage.ErrClaimToken):
		writeError(w, http.StatusUnauthorized, "unauthorized", "claim token does not match this request")
	case errors.Is(err, storage.ErrRendezvousState):
		writeError(w, http.StatusConflict, "out_of_order", "reveal only after the other device has answered, and once")
	case err != nil:
		writeError(w, http.StatusNotFound, "not_found", "rendezvous not found or expired")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) handleEnrollDeliver(w http.ResponseWriter, r *http.Request) {
	identity := r.PathValue("identity")
	rid := rendezvousCode(r)
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
	switch err := s.rendezvous.Deliver(strings.ToLower(identity), rid, req.Sealed); {
	case errors.Is(err, storage.ErrRendezvousState):
		writeError(w, http.StatusConflict, "out_of_order", "deliver once, after the new device has revealed its key")
	case err != nil:
		writeError(w, http.StatusNotFound, "not_found", "rendezvous not found or expired")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleEnrollPoll is the new device waiting: for the approver's nonce, then
// for the sealed seed, which it takes exactly once.
func (s *Server) handleEnrollPoll(w http.ResponseWriter, r *http.Request) {
	identity := strings.ToLower(r.PathValue("identity"))
	entry, err := s.rendezvous.Poll(identity, rendezvousCode(r), claimToken(r))
	switch {
	case errors.Is(err, storage.ErrClaimToken):
		writeError(w, http.StatusUnauthorized, "unauthorized", "claim token does not match this request")
		return
	case err != nil:
		writeError(w, http.StatusNotFound, "not_found", "rendezvous not found or expired")
		return
	}
	writeJSON(w, http.StatusOK, EnrollPollResponse{
		State: entry.State(), ApproverNonce: entry.ApproverNonce, Mode: entry.Mode,
		Sealed: entry.Sealed, Ready: entry.Delivered,
	})
}

// handleEnrollCancel abandons an offer, from the new device (claim token).
// Without it an abandoned ceremony would occupy a slot until it expired; the
// approving side simply stops, and the offer expires.
func (s *Server) handleEnrollCancel(w http.ResponseWriter, r *http.Request) {
	identity := strings.ToLower(r.PathValue("identity"))
	rid := rendezvousCode(r)
	if _, err := s.rendezvous.Poll(identity, rid, claimToken(r)); err != nil {
		writeError(w, http.StatusNotFound, "not_found", "rendezvous not found or expired")
		return
	}
	s.rendezvous.Cancel(identity, rid)
	w.WriteHeader(http.StatusNoContent)
}

func checkB64Bytes(v string, n int) error {
	raw, err := decodeB64(v)
	if err != nil || len(raw) != n {
		return errors.New("bad length")
	}
	return nil
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
