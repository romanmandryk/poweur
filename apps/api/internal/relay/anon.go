package relay

import (
	"crypto/rand"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/poweur/api/internal/storage"
	idpkg "github.com/poweur/identity"
)

// newPowSecret mints the relay's challenge-sealing key. Memory-only: a
// restart invalidates outstanding challenges (clients re-request), exactly
// like sessions and DAV tokens.
func newPowSecret() []byte {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		panic("cannot generate pow secret: " + err.Error())
	}
	return secret
}

// Anonymous messaging (EPIC-014 E14-T3). Unsigned senders are opt-in per
// recipient (inbox-policy `anonymous` block, default deny), pay the
// recipient's challenge (v1: proof-of-work), and land in a dedicated anon
// queue — never the signed inbox. PoW rate-limits; it does not
// authenticate: hard caps and per-IP limits stay on regardless.

const (
	anonChallengeTTL = 10 * time.Minute
	// anonLoadPerMinute is the relay-wide challenge-issuance rate above
	// which the effective difficulty floor auto-raises (Hashcash answer to
	// distributed flooding).
	anonLoadPerMinute = 120
	anonLoadRaiseBits = 4
)

// anonState is the relay's anonymous-ingress bookkeeping. Memory-only:
// a restart invalidates outstanding challenges (clients just re-request).
type anonState struct {
	mu sync.Mutex
	// seen marks consumed challenge nonces until token expiry (single-use).
	seen map[string]time.Time
	// daily counts accepted anon messages per recipient per UTC day.
	daily map[string]int
	// load is the sliding-minute challenge issuance counter.
	loadWindow time.Time
	loadCount  int
}

func newAnonState() *anonState {
	return &anonState{seen: map[string]time.Time{}, daily: map[string]int{}}
}

// noteChallenge counts an issuance and reports whether the relay is under
// anon load (difficulty floor raise).
func (a *anonState) noteChallenge() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	if now.Sub(a.loadWindow) > time.Minute {
		a.loadWindow = now
		a.loadCount = 0
	}
	a.loadCount++
	return a.loadCount > anonLoadPerMinute
}

// consumeNonce enforces single-use solutions; reports false on replay.
func (a *anonState) consumeNonce(nonce string, expiresAt time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, used := a.seen[nonce]; used {
		return false
	}
	a.seen[nonce] = expiresAt
	return true
}

// countDaily increments and checks the recipient's daily acceptance cap.
func (a *anonState) countDaily(recipient string, max int) bool {
	key := strings.ToLower(recipient) + ":" + time.Now().UTC().Format("2006-01-02")
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.daily[key] >= max {
		return false
	}
	a.daily[key]++
	return true
}

// prune drops expired nonces and stale daily counters.
func (a *anonState) prune() {
	now := time.Now()
	today := now.UTC().Format("2006-01-02")
	yesterday := now.UTC().AddDate(0, 0, -1).Format("2006-01-02")
	a.mu.Lock()
	defer a.mu.Unlock()
	for nonce, exp := range a.seen {
		if now.After(exp) {
			delete(a.seen, nonce)
		}
	}
	for key := range a.daily {
		if !strings.HasSuffix(key, today) && !strings.HasSuffix(key, yesterday) {
			delete(a.daily, key)
		}
	}
}

// anonPurpose binds a challenge token to one recipient's message surface.
func anonPurpose(recipient string) string {
	return "msg:" + strings.ToLower(recipient)
}

