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
	"github.com/poweur/identity/signin"
)

// identify starts an authorization in b and identifies as u, returning the
// transaction id.
func (b *browser) identify(path string, u user) string {
	b.h.t.Helper()
	p := b.get(path)
	id := txnIDFrom(b.h.t, p.location)
	p = b.post("/t/"+id+"/identify", url.Values{"identity": {u.id}})
	if p.status != http.StatusSeeOther {
		b.h.t.Fatalf("identify = %d %s", p.status, p.body)
	}
	return id
}

func (b *browser) status(id string) Status {
	b.h.t.Helper()
	p := b.get("/t/" + id + "/status")
	var st Status
	if err := json.Unmarshal([]byte(p.body), &st); err != nil {
		b.h.t.Fatalf("status %q: %v", p.body, err)
	}
	return st
}

func TestNativeMetadataIsWhatSignersAccept(t *testing.T) {
	h := newHarness(t)
	meta := h.srv.nativeMetadata()
	if err := meta.Validate(h.issuer); err != nil {
		t.Fatalf("bridge metadata would be refused by a signer: %v", err)
	}
	p := h.browser().get(signin.MetadataPath)
	if p.status != 200 || p.header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("metadata = %d %v", p.status, p.header)
	}
}

func TestAwaitPageOffersSignersCodeAndQR(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	id := b.identify(authorizeQuery("rp", rpRedirect, "openid"), h.users[alice])
	p := b.get("/t/" + id)
	txn := h.txn(id)
	for _, want := range []string{
		"Approve as " + alice,
		"https://alice.poweur.net/app/?auth=",      // the identity's own signer, from capabilities
		"https://poweur.net/app/?auth=",            // the operator default
		"Works only if this browser already holds", // …labelled for what it is
		"poweur://auth?request=",                   // deep link
		`<svg xmlns="http://www.w3.org/2000/svg"`,  // QR
		`class="match"`,                            // match code…
		txn.Match,                                  // …with its value
		"Signing in to Relying Party",
	} {
		if !strings.Contains(p.body, want) {
			t.Errorf("await page lacks %q", want)
		}
	}
	if len(txn.Match) != signin.MatchCodeDigits {
		t.Fatalf("match = %q", txn.Match)
	}
	if b.status(id).Status != "pending" {
		t.Fatal("status before approval should be pending")
	}
}

func TestIdentifyRefusesBadAndUnknownIDs(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	p := b.get(authorizeQuery("rp", rpRedirect, "openid"))
	id := txnIDFrom(t, p.location)
	for input, want := range map[string]string{
		"not an id":                     "does not look like",
		"https://alice.poweur.net/blog": "root of the identity",
		"carol.poweur.net":              "No Poweur ID named carol.poweur.net",
		"www.poweur.net":                "reserved",
	} {
		p := b.post("/t/"+id+"/identify", url.Values{"identity": {input}})
		if p.status != http.StatusBadRequest || !strings.Contains(p.body, want) {
			t.Errorf("%q: %d, want %q in %s", input, p.status, want, p.body)
		}
	}
	// Normalization: a pasted profile URL works.
	p = b.post("/t/"+id+"/identify", url.Values{"identity": {"HTTPS://Alice.Poweur.net/"}})
	if p.status != http.StatusSeeOther || h.txn(id).Identity != alice {
		t.Fatalf("profile URL identify = %d %v", p.status, h.txn(id).Identity)
	}
}

// Mallory starts a sign-in and sends Alice the signer link; Alice approves
// and her signer sends her browser to the resume URI. Nobody is signed in.
func TestForwardedSignInLinkSignsNobodyIn(t *testing.T) {
	h := newHarness(t)
	mallory, victim := h.browser(), h.browser()
	id := mallory.identify(authorizeQuery("rp", rpRedirect, "openid"), h.users[alice])

	status, receipt := h.deliver(h.approve(id, h.users[alice]), "")
	if status != 200 || receipt.ResumeURI == "" {
		t.Fatalf("callback = %d %+v", status, receipt)
	}
	if strings.Contains(receipt.ResumeURI, id) {
		t.Fatal("the resume URI must not be derivable from the transaction id")
	}
	if got := mallory.status(id).Status; got != "approved" {
		t.Fatalf("before resume, the starting browser sees %q", got)
	}

	p := victim.get(receipt.ResumeURI)
	if p.status != http.StatusForbidden || !strings.Contains(p.body, "different browser") {
		t.Fatalf("resume in the wrong browser = %d %s", p.status, p.body)
	}
	for _, c := range p.header.Values("Set-Cookie") {
		if strings.HasPrefix(c, sessionCookie+"=") {
			t.Fatal("the victim's browser was given a session")
		}
	}
	st := mallory.status(id)
	if st.Status != "failed" || st.Next != "" {
		t.Fatalf("the starting browser after a wrong-browser resume: %+v", st)
	}
	if p := mallory.get("/t/" + id + "/continue"); p.status == http.StatusSeeOther && strings.Contains(p.location, "code=") {
		t.Fatal("the attacker got a code")
	}
	// The link is spent even for the right browser now.
	if p := mallory.get(receipt.ResumeURI); p.status != http.StatusGone {
		t.Fatalf("second use of a resume code = %d", p.status)
	}
	if s, _ := mallory.client.Jar.Cookies(mustURL(h.issuer)), 0; hasCookie(s, sessionCookie) {
		t.Fatal("the attacker's browser holds a session")
	}
}

