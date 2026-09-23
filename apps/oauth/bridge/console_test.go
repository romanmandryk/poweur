package bridge

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"github.com/poweur/identity/signin"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

// signInToBridge gives b a bridge session as u.
func (b *browser) signInToBridge(u user) {
	b.h.t.Helper()
	p := b.get("/login?return_to=/developers&prompt=login")
	id := txnIDFrom(b.h.t, p.location)
	b.post("/t/"+id+"/identify", url.Values{"identity": {u.id}})
	_, receipt := b.h.deliver(b.h.approve(id, u), "")
	p = b.follow(b.get(receipt.ResumeURI))
	if p.status != http.StatusOK {
		b.h.t.Fatalf("bridge sign-in = %d %s", p.status, p.body)
	}
}

var (
	clientIDPat = regexp.MustCompile(`pwc_[A-Za-z0-9]+`)
	secretPat   = regexp.MustCompile(`pws_[A-Za-z0-9_-]{43}`)
)

// register creates a secret client and returns its id and secret.
func (b *browser) register(name string, redirects ...string) (string, string) {
	b.h.t.Helper()
	p := b.post("/developers/new", url.Values{
		"name":          {name},
		"redirect_uris": {strings.Join(redirects, "\n")},
		"auth_method":   {AuthSecretBasic},
	})
	var shown clientPage
	if p.status != http.StatusOK || !p.is("client") {
		b.h.t.Fatalf("register = %d %s", p.status, p.body)
	}
	if p.data(b.h.t, &shown); shown.Secret == "" {
		b.h.t.Fatalf("register = %d %s", p.status, p.body)
	}
	return clientIDPat.FindString(p.body), secretPat.FindString(p.body)
}

