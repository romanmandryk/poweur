package relay

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	idpkg "github.com/poweur/identity"
	signinpkg "github.com/poweur/identity/signin"
)

func signInGrantApproval(t *testing.T, who davTestIdentity, audience string, scopes []string) string {
	t.Helper()
	now := time.Now().UTC()
	req, err := (idpkg.SignInRequest{
		PoweurAuth: idpkg.SignInVersion, RequestID: "req_grant", Nonce: "nonce_grant",
		Audience: audience, Action: idpkg.SignInActionLink,
		IssuedAt:  now.Add(-time.Minute).Format(time.RFC3339),
		ExpiresAt: now.Add(time.Minute).Format(time.RFC3339), Scopes: scopes,
	}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	approval, err := signinpkg.Sign(req, signinpkg.SignOptions{Identity: who.name, PrivateKey: who.priv})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := idpkg.EncodeSignInResponse(approval)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestSignInGrantIsPathScopedAndFileRevocable(t *testing.T) {
	server, ts := newDAVTestServer(t, 0, 0)
	alice := registerDAVIdentity(t, server, ts, "grant-alice.poweur.net")
	appID := "example.guestbook"
	approval := signInGrantApproval(t, alice, "https://guestbook.example", []string{
		idpkg.ScopeProfileRead, idpkg.ScopeDAVRWPfx + "apps/" + appID,
	})
	body, _ := json.Marshal(SignInGrantRequest{Response: approval})
	resp, err := http.Post(ts.URL+"/auth/grant", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("grant: %d %s", resp.StatusCode, raw)
	}
	var grant SignInGrantResponse
	if err := json.NewDecoder(resp.Body).Decode(&grant); err != nil {
		t.Fatal(err)
	}
	if grant.AppID != appID || grant.Path != "apps/"+appID || grant.Token == "" {
		t.Fatalf("grant = %#v", grant)
	}

	base := "/dav/" + alice.name
	// The token can create and write inside its own app namespace.
	r := davReq(t, ts, "MKCOL", base+"/apps/"+appID+"/entries", grant.Token, nil, nil)
	r.Body.Close()
	if r.StatusCode >= 300 && r.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("mkdir inside scope: %d", r.StatusCode)
	}
	r = davReq(t, ts, http.MethodPut, base+"/apps/"+appID+"/entries/one.json", grant.Token, []byte(`{"ok":true}`), nil)
	r.Body.Close()
	if r.StatusCode >= 300 {
		t.Fatalf("write inside scope: %d", r.StatusCode)
	}
	// It cannot cross the path boundary even though it acts on Alice's tree.
	r = davReq(t, ts, http.MethodPut, base+"/private/stolen.txt", grant.Token, []byte("no"), nil)
	r.Body.Close()
	if r.StatusCode != http.StatusForbidden {
		t.Fatalf("outside scope: %d want 403", r.StatusCode)
	}

	// Revocation is a user-owned file edit, checked on the very next request.
	ownerToken := mintDAVToken(t, ts, alice, "", "")
	raw, status, err := func() ([]byte, int, error) {
		r := davReq(t, ts, http.MethodGet, base+"/"+idpkg.ConnectedAppsPath, ownerToken, nil, nil)
		defer r.Body.Close()
		b, e := io.ReadAll(r.Body)
		return b, r.StatusCode, e
	}()
	if err != nil || status != http.StatusOK {
		t.Fatalf("connected apps read: %d %v", status, err)
	}
	doc, err := idpkg.ParseConnectedApps(raw)
	if err != nil {
		t.Fatal(err)
	}
	app, ok := doc.Active(appID, time.Now())
	if !ok {
		t.Fatalf("app not recorded: %#v", doc)
	}
	app.RevokedAt = time.Now().UTC().Format(time.RFC3339)
	doc = doc.Upsert(app)
	raw, _ = json.Marshal(doc)
	r = davReq(t, ts, http.MethodPut, base+"/"+idpkg.ConnectedAppsPath, ownerToken, raw, nil)
	r.Body.Close()
	if r.StatusCode >= 300 {
		t.Fatalf("revoke file write: %d", r.StatusCode)
	}
	r = davReq(t, ts, http.MethodGet, base+"/apps/"+appID+"/entries/one.json", grant.Token, nil, nil)
	r.Body.Close()
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked token: %d want 401", r.StatusCode)
	}
}

func TestSignInGrantRejectsTamperingAndMissingDAVScope(t *testing.T) {
	server, ts := newDAVTestServer(t, 0, 0)
	alice := registerDAVIdentity(t, server, ts, "grant-bad.poweur.net")
	valid := signInGrantApproval(t, alice, "https://guestbook.example", []string{idpkg.ScopeProfileRead})
	for _, encoded := range []string{valid, valid + "tampered"} {
		body, _ := json.Marshal(SignInGrantRequest{Response: encoded})
		resp, err := http.Post(ts.URL+"/auth/grant", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode < 400 {
			t.Fatalf("grant unexpectedly accepted with status %d", resp.StatusCode)
		}
	}
}
