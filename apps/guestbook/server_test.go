package guestbook

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/poweur/identity"
	"github.com/poweur/identity/signin"
)

const (
	rpOrigin = "https://guestbook.poweur.net"
	who      = "alice.poweur.net"
)

var testNow = time.Date(2026, 1, 15, 9, 30, 0, 0, time.UTC)

// user is a Poweur identity that can approve requests, plus the document a
// verifier would resolve for it.
type user struct {
	name string
	priv ed25519.PrivateKey
	doc  identity.IdentityDocument
}

func newUser(t *testing.T, name string) user {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return user{
		name: name,
		priv: priv,
		doc: identity.IdentityDocument{
			Version:   1,
			Identity:  name,
			PublicKey: identity.FormatEd25519PublicKey(pub),
			Relay:     "https://relay.poweur.net",
			UpdatedAt: "2026-01-01T00:00:00Z",
		},
	}
}

// zone is a fake resolver: the published names, and nothing else resolves.
type zone map[string]identity.IdentityDocument

func (z zone) Resolve(_ context.Context, name string) (identity.Result, error) {
	doc, ok := z[strings.ToLower(name)]
	if !ok {
		return identity.Result{}, &url.Error{Op: "resolve", URL: name, Err: http.ErrNoLocation}
	}
	return identity.Result{Document: doc, Source: "web"}, nil
}

func newServer(t *testing.T, users ...user) (*Server, zone) {
	t.Helper()
	z := zone{}
	for _, u := range users {
		z[u.name] = u.doc
	}
	srv, err := New(Config{
		Origin:   rpOrigin,
		Resolver: z,
		Now:      func() time.Time { return testNow },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return srv, z
}

// start presses "Sign in with Poweur ID" and returns what the browser got.
func start(t *testing.T, srv *Server) StartResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/auth/start", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /auth/start = %d: %s", rec.Code, rec.Body.String())
	}
	var out StartResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode start: %v", err)
	}
	return out
}

// approve signs a request the way a signer would.
func approve(t *testing.T, u user, encodedRequest string) string {
	t.Helper()
	req, err := identity.DecodeSignInRequest(encodedRequest)
	if err != nil {
		t.Fatalf("decode request: %v", err)
	}
	resp, err := signin.Sign(req, signin.SignOptions{
		Identity:   u.name,
		PrivateKey: u.priv,
		Now:        func() time.Time { return testNow },
	})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	encoded, err := identity.EncodeSignInResponse(resp)
	if err != nil {
		t.Fatalf("encode response: %v", err)
	}
	return encoded
}

func post(t *testing.T, srv *Server, path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		r.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, r)
	return rec
}

func get(t *testing.T, srv *Server, path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	for _, c := range cookies {
		r.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, r)
	return rec
}

// --- Construction ------------------------------------------------------------

func TestNewRejectsUnusableConfig(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
	}{
		{"no origin", Config{}},
		{"origin with a path", Config{Origin: "https://guestbook.poweur.net/app"}},
		{"origin that is not a URL", Config{Origin: "guestbook.poweur.net"}},
		// A single-label host has no reverse-DNS namespace to confine it to.
		{"single-label host", Config{Origin: "https://localhost"}},
		{"unknown scope", Config{Origin: rpOrigin, Scopes: []string{"root:everything"}}},
		// The check that matters: an RP cannot ask for another app's tree,
		// and it is refused here as well as at the signer and the relay.
		{"scope in another app's namespace", Config{Origin: rpOrigin, Scopes: []string{"dav:rw:apps/net.poweur.mail"}}},
		{"scope outside apps/", Config{Origin: rpOrigin, Scopes: []string{"dav:rw:poweur-sys"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(tc.cfg); err == nil {
				t.Fatalf("expected New to refuse %+v", tc.cfg)
			}
		})
	}
}

