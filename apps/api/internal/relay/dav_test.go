package relay

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/poweur/api/internal/config"
	"github.com/poweur/api/internal/crypto"
	"github.com/poweur/api/internal/dns"
	idpkg "github.com/poweur/identity"
)

type davTestIdentity struct {
	name string
	pub  ed25519.PublicKey
	priv ed25519.PrivateKey
}

func newDAVTestServer(t *testing.T, quota, maxFile int64) (*Server, *httptest.Server) {
	t.Helper()
	cfg := config.Config{
		ListenAddr:           ":0",
		RelayAddress:         "relay.test",
		RelayScheme:          "http",
		DNSTTL:               time.Minute,
		ChallengeTTL:         time.Minute,
		Version:              "test",
		DataDir:              t.TempDir(),
		HostedDomains:        []string{"poweur.net"},
		ResolverAllowPrivate: true,
		MaxIdentityBytes:     quota,
		MaxFileBytes:         maxFile,
		RateLimits:           config.RateLimits{PerMinute: 100000, PerHour: 100000, PerDay: 100000},
	}
	server := NewServer(cfg, dns.NewNetResolver(), dns.NewProviderFactory(cfg))
	ts := httptest.NewServer(server.Router())
	t.Cleanup(ts.Close)
	server.cfg.RelayAddress = strings.TrimPrefix(ts.URL, "http://")
	server.cfg.MaxIdentityBytes = quota
	server.cfg.MaxFileBytes = maxFile
	return server, ts
}

func registerDAVIdentity(t *testing.T, server *Server, ts *httptest.Server, name string) davTestIdentity {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(nil)
	pubB64 := base64.RawURLEncoding.EncodeToString(pub)
	issued := time.Now().UTC().Format(time.RFC3339)
	nonce := "n-" + name
	canon := crypto.CanonicalIdentityRegistration(name, pubB64, "", server.cfg.RelayAddress, issued, nonce)
	sig := base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, []byte(canon)))
	doc := idpkg.NewDocument(name, idpkg.FormatEd25519PublicKey(pub), "", server.cfg.RelayAddress, nil)
	doc.UpdatedAt = issued
	if err := doc.Sign(priv); err != nil {
		t.Fatal(err)
	}
	docRaw, _ := json.Marshal(doc)
	body, _ := json.Marshal(IdentityRequest{
		Identity: name, PublicKey: pubB64, IssuedAt: issued, Nonce: nonce,
		IdentitySignature: sig, IdentityDocument: docRaw,
	})
	resp, err := http.Post(ts.URL+"/identities", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("register %s: %d %s", name, resp.StatusCode, raw)
	}
	return davTestIdentity{name: name, pub: pub, priv: priv}
}

func mintDAVToken(t *testing.T, ts *httptest.Server, id davTestIdentity, audience, scope string) string {
	t.Helper()
	issued := time.Now().UTC().Format(time.RFC3339)
	nonce := "tok-" + time.Now().Format("150405.000000000")
	aud := audience
	if aud == "" {
		aud = id.name
	}
	sc := scope
	if sc == "" {
		if aud == id.name {
			sc = "dav:full"
		} else {
			sc = "dav:read"
		}
	}
	canon := crypto.CanonicalDAVToken(id.name, aud, sc, issued, nonce)
	sig := base64.RawURLEncoding.EncodeToString(ed25519.Sign(id.priv, []byte(canon)))
	body, _ := json.Marshal(DAVTokenRequest{
		Identity: id.name, Audience: aud, Scope: sc,
		IssuedAt: issued, Nonce: nonce, Signature: sig,
	})
	resp, err := http.Post(ts.URL+"/auth/dav-token", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("dav-token: %d %s", resp.StatusCode, raw)
	}
	var tr DAVTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		t.Fatal(err)
	}
	return tr.Token
}

