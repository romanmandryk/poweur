package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/poweur/guestbook"
	idpkg "github.com/poweur/identity"
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

func TestINT_SIGNIN_01_CLIApprovesAndGuestbookWritesUserHome(t *testing.T) {
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
	appID, err := idpkg.SignInAppID(origin)
	if err != nil {
		t.Fatal(err)
	}
	rp, err := guestbook.New(guestbook.Config{
		Origin: origin, Resolver: fixedSignInResolver{doc: doc},
		Scopes: []string{idpkg.ScopeDAVRWPfx + "apps/" + appID},
	})
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

	out, _ := runCLI(t, home, "auth", "approve", started.DeepLink, "--sign-with", "identity")
	if !bytes.Contains([]byte(out), []byte("approved sign-in")) {
		t.Fatalf("approve output: %s", out)
	}
	poll, err := http.Get(origin + "/auth/poll?request_id=" + url.QueryEscape(started.RequestID))
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
	if len(entries) != 1 || entries[0].StoredAt == "" {
		t.Fatalf("entry was not written through the scoped home grant: %#v", entries)
	}

	// The signer also persisted its own audit trail in the user's private tree.
	ownerToken := mintTokenViaCLI(t, home, "--use-identity", alice)
	logResp := davDo(t, http.MethodGet, relayURL+"/dav/"+alice+"/"+idpkg.AuthLogPath, ownerToken, nil, nil)
	logRaw, _ := io.ReadAll(logResp.Body)
	logResp.Body.Close()
	if logResp.StatusCode != http.StatusOK || len(idpkg.ParseAuthLog(logRaw)) != 1 {
		t.Fatalf("consent log: %d %s", logResp.StatusCode, logRaw)
	}
}
