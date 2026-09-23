package bridge

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/poweur/identity"
	"github.com/poweur/identity/signin"
)

// resumeWindow is how long a same-device approval waits for its browser.
const resumeWindow = time.Minute

var (
	errExpired = errors.New("this sign-in has expired")
	errClaimed = errors.New("this sign-in has already been answered")
)

// --- Native relying-party metadata ---------------------------------------------

func (s *Server) nativeMetadata() signin.Metadata {
	return signin.Metadata{
		PoweurAuth:   identity.SignInVersion,
		Origin:       s.cfg.Issuer,
		Name:         s.cfg.Name,
		ResponseURIs: []string{s.cfg.Issuer + "/poweur/callback"},
		Transports:   []string{"redirect", "qr", "deeplink"},
		ContactURI:   s.cfg.ContactURI,
		ContextURI:   s.cfg.Issuer + "/poweur/context",
	}
}

// handleNativeContext tells a signer where a pending sign-in was started.
// It answers only while the request can still be approved, and says nothing
// the holder of the request id could not already see.
func (s *Server) handleNativeContext(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	requestID := r.URL.Query().Get("request_id")
	id, err := s.store.TxnIDByRequest(r.Context(), requestID)
	if err != nil {
		writeOAuthError(w, http.StatusNotFound, "invalid_request", "no sign-in is waiting for this request")
		return
	}
	t, err := s.store.GetTxn(r.Context(), id)
	if err != nil || t.usable() != nil || t.Claimed || s.now().After(t.RequestExpires) {
		writeOAuthError(w, http.StatusNotFound, "invalid_request", "no sign-in is waiting for this request")
		return
	}
	c := signin.SignInContext{RequestID: requestID, StartedAt: t.CreatedAt, Browser: t.Browser}
	if t.Authorize != nil {
		c.Client = t.Authorize.ClientName
		if u, err := url.Parse(t.Authorize.RedirectURI); err == nil {
			c.ClientHost = u.Hostname()
		}
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handleNativeMetadata(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	writeJSON(w, http.StatusOK, s.nativeMetadata())
}

// --- Transactions ----------------------------------------------------------------

func (s *Server) newTxn(w http.ResponseWriter, r *http.Request, kind string) (*Txn, error) {
	binding, err := s.browserBinding(w, r)
	if err != nil {
		return nil, err
	}
	id, err := signin.NewSecret()
	if err != nil {
		return nil, err
	}
	now := s.now()
	return &Txn{
		ID:          id,
		Kind:        kind,
		CreatedAt:   now,
		ExpiresAt:   now.Add(s.cfg.TxnTTL),
		BindingHash: signin.HashSecret(binding),
		Browser:     summarizeUserAgent(r.UserAgent()),
	}, nil
}

// loadTxnForBrowser returns the transaction only to the browser that started it.
func (s *Server) loadTxnForBrowser(r *http.Request, id string) (*Txn, error) {
	t, err := s.store.GetTxn(r.Context(), id)
	if err != nil {
		return nil, err
	}
	if !signin.SecretMatches(t.BindingHash, s.cookieValue(r, bindingCookie)) {
		return nil, errWrongBrowse
	}
	return t, nil
}

// txnError renders the page for a transaction that cannot be used.
func (s *Server) txnError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		s.renderError(w, r, http.StatusGone, "This sign-in has expired or does not exist. Start again from the app you were signing in to.")
	case errors.Is(err, errWrongBrowse):
		s.renderError(w, r, http.StatusForbidden, "This sign-in was started in a different browser. If you did not start it yourself, someone may have sent you their sign-in link — nothing was signed in.")
	case errors.Is(err, errTxnDone):
		s.renderError(w, r, http.StatusGone, "This sign-in has already been completed. Start again from the app if you need to sign in again.")
	default:
		s.renderError(w, r, http.StatusBadRequest, err.Error())
	}
}

// localPath accepts only a path on this origin.
func localPath(raw, fallback string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || strings.ContainsAny(raw, "\\\r\n") {
		return fallback
	}
	u, err := url.Parse(raw)
	if err != nil || u.IsAbs() || u.Host != "" {
		return fallback
	}
	return u.RequestURI()
}