func davReq(t *testing.T, ts *httptest.Server, method, path, token string, body []byte, hdr map[string]string) *http.Response {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, ts.URL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestDAVOwnerLifecycle(t *testing.T) {
	server, ts := newDAVTestServer(t, 0, 0)
	alice := registerDAVIdentity(t, server, ts, "davalice.poweur.net")
	token := mintDAVToken(t, ts, alice, "", "")
	base := "/dav/" + alice.name

	// PUT
	resp := davReq(t, ts, http.MethodPut, base+"/private/notes.txt", token, []byte("hello dav"), nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT status %d", resp.StatusCode)
	}

	// GET with content-hash ETag
	resp = davReq(t, ts, http.MethodGet, base+"/private/notes.txt", token, nil, nil)
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(got) != "hello dav" {
		t.Fatalf("GET body %q", got)
	}
	if etag := resp.Header.Get("ETag"); !strings.HasPrefix(etag, `"sha256-`) {
		t.Fatalf("expected content-hash etag, got %q", etag)
	}

	// PROPFIND root (depth 1)
	resp = davReq(t, ts, "PROPFIND", base+"/", token, nil, map[string]string{"Depth": "1"})
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 207 {
		t.Fatalf("PROPFIND status %d", resp.StatusCode)
	}
	for _, root := range []string{"poweur-sys", "public", "shared", "private", "apps"} {
		if !strings.Contains(string(raw), root) {
			t.Fatalf("PROPFIND missing root %s", root)
		}
	}

	// MKCOL + MOVE + DELETE
	resp = davReq(t, ts, "MKCOL", base+"/private/projects", token, nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("MKCOL status %d", resp.StatusCode)
	}
	resp = davReq(t, ts, "MOVE", base+"/private/notes.txt", token, nil, map[string]string{
		"Destination": ts.URL + base + "/private/projects/notes.txt",
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		t.Fatalf("MOVE status %d", resp.StatusCode)
	}
	resp = davReq(t, ts, http.MethodDelete, base+"/private/projects", token, nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE status %d", resp.StatusCode)
	}

	// Roots cannot be deleted
	resp = davReq(t, ts, http.MethodDelete, base+"/private", token, nil, nil)
	resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		t.Fatal("deleting a root must fail")
	}

	// id.json is relay-managed: owner write via DAV is denied
	resp = davReq(t, ts, http.MethodPut, base+"/poweur-sys/public/id.json", token, []byte("{}"), nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("id.json PUT status %d want 403", resp.StatusCode)
	}

	// Quota endpoint
	resp = davReq(t, ts, http.MethodGet, "/files/"+alice.name+"/quota", token, nil, nil)
	var q map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&q)
	resp.Body.Close()
	if q["provider"] != "relay-fs" {
		t.Fatalf("quota provider %v", q["provider"])
	}
	if q["change_id"].(float64) < 3 {
		t.Fatalf("change_id should reflect mutations, got %v", q["change_id"])
	}
}

func TestDAVVisitorAndAnonymousRules(t *testing.T) {
	server, ts := newDAVTestServer(t, 0, 0)
	alice := registerDAVIdentity(t, server, ts, "davowner.poweur.net")
	bob := registerDAVIdentity(t, server, ts, "davvisitor.poweur.net")

	ownerToken := mintDAVToken(t, ts, alice, "", "")
	base := "/dav/" + alice.name
	resp := davReq(t, ts, http.MethodPut, base+"/public/hello.txt", ownerToken, []byte("public data"), nil)
	resp.Body.Close()
	resp = davReq(t, ts, http.MethodPut, base+"/private/secret.txt", ownerToken, []byte("secret"), nil)
	resp.Body.Close()

	// Visitor obtains a token for alice's tree (audience) with their own key.
	visitorToken := mintDAVToken(t, ts, bob, alice.name, "dav:read")

	resp = davReq(t, ts, http.MethodGet, base+"/public/hello.txt", visitorToken, nil, nil)
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(got) != "public data" {
		t.Fatalf("visitor /public read: %d %q", resp.StatusCode, got)
	}

	resp = davReq(t, ts, http.MethodGet, base+"/private/secret.txt", visitorToken, nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("visitor /private read: %d want 403", resp.StatusCode)
	}
	resp = davReq(t, ts, http.MethodGet, base+"/shared/x", visitorToken, nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("visitor /shared read without grant: %d want 403", resp.StatusCode)
	}
	resp = davReq(t, ts, http.MethodPut, base+"/public/spam.txt", visitorToken, []byte("x"), nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("visitor /public write: %d want 403", resp.StatusCode)
	}

	// Visitor write scopes mint fine since EPIC-005 (grants decide what a
	// visitor may actually write) — but without a grant, writes still 403.
	fullTok := mintDAVToken(t, ts, bob, alice.name, "dav:full")
	resp = davReq(t, ts, http.MethodPut, base+"/shared/nope.txt", fullTok, []byte("x"), nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("ungranted visitor write with dav:full: %d want 403", resp.StatusCode)
	}

	// Anonymous: poweur-sys/public readable, everything else 401.
	resp = davReq(t, ts, http.MethodGet, base+"/poweur-sys/public/id.json", "", nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("anonymous id.json read: %d", resp.StatusCode)
	}
	resp = davReq(t, ts, http.MethodGet, base+"/public/hello.txt", "", nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous /public read: %d want 401", resp.StatusCode)
	}

	// Cross-identity reads are audit-logged in the owner's tree.
	resp = davReq(t, ts, http.MethodGet, base+"/poweur-sys/relay/logs/access.log", ownerToken, nil, nil)
	logRaw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(logRaw), bob.name) {
		t.Fatalf("audit log missing visitor entry: %d %s", resp.StatusCode, logRaw)
	}

	// Token revocation is immediate.
	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/auth/dav-token/"+visitorToken, nil)
	rv, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	rv.Body.Close()
	resp = davReq(t, ts, http.MethodGet, base+"/public/hello.txt", visitorToken, nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked token: %d want 401", resp.StatusCode)
	}
}

