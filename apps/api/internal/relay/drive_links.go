package relay

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/poweur/api/internal/drive/engine"
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
	info, err := s.engine.Link(r.Context(), driveID, r.PathValue("link"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "no such link")
		return
	}
	noStore(w)
	w.Header().Set("Referrer-Policy", "no-referrer")
	writeJSON(w, http.StatusOK, info)
}