func TestNewAcceptsItsOwnNamespace(t *testing.T) {
	srv, err := New(Config{Origin: rpOrigin, Scopes: []string{"dav:rw:/apps/net.poweur.guestbook/", "profile:read"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := srv.AppID(); got != AppID {
		t.Fatalf("app id = %q, want %q", got, AppID)
	}
	// Normalized and sorted, so the consent screen and the signature agree.
	want := []string{"dav:rw:apps/net.poweur.guestbook", "profile:read"}
	if got := srv.Metadata().Scopes; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("scopes = %v, want %v", got, want)
	}
}

// --- Metadata ----------------------------------------------------------------

func TestMetadataIsWhatASignerWillAccept(t *testing.T) {
	srv, _ := newServer(t, newUser(t, who))
	rec := get(t, srv, "/.well-known/poweur.json")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var meta signin.Metadata
	if err := json.Unmarshal(rec.Body.Bytes(), &meta); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The signer validates it against the origin it fetched it from; if that
	// fails, no signer will ever render this RP's name.
	if err := meta.Validate(rpOrigin); err != nil {
		t.Fatalf("published metadata is not valid at its own origin: %v", err)
	}
	if meta.Name == "" || meta.AppID != AppID {
		t.Fatalf("metadata = %+v", meta)
	}
	// Publishing the list is the hardened posture; the callback must be in it.
	if !meta.AllowsResponseURI(rpOrigin + "/auth/callback") {
		t.Fatal("the RP does not publish its own callback")
	}
	if meta.AllowsResponseURI(rpOrigin + "/somewhere/else") {
		t.Fatal("an unpublished same-origin URI should not be allowed")
	}
}

// --- The happy path ----------------------------------------------------------

func TestSignInEndToEnd(t *testing.T) {
	alice := newUser(t, who)
	srv, _ := newServer(t, alice)

	started := start(t, srv)
	if started.RequestID == "" || started.Request == "" {
		t.Fatalf("start = %+v", started)
	}
	if !strings.HasPrefix(started.DeepLink, "poweur://auth?request=") {
		t.Fatalf("deep link = %q", started.DeepLink)
	}

	// The request must be one a signer would accept: bound to this origin,
	// short-lived, and delivering only to a published callback.
	req, err := identity.DecodeSignInRequest(started.Request)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := req.Validate(testNow); err != nil {
		t.Fatalf("the RP built a request it could not itself accept: %v", err)
	}
	if err := signin.CheckRequestAgainstMetadata(req, srv.Metadata()); err != nil {
		t.Fatalf("request does not match published metadata: %v", err)
	}

	// The waiting tab sees nothing yet.
	if got := pollStatus(t, srv, started.RequestID); got != "pending" {
		t.Fatalf("poll before approval = %q", got)
	}

	// The phone approves and delivers to response_uri.
	rec := post(t, srv, "/auth/callback", `{"response":"`+approve(t, alice, started.Request)+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("callback = %d: %s", rec.Code, rec.Body.String())
	}

	// The tab polls again and is handed its cookie.
	pollRec := get(t, srv, "/auth/poll?request_id="+url.QueryEscape(started.RequestID))
	var poll map[string]any
	_ = json.Unmarshal(pollRec.Body.Bytes(), &poll)
	if poll["status"] != "complete" || poll["identity"] != who {
		t.Fatalf("poll = %v", poll)
	}
	cookie := sessionCookie(t, pollRec)

	// And the cookie is a signed-in session.
	me := get(t, srv, "/api/session", cookie)
	var session map[string]any
	_ = json.Unmarshal(me.Body.Bytes(), &session)
	if session["signed_in"] != true || session["identity"] != who {
		t.Fatalf("session = %v", session)
	}

	// Which can write to the guestbook.
	if rec := post(t, srv, "/api/entries", `{"message":"hello from alice"}`, cookie); rec.Code != http.StatusCreated {
		t.Fatalf("post entry = %d: %s", rec.Code, rec.Body.String())
	}
	entries := srv.Entries()
	if len(entries) != 1 || entries[0].Identity != who || entries[0].Message != "hello from alice" {
		t.Fatalf("entries = %+v", entries)
	}
	// Login-only: nothing was written to anyone's home.
	if entries[0].StoredAt != "" {
		t.Fatalf("entry claims home storage without a grant: %+v", entries[0])
	}

	// The poll is one-shot: a second read must not hand the cookie out again.
	if got := pollStatus(t, srv, started.RequestID); got != "unknown" {
		t.Fatalf("second poll = %q, want unknown", got)
	}
}

func TestRedirectFlowSetsTheCookieDirectly(t *testing.T) {
	alice := newUser(t, who)
	srv, _ := newServer(t, alice)
	started := start(t, srv)
	encoded := approve(t, alice, started.Request)

	rec := get(t, srv, "/auth/callback?response="+url.QueryEscape(encoded))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("redirect callback = %d: %s", rec.Code, rec.Body.String())
	}
	cookie := sessionCookie(t, rec)
	me := get(t, srv, "/api/session", cookie)
	if !strings.Contains(me.Body.String(), who) {
		t.Fatalf("session after redirect = %s", me.Body.String())
	}
}

func TestCallbackAcceptsEveryDeliveryShape(t *testing.T) {
	alice := newUser(t, who)
	for _, shape := range []string{"json-envelope", "raw-encoded", "raw-json-object", "form"} {
		t.Run(shape, func(t *testing.T) {
			srv, _ := newServer(t, alice)
			started := start(t, srv)
			encoded := approve(t, alice, started.Request)

			var r *http.Request
			switch shape {
			case "json-envelope":
				r = httptest.NewRequest(http.MethodPost, "/auth/callback", strings.NewReader(`{"response":"`+encoded+`"}`))
				r.Header.Set("Content-Type", "application/json")
			case "raw-encoded":
				r = httptest.NewRequest(http.MethodPost, "/auth/callback", strings.NewReader(encoded))
			case "raw-json-object":
				raw, _ := base64.RawURLEncoding.DecodeString(encoded)
				r = httptest.NewRequest(http.MethodPost, "/auth/callback", strings.NewReader(string(raw)))
				r.Header.Set("Content-Type", "application/json")
			case "form":
				r = httptest.NewRequest(http.MethodPost, "/auth/callback", strings.NewReader("response="+url.QueryEscape(encoded)))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			}
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, r)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s callback = %d: %s", shape, rec.Code, rec.Body.String())
			}
		})
	}
}