// handleLogin signs a browser in to the bridge itself.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	returnTo := localPath(r.URL.Query().Get("return_to"), "/account")
	if sess, _ := s.currentSession(r); sess != nil && r.URL.Query().Get("prompt") != "login" {
		http.Redirect(w, r, returnTo, http.StatusSeeOther)
		return
	}
	t, err := s.newTxn(w, r, KindLogin)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "Could not start signing in.")
		return
	}
	t.ReturnTo = returnTo
	t.LoginHint = r.URL.Query().Get("login_hint")
	if err := s.store.CreateTxn(r.Context(), t); err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "Could not start signing in.")
		return
	}
	http.Redirect(w, r, "/t/"+t.ID, http.StatusSeeOther)
}

// handleTxnPage shows the step a transaction is at.
func (s *Server) handleTxnPage(w http.ResponseWriter, r *http.Request) {
	t, err := s.loadTxnForBrowser(r, r.PathValue("id"))
	if err != nil {
		s.txnError(w, r, err)
		return
	}
	if err := t.usable(); err != nil {
		s.renderFailed(w, r, t)
		return
	}
	if t.Resumed {
		http.Redirect(w, r, "/t/"+t.ID+"/continue", http.StatusSeeOther)
		return
	}
	st := t.status(s.now())
	if st.Status == "identify" || r.URL.Query().Get("change") == "1" && !t.Claimed {
		s.renderIdentify(w, r, t, "", http.StatusOK)
		return
	}
	if st.Status == "expired" {
		s.renderIdentify(w, r, t, st.Error+" Enter your Poweur ID to try again.", http.StatusOK)
		return
	}
	s.renderAwait(w, r, t)
}

// handleIdentify takes the Poweur ID and issues the native request.
func (s *Server) handleIdentify(w http.ResponseWriter, r *http.Request) {
	if !s.sameOriginPost(r) {
		s.renderError(w, r, http.StatusForbidden, "That request did not come from this site.")
		return
	}
	t, err := s.loadTxnForBrowser(r, r.PathValue("id"))
	if err != nil {
		s.txnError(w, r, err)
		return
	}
	if err := t.usable(); err != nil {
		s.renderFailed(w, r, t)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderIdentify(w, r, t, "That form could not be read.", http.StatusBadRequest)
		return
	}
	id, err := identity.NormalizeIDInput(r.PostForm.Get("identity"), !s.secure)
	if err != nil {
		t.LoginHint = r.PostForm.Get("identity")
		s.renderIdentify(w, r, t, capitalize(err.Error())+".", http.StatusBadRequest)
		return
	}
	res, err := s.resolve(r.Context(), id)
	if err != nil {
		t.LoginHint = id
		s.renderIdentify(w, r, t, "No Poweur ID named "+id+" could be found.", http.StatusBadRequest)
		return
	}
	req, err := s.verifier.NewRequest(signin.RequestOptions{
		Statement:   "Sign in to " + s.cfg.Name,
		ResponseURI: s.cfg.Issuer + "/poweur/callback",
	})
	if err != nil {
		s.log.Error("create native request", "err", err)
		s.renderError(w, r, http.StatusInternalServerError, "Could not create the sign-in request.")
		return
	}
	encoded, err := identity.EncodeSignInRequest(req)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "Could not create the sign-in request.")
		return
	}
	match, err := signin.NewMatchCode()
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "Could not create the sign-in request.")
		return
	}
	expires, _ := time.Parse(time.RFC3339, req.ExpiresAt)
	signers := s.discoverSigners(r.Context(), id, res)

	_, err = s.store.UpdateTxn(r.Context(), t.ID, func(t *Txn) error {
		if err := t.usable(); err != nil {
			return err
		}
		if t.Claimed {
			return errClaimed
		}
		t.Identity = id
		t.LoginHint = id
		t.RequestID = req.RequestID
		t.Request = encoded
		t.RequestExpires = expires
		t.Match = match
		t.Signers = signers
		return nil
	})
	if err != nil {
		s.txnError(w, r, err)
		return
	}
	http.Redirect(w, r, "/t/"+t.ID, http.StatusSeeOther)
}

func (s *Server) resolve(ctx context.Context, id string) (identity.Result, error) {
	if s.cfg.Resolver != nil {
		return s.cfg.Resolver.Resolve(ctx, id)
	}
	return identity.Resolve(ctx, id, s.cfg.ResolveOptions)
}