// writeChallengeRequired answers 428 with the typed challenge envelope.
func (s *Server) writeChallengeRequired(w http.ResponseWriter, recipient string, anon idpkg.AnonymousPolicy) {
	switch anon.EffectiveChallenge() {
	case idpkg.AnonChallengePow:
		bits := idpkg.ClampPowBits(anon.PowBits)
		if s.anon.noteChallenge() {
			bits = min(idpkg.PowMaxBits, bits+anonLoadRaiseBits)
		}
		token, challenge, err := idpkg.NewPowChallenge(s.powSecret, anonPurpose(recipient), bits, anonChallengeTTL)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "challenge_failed", "failed to mint challenge")
			return
		}
		writeJSON(w, http.StatusPreconditionRequired, map[string]any{
			"error": "challenge_required",
			"challenge": map[string]any{
				"type":       idpkg.AnonChallengePow,
				"algo":       idpkg.PowAlgo,
				"token":      token,
				"bits":       challenge.Bits,
				"expires_at": time.Unix(challenge.ExpiresAt, 0).UTC().Format(time.RFC3339),
			},
		})
	default:
		// verified / payment: designed slots — clients get the typed
		// envelope and can explain; solutions are not accepted in v1.
		writeJSON(w, http.StatusPreconditionRequired, map[string]any{
			"error": "challenge_required",
			"challenge": map[string]any{
				"type":   anon.EffectiveChallenge(),
				"detail": "this challenge type is not implemented yet",
			},
		})
	}
}

// handleAnonMessage processes an unsigned message (Sender and Signature
// empty). Caller has already decoded the envelope.
func (s *Server) handleAnonMessage(w http.ResponseWriter, r *http.Request, msg Message) {
	if msg.ID == "" || msg.Recipient == "" || msg.Timestamp == "" || msg.Payload == "" {
		writeError(w, http.StatusBadRequest, "invalid_message", "missing required message fields (id, recipient, timestamp, payload)")
		return
	}
	if _, err := time.Parse(time.RFC3339, msg.Timestamp); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_message", "timestamp must be RFC3339")
		return
	}
	// Encrypt-only holds for anonymous senders too: an ephemeral X25519
	// key needs no identity, so there is no plaintext excuse.
	if msg.Encryption == nil || msg.Encryption.Alg == "" ||
		msg.Encryption.EphemeralPublicKey == "" || msg.Encryption.Nonce == "" {
		writeError(w, http.StatusBadRequest, "encryption_required",
			"messages must be end-to-end encrypted (alg, ephemeral_public_key, nonce required)")
		return
	}
	if !s.isLocalIdentity(r.Context(), msg.Recipient) {
		writeError(w, http.StatusForbidden, "not_authorized", "anonymous messages are only accepted for locally hosted recipients")
		return
	}

	// Per-IP rate limit before any policy work.
	ip := r.RemoteAddr
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	if decision := s.rateLimit.Allow("anonip:" + ip); !decision.Allowed {
		writeError(w, http.StatusTooManyRequests, "rate_limit_exceeded", "too many anonymous messages from this address")
		return
	}

	policy, _ := s.recipientPolicy(r.Context(), msg.Recipient)
	if policy.Anonymous == nil || !policy.Anonymous.Allow {
		writeError(w, http.StatusForbidden, "policy_rejected", "recipient does not accept anonymous messages")
		return
	}
	anon := *policy.Anonymous

	if len(msg.Payload) > anon.EffectiveMaxBytes() {
		writeError(w, http.StatusRequestEntityTooLarge, "anon_too_large", "anonymous message exceeds the recipient's size limit")
		return
	}

	switch anon.EffectiveChallenge() {
	case idpkg.AnonChallengeNone:
		// no challenge required
	case idpkg.AnonChallengePow:
		if msg.ChallengeToken == "" || msg.ChallengeSolution == "" {
			s.writeChallengeRequired(w, msg.Recipient, anon)
			return
		}
		challenge, err := idpkg.VerifyPowSolution(s.powSecret, msg.ChallengeToken, msg.ChallengeSolution, anonPurpose(msg.Recipient))
		if err != nil {
			writeError(w, http.StatusForbidden, "challenge_failed", err.Error())
			return
		}
		if !s.anon.consumeNonce(challenge.Nonce, time.Unix(challenge.ExpiresAt, 0)) {
			writeError(w, http.StatusForbidden, "challenge_replayed", "challenge solution already used")
			return
		}
	default:
		if msg.ChallengeToken != "" || msg.ChallengeSolution != "" {
			writeError(w, http.StatusNotImplemented, "not_implemented",
				"the recipient requires a "+anon.EffectiveChallenge()+" challenge, which this relay cannot verify yet")
			return
		}
		s.writeChallengeRequired(w, msg.Recipient, anon)
		return
	}

	if !s.anon.countDaily(msg.Recipient, anon.EffectiveMaxPerDay()) {
		writeError(w, http.StatusTooManyRequests, "anon_daily_cap", "recipient's daily anonymous message cap reached")
		return
	}
	if !s.anonInbox.Add(msg.Recipient, storedFromMessage(msg), s.cfg.MaxInboxPerIdentity) {
		writeError(w, http.StatusServiceUnavailable, "inbox_full", "recipient anonymous queue is full")
		return
	}
	// An accepted anonymous message is a delivery like any other, and its own
	// event kind because it belongs to a different queue: told "message", a
	// client would fetch the inbox, find nothing, and leave the anon queue
	// unread until something else happened to make it look. The event says
	// nothing about the sender — there is nothing to say.
	s.notify(msg.Recipient, "anon", msg.ID)
	writeJSON(w, http.StatusAccepted, map[string]string{"id": msg.ID, "status": "anon_accepted"})
}

