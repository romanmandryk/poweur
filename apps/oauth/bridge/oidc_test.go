package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/poweur/identity"
)

func TestDiscoveryAndJWKS(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	p := b.get("/.well-known/openid-configuration")
	if p.status != http.StatusOK {
		t.Fatalf("discovery = %d", p.status)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(p.body), &doc); err != nil {
		t.Fatal(err)
	}
	if doc["issuer"] != h.issuer || doc["authorization_endpoint"] != h.issuer+"/authorize" {
		t.Fatalf("discovery = %v", doc)
	}
	if doc["authorization_response_iss_parameter_supported"] != true {
		t.Fatal("RFC 9207 support must be advertised")
	}
	if got := doc["code_challenge_methods_supported"].([]any); len(got) != 1 || got[0] != "S256" {
		t.Fatalf("PKCE methods = %v", got)
	}
	if got := doc["response_types_supported"].([]any); len(got) != 1 || got[0] != "code" {
		t.Fatalf("response types = %v", got)
	}
	if b.get("/.well-known/oauth-authorization-server").status != http.StatusOK {
		t.Fatal("RFC 8414 alias missing")
	}

	var set JWKS
	if err := json.Unmarshal([]byte(b.get("/jwks.json").body), &set); err != nil || len(set.Keys) != 1 {
		t.Fatalf("jwks = %v %v", set, err)
	}
	k := set.Keys[0]
	if k.Kty != "RSA" || k.Alg != "RS256" || k.Kid == "" || k.D != "" || k.P != "" {
		t.Fatalf("published key = %+v", k)
	}
}

func TestCodeFlowEndToEnd(t *testing.T) {
	h := newHarness(t)
	b := h.browser()

	res := h.fullFlow(b, h.users[alice], "openid")
	claims := h.idClaims(res.body["id_token"].(string))
	if claims["iss"] != h.issuer || claims["aud"] != "rp" || claims["nonce"] != sampleNonce {
		t.Fatalf("claims = %v", claims)
	}
	sub, _ := claims["sub"].(string)
	if sub == "" || strings.Contains(sub, "alice") {
		t.Fatalf("sub must be opaque, got %q", sub)
	}
	if _, ok := claims["poweur_id"]; ok {
		t.Fatal("the Poweur ID was released without being asked for")
	}
	if amr := claims["amr"].([]any); len(amr) != 1 || amr[0] != "poweur" {
		t.Fatalf("amr = %v", amr)
	}
	exp := int64(claims["exp"].(float64))
	if exp != h.clock().Add(idTokenTTL).Unix() {
		t.Fatalf("exp = %d", exp)
	}
	if res.body["token_type"] != "Bearer" || res.body["scope"] != "openid" {
		t.Fatalf("token response = %v", res.body)
	}

	// userinfo answers for the access token.
	req, _ := http.NewRequest(http.MethodGet, h.issuer+"/userinfo", nil)
	req.Header.Set("Authorization", "Bearer "+res.body["access_token"].(string))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var info map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&info)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || info["sub"] != sub {
		t.Fatalf("userinfo = %d %v", resp.StatusCode, info)
	}

	// A sign-in shows up in the user's history, with the client.
	recs, err := h.store.RecentSignIns(context.Background(), alice, 5)
	if err != nil || len(recs) != 1 || recs[0].ClientID != "rp" || recs[0].Finish != FinishSameDevice {
		t.Fatalf("sign-ins = %+v %v", recs, err)
	}
}

