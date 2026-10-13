package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/poweur/identity"
	"github.com/poweur/identity/signin"
)

// Pusher delivers a sign-in prompt to a Poweur ID as an encrypted
// `sys.auth.request` message sent from the bridge's own identity.
type Pusher interface {
	Push(ctx context.Context, to string, body []byte, expiresAt time.Time) error
}

// PusherFunc adapts a function.
type PusherFunc func(ctx context.Context, to string, body []byte, expiresAt time.Time) error

// Push implements Pusher.
func (f PusherFunc) Push(ctx context.Context, to string, body []byte, expiresAt time.Time) error {
	return f(ctx, to, body, expiresAt)
}

const (
	maxPushesPerTxn = 3
	pushSpacing     = 15 * time.Second
	// pushPause is how long a wrong pick on a pushed prompt stops pushes to
	// that identity (EPIC-028): the pick is the signal that someone else
	// started the sign-in, so it costs them the channel.
	pushPause = 15 * time.Minute
)

// handlePush sends the pending request to the user's app from the page that
// started the sign-in. The first push draws the number the starting screen
// shows and the app asks the user to pick (EPIC-028); the recipient's relay
// drops the prompt unless they listed this bridge.
//
// The page calls it with Accept: application/json and gets
// {"notice": sent|too-soon|too-many|paused|failed|closed, "left": n,
// "match": "42"}; a plain form post is sent back to the page.
func (s *Server) handlePush(w http.ResponseWriter, r *http.Request) {
	if !s.sameOriginPost(r) {
		s.renderError(w, r, http.StatusForbidden, "That request did not come from this site.")
		return
	}
	if s.cfg.Pusher == nil {
		s.renderError(w, r, http.StatusNotFound, "This service does not send sign-in requests to apps.")
		return
	}
	t, err := s.loadTxnForBrowser(r, r.PathValue("id"))
	if err != nil {
		s.txnError(w, r, err)
		return
	}
	notice, after, err := s.pushTxn(r.Context(), t)
	if err != nil {
		s.txnError(w, r, err)
		return
	}
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		if notice == "not-pending" {
			notice = "closed"
		}
		writeJSON(w, http.StatusOK, map[string]any{"notice": notice, "left": max(0, maxPushesPerTxn-after.Pushes), "match": after.Match})
		return
	}
	http.Redirect(w, r, "/t/"+t.ID+"?push="+url.QueryEscape(notice), http.StatusSeeOther)
}

// handleRequestPush is the same push asked for by a phone that opened the QR
// but holds no keys in that browser — an iPhone whose keys live in the
// home-screen app, which no link can open (EPIC-028). It is the context's
// push_uri. Anyone holding the QR may call it; it only ever produces a
// prompt that needs the number on the starting screen, under the same limits.
func (s *Server) handleRequestPush(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "no-store")
	if s.cfg.Pusher == nil {
		writeOAuthError(w, http.StatusNotFound, "invalid_request", "this service does not send sign-in requests to apps")
		return
	}
	t, err := s.txnByRequestCode(r)
	if err != nil {
		writeOAuthError(w, http.StatusGone, "expired_request", "this sign-in code expired or was already used")
		return
	}
	notice, after, err := s.pushTxn(r.Context(), t)
	if err != nil {
		writeOAuthError(w, http.StatusGone, "expired_request", "this sign-in code expired or was already used")
		return
	}
	if notice == "not-pending" {
		notice = "closed"
	}
	writeJSON(w, http.StatusOK, map[string]any{"notice": notice, "left": max(0, maxPushesPerTxn-after.Pushes)})
}

// pushURI is where a signer may ask for this transaction to be pushed.
func (s *Server) pushURI(t *Txn) string {
	if s.cfg.Pusher == nil || t.RequestCode == "" {
		return ""
	}
	return s.requestLink(t) + "/push"
}

// pushTxn sends one push for t, within the per-transaction limits and the
// identity's pause. It returns the notice for the user and the transaction
// as it now stands; err is only for a transaction that cannot be used.
func (s *Server) pushTxn(ctx context.Context, t *Txn) (string, *Txn, error) {
	now := s.now()
	if t.Identity != "" {
		if paused, err := s.store.PushesPaused(ctx, t.Identity, now); err == nil && paused {
			return "paused", t, nil
		}
	}
	var send bool
	after, err := s.store.UpdateTxn(ctx, t.ID, func(t *Txn) error {
		if err := t.usable(); err != nil {
			return err
		}
		switch {
		case t.RequestID == "" || t.Claimed || now.After(t.RequestExpires):
			return errors.New("not-pending")
		case t.Pushes >= maxPushesPerTxn:
			return errors.New("too-many")
		case !t.LastPush.IsZero() && now.Sub(t.LastPush) < pushSpacing:
			return errors.New("too-soon")
		}
		if t.Match == "" {
			code, choices, err := signin.NewMatchChoices()
			if err != nil {
				return err
			}
			t.Match, t.MatchChoices = code, choices
		}
		t.Pushes++
		t.LastPush = now
		send = true
		return nil
	})
	if err != nil {
		switch err.Error() {
		case "not-pending", "too-many", "too-soon":
			return err.Error(), t, nil
		}
		return "", nil, err
	}
	if !send {
		return "too-soon", after, nil
	}
	payload := identity.AuthRequestPayload{
		Version:   1,
		Request:   after.Request,
		ExpiresAt: after.RequestExpires.UTC().Format(time.RFC3339),
	}
	if after.Authorize != nil {
		payload.Client = after.Authorize.ClientName
		if u, err := url.Parse(after.Authorize.RedirectURI); err == nil {
			payload.ClientHost = u.Hostname()
		}
	}
	// The request's own expiry string is what the payload must repeat.
	if req, err := identity.DecodeSignInRequest(after.Request); err == nil {
		payload.ExpiresAt = req.ExpiresAt
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "failed", after, nil
	}
	pctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := s.cfg.Pusher.Push(pctx, after.Identity, body, after.RequestExpires); err != nil {
		s.log.Warn("push sign-in prompt", "txn", shortID(after.ID), "err", err)
		s.audit(ctx, "push.failed", map[string]any{"txn": shortID(after.ID)})
		return "failed", after, nil
	}
	s.audit(ctx, "push.sent", map[string]any{"txn": shortID(after.ID)})
	return "sent", after, nil
}
