package relay

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/poweur/api/internal/config"
	"github.com/poweur/api/internal/ratelimit"
	idpkg "github.com/poweur/identity"
)

// HTTP-level public-link scenarios (E05-T4). These reuse the E05-T2 share
// fixture — alice's seeded tree, the same DAV writes an owner really makes
// — so the link endpoint is exercised against the same world the
// identity-authenticated matrix runs in.

// putLinkGrant signs a link-share grant as alice and PUTs it into her tree,
// returning the capability token.
func putLinkGrant(t *testing.T, fx *shareFixture, shareID, path string, link *idpkg.ShareLink, expires string) string {
	t.Helper()
	token, err := idpkg.GenerateLinkToken()
	if err != nil {
		t.Fatal(err)
	}
	permissions := []string{idpkg.PermRead}
	if link != nil && link.FileRequest != nil {
		permissions = []string{idpkg.PermCreate}
	}
	putShareGrant(t, fx.ts, fx.alice, fx.aliceTok, idpkg.ShareGrant{
		ShareID: shareID, Path: path,
		Audience:    []idpkg.ShareAudience{{Link: token}},
		Permissions: permissions,
		ExpiresAt:   expires,
		Link:        link,
	})
	return token
}

func (fx *shareFixture) linkUpload(t *testing.T, token, filename, body string) (*http.Response, string) {
	t.Helper()
	var encoded bytes.Buffer
	form := multipart.NewWriter(&encoded)
	part, err := form.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(part, body)
	_ = form.Close()
	req, err := http.NewRequest(http.MethodPost, fx.ts.URL+fx.linkPath(token), &encoded)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(raw)
}

// linkGet fetches a /s/ URL with no credentials of any kind — the whole
// point of a capability URL.
func (fx *shareFixture) linkGet(t *testing.T, path string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, fx.ts.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	// No redirect following: a 303 is part of what we assert.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(raw)
}

// linkPath is the explicit-owner form; the Host-routed form is asserted
// separately.
func (fx *shareFixture) linkPath(token string, rest ...string) string {
	p := "/s/" + fx.alice.name + "/" + token
	if len(rest) > 0 && rest[0] != "" {
		p += "/" + rest[0]
	}
	return p
}

// TestShareLinkBrowseAndDownload: the acceptance path — a browser with no
// auth lists the folder and downloads a file.
func TestShareLinkBrowseAndDownload(t *testing.T) {
	fx := newShareFixture(t)
	token := putLinkGrant(t, fx, "shr_link", "shared/project", nil, "")

	resp, body := fx.linkGet(t, fx.linkPath(token))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("link root: %d %s", resp.StatusCode, body)
	}
	for _, want := range []string{"readme.txt", "docs/", fx.alice.name} {
		if !strings.Contains(body, want) {
			t.Fatalf("listing missing %q: %s", want, body)
		}
	}
	// The listing must not leak siblings of the granted folder.
	if strings.Contains(body, "private-project") || strings.Contains(body, "diary") {
		t.Fatalf("listing leaked outside the share: %s", body)
	}

	// A capability URL must not travel in a Referer, be indexed or cached.
	for header, want := range map[string]string{
		"Referrer-Policy":        "no-referrer",
		"X-Content-Type-Options": "nosniff",
	} {
		if got := resp.Header.Get(header); got != want {
			t.Fatalf("%s = %q want %q", header, got, want)
		}
	}
	if !strings.Contains(resp.Header.Get("X-Robots-Tag"), "noindex") {
		t.Fatalf("X-Robots-Tag = %q", resp.Header.Get("X-Robots-Tag"))
	}
	if !strings.Contains(resp.Header.Get("Cache-Control"), "no-store") {
		t.Fatalf("Cache-Control = %q", resp.Header.Get("Cache-Control"))
	}

	// Descend, then download.
	resp, body = fx.linkGet(t, fx.linkPath(token, "docs"))
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "design.md") {
		t.Fatalf("subfolder: %d %s", resp.StatusCode, body)
	}
	resp, body = fx.linkGet(t, fx.linkPath(token, "readme.txt"))
	if resp.StatusCode != http.StatusOK || body != "readme v1" {
		t.Fatalf("download: %d %q", resp.StatusCode, body)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, `filename="readme.txt"`) {
		t.Fatalf("Content-Disposition = %q", cd)
	}
}