// registrationPurpose binds PoW tokens to the hosted-registration surface.
const registrationPurpose = "registration"

// handleAuthPow mints a proof-of-work challenge for surfaces that are not
// message-recipient-bound (E14-T5; today: hosted registration under
// REGISTRATION_GATE=pow).
func (s *Server) handleAuthPow(w http.ResponseWriter, r *http.Request) {
	purpose := r.URL.Query().Get("purpose")
	if purpose != registrationPurpose {
		writeError(w, http.StatusBadRequest, "invalid_request", "purpose must be \"registration\"")
		return
	}
	if s.regGate.Mode() != RegistrationGatePow {
		writeError(w, http.StatusConflict, "pow_not_required", "this relay does not gate registration with proof-of-work")
		return
	}
	bits := idpkg.ClampPowBits(s.cfg.RegistrationPowBits)
	if s.anon.noteChallenge() {
		bits = min(idpkg.PowMaxBits, bits+anonLoadRaiseBits)
	}
	token, challenge, err := idpkg.NewPowChallenge(s.powSecret, registrationPurpose, bits, anonChallengeTTL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "challenge_failed", "failed to mint challenge")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"type":       idpkg.AnonChallengePow,
		"algo":       idpkg.PowAlgo,
		"token":      token,
		"bits":       challenge.Bits,
		"expires_at": time.Unix(challenge.ExpiresAt, 0).UTC().Format(time.RFC3339),
	})
}

// checkRegistrationPow enforces REGISTRATION_GATE=pow for a hosted
// registration request. Returns false with the response written on denial.
func (s *Server) checkRegistrationPow(w http.ResponseWriter, req IdentityRequest) bool {
	if req.PowToken == "" || req.PowSolution == "" {
		writeError(w, http.StatusUnauthorized, "pow_required",
			"hosted registration requires a solved proof-of-work challenge (GET /auth/pow?purpose=registration)")
		return false
	}
	challenge, err := idpkg.VerifyPowSolution(s.powSecret, req.PowToken, req.PowSolution, registrationPurpose)
	if err != nil {
		writeError(w, http.StatusForbidden, "pow_invalid", err.Error())
		return false
	}
	if !s.anon.consumeNonce(challenge.Nonce, time.Unix(challenge.ExpiresAt, 0)) {
		writeError(w, http.StatusForbidden, "pow_replayed", "challenge solution already used")
		return false
	}
	return true
}

// handleAnonGet drains the recipient's anonymous queue (challenge-signed
// by the owner, like /messages and /requests).
func (s *Server) handleAnonGet(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.authChallengeSignedGet(w, r)
	if !ok {
		return
	}
	messages := s.anonInbox.Drain(identity)
	if messages == nil {
		messages = []storage.StoredMessage{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": messages})
}