// --- Failure modes -----------------------------------------------------------

func TestCallbackRejections(t *testing.T) {
	alice := newUser(t, who)
	mallory := newUser(t, "mallory.poweur.net")

	tests := []struct {
		name string
		// mutate edits the signed approval after signing, or replaces it.
		build func(t *testing.T, srv *Server, started StartResponse) string
	}{
		{"empty body", func(*testing.T, *Server, StartResponse) string { return "" }},
		{"not base64 or JSON", func(*testing.T, *Server, StartResponse) string { return "%%%%" }},
		{"unsigned response object", func(t *testing.T, srv *Server, started StartResponse) string {
			req, _ := identity.DecodeSignInRequest(started.Request)
			resp, _ := identity.NewSignInResponse(req, who, identity.SignInKeyIDIdentity)
			resp.Signature = base64.RawURLEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
			encoded, _ := identity.EncodeSignInResponse(resp)
			return encoded
		}},
		{"signed by the wrong identity's key", func(t *testing.T, srv *Server, started StartResponse) string {
			// Mallory's key over Alice's name: the resolver hands back
			// Alice's document and the signature does not check out.
			req, _ := identity.DecodeSignInRequest(started.Request)
			resp, _ := signin.Sign(req, signin.SignOptions{
				Identity: who, PrivateKey: mallory.priv, Now: func() time.Time { return testNow },
			})
			encoded, _ := identity.EncodeSignInResponse(resp)
			return encoded
		}},
		{"identity that does not resolve", func(t *testing.T, srv *Server, started StartResponse) string {
			req, _ := identity.DecodeSignInRequest(started.Request)
			resp, _ := signin.Sign(req, signin.SignOptions{
				Identity: "nobody.poweur.net", PrivateKey: mallory.priv, Now: func() time.Time { return testNow },
			})
			encoded, _ := identity.EncodeSignInResponse(resp)
			return encoded
		}},
		{"statement edited after signing", func(t *testing.T, srv *Server, started StartResponse) string {
			encoded := approve(t, alice, started.Request)
			resp, _ := identity.DecodeSignInResponse(encoded)
			resp.Statement = "Sign in and hand over the keys"
			out, _ := identity.EncodeSignInResponse(resp)
			return out
		}},
		{"approval collected at another origin", func(t *testing.T, srv *Server, started StartResponse) string {
			// A phishing site's own request, perfectly approved — at the
			// phishing site. It is signed over that origin and worthless here.
			evil, err := New(Config{Origin: "https://evil.example", Now: func() time.Time { return testNow }})
			if err != nil {
				t.Fatalf("New(evil): %v", err)
			}
			evilStart := start(t, evil)
			return approve(t, alice, evilStart.Request)
		}},
		{"expired approval", func(t *testing.T, srv *Server, started StartResponse) string {
			encoded := approve(t, alice, started.Request)
			resp, _ := identity.DecodeSignInResponse(encoded)
			resp.IssuedAt = "2026-01-15T09:00:00Z"
			resp.ExpiresAt = "2026-01-15T09:02:00Z"
			out, _ := identity.EncodeSignInResponse(resp)
			return out
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := newServer(t, alice)
			started := start(t, srv)
			rec := post(t, srv, "/auth/callback", `{"response":"`+tc.build(t, srv, started)+`"}`)
			if rec.Code == http.StatusOK {
				t.Fatalf("expected the callback to refuse; got 200: %s", rec.Body.String())
			}
			if len(srv.Entries()) != 0 {
				t.Fatal("a refused sign-in left state behind")
			}
		})
	}
}

