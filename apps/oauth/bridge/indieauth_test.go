package bridge

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

const (
	iaClient   = "https://notes.example/app/client.json"
	iaRedirect = "https://notes.example/auth/callback"
)

func indieAuthQuery(clientID, redirect, scope string, extra ...string) string {
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirect},
		"state":                 {sampleState},
		"code_challenge":        {challenge(testVerifier)},
		"code_challenge_method": {"S256"},
	}
	if scope != "" {
		q.Set("scope", scope)
	}
	for i := 0; i+1 < len(extra); i += 2 {
		q.Set(extra[i], extra[i+1])
	}
	return "/authorize?" + q.Encode()
}

func redeemAt(h *harness, path string, form url.Values) (int, map[string]any) {
	h.t.Helper()
	resp, err := http.PostForm(h.issuer+path, form)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestIndieAuthLoginWithPublishedMetadata(t *testing.T) {
	h := newHarness(t)
	h.metadata[iaClient] = `{"client_id":"` + iaClient + `","client_name":"Example Notes","client_uri":"https://notes.example/",
		"redirect_uris":["` + iaRedirect + `"],"logo_uri":"https://cdn.elsewhere.example/logo.png"}`
	b := h.browser()

	id, p := b.signIn(indieAuthQuery(iaClient, iaRedirect, "profile", "me", "https://alice.poweur.net/"), h.users[alice])
	for _, want := range []string{"Allow Example Notes?", "notes.example", "verified host", "IndieAuth sign-in always shares it", "public profile"} {
		if !strings.Contains(p.body, want) {
			t.Errorf("IndieAuth consent lacks %q", want)
		}
	}
	if strings.Contains(p.body, "cdn.elsewhere.example") {
		t.Error("an off-origin logo was accepted")
	}
	if strings.Contains(p.body, "key fingerprint") {
		t.Error("IndieAuth must not offer poweur_id as optional")
	}
	code := h.codeFrom(b.consent(id, ScopeProfile), iaRedirect)

	form := url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "client_id": {iaClient},
		"redirect_uri": {iaRedirect}, "code_verifier": {testVerifier},
	}
	status, out := redeemAt(h, "/authorize", form)
	if status != 200 || out["me"] != "https://alice.poweur.net/" {
		t.Fatalf("redeem = %d %v", status, out)
	}
	profile, _ := out["profile"].(map[string]any)
	if profile["name"] != "Display "+alice || profile["url"] != "https://alice.poweur.net/" {
		t.Fatalf("profile = %v", out["profile"])
	}
	if _, ok := out["access_token"]; ok {
		t.Fatal("a login-only IndieAuth exchange issued an access token")
	}
	// Single use, at either endpoint.
	if status, _ := redeemAt(h, "/authorize", form); status == 200 {
		t.Fatal("code redeemed twice")
	}
}

func TestIndieAuthTokenEndpointAndMeHint(t *testing.T) {
	h := newHarness(t)
	h.metadata[iaClient] = `{"client_id":"` + iaClient + `","client_name":"Notes","redirect_uris":["` + iaRedirect + `"]}`
	b := h.browser()
	// `me` names bob; alice signs in. IndieAuth returns who actually did.
	id := b.identify(indieAuthQuery(iaClient, iaRedirect, "", "me", "bob.poweur.net"), h.users[alice])
	if !strings.Contains(b.get("/t/"+id+"?change=1").body, `value="alice.poweur.net"`) {
		t.Fatal("identify page should show the ID typed, not the hint, after identify")
	}
	_, receipt := h.deliver(h.approve(id, h.users[alice]), "")
	p := b.follow(b.get(receipt.ResumeURI))
	code := h.codeFrom(b.consent(txnIDFrom(t, p.body)), iaRedirect)
	status, out := redeemAt(h, "/token", url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "client_id": {iaClient},
		"redirect_uri": {iaRedirect}, "code_verifier": {testVerifier},
	})
	if status != 200 || out["me"] != "https://alice.poweur.net/" || out["profile"] != nil {
		t.Fatalf("token endpoint = %d %v", status, out)
	}
}