func TestFileRequestUploadsAreCreateOnlyAndIsolated(t *testing.T) {
	fx := newShareFixture(t)
	fx.server.cfg.LauncherHost = "id.poweur.net"
	token := putLinkGrant(t, fx, "shr_request", "shared/project", &idpkg.ShareLink{
		FileRequest: &idpkg.ShareFileRequest{MaxUploads: 2, MaxBytes: 10, Notify: true},
	}, "2099-12-31T23:59:00Z")
	streamID, events, ok := fx.server.hub.subscribe(fx.alice.name, 0)
	if !ok {
		t.Fatal("subscribe owner stream")
	}
	defer fx.server.hub.unsubscribe(fx.alice.name, streamID)
	fx.server.recordShareClaimDelivery(context.Background(), Message{
		Type: idpkg.MsgTypeShareClaim, Recipient: fx.alice.name,
		Metadata: map[string]string{"share_id": "shr_request"},
	})
	fx.server.recordShareClaimDelivery(context.Background(), Message{
		Type: idpkg.MsgTypeShareClaim, Recipient: fx.alice.name,
		Metadata: map[string]string{"share_id": "shr_not_a_request"},
	})

	resp, page := fx.linkGet(t, fx.linkPath(token))
	if resp.StatusCode != http.StatusOK || !strings.Contains(page, "cannot see, replace, or remove") {
		t.Fatalf("request page: %d %s", resp.StatusCode, page)
	}
	if !strings.Contains(page, fx.linkPath(token, "claim")+"?action=viewed") {
		t.Fatalf("request page does not preserve claim handoff: %s", page)
	}
	for _, want := range []string{
		idpkg.KeyFingerprintBytes(fx.alice.pub),
		"31 Dec 2099, 23:59 UTC",
		"2 of 2 uploads remaining",
		"10 B upload capacity remaining",
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("request page missing public context %q: %s", want, page)
		}
	}
	resp, _ = fx.linkGet(t, fx.linkPath(token, "claim")+"?action=viewed")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("claim redirect: %d", resp.StatusCode)
	}
	location := resp.Header.Get("Location")
	encoded := strings.TrimPrefix(location, "https://id.poweur.net/app/#share=")
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("claim fragment %q: %v", location, err)
	}
	var handoff map[string]string
	if err := json.Unmarshal(raw, &handoff); err != nil || handoff["token"] != token || handoff["share_id"] != "shr_request" {
		t.Fatalf("claim payload = %s err=%v", raw, err)
	}
	// A guessed child path never reads or lists an existing owner file.
	resp, page = fx.linkGet(t, fx.linkPath(token, "readme.txt"))
	if resp.StatusCode != http.StatusNotFound || strings.Contains(page, "readme v1") {
		t.Fatalf("request leaked an existing file: %d %q", resp.StatusCode, page)
	}

	for _, body := range []string{"first", "other"} {
		resp, page = fx.linkUpload(t, token, "same.txt", body)
		if resp.StatusCode != http.StatusCreated || !strings.Contains(page, "Upload complete") {
			t.Fatalf("upload %q: %d %s", body, resp.StatusCode, page)
		}
		select {
		case event := <-events:
			if event.Type != "file_request" || event.MessageID != "shr_request" {
				t.Fatalf("owner notification = %+v", event)
			}
		default:
			t.Fatal("owner was not notified about requested upload")
		}
	}
	// Two anonymous submitters can use the same local name without either
	// overwriting the other; server-generated prefixes make both new.
	d, err := fx.server.filesProvider.OpenFile(context.Background(), fx.alice.name, "shared/project", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := d.Readdir(-1)
	_ = d.Close()
	if err != nil {
		t.Fatal(err)
	}
	var submitted int
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), "-same.txt") {
			submitted++
		}
	}
	if submitted != 2 {
		t.Fatalf("stored submissions = %d, entries=%v", submitted, entries)
	}
	resp, _ = fx.linkUpload(t, token, "third.txt", "x")
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("past upload cap: %d want 410", resp.StatusCode)
	}
	stat := fx.server.linkStats.Get(fx.alice.name, "shr_request")
	if stat.Opens != 1 || stat.Uploads != 2 || stat.UploadBytes != 10 || stat.ClaimStarted != 1 || stat.IDClaimed != 1 {
		t.Fatalf("privacy-safe metrics = %+v", stat)
	}

	// Revocation is immediate and indistinguishable from an unknown token.
	del := davReq(t, fx.ts, http.MethodDelete,
		"/dav/"+fx.alice.name+"/poweur-sys/relay/shares/shr_request.json", fx.aliceTok, nil, nil)
	del.Body.Close()
	resp, _ = fx.linkUpload(t, token, "late.txt", "x")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("revoked request: %d want 404", resp.StatusCode)
	}
}