func TestConsoleClientLifecycle(t *testing.T) {
	h := newHarness(t)
	dev := h.browser()
	dev.signInToBridge(h.users[alice])

	id, secret := dev.register("Team Wiki", "https://wiki.example/oauth/callback")
	if id == "" || secret == "" {
		t.Fatal("no client id or secret shown")
	}
	stored, err := h.store.GetClient(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(stored)
	if strings.Contains(string(raw), secret) {
		t.Fatal("the secret is stored in the clear")
	}
	if stored.Sector != "wiki.example" || stored.CreatedBy != alice || stored.Owners[0] != alice {
		t.Fatalf("stored client = %+v", stored)
	}
	// The secret is never shown again.
	if p := dev.get("/developers/clients/" + id); strings.Contains(p.body, secret) || !strings.Contains(p.body, id) ||
		strings.Contains(p.body, `"hash"`) || strings.Contains(p.body, signin.HashSecret(secret)) {
		t.Fatal("client page leaks the secret or its hash, or lacks the id")
	}
	if p := dev.get("/developers"); !strings.Contains(p.body, "Team Wiki") {
		t.Fatal("client missing from the list")
	}

	// A user signs in to it; the consent page names the registering ID.
	user := h.browser()
	txn, p := user.signIn(authorizeQuery(id, "https://wiki.example/oauth/callback", "openid"), h.users[bob])
	var cp consentPage
	if p.data(t, &cp); len(cp.Client.RegisteredBy) != 1 || cp.Client.RegisteredBy[0] != alice || cp.Client.VerifiedHost {
		t.Fatalf("consent page for a console client names no registering ID: %+v", cp)
	}
	code := h.codeFrom(user.consent(txn), "https://wiki.example/oauth/callback")
	res := h.token(codeForm(code, "https://wiki.example/oauth/callback"), id, secret)
	if res.status != 200 {
		t.Fatalf("token with console secret = %d %v", res.status, res.body)
	}
	sub := h.idClaims(res.body["id_token"].(string))["sub"]

	// Editing redirect URIs never changes a user's sub.
	p = dev.post("/developers/clients/"+id, url.Values{
		"name":          {"Team Wiki 2"},
		"redirect_uris": {"https://docs.other.example/cb"},
	})
	if p.status != http.StatusSeeOther {
		t.Fatalf("update = %d %s", p.status, p.body)
	}
	txn, p = user.signIn(authorizeQuery(id, "https://docs.other.example/cb", "openid", "prompt", "login"), h.users[bob])
	if p.is("consent") {
		p = user.consent(txn)
	}
	code = h.codeFrom(p, "https://docs.other.example/cb")
	res = h.token(codeForm(code, "https://docs.other.example/cb"), id, secret)
	if got := h.idClaims(res.body["id_token"].(string))["sub"]; got != sub {
		t.Fatalf("sub changed after a redirect edit: %v → %v", sub, got)
	}
	// The old redirect is gone.
	if p := user.get(authorizeQuery(id, "https://wiki.example/oauth/callback", "openid")); p.status != http.StatusBadRequest {
		t.Fatalf("removed redirect still accepted: %d", p.status)
	}

	// Rotation: new secret shown once, old one keeps working for a week.
	p = dev.post("/developers/clients/"+id+"/secrets", url.Values{})
	newSecret := secretPat.FindString(p.body)
	if newSecret == "" || newSecret == secret {
		t.Fatalf("rotation page = %s", p.body)
	}
	c, _ := h.store.GetClient(context.Background(), id)
	now := h.clock()
	if !c.checkSecret(secret, now) || !c.checkSecret(newSecret, now) {
		t.Fatal("both secrets should work during the overlap")
	}
	if c.checkSecret(secret, now.Add(secretRotationOverlap+time.Minute)) {
		t.Fatal("the old secret outlived the overlap")
	}
	// Retiring the old one ends it now; the last live one cannot be retired.
	oldID := c.Secrets[0].ID
	newID := c.Secrets[1].ID
	if p := dev.post("/developers/clients/"+id+"/secrets/"+oldID+"/retire", url.Values{}); p.status != http.StatusSeeOther {
		t.Fatalf("retire = %d %s", p.status, p.body)
	}
	if p := dev.post("/developers/clients/"+id+"/secrets/"+newID+"/retire", url.Values{}); p.status != http.StatusBadRequest {
		t.Fatalf("retiring the only secret = %d", p.status)
	}
	c, _ = h.store.GetClient(context.Background(), id)
	if c.checkSecret(secret, now) {
		t.Fatal("a retired secret still works")
	}

	// Deleting needs the id typed, then the client is gone everywhere.
	if p := dev.post("/developers/clients/"+id+"/delete", url.Values{"confirm": {"yes"}}); p.status != http.StatusBadRequest {
		t.Fatalf("delete without confirmation = %d", p.status)
	}
	if p := dev.post("/developers/clients/"+id+"/delete", url.Values{"confirm": {id}}); p.status != http.StatusSeeOther {
		t.Fatalf("delete = %d", p.status)
	}
	if p := user.get(authorizeQuery(id, "https://docs.other.example/cb", "openid")); p.status != http.StatusBadRequest {
		t.Fatalf("deleted client still authorizes: %d", p.status)
	}
	if res := h.token(codeForm("x", "https://docs.other.example/cb"), id, newSecret); res.status != 401 {
		t.Fatalf("deleted client still authenticates: %d", res.status)
	}
}

func TestConsoleOwnershipAndCoOwners(t *testing.T) {
	h := newHarness(t)
	a, b := h.browser(), h.browser()
	a.signInToBridge(h.users[alice])
	b.signInToBridge(h.users[bob])
	id, _ := a.register("Shared", "https://shared.example/cb")

	for _, path := range []string{"/developers/clients/" + id} {
		if p := b.get(path); p.status != http.StatusNotFound {
			t.Fatalf("non-owner GET %s = %d", path, p.status)
		}
	}
	if p := b.post("/developers/clients/"+id+"/delete", url.Values{"confirm": {id}}); p.status != http.StatusNotFound {
		t.Fatalf("non-owner delete = %d", p.status)
	}

	// Alice adds Bob; Bob can now manage it but cannot drop himself.
	p := a.post("/developers/clients/"+id, url.Values{
		"name": {"Shared"}, "redirect_uris": {"https://shared.example/cb"}, "co_owners": {"@Bob.Poweur.net"},
	})
	if p.status != http.StatusSeeOther {
		t.Fatalf("add co-owner = %d %s", p.status, p.body)
	}
	if p := b.get("/developers/clients/" + id); p.status != http.StatusOK {
		t.Fatalf("co-owner GET = %d", p.status)
	}
	p = b.post("/developers/clients/"+id, url.Values{"name": {"Shared"}, "redirect_uris": {"https://shared.example/cb"}})
	if p.status != http.StatusBadRequest || !strings.Contains(p.body, "cannot remove yourself") {
		t.Fatalf("co-owner removing self = %d %s", p.status, p.body)
	}
	// Bad co-owner input is refused.
	p = a.post("/developers/clients/"+id, url.Values{"name": {"Shared"}, "redirect_uris": {"https://shared.example/cb"}, "co_owners": {"not an id"}})
	if p.status != http.StatusBadRequest {
		t.Fatalf("bad co-owner = %d", p.status)
	}
}

func TestConsoleValidationAndLimits(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.MaxClientsPerOwner = 2 })
	dev := h.browser()
	dev.signInToBridge(h.users[alice])

	for name, form := range map[string]url.Values{
		"no name":         {"redirect_uris": {"https://a.example/cb"}, "auth_method": {AuthSecretBasic}},
		"script redirect": {"name": {"X"}, "redirect_uris": {"javascript:alert(1)"}, "auth_method": {AuthSecretBasic}},
		"fragment":        {"name": {"X"}, "redirect_uris": {"https://a.example/cb#x"}, "auth_method": {AuthSecretBasic}},
		"no redirect":     {"name": {"X"}, "auth_method": {AuthSecretBasic}},
		"no method":       {"name": {"X"}, "redirect_uris": {"https://a.example/cb"}},
		"jwt, no keys":    {"name": {"X"}, "redirect_uris": {"https://a.example/cb"}, "auth_method": {AuthPrivateJWT}},
		"private key":     {"name": {"X"}, "redirect_uris": {"https://a.example/cb"}, "auth_method": {AuthPrivateJWT}, "jwks": {`{"kty":"OKP","crv":"Ed25519","x":"AAAA","d":"secret"}`}},
		"html in name":    {"name": {"<b>X</b>"}, "redirect_uris": {"https://a.example/cb"}, "auth_method": {AuthSecretBasic}},
		"bad sector":      {"name": {"X"}, "redirect_uris": {"https://a.example/cb"}, "auth_method": {AuthSecretBasic}, "sector": {"https://a.example"}},
	} {
		if p := dev.post("/developers/new", form); p.status != http.StatusBadRequest {
			t.Errorf("%s: %d", name, p.status)
		}
	}

	// A public client and a JWT client register without secrets.
	pub, priv, _ := ed25519.GenerateKey(nil)
	_ = priv
	jwk := `{"kty":"OKP","crv":"Ed25519","x":"` + base64.RawURLEncoding.EncodeToString(pub) + `"}`
	p := dev.post("/developers/new", url.Values{"name": {"JWT"}, "redirect_uris": {"https://j.example/cb"}, "auth_method": {AuthPrivateJWT}, "jwks": {jwk}})
	if p.status != http.StatusOK || secretPat.MatchString(p.body) {
		t.Fatalf("jwt client = %d", p.status)
	}
	p = dev.post("/developers/new", url.Values{"name": {"SPA"}, "redirect_uris": {"https://s.example/cb"}, "auth_method": {AuthNone}})
	if p.status != http.StatusOK || secretPat.MatchString(p.body) {
		t.Fatalf("public client = %d", p.status)
	}
	// The limit.
	p = dev.post("/developers/new", url.Values{"name": {"Third"}, "redirect_uris": {"https://t.example/cb"}, "auth_method": {AuthNone}})
	if p.status != http.StatusBadRequest || !strings.Contains(p.body, "at most 2") {
		t.Fatalf("over the limit = %d %s", p.status, p.body)
	}
}