func TestReleasedClaimsAndRememberedConsent(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	u := h.users[alice]

	res := h.fullFlow(b, u, "openid poweur_id profile", ScopePoweurID, ScopeProfile)
	claims := h.idClaims(res.body["id_token"].(string))
	fp, _ := identity.KeyFingerprint(u.doc.PublicKey)
	if claims["poweur_id"] != alice || claims["poweur_key_fingerprint"] != fp || claims["poweur_id_url"] != "https://alice.poweur.net/" {
		t.Fatalf("poweur claims = %v", claims)
	}
	if claims["name"] != "Display "+alice || claims["picture"] != "https://alice.poweur.net/pub/avatar.png" {
		t.Fatalf("profile claims = %v", claims)
	}

	// The same browser, the same request: no signature, no consent page.
	p := b.get(authorizeQuery("rp", rpRedirect, "openid poweur_id profile"))
	code := h.codeFrom(p, rpRedirect)
	again := h.token(codeForm(code, rpRedirect), "rp", rpSecret)
	if again.status != http.StatusOK {
		t.Fatalf("SSO token = %d %v", again.status, again.body)
	}
	c2 := h.idClaims(again.body["id_token"].(string))
	if c2["sub"] != claims["sub"] || c2["poweur_id"] != alice {
		t.Fatalf("SSO claims differ: %v vs %v", c2, claims)
	}
	if c2["auth_time"] != claims["auth_time"] {
		t.Fatal("auth_time must be the time of the signature, not of the SSO")
	}

	// prompt=consent asks again.
	p = b.get(authorizeQuery("rp", rpRedirect, "openid poweur_id", "prompt", "consent"))
	if !strings.Contains(b.follow(p).body, "Allow Relying Party?") {
		t.Fatal("prompt=consent did not show the consent page")
	}
}

func TestDeclinedScopesStayDeclinedWithoutNagging(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	// Released nothing optional.
	res := h.fullFlow(b, h.users[alice], "openid poweur_id")
	if _, ok := h.idClaims(res.body["id_token"].(string))["poweur_id"]; ok {
		t.Fatal("an unticked scope was released")
	}
	// Asked again for the same thing: decided, so no page, still not released.
	code := h.codeFrom(b.get(authorizeQuery("rp", rpRedirect, "openid poweur_id")), rpRedirect)
	again := h.token(codeForm(code, rpRedirect), "rp", rpSecret)
	if _, ok := h.idClaims(again.body["id_token"].(string))["poweur_id"]; ok {
		t.Fatal("a declined scope was released on SSO")
	}
	// A scope never asked about does prompt.
	p := b.follow(b.get(authorizeQuery("rp", rpRedirect, "openid profile")))
	if !strings.Contains(p.body, "public profile") {
		t.Fatalf("a new scope must be asked for: %s", p.body)
	}
}

func TestPromptAndMaxAge(t *testing.T) {
	h := newHarness(t)
	b := h.browser()

	// No session: prompt=none fails at the client.
	if got := h.errorFrom(b.get(authorizeQuery("rp", rpRedirect, "openid", "prompt", "none")), rpRedirect); got != "login_required" {
		t.Fatalf("prompt=none without session = %s", got)
	}
	h.fullFlow(b, h.users[alice], "openid")

	// Session + consent: prompt=none succeeds.
	h.codeFrom(b.get(authorizeQuery("rp", rpRedirect, "openid", "prompt", "none")), rpRedirect)
	// Session but a scope never decided: consent_required.
	if got := h.errorFrom(b.get(authorizeQuery("rp", rpRedirect, "openid profile", "prompt", "none")), rpRedirect); got != "consent_required" {
		t.Fatalf("prompt=none needing consent = %s", got)
	}
	// prompt=login ignores the session.
	p := b.follow(b.get(authorizeQuery("rp", rpRedirect, "openid", "prompt", "login")))
	if !strings.Contains(p.body, `name="identity"`) {
		t.Fatal("prompt=login must ask for a new signature")
	}
	// max_age shorter than the session's age also does.
	h.advance(2 * time.Minute)
	p = b.follow(b.get(authorizeQuery("rp", rpRedirect, "openid", "max_age", "60")))
	if !strings.Contains(p.body, `name="identity"`) {
		t.Fatal("max_age must force a new signature")
	}
	// A login_hint for someone else ignores the session too.
	p = b.follow(b.get(authorizeQuery("rp", rpRedirect, "openid", "login_hint", bob)))
	if !strings.Contains(p.body, `value="`+bob+`"`) {
		t.Fatalf("login_hint must prefill: %s", p.body)
	}
}

func TestFirstPartyClientSkipsConsentForOpenIDOnly(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	_, p := b.signIn(authorizeQuery("internal", "https://tool.example/cb", "openid"), h.users[alice])
	h.codeFrom(p, "https://tool.example/cb")
	_, p = b.signIn(authorizeQuery("internal", "https://tool.example/cb", "openid poweur_id", "prompt", "login"), h.users[alice])
	if !strings.Contains(p.body, "Allow Internal Tool?") {
		t.Fatal("a first-party client must still ask before releasing the Poweur ID")
	}
}

