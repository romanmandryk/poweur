package bridge

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestKeyRingPersistsSealedAndRotates(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "oauth.db")
	store, err := OpenStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	kek := make([]byte, 32)
	rand.Read(kek)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	kr, err := LoadKeyRing(ctx, store, kek, clock)
	if err != nil {
		t.Fatal(err)
	}
	first := kr.JWKS().Keys[0].Kid
	// The private key is not in the database in any readable form.
	var sealed []byte
	if err := store.db.QueryRow(`SELECT data FROM signing_keys`).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sealed), "PRIVATE") || len(sealed) < 1000 {
		t.Fatalf("sealed key looks wrong (%d bytes)", len(sealed))
	}
	store.Close()

	// Reopen: same key; a wrong KEK is refused loudly.
	store, _ = OpenStore(ctx, path)
	defer store.Close()
	wrong := make([]byte, 32)
	if _, err := LoadKeyRing(ctx, store, wrong, clock); err == nil || !strings.Contains(err.Error(), "OAUTH_KEY_ENCRYPTION_KEY") {
		t.Fatalf("wrong KEK = %v", err)
	}
	kr, err = LoadKeyRing(ctx, store, kek, clock)
	if err != nil {
		t.Fatal(err)
	}
	if got := kr.JWKS().Keys[0].Kid; got != first {
		t.Fatalf("key changed across restart: %s → %s", first, got)
	}
	// A swapped row (kid bound into the AEAD) does not open.
	if _, err := kr.open(sealed, "another-kid"); err == nil {
		t.Fatal("a sealed key opened under another kid")
	}

	// Rotate: both published, the new one signs, tokens from the old verify.
	oldToken, err := kr.Sign(map[string]any{"sub": "x"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := kr.Rotate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	set := kr.JWKS()
	if len(set.Keys) != 2 {
		t.Fatalf("after rotation %d keys published", len(set.Keys))
	}
	if _, err := verifyJWS(oldToken, set); err != nil {
		t.Fatalf("token signed before rotation no longer verifies: %v", err)
	}
	newToken, _ := kr.Sign(map[string]any{"sub": "y"})
	h, _, _, _, _ := parseJWS(newToken)
	if h.Kid != second {
		t.Fatalf("new tokens signed by %s, want %s", h.Kid, second)
	}
	// After the grace period the old key is unpublished, then deleted.
	now = now.Add(retiredKeyGrace + time.Minute)
	if got := len(kr.JWKS().Keys); got != 1 {
		t.Fatalf("after grace %d keys published", got)
	}
	if _, err := kr.Rotate(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := store.keyCount(ctx); n != 2 {
		t.Fatalf("expired keys not deleted: %d stored", n)
	}
	if list := kr.List(); len(list) != 2 || !list[0].Published {
		t.Fatalf("list = %+v", list)
	}
	if _, err := LoadKeyRing(ctx, store, []byte("short"), clock); err == nil {
		t.Fatal("short KEK accepted")
	}
}

func TestPairwiseSecretIsCreatedOnceAndKept(t *testing.T) {
	ctx := context.Background()
	store, _ := OpenStore(ctx, ":memory:")
	defer store.Close()
	gen := func() ([]byte, error) { return randomBytes(32) }
	a, err := store.getOrCreateSecret(ctx, "pairwise_subject", gen)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := store.getOrCreateSecret(ctx, "pairwise_subject", gen)
	if string(a) != string(b) || len(a) != 32 {
		t.Fatal("the pairwise secret changed")
	}
}

func signAssertion(t *testing.T, alg string, key any, kid string, claims map[string]any) string {
	t.Helper()
	input, err := jwsSigningInput(map[string]string{"alg": alg, "kid": kid}, claims)
	if err != nil {
		t.Fatal(err)
	}
	var sig []byte
	switch k := key.(type) {
	case ed25519.PrivateKey:
		sig = ed25519.Sign(k, []byte(input))
	case *ecdsa.PrivateKey:
		sum := sha256.Sum256([]byte(input))
		r, s, err := ecdsa.Sign(rand.Reader, k, sum[:])
		if err != nil {
			t.Fatal(err)
		}
		sig = append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)
	case *rsa.PrivateKey:
		kr := &signingKey{kid: kid, priv: k}
		tok, err := signJWT(kr, claims)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func TestVerifyJWS(t *testing.T) {
	edPub, edPriv, _ := ed25519.GenerateKey(nil)
	ecPriv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	rsaPriv, _ := rsa.GenerateKey(rand.Reader, 2048)
	ecPub, _ := ecPriv.PublicKey.Bytes()
	set := JWKS{Keys: []JWK{
		{Kty: "OKP", Crv: "Ed25519", Kid: "ed", X: base64.RawURLEncoding.EncodeToString(edPub)},
		{Kty: "EC", Crv: "P-256", Kid: "ec", X: base64.RawURLEncoding.EncodeToString(ecPub[1:33]), Y: base64.RawURLEncoding.EncodeToString(ecPub[33:])},
		{Kty: "RSA", Kid: "rs", N: base64.RawURLEncoding.EncodeToString(rsaPriv.N.Bytes()), E: base64.RawURLEncoding.EncodeToString(big.NewInt(int64(rsaPriv.E)).Bytes())},
	}}
	claims := map[string]any{"sub": "c"}
	for alg, tok := range map[string]string{
		"EdDSA": signAssertion(t, "EdDSA", edPriv, "ed", claims),
		"ES256": signAssertion(t, "ES256", ecPriv, "ec", claims),
		"RS256": signAssertion(t, "RS256", rsaPriv, "rs", claims),
	} {
		if _, err := verifyJWS(tok, set); err != nil {
			t.Errorf("%s: %v", alg, err)
		}
	}

	good := signAssertion(t, "EdDSA", edPriv, "ed", claims)
	parts := strings.Split(good, ".")
	bad := map[string]string{
		"tampered payload": parts[0] + "." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"d"}`)) + "." + parts[2],
		"alg none":         base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","kid":"ed"}`)) + "." + parts[1] + ".",
		"HS256 with a public key": func() string {
			in, _ := jwsSigningInput(map[string]string{"alg": "HS256", "kid": "ed"}, claims)
			return in + ".AAAA"
		}(),
		"unknown kid":   signAssertion(t, "EdDSA", edPriv, "zz", claims),
		"alg confusion": signAssertion(t, "EdDSA", edPriv, "rs", claims),
		"two parts":     parts[0] + "." + parts[1],
		"garbage":       "!!.!!.!!",
	}
	for name, tok := range bad {
		if _, err := verifyJWS(tok, set); err == nil {
			t.Errorf("%s: verified", name)
		}
	}
	// Short RSA keys are refused.
	small, _ := rsa.GenerateKey(rand.Reader, 1024)
	if _, err := rsaPublicKey(JWK{Kty: "RSA", N: base64.RawURLEncoding.EncodeToString(small.N.Bytes()), E: "AQAB"}); err == nil {
		t.Error("1024-bit RSA accepted")
	}
	// An EC point off the curve is refused.
	if _, err := ecPublicKey(JWK{Kty: "EC", Crv: "P-256", X: base64.RawURLEncoding.EncodeToString(make([]byte, 32)), Y: base64.RawURLEncoding.EncodeToString(make([]byte, 32))}); err == nil {
		t.Error("off-curve point accepted")
	}
}