func TestFileRequestObjectAndMediaLimits(t *testing.T) {
	fx := newShareFixture(t)
	token := putLinkGrant(t, fx, "shr_request_limits", "shared/project", &idpkg.ShareLink{
		FileRequest: &idpkg.ShareFileRequest{MaxObjectBytes: 3, AllowedTypes: []string{"image/png"}},
	}, "")
	resp, _ := fx.linkUpload(t, token, "too-large.txt", "four")
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize upload: %d want 413", resp.StatusCode)
	}
	resp, _ = fx.linkUpload(t, token, "wrong-type.txt", "abc")
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("wrong media type: %d want 415", resp.StatusCode)
	}
	if got := fx.server.linkStats.Get(fx.alice.name, "shr_request_limits").Uploads; got != 0 {
		t.Fatalf("rejected uploads consumed count quota: %d", got)
	}
}

// The vanity-host form is the one that actually gets shared around.
func TestShareLinkHostRouted(t *testing.T) {
	fx := newShareFixture(t)
	token := putLinkGrant(t, fx, "shr_host", "shared/project", nil, "")

	req, _ := http.NewRequest(http.MethodGet, fx.ts.URL+"/s/"+token+"/readme.txt", nil)
	req.Host = fx.alice.name
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "readme v1" {
		t.Fatalf("host-routed download: %d %q", resp.StatusCode, body)
	}

	// Without the Host there is no owner, so a bare token resolves to
	// nothing rather than to whoever happens to be first in the store.
	resp, _ = fx.linkGet(t, "/s/"+token+"/readme.txt")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("bare token without a vanity host: %d want 404", resp.StatusCode)
	}
}

// A share of a single file is a single file, not a folder to browse.
func TestShareLinkSingleFile(t *testing.T) {
	fx := newShareFixture(t)
	token := putLinkGrant(t, fx, "shr_file", "shared/project/readme.txt", nil, "")

	resp, body := fx.linkGet(t, fx.linkPath(token))
	if resp.StatusCode != http.StatusOK || body != "readme v1" {
		t.Fatalf("single-file link: %d %q", resp.StatusCode, body)
	}
	// Nothing else in the folder comes with it.
	resp, _ = fx.linkGet(t, fx.linkPath(token, "../docs/design.md"))
	if resp.StatusCode == http.StatusOK {
		t.Fatal("a single-file link must not reach its siblings")
	}
}