func TestApprovalIsSpentOnce(t *testing.T) {
	alice := newUser(t, who)
	srv, _ := newServer(t, alice)
	started := start(t, srv)
	encoded := approve(t, alice, started.Request)

	if rec := post(t, srv, "/auth/callback", `{"response":"`+encoded+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("first delivery = %d: %s", rec.Code, rec.Body.String())
	}
	// Captured off the wire and replayed inside the five-minute window.
	rec := post(t, srv, "/auth/callback", `{"response":"`+encoded+`"}`)
	if rec.Code == http.StatusOK {
		t.Fatal("a replayed approval was accepted")
	}
}

func TestFailedApprovalIsReportedToTheWaitingTab(t *testing.T) {
	alice := newUser(t, who)
	srv, _ := newServer(t, alice)
	started := start(t, srv)

	encoded := approve(t, alice, started.Request)
	resp, _ := identity.DecodeSignInResponse(encoded)
	resp.Statement = "tampered"
	broken, _ := identity.EncodeSignInResponse(resp)

	if rec := post(t, srv, "/auth/callback", `{"response":"`+broken+`"}`); rec.Code == http.StatusOK {
		t.Fatal("tampered approval accepted")
	}
	if got := pollStatus(t, srv, started.RequestID); got != "failed" {
		t.Fatalf("poll after a failed approval = %q, want failed", got)
	}
}