func TestDAVQuotaEnforcement(t *testing.T) {
	server, ts := newDAVTestServer(t, 64, 32)
	alice := registerDAVIdentity(t, server, ts, "davquota.poweur.net")
	token := mintDAVToken(t, ts, alice, "", "")
	base := "/dav/" + alice.name

	// id.json already occupies bytes; a small file within limits is fine.
	resp := davReq(t, ts, http.MethodPut, base+"/private/small.txt", token, []byte("0123456789"), nil)
	resp.Body.Close()
	// Over max-file cap → 413.
	resp = davReq(t, ts, http.MethodPut, base+"/private/big.txt", token, bytes.Repeat([]byte("x"), 64), nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("max-file PUT: %d want 413", resp.StatusCode)
	}
	// Over quota (used ≥ id.json + 10, quota 64) → 507.
	resp = davReq(t, ts, http.MethodPut, base+"/private/fill.txt", token, bytes.Repeat([]byte("y"), 30), nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusInsufficientStorage {
		t.Fatalf("quota PUT: %d want 507", resp.StatusCode)
	}
}

func TestDAVAppPasswordBasicAuth(t *testing.T) {
	server, ts := newDAVTestServer(t, 0, 0)
	alice := registerDAVIdentity(t, server, ts, "davbasic.poweur.net")
	token := mintDAVToken(t, ts, alice, "", "")
	base := "/dav/" + alice.name

	password, err := idpkg.GenerateAppPassword()
	if err != nil {
		t.Fatal(err)
	}
	hash, err := idpkg.HashAppPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	fileRaw, _ := json.Marshal(idpkg.AppPasswordsFile{Passwords: []idpkg.AppPassword{{
		Name: "finder", Hash: hash, Scope: "dav:full", CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}}})
	resp := davReq(t, ts, http.MethodPut, base+"/poweur-sys/relay/app-passwords.json", token, fileRaw, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("write app-passwords.json: %d", resp.StatusCode)
	}

	basic := base64.StdEncoding.EncodeToString([]byte(alice.name + ":" + password))
	req, _ := http.NewRequest("PROPFIND", ts.URL+base+"/private/", nil)
	req.Header.Set("Authorization", "Basic "+basic)
	req.Header.Set("Depth", "1")
	bresp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	bresp.Body.Close()
	if bresp.StatusCode != 207 {
		t.Fatalf("Basic PROPFIND: %d want 207", bresp.StatusCode)
	}

	// Wrong password fails.
	wrong := base64.StdEncoding.EncodeToString([]byte(alice.name + ":nope"))
	req, _ = http.NewRequest("PROPFIND", ts.URL+base+"/private/", nil)
	req.Header.Set("Authorization", "Basic "+wrong)
	req.Header.Set("Depth", "1")
	bresp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	bresp.Body.Close()
	if bresp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad Basic: %d want 401", bresp.StatusCode)
	}

	// Revocation = file edit; fails immediately.
	empty, _ := json.Marshal(idpkg.AppPasswordsFile{})
	resp = davReq(t, ts, http.MethodPut, base+"/poweur-sys/relay/app-passwords.json", token, empty, nil)
	resp.Body.Close()
	req, _ = http.NewRequest("PROPFIND", ts.URL+base+"/private/", nil)
	req.Header.Set("Authorization", "Basic "+basic)
	req.Header.Set("Depth", "1")
	bresp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	bresp.Body.Close()
	if bresp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked app password: %d want 401", bresp.StatusCode)
	}
}

func TestDAVVanityHostRouting(t *testing.T) {
	server, ts := newDAVTestServer(t, 0, 0)
	alice := registerDAVIdentity(t, server, ts, "davhost.poweur.net")
	token := mintDAVToken(t, ts, alice, "", "")

	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/dav/private/via-host.txt", bytes.NewReader([]byte("host routed")))
	req.Host = alice.name
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("host-routed PUT: %d", resp.StatusCode)
	}

	// Same file readable via the canonical path.
	resp = davReq(t, ts, http.MethodGet, "/dav/"+alice.name+"/private/via-host.txt", token, nil, nil)
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(got) != "host routed" {
		t.Fatalf("canonical read after host-routed write: %q", got)
	}
}

