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

// start presses "Sign in with Poweur ID" and returns what the page got.
func start(t *testing.T, srv *Server) StartResponse {
	t.Helper()
	out, _ := startIn(t, srv)
	return out
}

// startIn is start, also returning the browser's binding cookie.
func startIn(t *testing.T, srv *Server, cookies ...*http.Cookie) (StartResponse, *http.Cookie) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/auth/start", nil)
	for _, c := range cookies {
		r.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /auth/start = %d: %s", rec.Code, rec.Body.String())
	}
	var out StartResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode start: %v", err)
	}
	binding := findCookie(rec, bindingCookieName)
	if binding == nil && len(cookies) > 0 {
		binding = cookies[0]
	}
	if binding == nil {
		t.Fatal("start set no binding cookie")
	}
	if out.PollSecret == "" || out.PollSecret == out.RequestID || len(out.MatchCode) != signin.MatchCodeDigits {
		t.Fatalf("start = %+v", out)
	}
	return out, binding
}

// deliver POSTs an approval the way a signer does, optionally with the code a
// cross-device user typed.
func deliver(t *testing.T, srv *Server, encoded, match string) (*httptest.ResponseRecorder, signin.DeliveryReceipt) {
	t.Helper()
	body, _ := json.Marshal(signin.Delivery{Response: encoded, Match: match})
	rec := post(t, srv, "/auth/callback", string(body))
	var receipt signin.DeliveryReceipt
	_ = json.Unmarshal(rec.Body.Bytes(), &receipt)
	return rec, receipt
}

