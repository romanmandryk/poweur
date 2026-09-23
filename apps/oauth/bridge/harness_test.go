package bridge

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/poweur/identity"
	"github.com/poweur/identity/signin"
)

// --- Identities ------------------------------------------------------------------

type user struct {
	id   string
	priv ed25519.PrivateKey
	doc  identity.IdentityDocument
}

func newUser(t *testing.T, id string) user {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return user{id: id, priv: priv, doc: identity.IdentityDocument{
		Version:   1,
		Identity:  id,
		PublicKey: identity.FormatEd25519PublicKey(pub),
		Relay:     "https://relay.example",
		UpdatedAt: "2026-01-01T00:00:00Z",
	}}
}

// zone resolves exactly the identities put in it.
type zone struct {
	mu   sync.Mutex
	docs map[string]identity.IdentityDocument
}

func (z *zone) Resolve(_ context.Context, name string) (identity.Result, error) {
	z.mu.Lock()
	defer z.mu.Unlock()
	doc, ok := z.docs[strings.ToLower(name)]
	if !ok {
		return identity.Result{}, fmt.Errorf("identity %s not found", name)
	}
	return identity.Result{Document: doc, Source: identity.SourceWeb}, nil
}

// --- The bridge under test ---------------------------------------------------------

const (
	rpSecret     = "rp-secret-value-long-enough"
	rpRedirect   = "https://rp.example/callback"
	spaRedirect  = "https://spa.example/cb"
	alice        = "alice.poweur.net"
	bob          = "bob.poweur.net"
	sampleState  = "state-123"
	sampleNonce  = "nonce-456"
	testVerifier = "verifier-verifier-verifier-verifier-verifier-0123"
)

type harness struct {
	t      *testing.T
	srv    *Server
	http   *httptest.Server
	store  *Store
	zone   *zone
	users  map[string]user
	now    time.Time
	nowMu  sync.Mutex
	issuer string

	metadata map[string]string // URL client documents
}

type harnessOption func(*Config)

var (
	testKeyOnce sync.Once
	testKey     *rsa.PrivateKey
)

func sharedTestKey(t *testing.T) *rsa.PrivateKey {
	testKeyOnce.Do(func() {
		var err error
		if testKey, err = rsa.GenerateKey(rand.Reader, rsaKeyBits); err != nil {
			t.Fatal(err)
		}
	})
	return testKey
}

func newHarness(t *testing.T, opts ...harnessOption) *harness {
	t.Helper()
	h := &harness{
		t:        t,
		zone:     &zone{docs: map[string]identity.IdentityDocument{}},
		users:    map[string]user{},
		now:      time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC),
		metadata: map[string]string{},
	}
	for _, id := range []string{alice, bob} {
		u := newUser(t, id)
		h.users[id] = u
		h.zone.docs[id] = u.doc
	}
	store, err := OpenStore(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	h.store = store

	// Start the listener first: the issuer is its URL.
	h.http = httptest.NewUnstartedServer(nil)
	h.issuer = "http://" + h.http.Listener.Addr().String()
	keys, err := NewMemoryKeyRingFromKey(sharedTestKey(t), h.clock)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		Issuer:        h.issuer,
		Store:         store,
		Resolver:      h.zone,
		Keys:          keys,
		Now:           h.clock,
		DefaultSigner: "https://poweur.net/app/",
		FetchCapabilities: func(_ context.Context, id string) (identity.Capabilities, error) {
			return identity.Capabilities{Endpoints: map[string]string{"web_signer": "https://" + id + "/app/"}}, nil
		},
		FetchProfile: func(_ context.Context, id string) (identity.Profile, error) {
			return identity.Profile{Version: 1, DisplayName: "Display " + id, Avatar: "public/avatar.png"}, nil
		},
		FetchClientMetadata: func(_ context.Context, id string) ([]byte, error) {
			doc, ok := h.metadata[id]
			if !ok {
				return nil, fmt.Errorf("no document")
			}
			return []byte(doc), nil
		},
		StaticClients: []Client{
			{ID: "rp", Name: "Relying Party", RedirectURIs: []string{rpRedirect}, Secret: rpSecret},
			{ID: "spa", Name: "Single Page", RedirectURIs: []string{spaRedirect, "http://127.0.0.1/cb"}, AuthMethod: AuthNone},
			{ID: "internal", Name: "Internal Tool", RedirectURIs: []string{"https://tool.example/cb"}, Secret: rpSecret, FirstParty: true},
		},
		ClientRegistration: RegistrationOpen,
		Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		// The harness drives many journeys from one address in one minute.
		RateLimits: RateLimits{Authorize: -1, Identify: -1, Callback: -1, Token: -1, Console: -1},
	}
	for _, o := range opts {
		o(&cfg)
	}
	srv, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	h.srv = srv
	h.http.Config.Handler = srv
	h.http.Start()
	t.Cleanup(h.http.Close)
	return h
}

