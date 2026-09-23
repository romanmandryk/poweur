package bridge

import (
	"embed"
	"encoding/json"
	"html"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
)

// The pages are React (apps/oauth/ui, E22-T10), built into web/dist and
// embedded here. Go still owns every route, redirect, cookie and check: each
// page response is index.html with that page's data embedded as a JSON data
// block, which React renders. A data block is not executable, so the CSP
// stays script-src 'self'.
//
//go:embed all:web
var webFS embed.FS

const (
	pageMarker = "<!--poweur-page-->"
	titleTag   = "<title>Poweur sign-in</title>"
)

// uiIndex is the built index.html, or a plain stand-in when the UI was not
// built (go test, go run without pnpm): the page data is still there.
var uiIndex, uiBuilt = loadIndex()

func loadIndex() (string, bool) {
	raw, err := webFS.ReadFile("web/dist/index.html")
	if err != nil || !strings.Contains(string(raw), pageMarker) {
		return `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="robots" content="noindex">` +
			titleTag + `</head><body><p>This bridge was built without its pages ` +
			`(pnpm --filter @poweur/oauth-ui build).</p>` + pageMarker + `</body></html>`, false
	}
	return string(raw), true
}

// pagePayload is what every page gets; apps/oauth/ui/src/lib/page.ts is its
// TypeScript twin.
type pagePayload struct {
	Page    string       `json:"page"`
	Title   string       `json:"title"`
	Service serviceInfo  `json:"service"`
	Session *sessionInfo `json:"session,omitempty"`
	Data    any          `json:"data"`
}

type serviceInfo struct {
	Name    string `json:"name"`
	Issuer  string `json:"issuer"`
	Contact string `json:"contact"`
}

type sessionInfo struct {
	Identity string `json:"identity"`
}

