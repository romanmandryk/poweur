package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"fmt"

	"github.com/coreos/go-oidc/v3/oidc"
	clipkg "github.com/poweur/cli/pkg/cli"
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
	issuer  string
	store   *bridge.Store
	advance func(time.Duration)
}

func newOAuthBridge(t *testing.T, zone *fakedns.Zone, routes map[string]string, clients []bridge.Client) oauthFixture {
	t.Helper()
	return newOAuthBridgeWith(t, zone, routes, clients, nil)
}

func newOAuthBridgeWith(t *testing.T, zone *fakedns.Zone, routes map[string]string, clients []bridge.Client, tweak func(*bridge.Config)) oauthFixture {
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
	var mu sync.Mutex
	now := time.Now()
	fx := oauthFixture{advance: func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }}
	cfg := bridge.Config{
		Now:           func() time.Time { mu.Lock(); defer mu.Unlock(); return now },
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
	}
	if tweak != nil {
		tweak(&cfg)
	}
	srv, err := bridge.New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ts.Config.Handler = srv
	ts.Start()
	t.Cleanup(ts.Close)
	fx.issuer, fx.store = issuer, store
	return fx
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

// Bridge pages carry their data as JSON (apps/oauth/bridge/web.go).
var (
	pageTxnRe   = regexp.MustCompile(`"txn":"([A-Za-z0-9_-]{43})"`)
	pageMatchRe = regexp.MustCompile(`"match":"(\d+)"`)
)

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
		case resp.StatusCode == http.StatusOK && strings.Contains(body, `"page":"consent"`):
			txn := pageTxnRe.FindStringSubmatch(body)[1]
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
	match := pageMatchRe.FindStringSubmatch(page)
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

// cliPusher sends prompts as the bridge's identity through the real CLI,
// in-process, from the bridge's own HOME.
type cliPusherForTest struct {
	t        *testing.T
	home     string
	identity string
	mu       sync.Mutex
	outputs  []string
}

func (p *cliPusherForTest) Push(_ context.Context, to string, body []byte, exp time.Time) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	prev := os.Getenv("HOME")
	os.Setenv("HOME", p.home)
	defer os.Setenv("HOME", prev)
	var stdout, stderr bytes.Buffer
	code := clipkg.Run([]string{"send", to, string(body), "--type", idpkg.MsgTypeAuthRequest,
		"--expires", exp.UTC().Format(time.RFC3339), "--use-identity", p.identity, "--via-home-relay", "--json"}, &stdout, &stderr)
	p.outputs = append(p.outputs, stdout.String()+stderr.String())
	if code != 0 {
		return fmt.Errorf("send exited %d: %s", code, stderr.String())
	}
	var out struct {
		Status json.RawMessage `json:"status"`
	}
	_ = json.Unmarshal(stdout.Bytes(), &out)
	if string(out.Status) != "202" && string(out.Status) != "200" {
		return fmt.Errorf("not delivered: %s", stdout.String())
	}
	return nil
}

// TestINT_OAUTH_03: push-to-approve. The bridge sends a sign-in prompt as its
// own Poweur ID; the relay admits it only because the user listed the
// bridge; the user approves with the code the starting browser shows.
func TestINT_OAUTH_03_PushToApprove(t *testing.T) {
	zone := newZone(t)
	relay, addr := newHostedRelay(t, zone, t.TempDir())
	defer relay.Close()
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })
	// Every name here lives on the test relay; nothing may reach real DNS.
	zone.SetHost("pushuser.poweur.net", addr)
	zone.SetHost("pushbridge.poweur.net", addr)
	userHome := t.TempDir()
	user := "pushuser.poweur.net"
	runCLI(t, userHome, "identity", "create", user, "--hosted", "--relay", "http://"+addr, "--json")
	bridgeHome := t.TempDir()
	bridgeID := "pushbridge.poweur.net"
	runCLI(t, bridgeHome, "identity", "create", bridgeID, "--hosted", "--relay", "http://"+addr, "--json")

	pusher := &cliPusherForTest{t: t, home: bridgeHome, identity: bridgeID}
	const redirect = "https://rp.example/cb"
	fx := newOAuthBridgeWith(t, zone, map[string]string{"poweur.net": addr}, []bridge.Client{{
		ID: "rp", Name: "Push RP", RedirectURIs: []string{redirect}, Secret: "rp-secret-0123456789",
	}}, func(c *bridge.Config) { c.Pusher = pusher; c.PushIdentity = bridgeID })

	b := newOAuthBrowser(t, fx.issuer)
	txn, _ := b.startAndIdentify(fx.issuer+"/authorize?"+url.Values{
		"response_type": {"code"}, "client_id": {"rp"}, "redirect_uri": {redirect}, "scope": {"openid"},
		"state": {"s"}, "nonce": {"n"}, "code_challenge": {strings.Repeat("A", 43)}, "code_challenge_method": {"S256"},
	}.Encode(), user)
	_, page := b.do(http.MethodGet, fx.issuer+"/t/"+txn, nil)
	match := pageMatchRe.FindStringSubmatch(page)[1]

	push := func() string {
		resp, _ := b.do(http.MethodPost, fx.issuer+"/t/"+txn+"/push", url.Values{})
		return resp.Header.Get("Location")
	}
	// Not trusted yet: the relay refuses, the page says so.
	if loc := push(); !strings.HasSuffix(loc, "push=failed") {
		t.Fatalf("push before trust = %s (%v)", loc, pusher.outputs)
	}
	out, _ := runCLI(t, userHome, "policy", "set", "contacts_only", "--trusted-auth", bridgeID)
	if !strings.Contains(out, "sign-in prompts from "+bridgeID) {
		t.Fatalf("policy set: %s", out)
	}
	// A later mode change keeps the trusted service.
	runCLI(t, userHome, "policy", "set", "contacts_and_requests")
	shown, _ := runCLI(t, userHome, "policy", "show")
	if !strings.Contains(shown, "sign-in prompts from: "+bridgeID) {
		t.Fatalf("policy show: %s", shown)
	}

	fx.advance(20 * time.Second)
	if loc := push(); !strings.HasSuffix(loc, "push=sent") {
		t.Fatalf("push after trust = %s (%v)", loc, pusher.outputs)
	}
	inbox, _ := runCLI(t, userHome, "inbox")
	re := regexp.MustCompile(`poweur auth approve (\S+) --code`)
	m := re.FindStringSubmatch(inbox)
	if m == nil {
		t.Fatalf("inbox did not show the prompt: %s", inbox)
	}
	// The prompt is not archived as conversation.
	hist, _ := runCLI(t, userHome, "history", "--json")
	if strings.Contains(hist, idpkg.MsgTypeAuthRequest) {
		t.Fatalf("prompt archived into history: %s", hist)
	}
	runCLI(t, userHome, "auth", "approve", m[1], "--sign-with", "identity", "--code", match)
	resp, body := b.do(http.MethodGet, fx.issuer+"/t/"+txn+"/status", nil)
	if resp.StatusCode != 200 || !strings.Contains(body, `"complete"`) {
		t.Fatalf("status after pushed approval = %s", body)
	}
	// Trust admits prompts only: the bridge cannot chat.
	if code, _, _ := runCLIFull(t, bridgeHome, "send", user, "hello", "--use-identity", bridgeID); code == 0 {
		t.Fatal("a trusted sign-in service could send chat into a contacts-only inbox")
	}
}