func TestPollExpiry(t *testing.T) {
	alice := newUser(t, who)
	now := testNow
	z := zone{alice.name: alice.doc}
	srv, err := New(Config{Origin: rpOrigin, Resolver: z, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	started := start(t, srv)
	now = now.Add(10 * time.Minute)
	if got := pollStatus(t, srv, started.RequestID); got != "expired" {
		t.Fatalf("poll after expiry = %q", got)
	}
	if got := pollStatus(t, srv, "req_never_issued"); got != "unknown" {
		t.Fatalf("poll for an unknown request = %q", got)
	}
	if rec := get(t, srv, "/auth/poll"); rec.Code != http.StatusBadRequest {
		t.Fatalf("poll without request_id = %d", rec.Code)
	}
}

// --- The RP's own session ----------------------------------------------------

func TestEntriesRequireASession(t *testing.T) {
	alice := newUser(t, who)
	srv, _ := newServer(t, alice)

	if rec := post(t, srv, "/api/entries", `{"message":"hi"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous post = %d", rec.Code)
	}
	if rec := post(t, srv, "/api/entries", `{"message":"hi"}`,
		&http.Cookie{Name: cookieName, Value: "made-up"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("forged cookie = %d", rec.Code)
	}

	cookie := signIn(t, srv, alice)
	for _, body := range []string{`{"message":""}`, `{"message":"   "}`, `not json`} {
		if rec := post(t, srv, "/api/entries", body, cookie); rec.Code != http.StatusBadRequest {
			t.Fatalf("post %q = %d", body, rec.Code)
		}
	}
	if rec := post(t, srv, "/api/entries", `{"message":"`+strings.Repeat("x", 900)+`"}`, cookie); rec.Code != http.StatusCreated {
		t.Fatalf("long message = %d", rec.Code)
	}
	if got := len(srv.Entries()[0].Message); got != 500 {
		t.Fatalf("message length = %d, want it truncated to 500", got)
	}
}

func TestLogoutDropsTheSession(t *testing.T) {
	alice := newUser(t, who)
	srv, _ := newServer(t, alice)
	cookie := signIn(t, srv, alice)

	if rec := post(t, srv, "/auth/logout", "", cookie); rec.Code != http.StatusOK {
		t.Fatalf("logout = %d", rec.Code)
	}
	me := get(t, srv, "/api/session", cookie)
	if strings.Contains(me.Body.String(), `"signed_in":true`) {
		t.Fatalf("still signed in after logout: %s", me.Body.String())
	}
}

func TestSessionExpires(t *testing.T) {
	alice := newUser(t, who)
	now := testNow
	srv, err := New(Config{
		Origin:   rpOrigin,
		Resolver: zone{alice.name: alice.doc},
		Now:      func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cookie := signIn(t, srv, alice)
	now = now.Add(13 * time.Hour)
	if rec := post(t, srv, "/api/entries", `{"message":"late"}`, cookie); rec.Code != http.StatusUnauthorized {
		t.Fatalf("post on an expired session = %d", rec.Code)
	}
}

func TestIndexRenders(t *testing.T) {
	srv, _ := newServer(t, newUser(t, who))
	rec := get(t, srv, "/")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Sign in with Poweur ID") {
		t.Fatalf("index = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := get(t, srv, "/nope"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown path = %d", rec.Code)
	}
}

// --- helpers -----------------------------------------------------------------

func pollStatus(t *testing.T, srv *Server, requestID string) string {
	t.Helper()
	rec := get(t, srv, "/auth/poll?request_id="+url.QueryEscape(requestID))
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode poll: %v", err)
	}
	s, _ := out["status"].(string)
	return s
}

func sessionCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range (&http.Response{Header: rec.Header()}).Cookies() {
		if c.Name == cookieName && c.Value != "" {
			return c
		}
	}
	t.Fatalf("no session cookie in response headers: %v", rec.Header())
	return nil
}

func signIn(t *testing.T, srv *Server, u user) *http.Cookie {
	t.Helper()
	started := start(t, srv)
	encoded := approve(t, u, started.Request)
	rec := get(t, srv, "/auth/callback?response="+url.QueryEscape(encoded))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("sign in = %d: %s", rec.Code, rec.Body.String())
	}
	return sessionCookie(t, rec)
}
