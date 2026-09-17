package bridge

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func withLauncher(u, domain string) harnessOption {
	return func(c *Config) { c.LauncherURL = u; c.LauncherDomain = domain }
}

func TestIdentifyPageOffersToCreateAnID(t *testing.T) {
	h := newHarness(t, withLauncher("https://id.poweur.net/", ""))
	b := h.browser()
	p := b.follow(b.get(authorizeQuery("rp", rpRedirect, "openid")))
	for _, want := range []string{
		"Don't have a Poweur ID?",
		`data-launcher="https://id.poweur.net"`,
		`data-domain="poweur.net"`, // "id." dropped
		`<span class="suffix">.poweur.net</span>`,
		`href="https://id.poweur.net/app/?from=signin"`,
		`target="_blank" rel="noopener"`,
		"Relying Party signs you in with a Poweur ID instead of a password",
		"Create a Poweur ID at id.poweur.net",
		"/creating",
	} {
		if !strings.Contains(p.body, want) {
			t.Errorf("identify page lacks %q", want)
		}
	}
	if csp := p.header.Get("Content-Security-Policy"); !strings.Contains(csp, "connect-src 'self' https://id.poweur.net;") {
		t.Errorf("CSP = %q", csp)
	}
}

func TestNoLauncherNoOffer(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	p := b.follow(b.get(authorizeQuery("rp", rpRedirect, "openid")))
	if strings.Contains(p.body, "data-create") || strings.Contains(p.body, "have a Poweur ID") {
		t.Fatal("offer shown without a launcher")
	}
	if csp := p.header.Get("Content-Security-Policy"); !strings.Contains(csp, "connect-src 'self';") {
		t.Errorf("CSP = %q", csp)
	}
	id := txnIDFrom(t, b.get(authorizeQuery("rp", rpRedirect, "openid")).location)
	if p := b.post("/t/"+id+"/creating", url.Values{}); p.status != http.StatusNotFound {
		t.Fatalf("creating without launcher = %d", p.status)
	}
}

func TestCreatingKeepsTheSignInWaitingUpToACap(t *testing.T) {
	h := newHarness(t, withLauncher("https://poweur.net", ""))
	b := h.browser()
	id := txnIDFrom(t, b.get(authorizeQuery("rp", rpRedirect, "openid")).location)
	created := h.txn(id).CreatedAt

	p := b.post("/t/"+id+"/creating", url.Values{})
	if p.status != http.StatusOK {
		t.Fatalf("creating = %d %s", p.status, p.body)
	}
	var out struct {
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal([]byte(p.body), &out); err != nil || !out.ExpiresAt.Equal(created.Add(creatingWindow)) {
		t.Fatalf("expires_at = %s (%v)", p.body, err)
	}

	// Asking again later extends — never past the cap.
	h.advance(25 * time.Minute)
	b.post("/t/"+id+"/creating", url.Values{})
	h.advance(25 * time.Minute)
	b.post("/t/"+id+"/creating", url.Values{})
	if got := h.txn(id).ExpiresAt; !got.Equal(created.Add(maxTxnLifetime)) {
		t.Fatalf("expires = %v, want cap %v", got, created.Add(maxTxnLifetime))
	}

	// The extended transaction still works: identify after 50 minutes.
	if p := b.post("/t/"+id+"/identify", url.Values{"identity": {alice}}); p.status != http.StatusSeeOther {
		t.Fatalf("identify after creating = %d %s", p.status, p.body)
	}
}

func TestCreatingRefusals(t *testing.T) {
	h := newHarness(t, withLauncher("https://poweur.net", ""))
	owner, other := h.browser(), h.browser()
	id := txnIDFrom(t, owner.get(authorizeQuery("rp", rpRedirect, "openid")).location)
	before := h.txn(id).ExpiresAt

	if p := other.post("/t/"+id+"/creating", url.Values{}); p.status != http.StatusNotFound {
		t.Errorf("other browser = %d", p.status)
	}
	req, _ := http.NewRequest(http.MethodPost, h.issuer+"/t/"+id+"/creating", nil)
	req.Header.Set("Origin", "https://evil.example")
	resp, err := owner.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-site = %d", resp.StatusCode)
	}
	if !h.txn(id).ExpiresAt.Equal(before) {
		t.Fatal("a refused request extended the transaction")
	}
	owner.post("/t/"+id+"/cancel", url.Values{})
	if p := owner.post("/t/"+id+"/creating", url.Values{}); p.status == http.StatusOK {
		t.Errorf("cancelled transaction extended: %s", p.body)
	}
}

func TestWhyNamesTheApplication(t *testing.T) {
	if got := whyPoweur(&Txn{}); !strings.HasPrefix(got, "This site signs you in") {
		t.Fatal(got)
	}
	if got := whyPoweur(&Txn{Authorize: &AuthorizeRequest{ClientName: "Grafana"}}); !strings.HasPrefix(got, "Grafana signs you in") {
		t.Fatal(got)
	}
}
