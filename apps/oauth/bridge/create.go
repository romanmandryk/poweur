package bridge

import (
	"net/http"
	"time"
)

// Someone who reaches the bridge without a Poweur ID can create one at the
// launcher and come back. Creating an ID takes longer than a sign-in normally
// waits, so saying "I'm creating one" extends the transaction — up to a cap,
// so an abandoned tab does not keep state alive for long.
const (
	creatingWindow = 30 * time.Minute
	maxTxnLifetime = 60 * time.Minute
)

// launcherView is what the identify page needs to offer creation.
type launcherView struct {
	URL    string
	Domain string
	Host   string
}

func (s *Server) launcherView() *launcherView {
	if s.cfg.LauncherURL == "" {
		return nil
	}
	host := s.cfg.LauncherURL
	for _, prefix := range []string{"https://", "http://"} {
		if len(host) > len(prefix) && host[:len(prefix)] == prefix {
			host = host[len(prefix):]
		}
	}
	return &launcherView{URL: s.cfg.LauncherURL, Domain: s.cfg.LauncherDomain, Host: host}
}

func (s *Server) handleCreating(w http.ResponseWriter, r *http.Request) {
	if !s.sameOriginPost(r) {
		writeOAuthError(w, http.StatusForbidden, "invalid_request", "cross-site request")
		return
	}
	if s.cfg.LauncherURL == "" {
		writeOAuthError(w, http.StatusNotFound, "invalid_request", "identity creation is not offered here")
		return
	}
	t, err := s.loadTxnForBrowser(r, r.PathValue("id"))
	if err != nil {
		writeOAuthError(w, http.StatusNotFound, "invalid_request", "no such sign-in in this browser")
		return
	}
	now := s.now()
	t, err = s.store.UpdateTxn(r.Context(), t.ID, func(t *Txn) error {
		if err := t.usable(); err != nil {
			return err
		}
		want := now.Add(creatingWindow)
		if limit := t.CreatedAt.Add(maxTxnLifetime); want.After(limit) {
			want = limit
		}
		if want.After(t.ExpiresAt) {
			t.ExpiresAt = want
		}
		return nil
	})
	if err != nil {
		writeOAuthError(w, http.StatusConflict, "invalid_request", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"expires_at": t.ExpiresAt.UTC().Format(time.RFC3339)})
}