// Token failure modes, all of which must look alike from outside.
func TestShareLinkTokenFailureModes(t *testing.T) {
	fx := newShareFixture(t)
	good := putLinkGrant(t, fx, "shr_good", "shared/project", nil, "")
	other, _ := idpkg.GenerateLinkToken()

	cases := []struct {
		name string
		path string
		want int
	}{
		{"unknown token", fx.linkPath(other), http.StatusNotFound},
		{"forged near-miss", fx.linkPath(good[:len(good)-1] + "z"), http.StatusNotFound},
		{"truncated", fx.linkPath(good[:12]), http.StatusNotFound},
		{"malformed", fx.linkPath("not-a-real-token"), http.StatusNotFound},
		{"empty", "/s/", http.StatusNotFound},
		{"owner only", "/s/" + fx.alice.name, http.StatusNotFound},
		{"unknown owner", "/s/nobody.poweur.net/" + good, http.StatusNotFound},
		{"uppercase token still works", fx.linkPath(strings.ToUpper(good)), http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := fx.linkGet(t, tc.path)
			if resp.StatusCode != tc.want {
				t.Fatalf("%d want %d: %s", resp.StatusCode, tc.want, body)
			}
			// However it fails, it must never echo a valid token back.
			if tc.want != http.StatusOK && strings.Contains(body, good) {
				t.Fatalf("error page echoed the token: %s", body)
			}
		})
	}
}

// A link can never reach outside the folder it was made for, however the
// path is written.
func TestShareLinkTraversalRefused(t *testing.T) {
	fx := newShareFixture(t)
	token := putLinkGrant(t, fx, "shr_trav", "shared/project", nil, "")

	for _, rest := range []string{
		"../private-project/secret.txt",
		"../../private/diary.txt",
		"..%2f..%2fprivate%2fdiary.txt",
		"../../poweur-sys/relay/shares/shr_trav.json",
		"docs/../../private-project/secret.txt",
	} {
		resp, body := fx.linkGet(t, fx.linkPath(token, rest))
		if resp.StatusCode == http.StatusOK {
			t.Fatalf("traversal %q served content: %s", rest, body)
		}
		for _, secret := range []string{"top secret", "dear diary"} {
			if strings.Contains(body, secret) {
				t.Fatalf("traversal %q leaked %q", rest, secret)
			}
		}
	}
}

// Expiry says so; revocation is a blank wall.
func TestShareLinkExpiryAndRevocation(t *testing.T) {
	fx := newShareFixture(t)

	expiredTok := putLinkGrant(t, fx, "shr_exp", "shared/project", nil,
		time.Now().Add(-time.Minute).UTC().Format(time.RFC3339))
	resp, body := fx.linkGet(t, fx.linkPath(expiredTok))
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("expired link: %d want 410", resp.StatusCode)
	}
	if !strings.Contains(strings.ToLower(body), "expired") {
		t.Fatalf("expired page should say so: %s", body)
	}

	liveTok := putLinkGrant(t, fx, "shr_rev", "shared/project", nil, "")
	if resp, _ := fx.linkGet(t, fx.linkPath(liveTok, "readme.txt")); resp.StatusCode != http.StatusOK {
		t.Fatalf("live link: %d", resp.StatusCode)
	}

	// Revoke = the owner deletes the grant file. The next request is dead,
	// with no window: grants are re-read per request.
	del := davReq(t, fx.ts, http.MethodDelete,
		"/dav/alice.poweur.net/poweur-sys/relay/shares/shr_rev.json", fx.aliceTok, nil, nil)
	del.Body.Close()

	resp, body = fx.linkGet(t, fx.linkPath(liveTok, "readme.txt"))
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("revoked link: %d want 404", resp.StatusCode)
	}
	// A revoked link must be indistinguishable from one that never existed
	// — in particular it must not say "expired".
	if strings.Contains(strings.ToLower(body), "expired") {
		t.Fatalf("revoked link admitted it once existed: %s", body)
	}
}

// A forged grant sitting in the tree grants no link either.
func TestShareLinkForgedGrantIgnored(t *testing.T) {
	fx := newShareFixture(t)
	token, _ := idpkg.GenerateLinkToken()
	forged := idpkg.ShareGrant{
		ShareID: "shr_forgedlink", Owner: fx.alice.name, Path: "shared/private-project",
		Audience:    []idpkg.ShareAudience{{Link: token}},
		Permissions: []string{"read"},
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
	}
	if err := forged.Sign(fx.bob.priv); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(forged)
	resp := davReq(t, fx.ts, http.MethodPut,
		"/dav/alice.poweur.net/poweur-sys/relay/shares/shr_forgedlink.json", fx.aliceTok, raw, nil)
	resp.Body.Close()

	got, body := fx.linkGet(t, fx.linkPath(token))
	if got.StatusCode != http.StatusNotFound {
		t.Fatalf("forged link grant: %d want 404", got.StatusCode)
	}
	if strings.Contains(body, "top secret") {
		t.Fatalf("forged link served content: %s", body)
	}
}