// discoverSigners lists where this identity can approve, best first. The
// bridge never probes a signer; it offers links and says what each is.
func (s *Server) discoverSigners(ctx context.Context, id string, res identity.Result) []Signer {
	var out []Signer
	seen := map[string]bool{}
	add := func(sg Signer) {
		u, err := url.Parse(sg.URL)
		if err != nil || u.Host == "" || (u.Scheme != "https" && (s.secure || u.Scheme != "http")) {
			return
		}
		key := strings.ToLower(u.Host + u.Path)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, sg)
	}
	caps, err := s.fetchCapabilities(ctx, id)
	if err == nil {
		if v := strings.TrimSpace(caps.Endpoints["web_signer"]); v != "" {
			add(Signer{Label: "Continue in your Poweur app", URL: v})
		}
	}
	if len(out) == 0 && (res.Source == identity.SourceWeb || res.Source == identity.SourceBoth) {
		// A relay serving the identity's well-known document serves the web
		// app beside it.
		scheme := s.cfg.ResolveOptions.Scheme
		if scheme == "" {
			scheme = "https"
		}
		add(Signer{Label: "Continue in your Poweur app", URL: scheme + "://" + id + "/app/"})
	}
	if s.cfg.DefaultSigner != "" {
		u, _ := url.Parse(s.cfg.DefaultSigner)
		host := ""
		if u != nil {
			host = u.Host
		}
		add(Signer{
			Label:   "Continue at " + host,
			URL:     s.cfg.DefaultSigner,
			Note:    "Works only if this browser already holds your keys there.",
			Default: true,
		})
	}
	return out
}

func (s *Server) fetchCapabilities(ctx context.Context, id string) (identity.Capabilities, error) {
	if s.cfg.FetchCapabilities != nil {
		return s.cfg.FetchCapabilities(ctx, id)
	}
	return identity.FetchCapabilities(ctx, id, s.cfg.ResolveOptions)
}

// --- Approval delivery -------------------------------------------------------------

// handleNativeCallback is the native response_uri. The signer POSTs here from
// its own origin; the receipt tells it how the sign-in finishes.
func (s *Server) handleNativeCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	d, err := signin.ParseDelivery(r)
	if err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	requestID := ""
	if resp, err := identity.DecodeSignInResponse(d.Response); err == nil {
		requestID = resp.RequestID
	}
	ctx := r.Context()
	id, err := s.store.TxnIDByRequest(ctx, requestID)
	if err != nil {
		writeOAuthError(w, http.StatusNotFound, "invalid_request", "no sign-in is waiting for this approval")
		return
	}
	now := s.now()
	crossDevice := d.Match != ""

	// Claim before verifying: one approval per transaction is ever processed.
	t, err := s.store.UpdateTxn(ctx, id, func(t *Txn) error {
		if err := t.usable(); err != nil {
			return err
		}
		if t.RequestID != requestID {
			return ErrNotFound
		}
		if now.After(t.RequestExpires) {
			return errExpired
		}
		if t.Claimed {
			return errClaimed
		}
		t.Claimed = true
		if crossDevice && !signin.MatchCodesEqual(t.Match, d.Match) {
			t.Err = "The code entered on the approving device did not match this screen."
			return commitAnd(signin.ErrMatchCode)
		}
		return nil
	})
	switch {
	case errors.Is(err, signin.ErrMatchCode):
		s.audit(ctx, "native.match_failed", map[string]any{"txn": shortID(id)})
		writeOAuthError(w, http.StatusForbidden, "access_denied", err.Error())
		return
	case errors.Is(err, errClaimed), errors.Is(err, errTxnDone), errors.Is(err, errTxnFailed):
		writeOAuthError(w, http.StatusConflict, "invalid_request", err.Error())
		return
	case errors.Is(err, errExpired), errors.Is(err, ErrNotFound):
		writeOAuthError(w, http.StatusGone, "invalid_request", "this sign-in has expired")
		return
	case err != nil:
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "could not record the approval")
		return
	}

	result, verr := s.verifier.Verify(ctx, d.Response)
	if verr == nil && result.Identity != t.Identity {
		verr = fmt.Errorf("the approval is for %s, but this sign-in is for %s", result.Identity, t.Identity)
	}
	if verr != nil {
		_, _ = s.store.UpdateTxn(ctx, id, func(t *Txn) error {
			t.Err = "The approval could not be verified: " + verr.Error()
			return nil
		})
		s.audit(ctx, "native.rejected", map[string]any{"txn": shortID(id), "reason": verr.Error()})
		writeOAuthError(w, http.StatusUnauthorized, "access_denied", verr.Error())
		return
	}

	receipt := signin.DeliveryReceipt{Status: "ok", Identity: result.Identity}
	var resumeCode string
	if !crossDevice {
		if resumeCode, err = signin.NewSecret(); err != nil {
			writeOAuthError(w, http.StatusInternalServerError, "server_error", "could not finish the approval")
			return
		}
		receipt.ResumeURI = s.cfg.Issuer + "/poweur/resume?code=" + url.QueryEscape(resumeCode)
	}
	_, err = s.store.UpdateTxn(ctx, id, func(t *Txn) error {
		t.AuthTime = now
		t.SessionDelegated = result.SessionDelegated
		if crossDevice {
			t.Finish = FinishCrossDevice
		} else {
			t.Finish = FinishSameDevice
			t.ResumeHash = signin.HashSecret(resumeCode)
			t.ResumeExpires = now.Add(resumeWindow)
		}
		return nil
	})
	if err != nil {
		writeOAuthError(w, http.StatusGone, "invalid_request", "the sign-in expired while it was being verified")
		return
	}
	finish := FinishSameDevice
	if crossDevice {
		finish = FinishCrossDevice
	}
	s.audit(ctx, "native.approved", map[string]any{
		"txn": shortID(id), "finish": finish, "session_delegated": result.SessionDelegated,
	})
	writeJSON(w, http.StatusOK, receipt)
}