func TestDAVCanonicalPathOnIdentityHost(t *testing.T) {
	server, ts := newDAVTestServer(t, 0, 0)
	alice := registerDAVIdentity(t, server, ts, "davcanon.poweur.net")
	bob := registerDAVIdentity(t, server, ts, "davpeer.poweur.net")
	aliceTok := mintDAVToken(t, ts, alice, "", "")
	bobTok := mintDAVToken(t, ts, bob, "", "")

	// The web app on https://<identity> still writes /dav/<identity>/… —
	// Host must not swallow the identity segment as a tree root.
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/dav/"+alice.name+"/poweur-sys/relay/inbox-policy.json",
		bytes.NewReader([]byte(`{"version":1,"mode":"open"}`)))
	req.Host = alice.name
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		t.Fatalf("canonical PUT on identity Host: %d %s", resp.StatusCode, raw)
	}

	got := davReq(t, ts, http.MethodGet, "/dav/"+alice.name+"/poweur-sys/relay/inbox-policy.json", aliceTok, nil, nil)
	body, _ := io.ReadAll(got.Body)
	got.Body.Close()
	if got.StatusCode != http.StatusOK || !strings.Contains(string(body), `"mode":"open"`) {
		t.Fatalf("canonical read after identity-Host PUT: %d %s", got.StatusCode, body)
	}

	// Same Host, another identity's canonical tree — not alice's unknown root.
	put := davReq(t, ts, http.MethodPut, "/dav/"+bob.name+"/public/hello.txt", bobTok, []byte("hi"), nil)
	put.Body.Close()
	if put.StatusCode != http.StatusCreated {
		t.Fatalf("bob public PUT: %d", put.StatusCode)
	}
	visitTok := mintDAVToken(t, ts, alice, bob.name, "dav:read")
	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/dav/"+bob.name+"/public/hello.txt", nil)
	req.Host = alice.name
	req.Header.Set("Authorization", "Bearer "+visitTok)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "hi" {
		t.Fatalf("canonical visit from identity Host: %d %s", resp.StatusCode, body)
	}
}

func TestPubWebServing(t *testing.T) {
	server, ts := newDAVTestServer(t, 0, 0)
	alice := registerDAVIdentity(t, server, ts, "davpub.poweur.net")
	token := mintDAVToken(t, ts, alice, "", "")
	base := "/dav/" + alice.name

	for _, dir := range []string{"/public/site", "/public/unmarked"} {
		resp := davReq(t, ts, "MKCOL", base+dir, token, nil, nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("MKCOL %s: %d", dir, resp.StatusCode)
		}
	}
	for _, put := range []struct {
		path string
		body string
	}{
		{"/public/site/report.txt", "annual report"},
		{"/public/site/page.html", "<script>alert(1)</script>"},
		{"/public/unmarked/hidden.txt", "not for the web"},
	} {
		resp := davReq(t, ts, http.MethodPut, base+put.path, token, []byte(put.body), nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("PUT %s: %d", put.path, resp.StatusCode)
		}
	}
	// Mark /public/site as web-public with listings enabled.
	resp := davReq(t, ts, http.MethodPut, base+"/public/site/.poweur-web-public", token, []byte(`{"listings":true}`), nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT marker: %d", resp.StatusCode)
	}

	pubGet := func(path string) *http.Response {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		req.Host = alice.name
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	// Marked file served anonymously with nosniff.
	r := pubGet("/pub/site/report.txt")
	body, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != http.StatusOK || string(body) != "annual report" {
		t.Fatalf("pub file: %d %q", r.StatusCode, body)
	}
	if r.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing nosniff")
	}

	// HTML is neutered to text/plain.
	r = pubGet("/pub/site/page.html")
	r.Body.Close()
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("html content-type %q want text/plain", ct)
	}

	// Listings opt-in works.
	r = pubGet("/pub/site/")
	listing, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != http.StatusOK || !strings.Contains(string(listing), "report.txt") {
		t.Fatalf("listing: %d %s", r.StatusCode, listing)
	}

	// Unmarked folders stay invisible to the web.
	r = pubGet("/pub/unmarked/hidden.txt")
	r.Body.Close()
	if r.StatusCode != http.StatusNotFound {
		t.Fatalf("unmarked pub: %d want 404", r.StatusCode)
	}

	// Path escape attempts never serve files: the mux cleans ".." with a
	// redirect (away from /pub), and the handler's own root check rejects
	// anything that resolves outside /public.
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/pub/../poweur-sys/public/id.json", nil)
	req.Host = alice.name
	r, err := noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	escBody, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode == http.StatusOK && strings.Contains(string(escBody), "public_key") {
		t.Fatal("pub must not escape /public")
	}
}