func TestAuthorizeRefusesBeforeTrustingTheRedirect(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	cases := map[string]string{
		"unknown client":       authorizeQuery("nope", rpRedirect, "openid"),
		"missing client":       authorizeQuery("", rpRedirect, "openid", "client_id", ""),
		"unregistered target":  authorizeQuery("rp", "https://evil.example/cb", "openid"),
		"prefix of registered": authorizeQuery("rp", rpRedirect+"/../evil", "openid"),
		"missing redirect":     authorizeQuery("rp", "", "openid", "redirect_uri", ""),
		"loopback for secret":  authorizeQuery("rp", "http://127.0.0.1:9999/callback", "openid"),
	}
	for name, path := range cases {
		p := b.get(path)
		if p.status != http.StatusBadRequest || p.location != "" {
			t.Errorf("%s: %d %q — must be a page, never a redirect", name, p.status, p.location)
		}
	}
	// Duplicated parameters are refused outright.
	p := b.get(authorizeQuery("rp", rpRedirect, "openid") + "&state=second")
	if p.status != http.StatusBadRequest || p.location != "" {
		t.Errorf("duplicate state: %d %q", p.status, p.location)
	}
}

func TestAuthorizeErrorsGoToTheClient(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	cases := []struct {
		name  string
		path  string
		error string
	}{
		{"implicit", authorizeQuery("rp", rpRedirect, "openid", "response_type", "token"), "unsupported_response_type"},
		{"no openid", authorizeQuery("rp", rpRedirect, "profile"), "invalid_scope"},
		{"no pkce", authorizeQuery("rp", rpRedirect, "openid", "code_challenge", ""), "invalid_request"},
		{"plain pkce", authorizeQuery("rp", rpRedirect, "openid", "code_challenge_method", "plain"), "invalid_request"},
		{"request object", authorizeQuery("rp", rpRedirect, "openid", "request", "eyJ"), "request_not_supported"},
		{"fragment mode", authorizeQuery("rp", rpRedirect, "openid", "response_mode", "fragment"), "invalid_request"},
		{"bad prompt", authorizeQuery("rp", rpRedirect, "openid", "prompt", "nonsense"), "invalid_request"},
	}
	for _, tc := range cases {
		if got := h.errorFrom(b.get(tc.path), rpRedirect); got != tc.error {
			t.Errorf("%s: error = %s, want %s", tc.name, got, tc.error)
		}
	}
	// No state: the error still goes to the client, without state.
	p := b.get(authorizeQuery("rp", rpRedirect, "openid", "state", ""))
	u, _ := url.Parse(p.location)
	if p.status != http.StatusSeeOther || u.Query().Get("error") != "invalid_request" {
		t.Fatalf("missing state = %d %s", p.status, p.location)
	}
}

func TestPublicClientLoopbackAnyPort(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	redirect := "http://127.0.0.1:53124/cb"
	_, p := b.signIn(authorizeQuery("spa", redirect, "openid"), h.users[alice])
	p = b.consent(txnIDFrom(t, p.body))
	code := h.codeFrom(p, redirect)
	form := codeForm(code, redirect)
	form.Set("client_id", "spa")
	res := h.token(form, "", "")
	if res.status != http.StatusOK {
		t.Fatalf("public token = %d %v", res.status, res.body)
	}
}