func (h *harness) clock() time.Time {
	h.nowMu.Lock()
	defer h.nowMu.Unlock()
	return h.now
}

func (h *harness) advance(d time.Duration) {
	h.nowMu.Lock()
	defer h.nowMu.Unlock()
	h.now = h.now.Add(d)
}

// --- Browsers --------------------------------------------------------------------

type browser struct {
	h      *harness
	client *http.Client
}

func (h *harness) browser() *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{h: h, client: &http.Client{
		Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

type page struct {
	status   int
	body     string
	location string
	header   http.Header
}

func (b *browser) do(method, path string, form url.Values) page {
	b.h.t.Helper()
	target := path
	if !strings.HasPrefix(path, "http") {
		target = b.h.issuer + path
	}
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, target, body)
	if err != nil {
		b.h.t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", b.h.issuer)
	}
	resp, err := b.client.Do(req)
	if err != nil {
		b.h.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return page{status: resp.StatusCode, body: string(raw), location: resp.Header.Get("Location"), header: resp.Header}
}

// shownPage is the page a response carries: web.go's data block.
type shownPage struct {
	Page    string          `json:"page"`
	Title   string          `json:"title"`
	Session *sessionInfo    `json:"session"`
	Data    json.RawMessage `json:"data"`
}

var untaggedKey = regexp.MustCompile(`"[A-Z][A-Za-z0-9]*":`)

var pageBlock = regexp.MustCompile(`<script type="application/json" id="poweur-page">(.*?)</script>`)

// shown decodes the page data; the zero value when the response is no page.
func (p page) shown() shownPage {
	var out shownPage
	if m := pageBlock.FindStringSubmatch(p.body); m != nil {
		_ = json.Unmarshal([]byte(m[1]), &out)
	}
	return out
}

// is reports whether the response is the named page.
func (p page) is(name string) bool { return p.shown().Page == name }

// data decodes the page's data into v.
func (p page) data(t *testing.T, v any) {
	t.Helper()
	sp := p.shown()
	if sp.Page == "" {
		t.Fatalf("not a page: %d %s", p.status, p.body)
	}
	// Every key is the page's contract (apps/oauth/ui/src/lib/page.ts); an
	// exported Go field without a json tag would arrive capitalised.
	if m := untaggedKey.FindString(string(sp.Data)); m != "" {
		t.Fatalf("page %s data has an untagged field %s: %s", sp.Page, m, sp.Data)
	}
	if err := json.Unmarshal(sp.Data, v); err != nil {
		t.Fatalf("page %s data: %v", sp.Page, err)
	}
}

// fetchJSON posts like the pages' own scripts do: same origin, JSON wanted.
func (b *browser) fetchJSON(path string, out any) int {
	b.h.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, b.h.issuer+path, nil)
	req.Header.Set("Origin", b.h.issuer)
	req.Header.Set("Accept", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		b.h.t.Fatal(err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		b.h.t.Fatalf("%s: %v", path, err)
	}
	return resp.StatusCode
}

func (b *browser) get(path string) page                   { return b.do(http.MethodGet, path, nil) }
func (b *browser) post(path string, form url.Values) page { return b.do(http.MethodPost, path, form) }

// follow GETs same-origin redirects until a page or an off-site redirect.
func (b *browser) follow(p page) page {
	b.h.t.Helper()
	for i := 0; i < 10 && p.location != "" && (p.status == 302 || p.status == 303); i++ {
		loc := p.location
		if strings.HasPrefix(loc, "http") && !strings.HasPrefix(loc, b.h.issuer) {
			return p
		}
		p = b.get(loc)
	}
	return p
}

var txnPath = regexp.MustCompile(`/t/([A-Za-z0-9_-]{43})`)
var txnField = regexp.MustCompile(`"txn":"([A-Za-z0-9_-]{43})"`)

func txnIDFrom(t *testing.T, s string) string {
	t.Helper()
	m := txnPath.FindStringSubmatch(s)
	if m == nil {
		// A page carries its transaction in its data.
		m = txnField.FindStringSubmatch(s)
	}
	if m == nil {
		t.Fatalf("no transaction id in %q", s)
	}
	return m[1]
}

// --- Signing -----------------------------------------------------------------------

// approve signs the transaction's native request as u, like a signer.
func (h *harness) approve(txnID string, u user) string {
	h.t.Helper()
	txn, err := h.store.GetTxn(context.Background(), txnID)
	if err != nil {
		h.t.Fatal(err)
	}
	req, err := identity.DecodeSignInRequest(txn.Request)
	if err != nil {
		h.t.Fatal(err)
	}
	// A signer checks the request against the bridge's own metadata.
	if err := signin.CheckRequestAgainstMetadata(req, h.srv.nativeMetadata()); err != nil {
		h.t.Fatalf("request does not match bridge metadata: %v", err)
	}
	resp, err := signin.Sign(req, signin.SignOptions{Identity: u.id, PrivateKey: u.priv, Now: h.clock})
	if err != nil {
		h.t.Fatal(err)
	}
	enc, err := identity.EncodeSignInResponse(resp)
	if err != nil {
		h.t.Fatal(err)
	}
	return enc
}

// deliver POSTs an approval from the signer (no cookies).
func (h *harness) deliver(encoded, match string) (int, signin.DeliveryReceipt) {
	h.t.Helper()
	body, _ := json.Marshal(signin.Delivery{Response: encoded, Match: match})
	resp, err := http.Post(h.issuer+"/poweur/callback", "text/plain;charset=UTF-8", strings.NewReader(string(body)))
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	var receipt signin.DeliveryReceipt
	_ = json.NewDecoder(resp.Body).Decode(&receipt)
	return resp.StatusCode, receipt
}

func (h *harness) txn(id string) *Txn {
	h.t.Helper()
	t, err := h.store.GetTxn(context.Background(), id)
	if err != nil {
		h.t.Fatal(err)
	}
	return t
}

// --- OAuth helpers ------------------------------------------------------------------

func challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func authorizeQuery(clientID, redirect, scope string, extra ...string) string {
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirect},
		"scope":                 {scope},
		"state":                 {sampleState},
		"nonce":                 {sampleNonce},
		"code_challenge":        {challenge(testVerifier)},
		"code_challenge_method": {"S256"},
	}
	for i := 0; i+1 < len(extra); i += 2 {
		if extra[i+1] == "" {
			q.Del(extra[i])
		} else {
			q.Set(extra[i], extra[i+1])
		}
	}
	return "/authorize?" + q.Encode()
}