// resume follows a receipt's resume_uri in a browser holding cookies.
func resume(t *testing.T, srv *Server, resumeURI string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	u, err := url.Parse(resumeURI)
	if err != nil {
		t.Fatalf("resume_uri %q: %v", resumeURI, err)
	}
	return get(t, srv, u.RequestURI(), cookies...)
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
		// dav: scopes went with WebDAV (storage v2).
		{"retired dav scope", Config{Origin: rpOrigin, Scopes: []string{"dav:rw:apps/net.poweur.guestbook"}}},
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
	srv, err := New(Config{Origin: rpOrigin, Scopes: []string{"profile:read"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := srv.AppID(); got != AppID {
		t.Fatalf("app id = %q, want %q", got, AppID)
	}
	// Normalized and sorted, so the consent screen and the signature agree.
	want := []string{"profile:read"}
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
	if got := pollStatus(t, srv, started); got != "pending" {
		t.Fatalf("poll before approval = %q", got)
	}

	// The phone's user types the code shown on the screen and approves.
	rec, receipt := deliver(t, srv, approve(t, alice, started.Request), started.MatchCode)
	if rec.Code != http.StatusOK {
		t.Fatalf("callback = %d: %s", rec.Code, rec.Body.String())
	}
	if receipt.Status != "ok" || receipt.ResumeURI != "" {
		t.Fatalf("a cross-device receipt must not offer a resume: %+v", receipt)
	}

	// The tab polls again and is handed its cookie.
	pollRec := get(t, srv, pollPath(started))
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

	// The poll is one-shot: a second read must not hand the cookie out again.
	if got := pollStatus(t, srv, started); got != "unknown" {
		t.Fatalf("second poll = %q, want unknown", got)
	}
}

func TestSameDeviceFinishesThroughResume(t *testing.T) {
	alice := newUser(t, who)
	srv, _ := newServer(t, alice)
	started, binding := startIn(t, srv)

	rec, receipt := deliver(t, srv, approve(t, alice, started.Request), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("callback = %d: %s", rec.Code, rec.Body.String())
	}
	if err := signin.CheckResumeURI(rpOrigin, receipt.ResumeURI); err != nil || receipt.ResumeURI == "" {
		t.Fatalf("resume_uri %q: %v", receipt.ResumeURI, err)
	}
	if strings.Contains(receipt.ResumeURI, started.RequestID) {
		t.Fatal("resume_uri must not be derivable from the request")
	}
	// Approved, but nothing is handed to the poller: this approval finishes
	// in the browser, not through the poll.
	if got := pollStatus(t, srv, started); got != "approved" {
		t.Fatalf("poll after same-device approval = %q", got)
	}
	if c := findCookie(get(t, srv, pollPath(started)), cookieName); c != nil {
		t.Fatal("the poll handed out a same-device session")
	}

	res := resume(t, srv, receipt.ResumeURI, binding)
	if res.Code != http.StatusSeeOther {
		t.Fatalf("resume = %d: %s", res.Code, res.Body.String())
	}
	if res.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("resume must not leak its URL through Referer")
	}
	cookie := sessionCookie(t, res)
	if me := get(t, srv, "/api/session", cookie); !strings.Contains(me.Body.String(), who) {
		t.Fatalf("session after resume = %s", me.Body.String())
	}
	// The starting tab, polling in the same browser, learns it is done.
	if got := pollStatus(t, srv, started); got != "complete" {
		t.Fatalf("poll after resume = %q", got)
	}
	// The code is single-use.
	if again := resume(t, srv, receipt.ResumeURI, binding); again.Code == http.StatusSeeOther {
		t.Fatal("a resume code was accepted twice")
	}
}

func TestApprovalsInURLsAreRefused(t *testing.T) {
	alice := newUser(t, who)
	srv, _ := newServer(t, alice)
	started := start(t, srv)
	encoded := approve(t, alice, started.Request)

	rec := get(t, srv, "/auth/callback?response="+url.QueryEscape(encoded))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("GET callback = %d, want 400", rec.Code)
	}
	if findCookie(rec, cookieName) != nil {
		t.Fatal("an approval in a URL signed someone in")
	}
	// Nor was the approval spent: the signer can still POST it.
	if rec, _ := deliver(t, srv, encoded, ""); rec.Code != http.StatusOK {
		t.Fatalf("POST after refused GET = %d: %s", rec.Code, rec.Body.String())
	}
}

// --- Completion binding: who may finish a sign-in ----------------------------

// The attack from the sign-in spec: Mallory starts a login and sends Alice the
// signer link. Alice approves. Neither of them is signed in.
func TestForwardedSignInLinkSignsNobodyIn(t *testing.T) {
	alice := newUser(t, who)
	srv, _ := newServer(t, alice)
	started, _ := startIn(t, srv) // Mallory's browser

	// Alice's web signer delivers without a code (she believes she is on the
	// device that started it) and sends *her* browser to the resume URI.
	rec, receipt := deliver(t, srv, approve(t, alice, started.Request), "")
	if rec.Code != http.StatusOK || receipt.ResumeURI == "" {
		t.Fatalf("callback = %d %+v", rec.Code, receipt)
	}
	aliceBrowser := &http.Cookie{Name: bindingCookieName, Value: "alices-own-binding-value-which-is-long-enough-xx"}
	res := resume(t, srv, receipt.ResumeURI, aliceBrowser)
	if res.Code != http.StatusForbidden {
		t.Fatalf("resume in the wrong browser = %d, want 403", res.Code)
	}
	if findCookie(res, cookieName) != nil {
		t.Fatal("the victim's browser was given a session it did not start")
	}
	// Mallory polls with her own secret and gets nothing but a failure.
	pollRec := get(t, srv, pollPath(started))
	if findCookie(pollRec, cookieName) != nil {
		t.Fatal("the attacker's poll was handed the victim's session")
	}
	if got := decodeStatus(t, pollRec); got != "failed" {
		t.Fatalf("attacker's poll = %q, want failed", got)
	}
	// And no session for Alice lingers on the server.
	srv.mu.Lock()
	n := len(srv.sessions)
	srv.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d prepared sessions left behind", n)
	}
}

// A bystander who saw the QR code knows the request_id. It gets them nothing.
func TestPollNeedsThePollSecret(t *testing.T) {
	alice := newUser(t, who)
	srv, _ := newServer(t, alice)
	started := start(t, srv)
	if rec, _ := deliver(t, srv, approve(t, alice, started.Request), started.MatchCode); rec.Code != http.StatusOK {
		t.Fatalf("callback = %d: %s", rec.Code, rec.Body.String())
	}

	for _, path := range []string{
		"/auth/poll?request_id=" + url.QueryEscape(started.RequestID),
		"/auth/poll?request_id=" + url.QueryEscape(started.RequestID) + "&poll_secret=guess",
		"/auth/poll?request_id=" + url.QueryEscape(started.RequestID) + "&poll_secret=" + url.QueryEscape(started.RequestID),
	} {
		rec := get(t, srv, path)
		if findCookie(rec, cookieName) != nil {
			t.Fatalf("%s handed out the session", path)
		}
		if rec.Code == http.StatusOK && decodeStatus(t, rec) != "unknown" {
			t.Fatalf("%s = %s, want unknown", path, rec.Body.String())
		}
	}
	// The real page still gets it.
	if c := findCookie(get(t, srv, pollPath(started)), cookieName); c == nil {
		t.Fatal("the starting page lost its session to the probes")
	}
}