func mustURL(s string) *url.URL { u, _ := url.Parse(s); return u }

func hasCookie(cs []*http.Cookie, name string) bool {
	for _, c := range cs {
		if c.Name == name && c.Value != "" {
			return true
		}
	}
	return false
}

func TestCrossDeviceApprovalFinishesThroughThePoll(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	id := b.identify(authorizeQuery("rp", rpRedirect, "openid"), h.users[alice])
	match := h.txn(id).Match

	status, receipt := h.deliver(h.approve(id, h.users[alice]), " "+match+" ")
	if status != 200 || receipt.ResumeURI != "" || receipt.Identity != alice {
		t.Fatalf("cross-device callback = %d %+v", status, receipt)
	}
	st := b.status(id)
	if st.Status != "complete" || st.Next != "/t/"+id+"/continue" {
		t.Fatalf("status = %+v", st)
	}
	// A bystander's browser polling the same id gets nothing.
	if st := h.browser().status(id); st.Status != "failed" || st.Next != "" {
		t.Fatalf("bystander status = %+v", st)
	}
	p := b.follow(b.get(st.Next))
	if !strings.Contains(p.body, "Allow Relying Party?") {
		t.Fatalf("expected consent, got %d %s", p.status, p.body)
	}
	h.codeFrom(b.consent(id), rpRedirect)
	recs, _ := h.store.RecentSignIns(context.Background(), alice, 1)
	if len(recs) != 1 || recs[0].Finish != FinishCrossDevice {
		t.Fatalf("sign-in record = %+v", recs)
	}
}

func TestWrongMatchCodeEndsTheSignIn(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	id := b.identify(authorizeQuery("rp", rpRedirect, "openid"), h.users[alice])
	wrong := "00"
	if h.txn(id).Match == wrong {
		wrong = "01"
	}
	approval := h.approve(id, h.users[alice])
	if status, _ := h.deliver(approval, wrong); status != http.StatusForbidden {
		t.Fatalf("wrong code = %d", status)
	}
	if status, _ := h.deliver(approval, h.txn(id).Match); status == 200 {
		t.Fatal("a second guess was accepted")
	}
	if st := b.status(id); st.Status != "failed" {
		t.Fatalf("status = %+v", st)
	}
}

func TestApprovalForAnotherIdentityIsRefused(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	id := b.identify(authorizeQuery("rp", rpRedirect, "openid"), h.users[alice])
	status, _ := h.deliver(h.approve(id, h.users[bob]), "")
	if status != http.StatusUnauthorized {
		t.Fatalf("bob approving alice's sign-in = %d", status)
	}
	if st := b.status(id); st.Status != "failed" || !strings.Contains(st.Error, bob) {
		t.Fatalf("status = %+v", st)
	}
}

func TestCallbackRefusals(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	id := b.identify(authorizeQuery("rp", rpRedirect, "openid"), h.users[alice])
	approval := h.approve(id, h.users[alice])

	// Approvals in URLs are refused and not spent.
	resp, err := http.Get(h.issuer + "/poweur/callback?response=" + url.QueryEscape(approval))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("GET callback = %d", resp.StatusCode)
	}
	// Garbage, and approvals for requests nobody is waiting on.
	for _, body := range []string{"", "%%%%", `{"response":"eyJub3QiOiJhIHJlc3BvbnNlIn0"}`} {
		r, err := http.Post(h.issuer+"/poweur/callback", "text/plain", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode == 200 {
			t.Fatalf("body %q accepted", body)
		}
	}
	// A tampered approval fails the transaction.
	resp2, _ := identity.DecodeSignInResponse(approval)
	resp2.Statement = "something else"
	tampered, _ := identity.EncodeSignInResponse(resp2)
	if status, _ := h.deliver(tampered, ""); status != http.StatusUnauthorized {
		t.Fatalf("tampered = %d", status)
	}
	// …after which even the genuine one is refused: one approval per sign-in.
	if status, _ := h.deliver(approval, ""); status != http.StatusConflict {
		t.Fatalf("genuine after tampered = %d", status)
	}
}