// A link grant must not widen what an authenticated visitor can do.
func TestShareLinkGrantsNothingOverDAV(t *testing.T) {
	fx := newShareFixture(t)
	token := putLinkGrant(t, fx, "shr_davlink", "shared/project", nil, "")
	bobTok := fx.visitorTok(t, fx.bob, "dav:full")

	if code, _ := fx.davGet(t, "/shared/project/readme.txt", bobTok); code != http.StatusForbidden {
		t.Fatalf("link grant gave a DAV visitor access: %d want 403", code)
	}
	// Nor does the grant show up as something bob has been shared.
	if code, _ := fx.davGet(t, "/poweur-sys/relay/shares/shr_davlink.json", bobTok); code == http.StatusOK {
		t.Fatal("a visitor must not be able to read the grant document itself")
	}
	// Meanwhile the same token still works where it is supposed to, so the
	// refusals above are about the DAV path and not a broken grant.
	if resp, _ := fx.linkGet(t, fx.linkPath(token, "readme.txt")); resp.StatusCode != http.StatusOK {
		t.Fatalf("link path: %d", resp.StatusCode)
	}
}

// The password gate: wrong is refused, right is remembered, and the cookie
// is scoped to the share that issued it.
func TestShareLinkPasswordGate(t *testing.T) {
	fx := newShareFixture(t)
	hash, err := idpkg.HashLinkPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	token := putLinkGrant(t, fx, "shr_pw", "shared/project", &idpkg.ShareLink{Password: hash}, "")
	base := fx.linkPath(token)

	// Unauthenticated GET is the form, not the content.
	resp, body := fx.linkGet(t, base)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("password gate: %d want 401", resp.StatusCode)
	}
	if !strings.Contains(body, `type="password"`) || strings.Contains(body, "readme.txt") {
		t.Fatalf("gate must show a form and no content: %s", body)
	}

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	post := func(path, password string) (*http.Response, string) {
		t.Helper()
		resp, err := client.PostForm(fx.ts.URL+path, url.Values{"password": {password}})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(raw)
	}

	// Wrong password: refused, no cookie, no content.
	resp, body = post(base, "wrong")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d want 401", resp.StatusCode)
	}
	if strings.Contains(body, "readme.txt") {
		t.Fatalf("wrong password served content: %s", body)
	}
	if u, _ := url.Parse(fx.ts.URL + base); len(jar.Cookies(u)) != 0 {
		t.Fatal("a wrong password must not set a session cookie")
	}

	// Right password: a redirect back to the share, and then the content.
	resp, _ = post(base, "correct horse")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("correct password: %d want 303", resp.StatusCode)
	}
	resp, err = client.Get(fx.ts.URL + base)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), "readme.txt") {
		t.Fatalf("after password: %d %s", resp.StatusCode, raw)
	}

	// The cookie is HttpOnly and scoped to this share's own URL prefix.
	u, _ := url.Parse(fx.ts.URL + base)
	cookies := jar.Cookies(u)
	if len(cookies) == 0 {
		t.Fatal("expected a session cookie")
	}
	// A *different* password-protected share is not unlocked by it.
	otherHash, _ := idpkg.HashLinkPassword("something else")
	otherTok := putLinkGrant(t, fx, "shr_pw2", "shared/private-project",
		&idpkg.ShareLink{Password: otherHash}, "")
	resp, _ = client.Get(fx.ts.URL + fx.linkPath(otherTok))
	if err == nil && resp != nil {
		resp.Body.Close()
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("one share's cookie unlocked another: %d want 401", resp.StatusCode)
	}
}