func TestPrivateKeyJWTClient(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	jwt := Client{
		ID: "jwt", Name: "JWT client", RedirectURIs: []string{"https://jwt.example/cb"},
		AuthMethod: AuthPrivateJWT,
		JWKS:       &JWKS{Keys: []JWK{{Kty: "OKP", Crv: "Ed25519", Kid: "k1", X: base64.RawURLEncoding.EncodeToString(pub)}}},
	}
	h := newHarness(t, func(c *Config) { c.StaticClients = append(c.StaticClients, jwt) })
	b := h.browser()
	newCode := func() string {
		id, p := b.signIn(authorizeQuery("jwt", "https://jwt.example/cb", "openid", "prompt", "login"), h.users[alice])
		if p.is("consent") {
			p = b.consent(id)
		}
		return h.codeFrom(p, "https://jwt.example/cb")
	}
	assertion := func(claims map[string]any) string {
		base := map[string]any{"iss": "jwt", "sub": "jwt", "aud": h.issuer + "/token",
			"exp": h.clock().Add(time.Minute).Unix(), "jti": "j-" + strings.Repeat("1", 8)}
		for k, v := range claims {
			if v == nil {
				delete(base, k)
			} else {
				base[k] = v
			}
		}
		return signAssertion(t, "EdDSA", priv, "k1", base)
	}
	withAssertion := func(code, a string) url.Values {
		f := codeForm(code, "https://jwt.example/cb")
		f.Set("client_assertion_type", clientAssertionType)
		f.Set("client_assertion", a)
		return f
	}

	a := assertion(nil)
	if res := h.token(withAssertion(newCode(), a), "", ""); res.status != 200 {
		t.Fatalf("private_key_jwt = %d %v", res.status, res.body)
	}
	if res := h.token(withAssertion(newCode(), a), "", ""); res.status != 401 {
		t.Fatalf("replayed assertion = %d", res.status)
	}
	for name, claims := range map[string]map[string]any{
		"wrong aud":    {"aud": "https://elsewhere.example", "jti": "2"},
		"expired":      {"exp": h.clock().Add(-time.Hour).Unix(), "jti": "3"},
		"too long":     {"exp": h.clock().Add(time.Hour).Unix(), "jti": "4"},
		"no jti":       {"jti": nil},
		"wrong issuer": {"iss": "rp", "jti": "5"},
	} {
		if res := h.token(withAssertion(newCode(), assertion(claims)), "", ""); res.status != 401 {
			t.Errorf("%s: %d %v", name, res.status, res.body)
		}
	}
	// A secret is not a substitute.
	if res := h.token(codeForm(newCode(), "https://jwt.example/cb"), "jwt", "anything"); res.status != 401 {
		t.Fatalf("secret for a JWT client = %d", res.status)
	}
	// Two methods at once are refused.
	f := withAssertion(newCode(), assertion(map[string]any{"jti": "6"}))
	if res := h.token(f, "jwt", "x"); res.status != 401 {
		t.Fatalf("basic + assertion = %d", res.status)
	}
}

