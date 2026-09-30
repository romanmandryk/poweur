package integration_test

// Sign in with Poweur ID against the reference relying party (EPIC-008),
// restored on storage v2 with the guestbook as an EPIC-026 headless app.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	idpkg "github.com/poweur/identity"
	"github.com/poweur/integration/refapps/guestbook"
)

type fixedSignInResolver struct{ doc idpkg.IdentityDocument }

func (r fixedSignInResolver) Resolve(_ context.Context, name string) (idpkg.Result, error) {
	return idpkg.Result{Document: r.doc, Source: idpkg.SourceWeb}, nil
}

func hostedIdentityDocument(t *testing.T, relayURL, identity string) idpkg.IdentityDocument {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, relayURL+idpkg.WellKnownPath, nil)
	req.Host = identity
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("identity document: %d %s", resp.StatusCode, raw)
	}
	var doc idpkg.IdentityDocument
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestINT_SIGNIN_01_CLIApprovesAndGuestbookSignsIn(t *testing.T) {
	zone := newZone(t)
	relayServer, relayAddr := newHostedRelay(t, zone, t.TempDir())
	defer relayServer.Close()
	relayURL := "http://" + relayAddr
	alice := "signinalice.poweur.net"
	home := t.TempDir()
	runCLI(t, home, "identity", "create", alice, "--hosted", "--relay", relayURL, "--json")
	doc := hostedIdentityDocument(t, relayURL, alice)

	stub := httptest.NewUnstartedServer(nil)
	origin := "http://" + stub.Listener.Addr().String()
	rp, err := guestbook.New(guestbook.Config{Origin: origin, Resolver: fixedSignInResolver{doc: doc}})
	if err != nil {
		t.Fatal(err)
	}
	stub.Config.Handler = rp
	stub.Start()
	defer stub.Close()

	startResp, err := http.Post(origin+"/auth/start", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var started guestbook.StartResponse
	if err := json.NewDecoder(startResp.Body).Decode(&started); err != nil {
		t.Fatal(err)
	}
	startResp.Body.Close()
	if started.Request == "" || started.RequestID == "" {
		t.Fatalf("start = %#v", started)
	}

	// Approving from a terminal is approving from another device: the user
	// types the code the browser shows, and the browser's poll finishes.
	out, _ := runCLI(t, home, "auth", "approve", started.DeepLink, "--sign-with", "identity", "--code", started.MatchCode)
	if !bytes.Contains([]byte(out), []byte("approved sign-in")) {
		t.Fatalf("approve output: %s", out)
	}
	if bytes.Contains([]byte(out), []byte("/auth/resume")) {
		t.Fatalf("a cross-device approval must not print a resume link: %s", out)
	}
	poll, err := http.Get(origin + "/auth/poll?request_id=" + url.QueryEscape(started.RequestID) +
		"&poll_secret=" + url.QueryEscape(started.PollSecret))
	if err != nil {
		t.Fatal(err)
	}
	var pollBody map[string]any
	if err := json.NewDecoder(poll.Body).Decode(&pollBody); err != nil {
		t.Fatal(err)
	}
	poll.Body.Close()
	if pollBody["status"] != "complete" || pollBody["identity"] != alice {
		t.Fatalf("poll = %#v", pollBody)
	}
	var cookie *http.Cookie
	for _, c := range poll.Cookies() {
		if c.Name == "guestbook_session" {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("poll did not return the RP session cookie")
	}

	entryReq, _ := http.NewRequest(http.MethodPost, origin+"/api/entries", bytes.NewBufferString(`{"message":"portable hello"}`))
	entryReq.Header.Set("Content-Type", "application/json")
	entryReq.AddCookie(cookie)
	entryResp, err := http.DefaultClient.Do(entryReq)
	if err != nil {
		t.Fatal(err)
	}
	rawEntry, _ := io.ReadAll(entryResp.Body)
	entryResp.Body.Close()
	if entryResp.StatusCode != http.StatusCreated {
		t.Fatalf("entry: %d %s", entryResp.StatusCode, rawEntry)
	}
	entries := rp.Entries()
	if len(entries) != 1 || entries[0].Identity != alice || entries[0].Message != "portable hello" {
		t.Fatalf("entries: %#v", entries)
	}

	// The signer kept its own audit trail, encrypted in the user's drive.
	logOut, _ := runCLI(t, home, "auth", "log", "--use-identity", alice)
	if !strings.Contains(logOut, stub.Listener.Addr().String()) {
		t.Fatalf("consent log does not name the guestbook:\n%s", logOut)
	}
}

// TestINT_SIGNIN_02: a same-device approval finishes only in the browser that
// started the sign-in. The CLI plays the signer; two cookie jars play the
// browser that pressed "Sign in" and a browser somebody forwarded the link to.
func TestINT_SIGNIN_02_SameDeviceApprovalFinishesOnlyInTheStartingBrowser(t *testing.T) {
	zone := newZone(t)
	relayServer, relayAddr := newHostedRelay(t, zone, t.TempDir())
	defer relayServer.Close()
	relayURL := "http://" + relayAddr
	alice := "signinbob.poweur.net"
	home := t.TempDir()
	runCLI(t, home, "identity", "create", alice, "--hosted", "--relay", relayURL, "--json")
	doc := hostedIdentityDocument(t, relayURL, alice)

	stub := httptest.NewUnstartedServer(nil)
	origin := "http://" + stub.Listener.Addr().String()
	rp, err := guestbook.New(guestbook.Config{Origin: origin, Resolver: fixedSignInResolver{doc: doc}})
	if err != nil {
		t.Fatal(err)
	}
	stub.Config.Handler = rp
	stub.Start()
	defer stub.Close()

	browser := func() *http.Client {
		jar, _ := cookiejar.New(nil)
		return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	start := func(c *http.Client) guestbook.StartResponse {
		resp, err := c.Post(origin+"/auth/start", "application/json", nil)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var started guestbook.StartResponse
		if err := json.NewDecoder(resp.Body).Decode(&started); err != nil {
			t.Fatal(err)
		}
		return started
	}
	approve := func(started guestbook.StartResponse) string {
		out, _ := runCLI(t, home, "auth", "approve", started.DeepLink, "--sign-with", "identity", "--json")
		var payload struct {
			ResumeURI string `json:"resume_uri"`
		}
		if err := json.Unmarshal([]byte(out), &payload); err != nil || payload.ResumeURI == "" {
			t.Fatalf("approve printed no resume link: %v %s", err, out)
		}
		return payload.ResumeURI
	}
	signedInAs := func(c *http.Client) string {
		resp, err := c.Get(origin + "/api/session")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var s struct {
			Identity string `json:"identity"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&s)
		return s.Identity
	}

	// The honest journey: same browser starts and resumes.
	own := browser()
	resumeURI := approve(start(own))
	resp, err := own.Get(resumeURI)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || signedInAs(own) != alice {
		t.Fatalf("own browser: resume %d, signed in as %q", resp.StatusCode, signedInAs(own))
	}

	// The forwarded link: one browser starts, the approver's browser resumes.
	attacker, victim := browser(), browser()
	started := start(attacker)
	resumeURI = approve(started)
	resp, err = victim.Get(resumeURI)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("resume in a browser that did not start the sign-in = %d, want 403", resp.StatusCode)
	}
	pollResp, err := attacker.Get(origin + "/auth/poll?request_id=" + url.QueryEscape(started.RequestID) +
		"&poll_secret=" + url.QueryEscape(started.PollSecret))
	if err != nil {
		t.Fatal(err)
	}
	pollResp.Body.Close()
	if who := signedInAs(attacker); who != "" {
		t.Fatalf("the browser that started the forwarded sign-in is signed in as %q", who)
	}
	if who := signedInAs(victim); who != "" {
		t.Fatalf("the victim's browser is signed in as %q", who)
	}
}