// A cookie nobody minted is not a session.
func TestShareLinkForgedSessionCookie(t *testing.T) {
	fx := newShareFixture(t)
	hash, _ := idpkg.HashLinkPassword("hunter2")
	token := putLinkGrant(t, fx, "shr_cookie", "shared/project", &idpkg.ShareLink{Password: hash}, "")
	base := fx.linkPath(token)

	future := strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	past := strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10)
	for _, value := range []string{
		"",
		"garbage",
		"1.2",
		// A plausible expiry with an invented MAC.
		future + ".AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA0",
		// …and with none at all.
		future + ".",
		// An expiry so far out it must still fail on the MAC, not the clock.
		"99999999999.AAAA",
		// A stale session, even if the MAC were right, is over.
		past + ".AAAA",
	} {
		req, _ := http.NewRequest(http.MethodGet, fx.ts.URL+base, nil)
		req.AddCookie(&http.Cookie{Name: "poweur_link_shr_cookie", Value: value})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("cookie %q: %d want 401", value, resp.StatusCode)
		}
		if strings.Contains(string(body), "readme.txt") {
			t.Fatalf("cookie %q served content", value)
		}
	}
}

// The download cap counts files, survives across requests, and does not
// count listings.
func TestShareLinkDownloadCap(t *testing.T) {
	fx := newShareFixture(t)
	token := putLinkGrant(t, fx, "shr_cap", "shared/project", &idpkg.ShareLink{MaxDownloads: 2}, "")

	// Browsing is free.
	for i := 0; i < 3; i++ {
		if resp, _ := fx.linkGet(t, fx.linkPath(token)); resp.StatusCode != http.StatusOK {
			t.Fatalf("listing %d: %d", i, resp.StatusCode)
		}
	}
	for i := 1; i <= 2; i++ {
		resp, body := fx.linkGet(t, fx.linkPath(token, "readme.txt"))
		if resp.StatusCode != http.StatusOK || body != "readme v1" {
			t.Fatalf("download %d: %d %q", i, resp.StatusCode, body)
		}
	}
	resp, body := fx.linkGet(t, fx.linkPath(token, "readme.txt"))
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("past the cap: %d want 410", resp.StatusCode)
	}
	if body == "readme v1" {
		t.Fatal("past the cap the file was served anyway")
	}
	// A different file in the same share shares the cap: the cap is on the
	// link, not on a path.
	if resp, _ := fx.linkGet(t, fx.linkPath(token, "docs/design.md")); resp.StatusCode != http.StatusGone {
		t.Fatalf("cap is per-link: %d want 410", resp.StatusCode)
	}

	// The counters the owner is billed on line up with what was served.
	stat := fx.server.linkStats.Get(fx.alice.name, "shr_cap")
	if stat.Downloads != 2 {
		t.Fatalf("downloads = %d want 2", stat.Downloads)
	}
	if stat.Bytes != int64(2*len("readme v1")) {
		t.Fatalf("bytes = %d want %d", stat.Bytes, 2*len("readme v1"))
	}
	if stat.FirstAt.IsZero() || stat.LastAt.IsZero() {
		t.Fatal("stats must carry first/last timestamps")
	}
}

// Bandwidth accounting is per-share and independent of any cap.
func TestShareLinkBandwidthAccounting(t *testing.T) {
	fx := newShareFixture(t)
	a := putLinkGrant(t, fx, "shr_bw_a", "shared/project", nil, "")
	b := putLinkGrant(t, fx, "shr_bw_b", "shared/private-project", nil, "")

	for i := 0; i < 3; i++ {
		if resp, _ := fx.linkGet(t, fx.linkPath(a, "readme.txt")); resp.StatusCode != http.StatusOK {
			t.Fatalf("download: %d", resp.StatusCode)
		}
	}
	if resp, _ := fx.linkGet(t, fx.linkPath(b, "secret.txt")); resp.StatusCode != http.StatusOK {
		t.Fatalf("second share download: %d", resp.StatusCode)
	}

	all := fx.server.linkStats.All(fx.alice.name)
	if len(all) != 2 {
		t.Fatalf("want two shares accounted, got %+v", all)
	}
	byID := map[string]int64{}
	for _, st := range all {
		byID[st.ShareID] = st.Bytes
	}
	if byID["shr_bw_a"] != int64(3*len("readme v1")) {
		t.Fatalf("share a bytes = %d", byID["shr_bw_a"])
	}
	if byID["shr_bw_b"] != int64(len("top secret")) {
		t.Fatalf("share b bytes = %d", byID["shr_bw_b"])
	}
}