func TestConsoleRateLimit(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.MaxClientsPerOwner = 100 })
	dev := h.browser()
	dev.signInToBridge(h.users[alice])
	for i := 0; i < clientCreatesPerHour; i++ {
		dev.register("App", "https://a.example/cb")
	}
	p := dev.post("/developers/new", url.Values{"name": {"One more"}, "redirect_uris": {"https://a.example/cb"}, "auth_method": {AuthNone}})
	if p.status != http.StatusBadRequest || !strings.Contains(p.body, "too quickly") {
		t.Fatalf("burst = %d", p.status)
	}
	h.advance(time.Hour + time.Minute)
	dev.register("Later", "https://a.example/cb")
}

func TestRegistrationPostures(t *testing.T) {
	closed := newHarness(t, func(c *Config) { c.ClientRegistration = RegistrationClosed })
	b := closed.browser()
	b.signInToBridge(closed.users[alice])
	if p := b.get("/developers/new"); p.status != http.StatusForbidden {
		t.Fatalf("closed new form = %d", p.status)
	}
	if p := b.post("/developers/new", url.Values{"name": {"X"}}); p.status != http.StatusForbidden {
		t.Fatalf("closed create = %d", p.status)
	}

	allow := newHarness(t, func(c *Config) {
		c.ClientRegistration = RegistrationAllowlist
		c.RegistrationAllowlist = []string{"*.poweur.net"}
	})
	b = allow.browser()
	b.signInToBridge(allow.users[alice])
	b.register("Allowed", "https://a.example/cb")
	if ok, _ := allow.srv.canRegister("mallory.example.com"); ok {
		t.Fatal("allowlist admitted an outsider")
	}
	if ok, _ := allow.srv.canRegister("evilpoweur.net"); ok {
		t.Fatal("suffix match must respect the label boundary")
	}
}

