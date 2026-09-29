package relay

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/poweur/api/internal/drive/engine"
	idpkg "github.com/poweur/identity"
)

// Link access (E20-T7). A link holder authenticates with the link ID in
// X-Poweur-Link and, for a password-protected link, the password verifier
// in X-Poweur-Link-Verifier (base64url). The decryption key stays in the
// URL fragment, so neither header nor log ever carries it. Opening a link
// (listing its share) counts against its download cap; each link is also
// rate-limited per hour by its own cap and, separately, in wrong-password
// attempts.

const (
	linkHeader         = "X-Poweur-Link"
	linkVerifierHeader = "X-Poweur-Link-Verifier"
	// maxLinkPasswordFailures bounds wrong-password attempts per link per hour.
	maxLinkPasswordFailures = 20
)

// linkLimiter is a fixed one-hour window per key.
type linkLimiter struct {
	mu     sync.Mutex
	counts map[string]*linkWindow
}

type linkWindow struct {
	start time.Time
	n     uint64
}

func newLinkLimiter() *linkLimiter { return &linkLimiter{counts: map[string]*linkWindow{}} }

// allow counts one use of key and reports whether it stays within limit per
// hour (0 = unlimited).
func (l *linkLimiter) allow(key string, limit uint64, now time.Time) bool {
	if limit == 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.counts[key]
	if w == nil || now.Sub(w.start) >= time.Hour {
		if len(l.counts) > 100000 {
			l.counts = map[string]*linkWindow{}
		}
		w = &linkWindow{start: now}
		l.counts[key] = w
	}
	if w.n >= limit {
		return false
	}
	w.n++
	return true
}

// note counts one event against key without a limit (wrong passwords).
func (l *linkLimiter) note(key string, now time.Time) {
	l.allow(key, ^uint64(0), now)
}

func (l *linkLimiter) exhausted(key string, limit uint64, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.counts[key]
	return w != nil && now.Sub(w.start) < time.Hour && w.n >= limit
}

// linkCaller authenticates a link holder for driveID. count marks an open.
func (s *Server) linkCaller(w http.ResponseWriter, r *http.Request, driveID string, count bool) (string, bool) {
	linkID := strings.TrimSpace(r.Header.Get(linkHeader))
	info, err := s.engine.Link(r.Context(), driveID, linkID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "no such link")
		return "", false
	}
	now := time.Now()
	failKey := driveID + "/" + linkID
	if info.Password && s.linkFailures.exhausted(failKey, maxLinkPasswordFailures, now) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many wrong passwords for this link; try again later")
		return "", false
	}
	var verifier []byte
	if raw := r.Header.Get(linkVerifierHeader); raw != "" {
		if verifier, err = base64.RawURLEncoding.DecodeString(raw); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "link verifier must be base64url")
			return "", false
		}
	}
	if err := s.engine.OpenLink(r.Context(), driveID, linkID, verifier, count); err != nil {
		if errors.Is(err, engine.ErrForbidden) {
			s.linkFailures.note(failKey, now)
			writeError(w, http.StatusUnauthorized, "link_password", "wrong or missing link password")
			return "", false
		}
		s.writeDriveError(w, err)
		return "", false
	}
	if !s.linkUses.allow(failKey, s.linkRate(r, driveID, linkID), now) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "this link's hourly limit is reached")
		return "", false
	}
	return engine.LinkActor(linkID), true
}

// linkRate is the link's own per-hour cap.
func (s *Server) linkRate(r *http.Request, driveID, linkID string) uint64 {
	shares, err := s.engine.Shares(r.Context(), driveID, engine.LinkActor(linkID))
	if err != nil || len(shares) == 0 {
		return 0
	}
	return shares[0].Caps.PerHour
}

