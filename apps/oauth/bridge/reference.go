package bridge

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/poweur/identity"
)

// Requests by reference (EPIC-008 E08-T6).
//
// The QR on the approval page used to carry the whole signed request, ~600
// characters, which draws a symbol many phone cameras will not read. It now
// carries <issuer>/r/<code>. A signer fetches it asking for JSON and gets the
// request, which names this issuer as its audience — so the signer can check
// the link came from the site the request is for. A phone's own camera app
// opening the same link gets a page offering the Poweur app and the web
// signers. The request is not secret (the match code on the starting screen
// is what completes a cross-device sign-in, and it is never served here).

// requestLink is the short link for a transaction's current request.
func (s *Server) requestLink(t *Txn) string {
	if t.RequestCode == "" {
		return ""
	}
	return s.cfg.Issuer + "/r/" + t.RequestCode
}

type handoffPage struct {
	Identity string       `json:"identity"`
	Client   *appRef      `json:"client,omitempty"`
	Signers  []signerPage `json:"signers"`
	DeepLink string       `json:"deepLink"`
}

func (s *Server) handleRequestByReference(w http.ResponseWriter, r *http.Request) {
	wantsJSON := strings.Contains(r.Header.Get("Accept"), "application/json")
	if wantsJSON {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Cache-Control", "no-store")
	}
	gone := func() {
		if wantsJSON {
			writeOAuthError(w, http.StatusGone, "expired_request", "this sign-in code expired or was already used")
			return
		}
		s.renderError(w, r, http.StatusGone, "This sign-in code expired or was already used. Start again on the other screen.")
	}
	code, err := identity.NormalizeShortCode(r.PathValue("code"))
	if err != nil {
		gone()
		return
	}
	id, err := s.store.TxnIDByRequestCode(r.Context(), code)
	if err != nil {
		gone()
		return
	}
	t, err := s.store.GetTxn(r.Context(), id)
	// Only the transaction's current request, and only while it waits.
	if err != nil || t.RequestCode != code || t.usable() != nil || t.Claimed || t.Request == "" || s.now().After(t.RequestExpires) {
		gone()
		return
	}
	if wantsJSON {
		writeJSON(w, http.StatusOK, map[string]string{"request": t.Request})
		return
	}
	link := s.requestLink(t)
	page := handoffPage{
		Identity: t.Identity,
		Client:   appRefOf(t.Authorize),
		Signers:  []signerPage{},
		DeepLink: identity.SignInReferenceDeepLink(link),
	}
	for _, sg := range t.Signers {
		base := sg.URL
		if !strings.Contains(base, "?") {
			base += "?auth=" + url.QueryEscape(link)
		} else {
			base += "&auth=" + url.QueryEscape(link)
		}
		page.Signers = append(page.Signers, signerPage{Label: sg.Label, Href: base, Note: sg.Note, Own: !sg.Default})
	}
	s.render(w, r, http.StatusOK, "handoff", "Approve this sign-in", page)
}
