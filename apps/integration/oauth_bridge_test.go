package integration_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/poweur/integration/fakedns"
	"github.com/poweur/oauth/bridge"
	"golang.org/x/oauth2"

	relaypkg "github.com/poweur/api/pkg/relay"
	idpkg "github.com/poweur/identity"
)

// routingClient sends each identity host to the relay that hosts it, which is
// what public DNS does outside the test.
func routingClient(routes map[string]string) *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, _, _ := net.SplitHostPort(addr)
				for suffix, target := range routes {
					if host == suffix || strings.HasSuffix(host, "."+suffix) {
						return dialer.DialContext(ctx, network, target)
					}
				}
				return dialer.DialContext(ctx, network, addr)
			},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

type oauthFixture struct {
	issuer string
	store  *bridge.Store
}

func newOAuthBridge(t *testing.T, zone *fakedns.Zone, routes map[string]string, clients []bridge.Client) oauthFixture {
	t.Helper()
	ctx := context.Background()
	store, err := bridge.OpenStore(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	keys, err := bridge.NewMemoryKeyRing(nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(nil)
	issuer := "http://" + ts.Listener.Addr().String()
	srv, err := bridge.New(ctx, bridge.Config{
		Issuer:        issuer,
		Store:         store,
		Keys:          keys,
		StaticClients: clients,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		ResolveOptions: idpkg.ResolveOptions{
			Scheme:       "http",
			AllowPrivate: true,
			TXT:          zone,
			HTTPClient:   routingClient(routes),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ts.Config.Handler = srv
	ts.Start()
	t.Cleanup(ts.Close)
	return oauthFixture{issuer: issuer, store: store}
}

type oauthBrowser struct {
	t      *testing.T
	client *http.Client
	origin string
}

func newOAuthBrowser(t *testing.T, origin string) *oauthBrowser {
	jar, _ := cookiejar.New(nil)
	return &oauthBrowser{t: t, origin: origin, client: &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (b *oauthBrowser) do(method, target string, form url.Values) (*http.Response, string) {
	b.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, _ := http.NewRequest(method, target, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", b.origin)
	}
	resp, err := b.client.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(raw)
}

func (b *oauthBrowser) abs(loc string) string {
	if strings.HasPrefix(loc, "/") {
		return b.origin + loc
	}
	return loc
}

var txnRe = regexp.MustCompile(`/t/([A-Za-z0-9_-]{43})`)

// startAndIdentify opens an authorization URL, enters the ID and returns the
// transaction id and the native request the waiting page offers.
func (b *oauthBrowser) startAndIdentify(authURL, id string) (string, string) {
	b.t.Helper()
	resp, body := b.do(http.MethodGet, authURL, nil)
	if resp.StatusCode != http.StatusSeeOther {
		b.t.Fatalf("authorize = %d %s", resp.StatusCode, body)
	}
	txn := txnRe.FindStringSubmatch(resp.Header.Get("Location"))[1]
	resp, body = b.do(http.MethodPost, b.origin+"/t/"+txn+"/identify", url.Values{"identity": {id}})
	if resp.StatusCode != http.StatusSeeOther {
		b.t.Fatalf("identify %s = %d %s", id, resp.StatusCode, body)
	}
	_, page := b.do(http.MethodGet, b.origin+"/t/"+txn, nil)
	m := regexp.MustCompile(`poweur://auth\?request=([A-Za-z0-9_%-]+)`).FindStringSubmatch(page)
	if m == nil {
		b.t.Fatalf("waiting page offers no request: %s", page)
	}
	request, _ := url.QueryUnescape(m[1])
	return txn, request
}

// finish follows the browser from a bridge URL to the client's redirect,
// allowing the consent it meets, and returns the redirect.
func (b *oauthBrowser) finish(start string) *url.URL {
	b.t.Helper()
	next := start
	for i := 0; i < 10; i++ {
		resp, body := b.do(http.MethodGet, b.abs(next), nil)
		switch {
		case resp.StatusCode == http.StatusSeeOther:
			loc := resp.Header.Get("Location")
			if !strings.HasPrefix(b.abs(loc), b.origin) {
				u, _ := url.Parse(loc)
				return u
			}
			next = loc
		case resp.StatusCode == http.StatusOK && strings.Contains(body, "/consent"):
			txn := txnRe.FindStringSubmatch(body)[1]
			resp, _ := b.do(http.MethodPost, b.origin+"/t/"+txn+"/consent", url.Values{
				"decision": {"allow"}, "release_poweur_id": {"on"},
			})
			u, _ := url.Parse(resp.Header.Get("Location"))
			return u
		default:
			b.t.Fatalf("unexpected %d at %s: %s", resp.StatusCode, next, body)
		}
	}
	b.t.Fatal("too many redirects")
	return nil
}

// TestINT_OAUTH_01: one bridge signs in identities hosted on two different
// relays and a DNS-published identity, for an unmodified OIDC relying party
// (go-oidc + x/oauth2, the stack oauth2-proxy is built on). The Go CLI is the
// signer; no relay knows the bridge exists.
func TestINT_OAUTH_01_GenericOIDCClientSignsInIdentitiesFromAnyRelay(t *testing.T) {
	zone := newZone(t)
	relayA, addrA := newHostedRelay(t, zone, t.TempDir())
	defer relayA.Close()
	addrB, _ := newCountingRelay(t, zone, []string{"otherhost.net"}, relaypkg.RateLimits{})

	aliceHome := t.TempDir()
	alice := "oauthalice.poweur.net"
	runCLI(t, aliceHome, "identity", "create", alice, "--hosted", "--relay", "http://"+addrA, "--json")
	bobHome := t.TempDir()
	bob := "oauthbob.otherhost.net"
	runCLI(t, bobHome, "identity", "create", bob, "--hosted", "--relay", "http://"+addrB, "--json")
	carolHome, carol := newDNSIdentity(t, "oauthcarol", "example.org", addrA)

	const redirect = "https://rp.example/oauth2/callback"
	fx := newOAuthBridge(t, zone, map[string]string{
		"poweur.net":    addrA,
		"otherhost.net": addrB,
		"example.org":   addrA,
	}, []bridge.Client{{
		ID: "oauth2-proxy", Name: "Team dashboard", RedirectURIs: []string{redirect}, Secret: "proxy-secret-0123456789",
	}})

	ctx := context.Background()
	provider, err := oidc.NewProvider(ctx, fx.issuer)
	if err != nil {
		t.Fatalf("discovery: %v", err)
	}
	cfg := oauth2.Config{
		ClientID: "oauth2-proxy", ClientSecret: "proxy-secret-0123456789",
		Endpoint: provider.Endpoint(), RedirectURL: redirect,
		Scopes: []string{oidc.ScopeOpenID, "poweur_id"},
	}
	verifier := provider.Verifier(&oidc.Config{ClientID: "oauth2-proxy"})

	subjects := map[string]string{}
	for _, who := range []struct{ id, home string }{{alice, aliceHome}, {bob, bobHome}, {carol, carolHome}} {
		t.Run(who.id, func(t *testing.T) {
			b := newOAuthBrowser(t, fx.issuer)
			pkce := oauth2.GenerateVerifier()
			nonce := "n-" + who.id
			authURL := cfg.AuthCodeURL("st-"+who.id, oauth2.S256ChallengeOption(pkce), oidc.Nonce(nonce))
			_, request := b.startAndIdentify(authURL, who.id)

			out, _ := runCLI(t, who.home, "auth", "approve", request, "--sign-with", "identity", "--json")
			var approved struct {
				ResumeURI string `json:"resume_uri"`
			}
			if err := json.Unmarshal([]byte(out), &approved); err != nil || approved.ResumeURI == "" {
				t.Fatalf("approve: %v %s", err, out)
			}
			back := b.finish(approved.ResumeURI)
			q := back.Query()
			if q.Get("iss") != fx.issuer || q.Get("state") != "st-"+who.id || q.Get("code") == "" {
				t.Fatalf("redirect = %s", back)
			}

			tok, err := cfg.Exchange(ctx, q.Get("code"), oauth2.VerifierOption(pkce))
			if err != nil {
				t.Fatalf("exchange: %v", err)
			}
			raw, _ := tok.Extra("id_token").(string)
			idt, err := verifier.Verify(ctx, raw)
			if err != nil {
				t.Fatalf("go-oidc refused the ID token: %v", err)
			}
			if idt.Nonce != nonce {
				t.Fatalf("nonce = %q", idt.Nonce)
			}
			var claims struct {
				PoweurID string `json:"poweur_id"`
			}
			if err := idt.Claims(&claims); err != nil || claims.PoweurID != who.id {
				t.Fatalf("poweur_id = %q %v", claims.PoweurID, err)
			}
			info, err := provider.UserInfo(ctx, oauth2.StaticTokenSource(tok))
			if err != nil || info.Subject != idt.Subject {
				t.Fatalf("userinfo = %+v %v", info, err)
			}
			subjects[who.id] = idt.Subject
		})
	}
	if len(subjects) != 3 || subjects[alice] == subjects[bob] || subjects[bob] == subjects[carol] {
		t.Fatalf("subjects = %v", subjects)
	}
}

// TestINT_OAUTH_02: approving from another device finishes in the starting
// browser only with the code that browser showed; a forwarded same-device
// approval signs nobody in.
func TestINT_OAUTH_02_CrossDeviceAndForwardedLinks(t *testing.T) {
	zone := newZone(t)
	relay, addr := newHostedRelay(t, zone, t.TempDir())
	defer relay.Close()
	home := t.TempDir()
	alice := "oauthdave.poweur.net"
	runCLI(t, home, "identity", "create", alice, "--hosted", "--relay", "http://"+addr, "--json")

	const redirect = "https://rp.example/cb"
	fx := newOAuthBridge(t, zone, map[string]string{"poweur.net": addr}, []bridge.Client{{
		ID: "rp", Name: "RP", RedirectURIs: []string{redirect}, Secret: "rp-secret-0123456789",
	}})
	ctx := context.Background()
	provider, err := oidc.NewProvider(ctx, fx.issuer)
	if err != nil {
		t.Fatal(err)
	}
	cfg := oauth2.Config{ClientID: "rp", ClientSecret: "rp-secret-0123456789", Endpoint: provider.Endpoint(), RedirectURL: redirect, Scopes: []string{"openid"}}

	// Cross-device: the terminal types the code the browser shows.
	b := newOAuthBrowser(t, fx.issuer)
	pkce := oauth2.GenerateVerifier()
	txn, request := b.startAndIdentify(cfg.AuthCodeURL("s1", oauth2.S256ChallengeOption(pkce), oidc.Nonce("n1")), alice)
	_, page := b.do(http.MethodGet, fx.issuer+"/t/"+txn, nil)
	match := regexp.MustCompile(`class="match"[^>]*>(\d+)<`).FindStringSubmatch(page)
	if match == nil {
		t.Fatalf("no match code on the page")
	}
	out, _ := runCLI(t, home, "auth", "approve", request, "--sign-with", "identity", "--code", match[1], "--json")
	if strings.Contains(out, "resume_uri") {
		t.Fatalf("a cross-device approval printed a resume link: %s", out)
	}
	resp, body := b.do(http.MethodGet, fx.issuer+"/t/"+txn+"/status", nil)
	var st bridge.Status
	_ = json.Unmarshal([]byte(body), &st)
	if resp.StatusCode != 200 || st.Status != "complete" {
		t.Fatalf("status = %s", body)
	}
	back := b.finish(st.Next)
	if _, err := cfg.Exchange(ctx, back.Query().Get("code"), oauth2.VerifierOption(pkce)); err != nil {
		t.Fatalf("exchange after cross-device approval: %v", err)
	}

	// Forwarded: the attacker's browser starts, the victim's browser resumes.
	attacker, victim := newOAuthBrowser(t, fx.issuer), newOAuthBrowser(t, fx.issuer)
	txn, request = attacker.startAndIdentify(cfg.AuthCodeURL("s2", oauth2.S256ChallengeOption(oauth2.GenerateVerifier()), oidc.Nonce("n2")), alice)
	out, _ = runCLI(t, home, "auth", "approve", request, "--sign-with", "identity", "--json")
	var approved struct {
		ResumeURI string `json:"resume_uri"`
	}
	_ = json.Unmarshal([]byte(out), &approved)
	resp, _ = victim.do(http.MethodGet, approved.ResumeURI, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("resume in the victim's browser = %d", resp.StatusCode)
	}
	resp, body = attacker.do(http.MethodGet, fx.issuer+"/t/"+txn+"/status", nil)
	if !strings.Contains(body, `"failed"`) {
		t.Fatalf("attacker status = %s", body)
	}
	resp, _ = attacker.do(http.MethodGet, fx.issuer+"/t/"+txn+"/continue", nil)
	if loc := resp.Header.Get("Location"); strings.Contains(loc, "code=") {
		t.Fatalf("the attacker got a code: %s", loc)
	}
}
