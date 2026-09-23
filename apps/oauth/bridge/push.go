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
)

// handlePush sends the pending request to the user's app. It is a
// notification only: approving still takes the code on this screen, and the
// recipient's relay drops it unless they listed this bridge.
//
// The page calls it with Accept: application/json and gets
// {"notice": sent|too-soon|too-many|failed|closed, "left": n}; a plain form
// post is sent back to the page.
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
	now := s.now()
	id := t.ID
	sent := t.Pushes
	back := func(notice string) {
		if strings.Contains(r.Header.Get("Accept"), "application/json") {
			if notice == "not-pending" {
				notice = "closed"
			}
			writeJSON(w, http.StatusOK, map[string]any{"notice": notice, "left": max(0, maxPushesPerTxn-sent)})
			return
		}
		http.Redirect(w, r, "/t/"+id+"?push="+url.QueryEscape(notice), http.StatusSeeOther)
	}
	var send bool
	t, err = s.store.UpdateTxn(r.Context(), id, func(t *Txn) error {
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
		t.Pushes++
		t.LastPush = now
		sent = t.Pushes
		send = true
		return nil
	})
	if err != nil {
		switch err.Error() {
		case "not-pending", "too-many", "too-soon":
			back(err.Error())
		default:
			s.txnError(w, r, err)
		}
		return
	}
	if !send {
		back("too-soon")
		return
	}
	payload := identity.AuthRequestPayload{
		Version:   1,
		Request:   t.Request,
		ExpiresAt: t.RequestExpires.UTC().Format(time.RFC3339),
	}
	if t.Authorize != nil {
		payload.Client = t.Authorize.ClientName
		if u, err := url.Parse(t.Authorize.RedirectURI); err == nil {
			payload.ClientHost = u.Hostname()
		}
	}
	// The request's own expiry string is what the payload must repeat.
	if req, err := identity.DecodeSignInRequest(t.Request); err == nil {
		payload.ExpiresAt = req.ExpiresAt
	}
	body, err := json.Marshal(payload)
	if err != nil {
		back("failed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if err := s.cfg.Pusher.Push(ctx, t.Identity, body, t.RequestExpires); err != nil {
		s.log.Warn("push sign-in prompt", "txn", shortID(t.ID), "err", err)
		s.audit(r.Context(), "push.failed", map[string]any{"txn": shortID(t.ID)})
		back("failed")
		return
	}
	s.audit(r.Context(), "push.sent", map[string]any{"txn": shortID(t.ID)})
	back("sent")
}