func TestReplayedApprovalIsRefused(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	id := b.identify(authorizeQuery("rp", rpRedirect, "openid"), h.users[alice])
	approval := h.approve(id, h.users[alice])
	if status, _ := h.deliver(approval, ""); status != 200 {
		t.Fatal("first delivery failed")
	}
	if status, _ := h.deliver(approval, ""); status != http.StatusConflict {
		t.Fatalf("replay = %d", status)
	}
}

func TestExpiries(t *testing.T) {
	h := newHarness(t)
	b := h.browser()

	// The native request expires: the page asks for the ID again.
	id := b.identify(authorizeQuery("rp", rpRedirect, "openid"), h.users[alice])
	approval := h.approve(id, h.users[alice])
	h.advance(h.srv.cfg.RequestTTL + time.Second)
	if st := b.status(id); st.Status != "expired" {
		t.Fatalf("status = %+v", st)
	}
	if status, _ := h.deliver(approval, ""); status == 200 {
		t.Fatal("a late approval was accepted")
	}
	if p := b.get("/t/" + id); !strings.Contains(p.body, "expired") || !strings.Contains(p.body, `name="identity"`) {
		t.Fatalf("expired page = %s", p.body)
	}

	// The resume window expires.
	id = b.identify(authorizeQuery("rp", rpRedirect, "openid"), h.users[alice])
	_, receipt := h.deliver(h.approve(id, h.users[alice]), "")
	h.advance(resumeWindow + time.Second)
	if p := b.get(receipt.ResumeURI); p.status != http.StatusGone {
		t.Fatalf("late resume = %d", p.status)
	}

	// The whole transaction expires.
	p := b.get(authorizeQuery("rp", rpRedirect, "openid"))
	id = txnIDFrom(t, p.location)
	h.advance(h.srv.cfg.TxnTTL + time.Second)
	if p := b.get("/t/" + id); p.status != http.StatusGone {
		t.Fatalf("expired transaction page = %d", p.status)
	}
}

func TestTwoTabsDoNotOrphanEachOther(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	first := b.identify(authorizeQuery("rp", rpRedirect, "openid"), h.users[alice])
	second := b.identify(authorizeQuery("rp", rpRedirect, "openid"), h.users[alice])
	for _, id := range []string{first, second} {
		_, receipt := h.deliver(h.approve(id, h.users[alice]), "")
		if p := b.get(receipt.ResumeURI); p.status != http.StatusSeeOther {
			t.Fatalf("resume %s = %d %s", id, p.status, p.body)
		}
	}
}

func TestChangingIdentityBeforeApproval(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	id := b.identify(authorizeQuery("rp", rpRedirect, "openid"), h.users[alice])
	old := h.approve(id, h.users[alice])
	if p := b.get("/t/" + id + "?change=1"); !strings.Contains(p.body, `name="identity"`) {
		t.Fatal("change=1 did not show the identify form")
	}
	b.post("/t/"+id+"/identify", url.Values{"identity": {bob}})
	// The request issued for alice is dead.
	if status, _ := h.deliver(old, ""); status == 200 {
		t.Fatal("an approval for a superseded request was accepted")
	}
	_, receipt := h.deliver(h.approve(id, h.users[bob]), "")
	if p := b.get(receipt.ResumeURI); p.status != http.StatusSeeOther {
		t.Fatalf("resume as bob = %d", p.status)
	}
}

func TestLoginAccountAndLogout(t *testing.T) {
	h := newHarness(t)
	b := h.browser()

	p := b.get("/account")
	if p.status != http.StatusSeeOther || !strings.HasPrefix(p.location, "/login?return_to=%2Faccount") {
		t.Fatalf("account without session = %d %s", p.status, p.location)
	}
	p = b.get(p.location)
	id := txnIDFrom(t, p.location)
	b.post("/t/"+id+"/identify", url.Values{"identity": {alice}})
	_, receipt := h.deliver(h.approve(id, h.users[alice]), "")
	p = b.follow(b.get(receipt.ResumeURI))
	if p.status != 200 || !strings.Contains(p.body, "Authorized apps") || !strings.Contains(p.body, alice) {
		t.Fatalf("account after login = %d %s", p.status, p.body)
	}
	// /login with a session goes straight through; open redirects do not.
	if p := b.get("/login?return_to=//evil.example/x"); p.location != "/account" {
		t.Fatalf("open redirect via return_to: %q", p.location)
	}
	if p := b.get("/login?return_to=/developers"); p.location != "/developers" {
		t.Fatalf("local return_to: %q", p.location)
	}
	b.post("/logout", url.Values{})
	if p := b.get("/account"); p.status != http.StatusSeeOther {
		t.Fatal("still signed in after logout")
	}
}