func TestSafeHTTPClientRefusesPrivateAddresses(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer ts.Close()
	if _, err := newSafeHTTPClient(false).Get(ts.URL); err == nil {
		t.Fatal("the safe client reached a loopback server")
	}
	resp, err := newSafeHTTPClient(true).Get(ts.URL)
	if err != nil {
		t.Fatalf("allowPrivate client: %v", err)
	}
	resp.Body.Close()

	redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, ts.URL, http.StatusFound)
	}))
	defer redirecting.Close()
	if _, err := newSafeHTTPClient(true).Get(redirecting.URL); err == nil {
		t.Fatal("the safe client followed a redirect")
	}

	for ip, want := range map[string]bool{
		"8.8.8.8": true, "2606:4700::1111": true,
		"127.0.0.1": false, "10.1.2.3": false, "169.254.169.254": false, "100.64.0.1": false,
		"::1": false, "fe80::1": false, "::ffff:127.0.0.1": false, "0.0.0.0": false, "192.0.2.1": false,
	} {
		if got := publicIP(net.ParseIP(ip)); got != want {
			t.Errorf("publicIP(%s) = %v", ip, got)
		}
	}
}

func TestFetchPublicJSONLimits(t *testing.T) {
	big := strings.Repeat("a", maxFetchBytes+1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/big":
			_, _ = w.Write([]byte(big))
		case "/missing":
			http.NotFound(w, r)
		default:
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	}))
	defer ts.Close()
	h := newHarness(t, func(c *Config) {
		c.ResolveOptions.HTTPClient = ts.Client()
	})
	ctx := context.Background()
	if raw, err := h.srv.fetchPublicJSON(ctx, ts.URL+"/ok"); err != nil || !json.Valid(raw) {
		t.Fatalf("ok fetch = %s %v", raw, err)
	}
	for _, path := range []string{"/big", "/missing"} {
		if _, err := h.srv.fetchPublicJSON(ctx, ts.URL+path); err == nil {
			t.Errorf("%s fetched", path)
		}
	}
	for _, u := range []string{"ftp://x.example/", "https://user:pw@x.example/", "not a url"} {
		if _, err := h.srv.fetchPublicJSON(ctx, u); err == nil {
			t.Errorf("%s fetched", u)
		}
	}
}