// writePage answers with index.html carrying p.
func writePage(w http.ResponseWriter, status int, p pagePayload) error {
	raw, err := json.Marshal(p) // escapes <, > and &: no way out of the script element
	if err != nil {
		return err
	}
	doc := strings.Replace(uiIndex, pageMarker,
		`<script type="application/json" id="poweur-page">`+string(raw)+`</script>`, 1)
	title := p.Service.Name
	if p.Title != "" && p.Title != p.Service.Name {
		title = p.Title + " · " + p.Service.Name
	}
	doc = strings.Replace(doc, titleTag, "<title>"+html.EscapeString(title)+"</title>", 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, err = w.Write([]byte(doc))
	return err
}

// handleAsset serves the build's hashed files; their names change with their
// content, so browsers may keep them.
func (s *Server) handleAsset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	if name == "" || strings.Contains(name, "..") || strings.ContainsAny(name, `/\`) {
		http.NotFound(w, r)
		return
	}
	raw, err := webFS.ReadFile("web/dist/assets/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ctype := mime.TypeByExtension(path.Ext(name))
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	_, _ = w.Write(raw)
}

// --- What each page shows -----------------------------------------------------
//
// Views hold server types (a *Txn carries binding and resume hashes, a
// *Client its secret hashes); pages get only these shapes.

type appRef struct {
	Name string `json:"name"`
	Host string `json:"host,omitempty"`
}

func appRefOf(a *AuthorizeRequest) *appRef {
	if a == nil || a.ClientName == "" {
		return nil
	}
	ref := &appRef{Name: a.ClientName}
	if u, err := urlHost(a.RedirectURI); err == nil {
		ref.Host = u
	}
	return ref
}

type homeView struct {
	Launcher    *launcherView `json:"launcher,omitempty"`
	Apps        int           `json:"apps,omitempty"`
	Clients     int           `json:"clients,omitempty"`
	CanRegister bool          `json:"canRegister,omitempty"`
}

type identifyPage struct {
	Txn      string        `json:"txn"`
	Client   *appRef       `json:"client,omitempty"`
	Hint     string        `json:"hint,omitempty"`
	Error    string        `json:"error,omitempty"`
	Launcher *launcherView `json:"launcher,omitempty"`
}

type signerPage struct {
	Label string `json:"label"`
	Href  string `json:"href"`
	Note  string `json:"note,omitempty"`
	Own   bool   `json:"own"`
}

type pushPage struct {
	From string `json:"from"`
	Sent int    `json:"sent"`
	Left int    `json:"left"`
}

type awaitPage struct {
	Txn      string       `json:"txn"`
	Identity string       `json:"identity"`
	Client   *appRef      `json:"client,omitempty"`
	Signers  []signerPage `json:"signers"`
	DeepLink string       `json:"deepLink,omitempty"`
	// Request is the short link to the request (E08-T6): what the QR shows
	// and "copy" copies. The whole request stays in the deep link and the
	// web signer links, which are clicked, not scanned.
	Request string    `json:"request"`
	Match   string    `json:"match"`
	QR      string    `json:"qr"`
	Push    *pushPage `json:"push,omitempty"`
}

type consentClient struct {
	Name         string   `json:"name"`
	Host         string   `json:"host,omitempty"`
	VerifiedHost bool     `json:"verifiedHost"`
	RegisteredBy []string `json:"registeredBy,omitempty"`
}

type consentPage struct {
	Txn        string        `json:"txn"`
	Identity   string        `json:"identity"`
	Client     consentClient `json:"client"`
	Optional   []string      `json:"optional"`
	IndieAuth  bool          `json:"indieAuth"`
	ProfileURL string        `json:"profileURL,omitempty"`
}

type consentRow struct {
	ClientID   string    `json:"clientId"`
	ClientName string    `json:"clientName"`
	ClientHost string    `json:"clientHost,omitempty"`
	Granted    []string  `json:"granted"`
	LastUsed   time.Time `json:"lastUsed"`
}

type signInRow struct {
	At          time.Time `json:"at"`
	ClientName  string    `json:"clientName,omitempty"`
	UserAgent   string    `json:"userAgent,omitempty"`
	CrossDevice bool      `json:"crossDevice"`
	SessionKey  bool      `json:"sessionKey"`
}

type accountPage struct {
	Consents []consentRow `json:"consents"`
	SignIns  []signInRow  `json:"signIns"`
	Notice   string       `json:"notice,omitempty"`
}

type clientSummary struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	RedirectURIs []string `json:"redirectURIs"`
	Suspended    bool     `json:"suspended"`
}

type developersPage struct {
	Clients  []clientSummary `json:"clients"`
	CanWrite bool            `json:"canWrite"`
	Why      string          `json:"why,omitempty"`
	Max      int             `json:"max"`
}

type clientFormPage struct {
	Name         string `json:"name"`
	RedirectURIs string `json:"redirectURIs"`
	AuthMethod   string `json:"authMethod"`
	JWKS         string `json:"jwks"`
	JWKSURI      string `json:"jwksURI"`
	Sector       string `json:"sector"`
	CoOwners     string `json:"coOwners"`
	Error        string `json:"error,omitempty"`
}

type secretRow struct {
	ID        string     `json:"id"`
	CreatedAt time.Time  `json:"createdAt"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	Status    string     `json:"status"`
}

type clientDetail struct {
	ID              string      `json:"id"`
	Name            string      `json:"name"`
	AuthMethod      string      `json:"authMethod"`
	Sector          string      `json:"sector"`
	CreatedBy       string      `json:"createdBy,omitempty"`
	CreatedAt       *time.Time  `json:"createdAt,omitempty"`
	Suspended       bool        `json:"suspended"`
	SuspendedReason string      `json:"suspendedReason,omitempty"`
	UsesSecret      bool        `json:"usesSecret"`
	Secrets         []secretRow `json:"secrets"`
}

type clientNewPage struct {
	Form   clientFormPage `json:"form"`
	Issuer string         `json:"issuer"`
}

type clientPage struct {
	Client clientDetail   `json:"client"`
	Form   clientFormPage `json:"form"`
	Secret string         `json:"secret,omitempty"`
	Issuer string         `json:"issuer"`
	Notice string         `json:"notice,omitempty"`
}

type messagePage struct {
	Message string `json:"message"`
}

type contactPage struct {
	Contact string `json:"contact,omitempty"`
}

// pageFor turns a template's view into what the page may see.
func (s *Server) pageFor(name string, data any) any {
	switch v := data.(type) {
	case nil:
		return struct{}{}
	case string:
		if name == "abuse" {
			return contactPage{Contact: v}
		}
		return messagePage{Message: v}
	case handoffPage:
		return v
	case identifyView:
		return identifyPage{Txn: v.Txn.ID, Client: appRefOf(v.Client), Hint: v.Hint, Error: v.Error, Launcher: v.Launcher}
	case awaitView:
		p := awaitPage{
			Txn: v.Txn.ID, Identity: v.Txn.Identity, Client: appRefOf(v.Client),
			Signers: []signerPage{}, DeepLink: string(v.DeepLink), Request: v.Request, Match: v.Match, QR: string(v.QR),
		}
		for _, sg := range v.Signers {
			p.Signers = append(p.Signers, signerPage{Label: sg.Label, Href: sg.Href, Note: sg.Note, Own: !sg.Default})
		}
		if v.Push {
			p.Push = &pushPage{From: v.PushFrom, Sent: v.Txn.Pushes, Left: maxPushesPerTxn - v.Txn.Pushes}
		}
		return p
	case consentView:
		p := consentPage{
			Txn: v.Txn.ID, Identity: v.Txn.Identity,
			Client:    consentClient{Name: v.Client.Name, Host: v.Host, VerifiedHost: v.URLClient},
			Optional:  append([]string{}, v.Optional...),
			IndieAuth: v.IndieAuth,
		}
		if v.Registered {
			p.Client.RegisteredBy = v.Client.Owners
		}
		if v.IndieAuth {
			p.ProfileURL = v.ProfileURL
		}
		return p
	case homeView:
		return v
	case accountView:
		p := accountPage{Consents: []consentRow{}, SignIns: []signInRow{}, Notice: v.Notice}
		for _, c := range v.Consents {
			p.Consents = append(p.Consents, consentRow{
				ClientID: c.ClientID, ClientName: c.ClientName, ClientHost: c.ClientHost,
				Granted: append([]string{}, c.Granted...), LastUsed: c.LastUsed,
			})
		}
		for _, r := range v.SignIns {
			p.SignIns = append(p.SignIns, signInRow{
				At: r.At, ClientName: r.ClientName, UserAgent: r.UserAgent,
				CrossDevice: r.Finish == FinishCrossDevice, SessionKey: r.SessionDelegated,
			})
		}
		return p
	case developersView:
		p := developersPage{Clients: []clientSummary{}, CanWrite: v.CanWrite, Why: v.Why, Max: v.Max}
		for _, c := range v.Clients {
			p.Clients = append(p.Clients, clientSummary{
				ID: c.ID, Name: c.Name, RedirectURIs: append([]string{}, c.RedirectURIs...), Suspended: c.Suspended,
			})
		}
		return p
	case clientView:
		form := clientFormPage(v.Form)
		if v.Client == nil {
			return clientNewPage{Form: form, Issuer: v.Issuer}
		}
		c := v.Client
		d := clientDetail{
			ID: c.ID, Name: c.Name, AuthMethod: c.AuthMethod, Sector: c.Sector, CreatedBy: c.CreatedBy,
			Suspended: c.Suspended, SuspendedReason: c.SuspendedReason,
			UsesSecret: c.AuthMethod == AuthSecretBasic || c.AuthMethod == AuthSecretPost,
			Secrets:    []secretRow{},
		}
		if !c.CreatedAt.IsZero() {
			created := c.CreatedAt
			d.CreatedAt = &created
		}
		for _, sec := range c.Secrets {
			row := secretRow{ID: sec.ID, CreatedAt: sec.CreatedAt, Status: "active"}
			if !sec.ExpiresAt.IsZero() {
				exp := sec.ExpiresAt
				row.ExpiresAt = &exp
				row.Status = "retiring"
				if v.Now.After(exp) {
					row.Status = "expired"
				}
			}
			d.Secrets = append(d.Secrets, row)
		}
		return clientPage{Client: d, Form: form, Secret: v.Secret, Issuer: v.Issuer, Notice: v.Notice}
	case policyView:
		return v
	}
	s.log.Error("page has no view shape", "page", name)
	return struct{}{}
}