// Rate limiting protects the endpoint from an unauthenticated grinder.
func TestShareLinkRateLimited(t *testing.T) {
	fx := newShareFixture(t)
	// A tight per-minute budget makes the limit observable without a
	// thousand requests (each view costs linkRateCost units).
	fx.server.rateLimit = ratelimit.NewLimiter(
		config.RateLimits{PerMinute: 2 * linkRateCost, PerHour: 100000, PerDay: 100000},
		config.GlobalRateLimits{})
	token := putLinkGrant(t, fx, "shr_rl", "shared/project", nil, "")

	var limited bool
	for i := 0; i < 6; i++ {
		resp, _ := fx.linkGet(t, fx.linkPath(token))
		if resp.StatusCode == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("the link endpoint must rate-limit an unauthenticated caller")
	}
}

// Active content is neutered exactly as /pub does it: an identity origin
// must not become a host for stored XSS just because a link was shared.
func TestShareLinkActiveContentNeutered(t *testing.T) {
	fx := newShareFixture(t)
	resp := davReq(t, fx.ts, http.MethodPut,
		"/dav/alice.poweur.net/shared/project/page.html", fx.aliceTok,
		[]byte("<script>alert(1)</script>"), nil)
	resp.Body.Close()
	token := putLinkGrant(t, fx, "shr_html", "shared/project", nil, "")

	got, _ := fx.linkGet(t, fx.linkPath(token, "page.html"))
	if ct := got.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("html content-type %q want text/plain", ct)
	}
	if cd := got.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") {
		t.Fatalf("Content-Disposition %q want attachment", cd)
	}
}

func TestParseShareLinkPath(t *testing.T) {
	token, _ := idpkg.GenerateLinkToken()
	cases := []struct {
		name      string
		path      string
		host      string
		wantOK    bool
		wantOwner string
		wantRest  string
		wantBase  string
	}{
		{"host-routed root", "/s/" + token, "alice.poweur.net", true,
			"alice.poweur.net", "", "/s/" + token},
		{"host-routed file", "/s/" + token + "/a/b.txt", "alice.poweur.net", true,
			"alice.poweur.net", "a/b.txt", "/s/" + token},
		{"explicit owner", "/s/alice.poweur.net/" + token, "relay.test", true,
			"alice.poweur.net", "", "/s/alice.poweur.net/" + token},
		{"explicit owner with path", "/s/alice.poweur.net/" + token + "/x", "relay.test", true,
			"alice.poweur.net", "x", "/s/alice.poweur.net/" + token},
		// An identity always carries a dot; the token alphabet has none, so
		// the two forms can never be confused.
		{"uppercase normalizes", "/s/ALICE.POWEUR.NET/" + strings.ToUpper(token), "relay.test", true,
			"alice.poweur.net", "", "/s/alice.poweur.net/" + token},
		{"bare token, no host", "/s/" + token, "", false, "", "", ""},
		{"empty", "/s/", "alice.poweur.net", false, "", "", ""},
		{"owner with no token", "/s/alice.poweur.net", "relay.test", false, "", "", ""},
		{"owner with a bad token", "/s/alice.poweur.net/nope", "relay.test", false, "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseShareLinkPath(tc.path, tc.host)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v want %v (%+v)", ok, tc.wantOK, got)
			}
			if !ok {
				return
			}
			if got.owner != tc.wantOwner || got.rest != tc.wantRest || got.base != tc.wantBase {
				t.Fatalf("got %+v, want owner=%q rest=%q base=%q",
					got, tc.wantOwner, tc.wantRest, tc.wantBase)
			}
			if got.token != strings.ToLower(token) {
				t.Fatalf("token = %q", got.token)
			}
		})
	}
}
