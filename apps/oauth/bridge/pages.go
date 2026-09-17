package bridge

import (
	"embed"
	"html/template"
	"net/http"
	"slices"
	"strings"

	"github.com/poweur/identity"
)

//go:embed templates/*.html static/*
var assets embed.FS

var pages = template.Must(template.New("").Funcs(template.FuncMap{
	"has":  func(list []string, v string) bool { return slices.Contains(list, v) },
	"join": strings.Join,
}).ParseFS(assets, "templates/*.html"))

// pageData is what every template gets.
type pageData struct {
	Name    string
	Title   string
	Session *BrowserSession
	Issuer  string
	Contact string
	Message string
	Data    any
	Refresh bool
	NoIndex bool
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, tmpl, title string, data any) {
	sess, _ := s.currentSession(r)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := pages.ExecuteTemplate(w, tmpl, pageData{
		Name:    s.cfg.Name,
		Title:   title,
		Session: sess,
		Issuer:  s.cfg.Issuer,
		Contact: s.cfg.ContactURI,
		Data:    data,
	}); err != nil {
		s.log.Error("render", "template", tmpl, "err", err)
	}
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
	Why      string
}

func (s *Server) renderIdentify(w http.ResponseWriter, r *http.Request, t *Txn, message string, status int) {
	s.render(w, r, status, "identify.html", "Sign in with your Poweur ID", identifyView{
		Txn: t, Hint: t.LoginHint, Error: message, Client: t.Authorize,
		Launcher: s.launcherView(),
		Why:      whyPoweur(t),
	})
}

type signerLink struct {
	Label string
	Href  string
	Note  string
}

type awaitView struct {
	Txn     *Txn
	Signers []signerLink
	// DeepLink is built here from our own request; html/template would
	// otherwise refuse the poweur: scheme.
	DeepLink template.URL
	Request  string
	Match    string
	Client   *AuthorizeRequest
	QR       template.HTML
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
		v.Signers = append(v.Signers, signerLink{Label: sg.Label, Href: link, Note: sg.Note})
	}
	deep, _ := identity.SignInDeepLink(req)
	if strings.HasPrefix(deep, "poweur://auth?") {
		v.DeepLink = template.URL(deep)
	}
	v.QR = qrSVG(deep)
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
}

func (s *Server) renderConsent(w http.ResponseWriter, r *http.Request, t *Txn, c *Client, prev *Consent) {
	v := consentView{
		Txn:        t,
		Client:     c,
		Request:    t.Authorize,
		Host:       clientHost(c),
		Registered: c.Source == ClientRegistered,
		URLClient:  c.Source == ClientURL,
		Optional:   t.Authorize.requestedOptional(),
		IndieAuth:  t.Authorize.Surface == surfaceIndieAuth,
		ProfileURL: identity.IndieAuthProfileURL(t.Identity),
	}
	if prev != nil {
		v.Previously = prev.Granted
	}
	s.render(w, r, http.StatusOK, "consent.html", "Allow "+c.Name+"?", v)
}

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, "home.html", s.cfg.Name, nil)
}

func (s *Server) handleCSS(w http.ResponseWriter, r *http.Request) {
	s.serveAsset(w, "static/bridge.css", "text/css; charset=utf-8")
}

func (s *Server) handleJS(w http.ResponseWriter, r *http.Request) {
	s.serveAsset(w, "static/bridge.js", "text/javascript; charset=utf-8")
}

func (s *Server) serveAsset(w http.ResponseWriter, name, contentType string) {
	raw, err := assets.ReadFile(name)
	if err != nil {
		http.NotFound(w, nil)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write(raw)
}

// whyPoweur is the one-line answer to "why do I need this?".
func whyPoweur(t *Txn) string {
	app := "This site"
	if t.Authorize != nil && t.Authorize.ClientName != "" {
		app = t.Authorize.ClientName
	}
	return app + " signs you in with a Poweur ID instead of a password: a name you own, " +
		"confirmed with a key that never leaves your device. The same ID works anywhere Poweur ID is accepted."
}