func TestTokenEndpointRefusals(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	newCode := func() string {
		t.Helper()
		id, p := b.signIn(authorizeQuery("rp", rpRedirect, "openid", "prompt", "login"), h.users[alice])
		if strings.Contains(p.body, "/consent") {
			p = b.consent(id)
		}
		return h.codeFrom(p, rpRedirect)
	}

	code := newCode()
	for name, tc := range map[string]struct {
		form   url.Values
		id     string
		secret string
		status int
		error  string
	}{
		"wrong secret":     {codeForm(code, rpRedirect), "rp", "nope", 401, "invalid_client"},
		"no auth":          {codeForm(code, rpRedirect), "", "", 401, "invalid_client"},
		"unknown client":   {codeForm(code, rpRedirect), "ghost", rpSecret, 401, "invalid_client"},
		"wrong grant type": {func() url.Values { f := codeForm(code, rpRedirect); f.Set("grant_type", "refresh_token"); return f }(), "rp", rpSecret, 400, "unsupported_grant_type"},
	} {
		res := h.token(tc.form, tc.id, tc.secret)
		if res.status != tc.status || res.body["error"] != tc.error {
			t.Errorf("%s: %d %v", name, res.status, res.body)
		}
	}
	// None of the refused attempts above redeemed the code.
	for name, mutate := range map[string]func(url.Values){
		"wrong verifier": func(f url.Values) { f.Set("code_verifier", strings.Repeat("x", 43)) },
		"wrong redirect": func(f url.Values) { f.Set("redirect_uri", rpRedirect+"x") },
	} {
		c := newCode()
		f := codeForm(c, rpRedirect)
		mutate(f)
		if res := h.token(f, "rp", rpSecret); res.status != 400 || res.body["error"] != "invalid_grant" {
			t.Errorf("%s: %d %v", name, res.status, res.body)
		}
	}

	// Another client cannot redeem rp's code.
	c := newCode()
	f := codeForm(c, rpRedirect)
	if res := h.token(f, "internal", rpSecret); res.status != 400 || res.body["error"] != "invalid_grant" {
		t.Errorf("cross-client redemption: %d %v", res.status, res.body)
	}

	// Reuse revokes what the first redemption issued.
	c = newCode()
	first := h.token(codeForm(c, rpRedirect), "rp", rpSecret)
	if first.status != 200 {
		t.Fatalf("first redemption = %d %v", first.status, first.body)
	}
	if res := h.token(codeForm(c, rpRedirect), "rp", rpSecret); res.status != 400 || res.body["error"] != "invalid_grant" {
		t.Fatalf("reuse = %d %v", res.status, res.body)
	}
	req, _ := http.NewRequest(http.MethodGet, h.issuer+"/userinfo", nil)
	req.Header.Set("Authorization", "Bearer "+first.body["access_token"].(string))
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("token from a reused code still works: %d", resp.StatusCode)
	}

	// Codes expire.
	c = newCode()
	h.advance(codeTTL + time.Second)
	if res := h.token(codeForm(c, rpRedirect), "rp", rpSecret); res.status != 400 {
		t.Fatalf("expired code = %d %v", res.status, res.body)
	}

	// A public client may not present a secret.
	form := codeForm("x", spaRedirect)
	form.Set("client_id", "spa")
	form.Set("client_secret", "anything")
	if res := h.token(form, "", ""); res.status != 401 {
		t.Fatalf("public client with secret = %d", res.status)
	}
	// Secret in the body works for a basic client too (servers vary).
	c = newCode()
	f = codeForm(c, rpRedirect)
	f.Set("client_id", "rp")
	f.Set("client_secret", rpSecret)
	if res := h.token(f, "", ""); res.status != 200 {
		t.Fatalf("client_secret_post = %d %v", res.status, res.body)
	}
}