func TestStorePruneAndAudit(t *testing.T) {
	ctx := context.Background()
	store, _ := OpenStore(ctx, ":memory:")
	defer store.Close()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }

	if err := store.CreateTxn(ctx, &Txn{ID: "t1", ExpiresAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	fresh, _ := store.Use(ctx, "n1", now.Add(time.Minute))
	again, _ := store.Use(ctx, "n1", now.Add(time.Minute))
	if !fresh || again {
		t.Fatalf("nonce use = %v %v", fresh, again)
	}
	_ = store.Audit(ctx, "e", map[string]any{"k": "v"})
	_ = store.RecordSignIn(ctx, alice, SignInRecord{At: now})

	now = now.Add(365 * 24 * time.Hour)
	if err := store.Prune(ctx, Retention{}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetTxn(ctx, "t1"); err != ErrNotFound {
		t.Fatalf("expired txn = %v", err)
	}
	if fresh, _ := store.Use(ctx, "n1", now.Add(time.Minute)); !fresh {
		t.Fatal("expired nonce was not pruned")
	}
	if evs, _ := store.AuditSince(ctx, time.Time{}); len(evs) != 0 {
		t.Fatalf("audit kept past retention: %v", evs)
	}
	if recs, _ := store.RecentSignIns(ctx, alice, 10); len(recs) != 0 {
		t.Fatalf("sign-ins kept past retention: %v", recs)
	}
	// UpdateTxn on a missing transaction.
	if _, err := store.UpdateTxn(ctx, "nope", func(*Txn) error { return nil }); err != ErrNotFound {
		t.Fatalf("update missing = %v", err)
	}
}

func TestQRSVG(t *testing.T) {
	svg := string(qrSVG("poweur://auth?request=abc"))
	if !strings.HasPrefix(svg, "<svg") || !strings.Contains(svg, "<path d=\"M") || strings.Contains(svg, "script") {
		t.Fatalf("svg = %.80s", svg)
	}
	if qrSVG("") != "" {
		t.Fatal("empty text rendered")
	}
}

func TestClientValidation(t *testing.T) {
	ok := Client{ID: "c1", Name: "C", RedirectURIs: []string{"https://a.example/cb", "https://a.example/cb", "http://127.0.0.1:8080/cb"}, Source: ClientStatic, Secret: "s"}
	if err := ok.validate(false); err != nil {
		t.Fatal(err)
	}
	if len(ok.RedirectURIs) != 2 || ok.Secret != "" || ok.AuthMethod != AuthSecretBasic || ok.Sector != "a.example" {
		t.Fatalf("normalized = %+v", ok)
	}
	// Exact match for confidential clients, even on loopback.
	if !ok.MatchRedirectURI("http://127.0.0.1:8080/cb") || ok.MatchRedirectURI("http://127.0.0.1:9090/cb") {
		t.Fatal("confidential loopback matching is wrong")
	}
	pub := Client{ID: "p1", Name: "P", RedirectURIs: []string{"http://127.0.0.1/cb", "http://localhost/cb"}, Source: ClientStatic}
	if err := pub.validate(false); err != nil {
		t.Fatal(err)
	}
	if !pub.MatchRedirectURI("http://127.0.0.1:5555/cb") {
		t.Fatal("public loopback must accept any port")
	}
	if pub.MatchRedirectURI("http://localhost:5555/cb") || pub.MatchRedirectURI("http://127.0.0.1:5555/other") {
		t.Fatal("public loopback matching is too loose")
	}
	for name, c := range map[string]Client{
		"bad id":               {ID: "has space", Name: "X", RedirectURIs: []string{"https://a.example/cb"}},
		"too many":             {ID: "c2", Name: "X", RedirectURIs: strings.Split(strings.Repeat("https://a.example/cb,", 21), ",")[:21]},
		"relative":             {ID: "c3", Name: "X", RedirectURIs: []string{"/cb"}},
		"custom":               {ID: "c4", Name: "X", RedirectURIs: []string{"com.example.app:/cb"}},
		"secret+none":          {ID: "c5", Name: "X", RedirectURIs: []string{"https://a.example/cb"}, Secret: "s", AuthMethod: AuthNone, Source: ClientStatic},
		"unknown auth":         {ID: "c6", Name: "X", RedirectURIs: []string{"https://a.example/cb"}, AuthMethod: "tls_client_auth"},
		"first party":          {ID: "c7", Name: "X", RedirectURIs: []string{"https://a.example/cb"}, FirstParty: true, Source: ClientRegistered},
		"secret on registered": {ID: "c8", Name: "X", RedirectURIs: []string{"https://a.example/cb"}, Secret: "s", Source: ClientRegistered},
	} {
		c := c
		if err := c.validate(false); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestRateLimits(t *testing.T) {
	h := newHarness(t, func(c *Config) {
		c.RateLimits = RateLimits{Authorize: 2, Identify: 1, Callback: 1, Token: 1, Console: 1}
	})
	b := h.browser()
	for i := 0; i < 2; i++ {
		if p := b.get(authorizeQuery("rp", rpRedirect, "openid")); p.status != http.StatusSeeOther {
			t.Fatalf("authorize %d = %d", i, p.status)
		}
	}
	p := b.get(authorizeQuery("rp", rpRedirect, "openid"))
	if p.status != http.StatusTooManyRequests || p.header.Get("Retry-After") == "" || !strings.Contains(p.body, "Too many attempts") {
		t.Fatalf("third authorize = %d %v", p.status, p.header)
	}
	// Other surfaces have their own budgets, and the window resets.
	if res := h.token(url.Values{"grant_type": {"authorization_code"}}, "rp", rpSecret); res.status != 400 {
		t.Fatalf("first token = %d", res.status)
	}
	if res := h.token(url.Values{}, "rp", rpSecret); res.status != http.StatusTooManyRequests || res.body["error"] != "slow_down" {
		t.Fatalf("second token = %d %v", res.status, res.body)
	}
	h.advance(time.Minute)
	if p := b.get(authorizeQuery("rp", rpRedirect, "openid")); p.status != http.StatusSeeOther {
		t.Fatalf("after the window = %d", p.status)
	}
	// Static pages are never limited.
	for i := 0; i < 5; i++ {
		if p := b.get("/.well-known/openid-configuration"); p.status != 200 {
			t.Fatalf("discovery = %d", p.status)
		}
	}
}

func TestClientIPTrustsProxiesOnlyWhenTold(t *testing.T) {
	h := newHarness(t)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.5:1234"
	r.Header.Set("X-Forwarded-For", "1.1.1.1, 203.0.113.7")
	if got := h.srv.clientIP(r); got != "10.0.0.5" {
		t.Fatalf("untrusted = %s", got)
	}
	h.srv.cfg.TrustProxyHeaders = true
	if got := h.srv.clientIP(r); got != "203.0.113.7" {
		t.Fatalf("trusted = %s (must be the proxy-appended hop, not the client-supplied one)", got)
	}
	r.Header.Set("X-Forwarded-For", "garbage")
	if got := h.srv.clientIP(r); got != "10.0.0.5" {
		t.Fatalf("garbage header = %s", got)
	}
}