// signIn runs /authorize → identify → approve (same device) → resume, and
// returns the page after the resume (consent, or a redirect to the client).
func (b *browser) signIn(authorizePath string, u user) (string, page) {
	b.h.t.Helper()
	p := b.get(authorizePath)
	if p.status != http.StatusSeeOther {
		b.h.t.Fatalf("authorize = %d %s", p.status, p.body)
	}
	id := txnIDFrom(b.h.t, p.location)
	p = b.follow(p)
	if !p.is("identify") {
		b.h.t.Fatalf("expected the identify page, got %d %s", p.status, p.body)
	}
	p = b.post("/t/"+id+"/identify", url.Values{"identity": {u.id}})
	if p.status != http.StatusSeeOther {
		b.h.t.Fatalf("identify = %d %s", p.status, p.body)
	}
	status, receipt := b.h.deliver(b.h.approve(id, u), "")
	if status != http.StatusOK || receipt.ResumeURI == "" {
		b.h.t.Fatalf("callback = %d %+v", status, receipt)
	}
	p = b.get(receipt.ResumeURI)
	if p.status != http.StatusSeeOther {
		b.h.t.Fatalf("resume = %d %s", p.status, p.body)
	}
	return id, b.follow(p)
}

// consent approves the consent page for txn, releasing the given scopes.
func (b *browser) consent(id string, release ...string) page {
	form := url.Values{"decision": {"allow"}}
	for _, s := range release {
		form.Set("release_"+s, "on")
	}
	return b.post("/t/"+id+"/consent", form)
}