// handleNativeCallbackGet refuses approvals carried in a URL.
func (s *Server) handleNativeCallbackGet(w http.ResponseWriter, r *http.Request) {
	writeOAuthError(w, http.StatusBadRequest, "invalid_request",
		"approvals are accepted only by POST; the browser continues at the resume_uri the POST returns")
}

// handleNativeResume finishes a same-device approval, in the browser that
// started the transaction and nowhere else.
func (s *Server) handleNativeResume(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	hash := ""
	if code != "" {
		hash = signin.HashSecret(code)
	}
	id, err := s.store.TxnIDByResume(ctx, hash)
	if err != nil {
		s.renderError(w, r, http.StatusGone, "This sign-in link has expired or was already used.")
		return
	}
	binding := s.cookieValue(r, bindingCookie)
	now := s.now()
	t, err := s.store.UpdateTxn(ctx, id, func(t *Txn) error {
		if err := t.usable(); err != nil {
			return err
		}
		if t.Finish != FinishSameDevice || t.ResumeHash != hash {
			return ErrNotFound
		}
		t.ResumeHash = "" // single use, whatever happens next
		if now.After(t.ResumeExpires) {
			t.Err = "The approval was not completed in time."
			return commitAnd(errExpired)
		}
		if !signin.SecretMatches(t.BindingHash, binding) {
			t.Err = "The sign-in was approved for a different browser from the one that started it."
			return commitAnd(errWrongBrowse)
		}
		t.Resumed = true
		return nil
	})
	switch {
	case errors.Is(err, errWrongBrowse):
		s.audit(ctx, "native.resume_wrong_browser", map[string]any{"txn": shortID(id)})
		s.txnError(w, r, err)
		return
	case errors.Is(err, errExpired), errors.Is(err, ErrNotFound):
		s.renderError(w, r, http.StatusGone, "This sign-in link has expired or was already used.")
		return
	case err != nil:
		s.txnError(w, r, err)
		return
	}
	if err := s.startSession(w, r, t); err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "Could not sign you in.")
		return
	}
	http.Redirect(w, r, "/t/"+t.ID+"/continue", http.StatusSeeOther)
}