func TestIndieAuthRedirectRules(t *testing.T) {
	h := newHarness(t)
	b := h.browser()

	// No document: only the client's own origin.
	noDoc := "https://bare.example/"
	if p := b.get(indieAuthQuery(noDoc, "https://bare.example/cb", "")); p.status != http.StatusSeeOther {
		t.Fatalf("same-origin redirect without metadata = %d %s", p.status, p.body)
	}
	if p := b.get(indieAuthQuery(noDoc, "https://other.example/cb", "")); p.status != http.StatusBadRequest {
		t.Fatalf("cross-origin redirect without metadata = %d", p.status)
	}
	if p := b.get(indieAuthQuery(noDoc, "http://bare.example/cb", "")); p.status != http.StatusBadRequest {
		t.Fatalf("scheme downgrade = %d", p.status)
	}

	// A document may list another origin, but only its own.
	h.metadata["https://listed.example/c"] = `{"client_id":"https://listed.example/c","redirect_uris":["https://evil.example/cb"]}`
	if p := b.get(indieAuthQuery("https://listed.example/c", "https://evil.example/cb", "")); p.status != http.StatusBadRequest {
		t.Fatalf("off-origin redirect from metadata = %d", p.status)
	}

	// A document claiming another client_id is refused.
	h.metadata["https://liar.example/c"] = `{"client_id":"https://victim.example/c","redirect_uris":["https://liar.example/cb"]}`
	if p := b.get(indieAuthQuery("https://liar.example/c", "https://liar.example/cb", "")); p.status != http.StatusBadRequest {
		t.Fatalf("mismatched client_id = %d", p.status)
	}

	// Client identifier rules.
	for _, bad := range []string{
		"https://notes.example",        // no path
		"https://notes.example/a/../b", // dot segments
		"https://user@notes.example/",  // credentials
		"https://notes.example/#frag",  // fragment
		"https://203.0.113.9/app",      // IP address
		"ftp://notes.example/",         // scheme
	} {
		if _, err := h.srv.checkURLClientID(bad); err == nil {
			t.Errorf("client_id %q accepted", bad)
		}
	}
	if _, err := h.srv.checkURLClientID("http://127.0.0.1:8080/app"); err != nil {
		t.Errorf("loopback client_id refused: %v", err)
	}
}

func TestURLClientsAreIndieAuthOnlyByDefault(t *testing.T) {
	h := newHarness(t)
	h.metadata[iaClient] = `{"client_id":"` + iaClient + `","redirect_uris":["` + iaRedirect + `"]}`
	b := h.browser()
	// With `openid`, a URL client is an OIDC client: refused by default.
	if p := b.get(authorizeQuery(iaClient, iaRedirect, "openid")); p.status != http.StatusBadRequest {
		t.Fatalf("URL client on the OIDC surface = %d", p.status)
	}
	// And a URL client can never redeem at the token endpoint with a secret.
	res := h.token(url.Values{"grant_type": {"authorization_code"}, "code": {"x"}, "client_id": {iaClient}, "client_secret": {"s"}}, "", "")
	if res.status != 401 {
		t.Fatalf("URL client with a secret = %d", res.status)
	}

	on := newHarness(t, func(c *Config) { c.URLClients = URLClientsOn })
	on.metadata[iaClient] = `{"client_id":"` + iaClient + `","client_name":"Notes","redirect_uris":["` + iaRedirect + `"]}`
	ob := on.browser()
	id, p := ob.signIn(authorizeQuery(iaClient, iaRedirect, "openid"), on.users[alice])
	code := on.codeFrom(ob.consent(id), iaRedirect)
	_ = p
	form := codeForm(code, iaRedirect)
	form.Set("client_id", iaClient)
	res = on.token(form, "", "")
	if res.status != 200 || on.idClaims(res.body["id_token"].(string))["aud"] != iaClient {
		t.Fatalf("CIMD OIDC client = %d %v", res.status, res.body)
	}

	off := newHarness(t, func(c *Config) { c.URLClients = URLClientsOff })
	off.metadata[iaClient] = `{"client_id":"` + iaClient + `","redirect_uris":["` + iaRedirect + `"]}`
	if p := off.browser().get(indieAuthQuery(iaClient, iaRedirect, "")); p.status != http.StatusBadRequest {
		t.Fatalf("URL clients off = %d", p.status)
	}
}