func TestSuspendedClientCannotSignAnyoneIn(t *testing.T) {
	h := newHarness(t)
	dev := h.browser()
	dev.signInToBridge(h.users[alice])
	id, secret := dev.register("Phishy", "https://phish.example/cb")
	c, _ := h.store.GetClient(context.Background(), id)
	c.Suspended = true
	c.SuspendedReason = "impersonation"
	if err := h.store.PutClient(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	p := h.browser().get(authorizeQuery(id, "https://phish.example/cb", "openid"))
	if p.status != http.StatusBadRequest || !strings.Contains(p.body, "suspended") {
		t.Fatalf("suspended authorize = %d %s", p.status, p.body)
	}
	if res := h.token(codeForm("x", "https://phish.example/cb"), id, secret); res.status != 401 {
		t.Fatalf("suspended token = %d", res.status)
	}
}

func TestAccountRevoke(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	res := h.fullFlow(b, h.users[alice], "openid poweur_id", ScopePoweurID)
	p := b.get("/account")
	var ap accountPage
	if p.data(t, &ap); len(ap.Consents) != 1 || ap.Consents[0].ClientName != "Relying Party" ||
		len(ap.Consents[0].Granted) != 1 || ap.Consents[0].Granted[0] != ScopePoweurID || len(ap.SignIns) == 0 {
		t.Fatalf("account page = %+v", ap)
	}
	p = b.post("/account/revoke", url.Values{"client_id": {"rp"}})
	if p.status != http.StatusSeeOther {
		t.Fatalf("revoke = %d", p.status)
	}
	// The access token died with the consent.
	req, _ := http.NewRequest(http.MethodGet, h.issuer+"/userinfo", nil)
	req.Header.Set("Authorization", "Bearer "+res.body["access_token"].(string))
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("token after revoke = %d", resp.StatusCode)
	}
	// And the app has to ask again.
	if p := b.follow(b.get(authorizeQuery("rp", rpRedirect, "openid"))); !p.is("consent") || p.shown().Title != "Allow Relying Party?" {
		t.Fatal("no consent after revoke")
	}
	// Revoking needs a same-origin form and a session.
	other := h.browser()
	if p := other.post("/account/revoke", url.Values{"client_id": {"rp"}}); p.status != http.StatusUnauthorized {
		t.Fatalf("revoke without session = %d", p.status)
	}
}

func TestPagesRender(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	for path, want := range map[string]string{
		"/":         "home",
		"/abuse":    "abuse",
		"/privacy":  "privacy",
		"/security": "security",
	} {
		if p := b.get(path); p.status != 200 || !p.is(want) {
			t.Errorf("%s = %d, not the %s page", path, p.status, want)
		}
	}
	if p := b.get("/health"); !strings.Contains(p.body, `"status":"ok"`) {
		t.Errorf("health = %s", p.body)
	}
	if p := b.get("/nope"); p.status != http.StatusNotFound {
		t.Errorf("unknown path = %d", p.status)
	}
}