func TestUserInfoAndRevoke(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	res := h.fullFlow(b, h.users[alice], "openid poweur_id", ScopePoweurID)
	token := res.body["access_token"].(string)

	get := func() (int, map[string]any) {
		req, _ := http.NewRequest(http.MethodPost, h.issuer+"/userinfo", strings.NewReader("access_token="+url.QueryEscape(token)))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		out := map[string]any{}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	if status, info := get(); status != 200 || info["poweur_id"] != alice {
		t.Fatalf("userinfo = %d %v", status, info)
	}

	// Introspection, for the owning client only.
	intro := func(id, secret string) map[string]any {
		req, _ := http.NewRequest(http.MethodPost, h.issuer+"/introspect", strings.NewReader("token="+url.QueryEscape(token)))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetBasicAuth(id, secret)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		out := map[string]any{}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return out
	}
	if got := intro("rp", rpSecret); got["active"] != true || got["me"] != "https://alice.poweur.net/" {
		t.Fatalf("introspect = %v", got)
	}
	if got := intro("internal", rpSecret); got["active"] != false {
		t.Fatalf("another client introspected rp's token: %v", got)
	}

	req, _ := http.NewRequest(http.MethodPost, h.issuer+"/revoke", strings.NewReader("token="+url.QueryEscape(token)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("rp", rpSecret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("revoke = %d", resp.StatusCode)
	}
	if status, _ := get(); status != http.StatusUnauthorized {
		t.Fatalf("revoked token still works: %d", status)
	}
	h.advance(accessTokenTTL)
	if status, _ := get(); status != http.StatusUnauthorized {
		t.Fatal("expired token works")
	}
}

func TestSubjects(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	a := pairwiseSubject(secret, "rp.example", alice)
	if a != pairwiseSubject(secret, "rp.example", alice) {
		t.Fatal("pairwise subject is not stable")
	}
	if a == pairwiseSubject(secret, "other.example", alice) {
		t.Fatal("two sectors share a subject")
	}
	if a == pairwiseSubject(secret, "rp.example", bob) {
		t.Fatal("two users share a subject")
	}
	if a == pairwiseSubject([]byte("another secret, another issuer!!"), "rp.example", alice) {
		t.Fatal("two issuers share a subject")
	}

	pub := newHarness(t, func(c *Config) { c.SubjectType = SubjectPublic })
	res := pub.fullFlow(pub.browser(), pub.users[alice], "openid")
	if got := pub.idClaims(res.body["id_token"].(string))["sub"]; got != alice {
		t.Fatalf("public sub = %v", got)
	}
}

func TestDenyAndCancelGoBackToTheClient(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	id, _ := b.signIn(authorizeQuery("rp", rpRedirect, "openid"), h.users[alice])
	p := b.post("/t/"+id+"/consent", url.Values{"decision": {"deny"}})
	if got := h.errorFrom(p, rpRedirect); got != "access_denied" {
		t.Fatalf("deny = %s", got)
	}
	// The transaction cannot be revived.
	if p := b.consent(id); p.status == http.StatusSeeOther {
		t.Fatal("a denied transaction issued a code")
	}

	p = b.get(authorizeQuery("rp", rpRedirect, "openid", "prompt", "login"))
	id = txnIDFrom(t, p.location)
	if got := h.errorFrom(b.post("/t/"+id+"/cancel", url.Values{}), rpRedirect); got != "access_denied" {
		t.Fatalf("cancel = %s", got)
	}
}

func TestConsentRequiresTheSameSignedInIdentity(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	id, p := b.signIn(authorizeQuery("rp", rpRedirect, "openid"), h.users[alice])
	if !strings.Contains(p.body, "/consent") {
		t.Fatal("expected consent")
	}
	// The browser signs out in another tab before deciding.
	b.post("/logout", url.Values{})
	if p := b.consent(id); p.status != http.StatusForbidden {
		t.Fatalf("consent after sign-out = %d", p.status)
	}
}

func TestCrossSiteFormPostsAreRefused(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	p := b.get(authorizeQuery("rp", rpRedirect, "openid"))
	id := txnIDFrom(t, p.location)
	req, _ := http.NewRequest(http.MethodPost, h.issuer+"/t/"+id+"/identify", strings.NewReader("identity="+alice))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example")
	resp, err := b.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin identify = %d", resp.StatusCode)
	}
	req, _ = http.NewRequest(http.MethodPost, h.issuer+"/t/"+id+"/identify", strings.NewReader("identity="+alice))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "same-site")
	resp, err = b.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("same-site sibling identify = %d", resp.StatusCode)
	}
}

func TestNonceIsOptionalAndEchoed(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	id, p := b.signIn(authorizeQuery("rp", rpRedirect, "openid", "nonce", ""), h.users[alice])
	if strings.Contains(p.body, "/consent") {
		p = b.consent(id)
	}
	res := h.token(codeForm(h.codeFrom(p, rpRedirect), rpRedirect), "rp", rpSecret)
	if res.status != 200 {
		t.Fatalf("token = %d %v", res.status, res.body)
	}
	if _, ok := h.idClaims(res.body["id_token"].(string))["nonce"]; ok {
		t.Fatal("a nonce appeared that the client never sent")
	}
	if got := h.errorFrom(b.get(authorizeQuery("rp", rpRedirect, "openid", "nonce", strings.Repeat("n", 513))), rpRedirect); got != "invalid_request" {
		t.Fatalf("oversized nonce = %s", got)
	}
}