// codeFrom extracts the code from a redirect to the client, checking state
// and iss on the way.
func (h *harness) codeFrom(p page, redirect string) string {
	h.t.Helper()
	if p.status != http.StatusSeeOther || !strings.HasPrefix(p.location, redirect+"?") {
		h.t.Fatalf("expected a redirect to %s, got %d %q %s", redirect, p.status, p.location, p.body)
	}
	u, _ := url.Parse(p.location)
	q := u.Query()
	if q.Get("state") != sampleState {
		h.t.Fatalf("state = %q", q.Get("state"))
	}
	if q.Get("iss") != h.issuer {
		h.t.Fatalf("iss = %q", q.Get("iss"))
	}
	if q.Get("error") != "" {
		h.t.Fatalf("authorization error %s: %s", q.Get("error"), q.Get("error_description"))
	}
	return q.Get("code")
}

func (h *harness) errorFrom(p page, redirect string) string {
	h.t.Helper()
	if p.status != http.StatusSeeOther || !strings.HasPrefix(p.location, redirect+"?") {
		h.t.Fatalf("expected an error redirect to %s, got %d %q %s", redirect, p.status, p.location, p.body)
	}
	u, _ := url.Parse(p.location)
	if u.Query().Get("iss") != h.issuer || u.Query().Get("state") != sampleState {
		h.t.Fatalf("error redirect lacks iss/state: %s", p.location)
	}
	return u.Query().Get("error")
}

type tokenResult struct {
	status int
	body   map[string]any
}

func (h *harness) token(form url.Values, basicID, basicSecret string) tokenResult {
	h.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, h.issuer+"/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if basicID != "" {
		req.SetBasicAuth(url.QueryEscape(basicID), url.QueryEscape(basicSecret))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	out := tokenResult{status: resp.StatusCode, body: map[string]any{}}
	_ = json.NewDecoder(resp.Body).Decode(&out.body)
	return out
}

func codeForm(code, redirect string) url.Values {
	return url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirect},
		"code_verifier": {testVerifier},
	}
}

// idClaims verifies an ID token against the published JWKS.
func (h *harness) idClaims(token string) map[string]any {
	h.t.Helper()
	payload, err := verifyJWS(token, h.srv.keys.JWKS())
	if err != nil {
		h.t.Fatalf("ID token does not verify: %v", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		h.t.Fatal(err)
	}
	return claims
}

// fullFlow signs u in to the "rp" client and returns the token response.
func (h *harness) fullFlow(b *browser, u user, scope string, release ...string) tokenResult {
	h.t.Helper()
	id, p := b.signIn(authorizeQuery("rp", rpRedirect, scope), u)
	if p.is("consent") {
		p = b.consent(id, release...)
	}
	code := h.codeFrom(p, rpRedirect)
	res := h.token(codeForm(code, rpRedirect), "rp", rpSecret)
	if res.status != http.StatusOK {
		h.t.Fatalf("token = %d %v", res.status, res.body)
	}
	return res
}