func TestLocalPath(t *testing.T) {
	for in, want := range map[string]string{
		"":                       "/x",
		"/account":               "/account",
		"/developers?tab=1":      "/developers?tab=1",
		"//evil.example":         "/x",
		"https://evil.example/":  "/x",
		"/\\evil.example":        "/x",
		"relative":               "/x",
		"/ok\r\nSet-Cookie: a=b": "/x",
	} {
		if got := localPath(in, "/x"); got != want {
			t.Errorf("localPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSecurityHeadersAndCookies(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	p := b.get(authorizeQuery("rp", rpRedirect, "openid"))
	csp := p.header.Get("Content-Security-Policy")
	for _, want := range []string{"frame-ancestors 'none'", "default-src 'none'", "script-src 'self'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q lacks %q", csp, want)
		}
	}
	if p.header.Get("Referrer-Policy") != "no-referrer" || p.header.Get("X-Frame-Options") != "DENY" {
		t.Fatalf("headers = %v", p.header)
	}
	cookie := p.header.Get("Set-Cookie")
	if !strings.Contains(cookie, "HttpOnly") || !strings.Contains(cookie, "SameSite=Lax") {
		t.Fatalf("binding cookie = %q", cookie)
	}

	// An https issuer uses __Host- cookies.
	store, _ := OpenStore(context.Background(), ":memory:")
	defer store.Close()
	keys, _ := NewMemoryKeyRing(nil)
	srv, err := New(context.Background(), Config{Issuer: "https://oauth.example", Store: store, Keys: keys})
	if err != nil {
		t.Fatal(err)
	}
	if got := srv.cookieName(sessionCookie); got != "__Host-"+sessionCookie {
		t.Fatalf("https cookie name = %q", got)
	}
	rec := &headerRecorder{h: http.Header{}}
	srv.setCookie(rec, sessionCookie, "v", time.Hour)
	c := rec.h.Get("Set-Cookie")
	if !strings.Contains(c, "Secure") || strings.Contains(c, "Domain=") || !strings.Contains(c, "Path=/") {
		t.Fatalf("https cookie = %q", c)
	}
}

type headerRecorder struct{ h http.Header }

func (r *headerRecorder) Header() http.Header         { return r.h }
func (r *headerRecorder) Write(b []byte) (int, error) { return len(b), nil }
func (r *headerRecorder) WriteHeader(int)             {}

func TestNewRejectsBadConfig(t *testing.T) {
	store, _ := OpenStore(context.Background(), ":memory:")
	defer store.Close()
	keys, _ := NewMemoryKeyRing(nil)
	base := func() Config { return Config{Issuer: "https://oauth.example", Store: store, Keys: keys} }
	cases := map[string]func(*Config){
		"issuer with path":  func(c *Config) { c.Issuer = "https://oauth.example/x" },
		"single-label host": func(c *Config) { c.Issuer = "http://localhost:8090" },
		"no store":          func(c *Config) { c.Store = nil },
		"no kek":            func(c *Config) { c.Keys = nil },
		"subject type":      func(c *Config) { c.SubjectType = "weird" },
		"registration":      func(c *Config) { c.ClientRegistration = "maybe" },
		"url clients":       func(c *Config) { c.URLClients = "sometimes" },
		"request ttl":       func(c *Config) { c.RequestTTL = time.Hour },
		"bad signer":        func(c *Config) { c.DefaultSigner = "javascript:alert(1)" },
		"bad static client": func(c *Config) { c.StaticClients = []Client{{ID: "x", Name: "X"}} },
		"url static client": func(c *Config) {
			c.StaticClients = []Client{{ID: "c", Name: "C", RedirectURIs: []string{"http://example.com/cb"}}}
		},
	}
	for name, mutate := range cases {
		cfg := base()
		mutate(&cfg)
		if _, err := New(context.Background(), cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := New(context.Background(), base()); err != nil {
		t.Fatalf("base config: %v", err)
	}
}

func TestSummarizeUserAgent(t *testing.T) {
	for ua, want := range map[string]string{
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) AppleWebKit/605 (KHTML) Version/17 Safari/605": "Safari on macOS",
		"Mozilla/5.0 (Windows NT 10.0) AppleWebKit/537 Chrome/120 Safari/537 Edg/120":                "Edge on Windows",
		"Mozilla/5.0 (Linux; Android 14) AppleWebKit/537 Chrome/120 Mobile Safari/537":               "Chrome on Android",
		"Go-http-client/1.1": "Script",
		"":                   "Browser",
	} {
		if got := summarizeUserAgent(ua); got != want {
			t.Errorf("summarizeUserAgent(%q) = %q, want %q", ua, got, want)
		}
	}
}