func TestWrongMatchCodeEndsTheSignIn(t *testing.T) {
	alice := newUser(t, who)
	srv, _ := newServer(t, alice)
	started := start(t, srv)
	wrong := "00"
	if started.MatchCode == wrong {
		wrong = "01"
	}
	encoded := approve(t, alice, started.Request)
	if rec, _ := deliver(t, srv, encoded, wrong); rec.Code != http.StatusForbidden {
		t.Fatalf("wrong code = %d, want 403", rec.Code)
	}
	// One attempt: the right code afterwards does not help.
	if rec, _ := deliver(t, srv, encoded, started.MatchCode); rec.Code == http.StatusOK {
		t.Fatal("a second guess at the match code was accepted")
	}
	if got := pollStatus(t, srv, started); got != "failed" {
		t.Fatalf("poll after wrong code = %q, want failed", got)
	}
}

func TestOnlyOneApprovalPerSignIn(t *testing.T) {
	alice := newUser(t, who)
	bob := newUser(t, "bob.poweur.net")
	srv, _ := newServer(t, alice, bob)
	started := start(t, srv)

	if rec, _ := deliver(t, srv, approve(t, alice, started.Request), started.MatchCode); rec.Code != http.StatusOK {
		t.Fatalf("first approval = %d", rec.Code)
	}
	// A different identity approving the same forwarded request.
	if rec, _ := deliver(t, srv, approve(t, bob, started.Request), started.MatchCode); rec.Code != http.StatusConflict {
		t.Fatalf("second approval = %d, want 409", rec.Code)
	}
	pollRec := get(t, srv, pollPath(started))
	if !strings.Contains(pollRec.Body.String(), who) {
		t.Fatalf("the first approver was replaced: %s", pollRec.Body.String())
	}
}

