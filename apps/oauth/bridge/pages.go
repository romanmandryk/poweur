package bridge

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/poweur/identity"
)

// render answers with a page: its name is the template name the handlers
// have always used ("consent.html"), its data their view, which pageFor
// narrows to what the page may show.
func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, tmpl, title string, data any) {
	name := strings.TrimSuffix(tmpl, ".html")
	p := pagePayload{
		Page:    name,
		Title:   title,
		Service: serviceInfo{Name: s.cfg.Name, Issuer: s.cfg.Issuer, Contact: s.cfg.ContactURI},
		Data:    s.pageFor(name, data),
	}
	if sess, _ := s.currentSession(r); sess != nil {
		p.Session = &sessionInfo{Identity: sess.Identity}
	}
	if s.cfg.TelemetryURL != "" {
		p.Telemetry = &telemetryInfo{URL: s.cfg.TelemetryURL, Version: Version}
	}
	if err := writePageWithHead(w, status, p, ""); err != nil {
		s.log.Error("render", "page", name, "err", err)
	}
}

func urlHost(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	return u.Hostname(), nil
}

func (s *Server) renderError(w http.ResponseWriter, r *http.Request, status int, message string) {
	s.render(w, r, status, "error.html", "Something went wrong", message)
}

func (s *Server) renderFailed(w http.ResponseWriter, r *http.Request, t *Txn) {
	msg := t.Err
	if msg == "" {
		msg = errTxnDone.Error()
	}
	s.render(w, r, http.StatusGone, "error.html", "Sign-in not completed", capitalize(msg))
}

type identifyView struct {
	Txn      *Txn
	Hint     string
	Error    string
	Client   *AuthorizeRequest
	Launcher *launcherView
}

func (s *Server) renderIdentify(w http.ResponseWriter, r *http.Request, t *Txn, message string, status int) {
	s.render(w, r, status, "identify.html", "Sign in with your Poweur ID", identifyView{
		Txn: t, Hint: t.LoginHint, Error: message, Client: t.Authorize,
		Launcher: s.launcherView(),
	})
}

type signerLink struct {
	Label   string
	Href    string
	Note    string
	Default bool
}

type awaitView struct {
	Txn      *Txn
	Signers  []signerLink
	DeepLink string
	Request  string
	Match    string
	Client   *AuthorizeRequest
	QR       string
	// Push is set when the bridge can send the request to the user's app.
	Push       bool
	PushFrom   string
	Pushed     bool
	PushNotice string
}

func (s *Server) renderAwait(w http.ResponseWriter, r *http.Request, t *Txn) {
	req, err := identity.DecodeSignInRequest(t.Request)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "The sign-in request is damaged.")
		return
	}
	v := awaitView{Txn: t, Request: t.Request, Match: t.Match, Client: t.Authorize}
	for _, sg := range t.Signers {
		link, err := identity.SignInWebLink(sg.URL, req)
		if err != nil {
			continue
		}
		v.Signers = append(v.Signers, signerLink{Label: sg.Label, Href: link, Note: sg.Note, Default: sg.Default})
	}
	deep, _ := identity.SignInDeepLink(req)
	if strings.HasPrefix(deep, "poweur://auth?") {
		v.DeepLink = deep
	}
	// The QR and "copy" carry the short link (E08-T6); a transaction from
	// before codes existed falls back to the whole request.
	if link := s.requestLink(t); link != "" {
		v.Request = link
	}
	v.QR = qrSVG(v.Request)
	v.Push = s.cfg.Pusher != nil && t.Pushes < maxPushesPerTxn
	v.PushFrom = s.cfg.PushIdentity
	v.Pushed = t.Pushes > 0
	v.PushNotice = r.URL.Query().Get("push")
	s.render(w, r, http.StatusOK, "await.html", "Approve with your Poweur ID", v)
}

type consentView struct {
	Txn        *Txn
	Client     *Client
	Request    *AuthorizeRequest
	Host       string
	Registered bool
	URLClient  bool
	Optional   []string
	Previously []string
	IndieAuth  bool
	ProfileURL string
	// HostVerified: the host named is the client's own (its client_id host). A URL
	// client whose request returns to another origin is shown that origin instead,
	// unverified, so the user sees where the browser will go.
	HostVerified bool
}

func (s *Server) renderConsent(w http.ResponseWriter, r *http.Request, t *Txn, c *Client, prev *Consent) {
	v := consentView{
		Txn:          t,
		Client:       c,
		Request:      t.Authorize,
		Host:         clientHost(c),
		Registered:   c.Source == ClientRegistered,
		URLClient:    c.Source == ClientURL,
		Optional:     t.Authorize.requestedOptional(),
		IndieAuth:    t.Authorize.Surface == surfaceIndieAuth,
		ProfileURL:   identity.IndieAuthProfileURL(t.Identity),
		HostVerified: c.Source == ClientURL,
	}
	if c.Source == ClientURL && t.Authorize != nil {
		cu, cerr := url.Parse(c.ID)
		ru, rerr := url.Parse(t.Authorize.RedirectURI)
		if cerr == nil && rerr == nil && ru.Host != "" && !identity.SameOrigin(cu.Scheme+"://"+cu.Host, t.Authorize.RedirectURI) {
			v.Host, v.HostVerified = ru.Hostname(), false
		}
	}
	if prev != nil {
		v.Previously = prev.Granted
	}
	s.render(w, r, http.StatusOK, "consent.html", "Allow "+c.Name+"?", v)
}

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	v := homeView{Launcher: s.launcherView()}
	if sess, _ := s.currentSession(r); sess != nil {
		ctx := r.Context()
		if consents, err := s.store.ListConsents(ctx, sess.Identity); err == nil {
			v.Apps = len(consents)
		}
		if clients, err := s.store.ClientsOwnedBy(ctx, sess.Identity); err == nil {
			v.Clients = len(clients)
		}
		v.CanRegister, _ = s.canRegister(sess.Identity)
	}
	s.render(w, r, http.StatusOK, "home.html", s.cfg.Name, v)
}