// handleDriveLink tells a link holder how to open it: role, expiry, the
// password salt and KDF, and the proof-of-work difficulty for writes. It
// reveals nothing about the drive's content.
func (s *Server) handleDriveLink(w http.ResponseWriter, r *http.Request) {
	if s.engine == nil {
		writeError(w, http.StatusServiceUnavailable, "drive_unavailable", "this relay has no drive storage")
		return
	}
	driveID := strings.ToLower(strings.TrimSpace(r.PathValue("identity")))
	if !s.identities.Exists(driveID) {
		writeError(w, http.StatusNotFound, "not_found", "no such link")
		return
	}
	linkID := r.PathValue("link")
	info, err := s.engine.Link(r.Context(), driveID, linkID)
	if err != nil {
		// A link whose subtree moved to another drive follows it.
		if fwd, moved, ferr := s.engine.Forwarded(r.Context(), driveID, linkID); ferr == nil && moved {
			target := "/drive/" + fwd.Drive + "/links/" + linkID
			if !s.identities.Exists(fwd.Drive) {
				target = s.cfg.RelayScheme + "://" + fwd.Drive + target
			}
			w.Header().Set("Referrer-Policy", "no-referrer")
			http.Redirect(w, r, target, http.StatusPermanentRedirect)
			return
		}
		writeError(w, http.StatusNotFound, "not_found", "no such link")
		return
	}
	noStore(w)
	w.Header().Set("Referrer-Policy", "no-referrer")
	writeJSON(w, http.StatusOK, struct {
		Drive string `json:"drive"`
		engine.LinkInfo
	}{driveID, info})
}

// handleDriveLinkStats exposes no capability secret or recipient data; only
// the drive owner may inspect the durable open counter.
func (s *Server) handleDriveLinkStats(w http.ResponseWriter, r *http.Request) {
	driveID, actor, ok := s.driveCaller(w, r)
	if !ok {
		return
	}
	if !engine.IsOwner(driveID, actor) {
		writeError(w, http.StatusForbidden, "forbidden", "only the drive owner sees link statistics")
		return
	}
	stats, err := s.engine.LinkStats(r.Context(), driveID, r.PathValue("link"))
	if err != nil {
		s.writeDriveError(w, err)
		return
	}
	noStore(w)
	writeJSON(w, http.StatusOK, stats)
}

// linkPowPurpose binds a proof-of-work to one link of one drive.
func linkPowPurpose(driveID, linkID string) string { return "drive-link:" + driveID + "/" + linkID }

// linkProofOfWork requires a fresh proof-of-work on a link write when the
// link's owner asked for one (X-Poweur-PoW-Token, X-Poweur-PoW-Solution;
// challenges from GET /auth/pow?purpose=drive-link&identity=&link=).
func (s *Server) linkProofOfWork(w http.ResponseWriter, r *http.Request, driveID, linkID string) bool {
	info, err := s.engine.Link(r.Context(), driveID, linkID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "no such link")
		return false
	}
	if info.PoW == 0 {
		return true
	}
	token, solution := r.Header.Get("X-Poweur-PoW-Token"), r.Header.Get("X-Poweur-PoW-Solution")
	if token == "" || solution == "" {
		writeError(w, http.StatusForbidden, "pow_required", fmt.Sprintf("this link requires proof-of-work (%d bits)", info.PoW))
		return false
	}
	challenge, err := idpkg.VerifyPowSolution(s.powSecret, token, solution, linkPowPurpose(driveID, linkID))
	if err != nil || uint64(challenge.Bits) < info.PoW {
		writeError(w, http.StatusForbidden, "pow_failed", "proof-of-work does not meet this link's difficulty")
		return false
	}
	if !s.anon.consumeNonce(challenge.Nonce, time.Unix(challenge.ExpiresAt, 0)) {
		writeError(w, http.StatusForbidden, "pow_replayed", "proof-of-work already used")
		return false
	}
	return true
}

// issueLinkPow mints a challenge at a link's difficulty.
func (s *Server) issueLinkPow(w http.ResponseWriter, r *http.Request) {
	if s.engine == nil {
		writeError(w, http.StatusServiceUnavailable, "drive_unavailable", "this relay has no drive storage")
		return
	}
	driveID := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("identity")))
	linkID := r.URL.Query().Get("link")
	info, err := s.engine.Link(r.Context(), driveID, linkID)
	if err != nil || !s.identities.Exists(driveID) {
		writeError(w, http.StatusNotFound, "not_found", "no such link")
		return
	}
	if info.PoW == 0 {
		writeError(w, http.StatusConflict, "pow_not_required", "this link does not require proof-of-work")
		return
	}
	token, challenge, err := idpkg.NewPowChallenge(s.powSecret, linkPowPurpose(driveID, linkID), int(info.PoW), anonChallengeTTL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "challenge_failed", "failed to mint challenge")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"type": idpkg.AnonChallengePow, "algo": idpkg.PowAlgo, "token": token, "bits": challenge.Bits,
		"expires_at": time.Unix(challenge.ExpiresAt, 0).UTC().Format(time.RFC3339),
	})
}