func TestResumeExpires(t *testing.T) {
	alice := newUser(t, who)
	now := testNow
	srv, err := New(Config{Origin: rpOrigin, Resolver: zone{alice.name: alice.doc}, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	started, binding := startIn(t, srv)
	_, receipt := deliver(t, srv, approve(t, alice, started.Request), "")
	now = now.Add(resumeWindow + time.Second)
	if res := resume(t, srv, receipt.ResumeURI, binding); res.Code != http.StatusGone {
		t.Fatalf("late resume = %d, want 410", res.Code)
	}
	if got := pollStatus(t, srv, started); got != "expired" {
		t.Fatalf("poll after late resume = %q", got)
	}
}

func TestResumeRejectsNonsense(t *testing.T) {
	srv, _ := newServer(t)
	for _, path := range []string{"/auth/resume", "/auth/resume?code=", "/auth/resume?code=abc"} {
		if rec := get(t, srv, path); rec.Code != http.StatusGone {
			t.Fatalf("%s = %d", path, rec.Code)
		}
	}
}

func TestTwoTabsShareOneBinding(t *testing.T) {
	alice := newUser(t, who)
	srv, _ := newServer(t, alice)
	first, binding := startIn(t, srv)
	second, again := startIn(t, srv, binding)
	if again.Value != binding.Value {
		t.Fatal("a second start replaced the browser binding and orphaned the first tab")
	}
	for _, started := range []StartResponse{first, second} {
		_, receipt := deliver(t, srv, approve(t, alice, started.Request), "")
		if res := resume(t, srv, receipt.ResumeURI, binding); res.Code != http.StatusSeeOther {
			t.Fatalf("resume = %d", res.Code)
		}
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
			rec, _ := deliver(t, srv, tc.build(t, srv, started), "")
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

	if rec, _ := deliver(t, srv, encoded, ""); rec.Code != http.StatusOK {
		t.Fatalf("first delivery = %d: %s", rec.Code, rec.Body.String())
	}
	// Captured off the wire and replayed inside the five-minute window.
	rec, _ := deliver(t, srv, encoded, "")
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

	if rec, _ := deliver(t, srv, broken, ""); rec.Code == http.StatusOK {
		t.Fatal("tampered approval accepted")
	}
	if got := pollStatus(t, srv, started); got != "failed" {
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
	if got := pollStatus(t, srv, started); got != "expired" {
		t.Fatalf("poll after expiry = %q", got)
	}
	if got := pollStatus(t, srv, StartResponse{RequestID: "req_never_issued", PollSecret: "x"}); got != "unknown" {
		t.Fatalf("poll for an unknown request = %q", got)
	}
	if rec := get(t, srv, "/auth/poll"); rec.Code != http.StatusBadRequest {
		t.Fatalf("poll without request_id = %d", rec.Code)
	}
	// An approval arriving after expiry has nothing to complete.
	if rec, _ := deliver(t, srv, approve(t, alice, started.Request), started.MatchCode); rec.Code == http.StatusOK {
		t.Fatal("a late approval was accepted")
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

func pollPath(started StartResponse) string {
	return "/auth/poll?request_id=" + url.QueryEscape(started.RequestID) +
		"&poll_secret=" + url.QueryEscape(started.PollSecret)
}

func pollStatus(t *testing.T, srv *Server, started StartResponse) string {
	t.Helper()
	return decodeStatus(t, get(t, srv, pollPath(started)))
}

func decodeStatus(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode poll: %v (%s)", err, rec.Body.String())
	}
	s, _ := out["status"].(string)
	return s
}

func findCookie(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range (&http.Response{Header: rec.Header()}).Cookies() {
		if c.Name == name && c.Value != "" {
			return c
		}
	}
	return nil
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
	started, binding := startIn(t, srv)
	rec, receipt := deliver(t, srv, approve(t, u, started.Request), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("sign in = %d: %s", rec.Code, rec.Body.String())
	}
	res := resume(t, srv, receipt.ResumeURI, binding)
	if res.Code != http.StatusSeeOther {
		t.Fatalf("resume = %d: %s", res.Code, res.Body.String())
	}
	return sessionCookie(t, res)
}

func TestSignInContext(t *testing.T) {
	alice := newUser(t, who)
	srv, _ := newServer(t, alice)
	r := httptest.NewRequest(http.MethodPost, "/auth/start", nil)
	r.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0) AppleWebKit Version/17 Mobile Safari/604")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, r)
	var started StartResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &started)

	if err := srv.Metadata().Validate(rpOrigin); err != nil {
		t.Fatal(err)
	}
	ctxRec := get(t, srv, "/auth/context?request_id="+url.QueryEscape(started.RequestID))
	var c signin.SignInContext
	if err := json.Unmarshal(ctxRec.Body.Bytes(), &c); err != nil || c.Browser != "Safari on iOS" || !c.StartedAt.Equal(testNow) {
		t.Fatalf("context = %d %s", ctxRec.Code, ctxRec.Body.String())
	}
	if rec := get(t, srv, "/auth/context?request_id=req_nope"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown = %d", rec.Code)
	}
	deliver(t, srv, approve(t, alice, started.Request), started.MatchCode)
	if rec := get(t, srv, "/auth/context?request_id="+url.QueryEscape(started.RequestID)); rec.Code != http.StatusNotFound {
		t.Fatalf("answered request still has context: %d", rec.Code)
	}
}

// The request by reference (E08-T6): a short link another device can open.
func TestRequestByReference(t *testing.T) {
	alice := newUser(t, who)
	srv, _ := newServer(t, alice)
	started := start(t, srv)
	if !strings.HasPrefix(started.RequestLink, rpOrigin+"/auth/r/") {
		t.Fatalf("request link = %q", started.RequestLink)
	}
	path := strings.TrimPrefix(started.RequestLink, rpOrigin)

	// A signer gets the request itself, open to any origin.
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, r)
	var got struct{ Request string }
	if rec.Code != 200 || rec.Header().Get("Access-Control-Allow-Origin") != "*" ||
		json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.Request != started.Request {
		t.Fatalf("json = %d %s", rec.Code, rec.Body)
	}
	decoded, err := identity.DecodeSignInRequest(got.Request)
	if err != nil || identity.CheckSignInRequestURI(started.RequestLink, decoded) != nil {
		t.Fatalf("the served request does not bind to its link: %v", err)
	}

	// A browser is sent into the web signer carrying the short link.
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "https://poweur.net/app/?auth="+url.QueryEscape(started.RequestLink) {
		t.Fatalf("browser = %d %q", rec.Code, rec.Header().Get("Location"))
	}

	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/auth/r/ZZZZZZZZ", nil))
	if rec.Code != http.StatusGone {
		t.Fatalf("unknown code = %d", rec.Code)
	}
}