// handleTxnStatus is the waiting page's poll. It is bound to the browser
// that started the transaction, and it is where a cross-device approval
// hands that browser its session.
func (s *Server) handleTxnStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	t, err := s.loadTxnForBrowser(r, r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusOK, Status{Status: "failed", Error: "This sign-in is not available in this browser."})
		return
	}
	now := s.now()
	st := t.status(now)
	if t.Done {
		st = Status{Status: "failed", Error: errTxnDone.Error()}
	}
	if st.Status == "complete" && !t.Resumed {
		started := false
		t, err = s.store.UpdateTxn(r.Context(), t.ID, func(t *Txn) error {
			if err := t.usable(); err != nil {
				return err
			}
			if !t.Resumed && t.Finish == FinishCrossDevice {
				t.Resumed = true
				started = true
			}
			return nil
		})
		if err != nil {
			writeJSON(w, http.StatusOK, Status{Status: "failed", Error: err.Error()})
			return
		}
		if started {
			if err := s.startSession(w, r, t); err != nil {
				writeJSON(w, http.StatusOK, Status{Status: "failed", Error: "Could not sign you in."})
				return
			}
		}
	}
	if st.Status == "complete" {
		st.Next = "/t/" + t.ID + "/continue"
	}
	writeJSON(w, http.StatusOK, st)
}

// handleTxnContinue moves a resumed transaction to its result.
func (s *Server) handleTxnContinue(w http.ResponseWriter, r *http.Request) {
	t, err := s.loadTxnForBrowser(r, r.PathValue("id"))
	if err != nil {
		s.txnError(w, r, err)
		return
	}
	if err := t.usable(); err != nil {
		s.renderFailed(w, r, t)
		return
	}
	if !t.Resumed {
		http.Redirect(w, r, "/t/"+t.ID, http.StatusSeeOther)
		return
	}
	switch t.Kind {
	case KindLogin:
		if _, err := s.store.UpdateTxn(r.Context(), t.ID, func(t *Txn) error {
			if err := t.usable(); err != nil {
				return err
			}
			t.Done = true
			return nil
		}); err != nil {
			s.txnError(w, r, err)
			return
		}
		s.recordSignIn(r, t, nil)
		http.Redirect(w, r, t.ReturnTo, http.StatusSeeOther)
	case KindAuthorize:
		s.continueAuthorize(w, r, t)
	default:
		s.renderError(w, r, http.StatusBadRequest, "Unknown sign-in.")
	}
}

// handleTxnCancel abandons a transaction from its own page.
func (s *Server) handleTxnCancel(w http.ResponseWriter, r *http.Request) {
	if !s.sameOriginPost(r) {
		s.renderError(w, r, http.StatusForbidden, "That request did not come from this site.")
		return
	}
	t, err := s.loadTxnForBrowser(r, r.PathValue("id"))
	if err != nil {
		s.txnError(w, r, err)
		return
	}
	t, err = s.store.UpdateTxn(r.Context(), t.ID, func(t *Txn) error {
		if err := t.usable(); err != nil {
			return err
		}
		t.Err = "You cancelled this sign-in."
		t.Done = true
		return nil
	})
	if err != nil {
		s.txnError(w, r, err)
		return
	}
	if t.Kind == KindAuthorize && t.Authorize != nil {
		s.redirectError(w, r, t.Authorize, "access_denied", "the user cancelled the sign-in")
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) recordSignIn(r *http.Request, t *Txn, c *Client) {
	rec := SignInRecord{
		At:               t.AuthTime,
		SessionDelegated: t.SessionDelegated,
		Finish:           t.Finish,
		UserAgent:        summarizeUserAgent(r.UserAgent()),
	}
	if c != nil {
		rec.ClientID, rec.ClientName = c.ID, c.Name
	}
	if rec.At.IsZero() {
		rec.At = s.now()
	}
	if err := s.store.RecordSignIn(r.Context(), t.Identity, rec); err != nil {
		s.log.Warn("record sign-in", "err", err)
	}
}

func (s *Server) audit(ctx context.Context, event string, fields map[string]any) {
	s.metrics.event(event)
	if err := s.store.Audit(ctx, event, fields); err != nil {
		s.log.Warn("audit", "event", event, "err", err)
	}
	s.log.Info(event, flatten(fields)...)
}

func flatten(m map[string]any) []any {
	out := make([]any, 0, len(m)*2)
	for k, v := range m {
		out = append(out, k, v)
	}
	return out
}

// shortID keeps enough of an id to correlate log lines, not to use it.
func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// --- JSON helpers ------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	if w.Header().Get("Cache-Control") == "" {
		w.Header().Set("Cache-Control", "no-store")
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeOAuthError(w http.ResponseWriter, status int, code, description string) {
	writeJSON(w, status, map[string]string{"error": code, "error_description": description})
}

func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	_, err := rand.Read(b)
	return b, err
}

func randomToken(n int) (string, error) {
	b, err := randomBytes(n)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
