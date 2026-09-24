// Integration tests for sharing (EPIC-005): a cross-relay share managed
// with the `poweur share` CLI, enforced end-to-end — grant, read+write
// through the share, group membership, revocation.
package integration_test

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	clipkg "github.com/poweur/cli/pkg/cli"
)

// TestINT_SHARE_07 proves the CLI-first transfer front door: local bytes go
// through the resumable upload endpoint, then a short-lived public grant makes
// them downloadable without an account.
func TestINT_SHARE_07_CLITransferCreate(t *testing.T) {
	zone := newZone(t)
	ts, addr := newHostedRelay(t, zone, t.TempDir())
	defer ts.Close()
	relayURL := "http://" + addr
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	home := t.TempDir()
	const owner = "sender.poweur.net"
	runCLI(t, home, "identity", "create", owner, "--hosted", "--relay", relayURL, "--json")
	local := filepath.Join(t.TempDir(), "package.txt")
	if err := os.WriteFile(local, []byte("transfer payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _ := runCLI(t, home, "transfer", "create", local, "--max-downloads", "1", "--json")
	var created struct {
		TransferID string `json:"transfer_id"`
		ShareID    string `json:"share_id"`
		Path       string `json:"path"`
		URL        string `json:"url"`
		ExpiresAt  string `json:"expires_at"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil || !strings.HasPrefix(created.TransferID, "tr_") || created.ShareID == "" {
		t.Fatalf("transfer create: %s err=%v", out, err)
	}
	if !strings.HasPrefix(created.Path, "shared/.transfers/"+created.TransferID+"/") || created.ExpiresAt == "" {
		t.Fatalf("transfer result: %+v", created)
	}
	token := created.URL[strings.LastIndex(created.URL, "/")+1:]
	resp, err := http.Get(relayURL + "/s/" + owner + "/" + token + "/package.txt")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "transfer payload" {
		t.Fatalf("transfer download: %d %q", resp.StatusCode, body)
	}
	resp, err = http.Get(relayURL + "/s/" + owner + "/" + token + "/package.txt")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("transfer download cap: %d want 410", resp.StatusCode)
	}
}

// TestINT_SHARE_06: the complete guest-to-ID lifecycle is operable from the
// CLI. The guest requests a claim, the owner explicitly approves and consumes
// the link, then the guest accepts the ordinary offer into a local mount.
func TestINT_SHARE_06_CLIFileRequestClaimLifecycle(t *testing.T) {
	zone := newZone(t)
	ts, addr := newHostedRelay(t, zone, t.TempDir())
	defer ts.Close()
	relayURL := "http://" + addr
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	ownerHome := t.TempDir()
	claimantHome := t.TempDir()
	const owner = "claimowner.poweur.net"
	const claimant = "claimant.poweur.net"
	runCLI(t, ownerHome, "identity", "create", owner, "--hosted", "--relay", relayURL, "--json")
	runCLI(t, claimantHome, "identity", "create", claimant, "--hosted", "--relay", relayURL, "--json")
	runCLI(t, ownerHome, "policy", "set", "contacts_and_requests")
	runCLI(t, claimantHome, "policy", "set", "contacts_and_requests")

	ownerToken := mintTokenViaCLI(t, ownerHome, "--use-identity", owner)
	ownerDAV := relayURL + "/dav/" + owner
	resp := davDo(t, "MKCOL", ownerDAV+"/shared/inbox", ownerToken, nil, nil)
	resp.Body.Close()
	resp = davDo(t, http.MethodPut, ownerDAV+"/shared/inbox/already-uploaded.txt", ownerToken, []byte("kept"), nil)
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("seed upload: %d", resp.StatusCode)
	}

	createdRaw, _ := runCLI(t, ownerHome, "share", "request", "add", "/shared/inbox", "--max-uploads", "2", "--json")
	var created struct {
		ShareID string `json:"share_id"`
		Token   string `json:"token"`
	}
	if err := json.Unmarshal([]byte(createdRaw), &created); err != nil || created.ShareID == "" || created.Token == "" {
		t.Fatalf("file request output: %s err=%v", createdRaw, err)
	}

	linkURL := relayURL + "/s/" + owner + "/" + created.Token
	for _, body := range []string{"from-guest-a", "from-guest-b"} {
		resp = postFileRequest(t, linkURL, "same.txt", body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("anonymous upload %q: %d", body, resp.StatusCode)
		}
	}
	resp = postFileRequest(t, linkURL, "late.txt", "nope")
	resp.Body.Close()
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("upload past cap: %d want 410", resp.StatusCode)
	}
	hidden, err := http.Get(linkURL + "/same.txt")
	if err != nil {
		t.Fatal(err)
	}
	hiddenBody, _ := io.ReadAll(hidden.Body)
	hidden.Body.Close()
	if hidden.StatusCode != http.StatusNotFound || strings.Contains(string(hiddenBody), "from-guest") {
		t.Fatalf("request leaked an upload: %d %s", hidden.StatusCode, hiddenBody)
	}
	uploads := inboxUploads(t, relayURL, owner, ownerToken)
	if len(uploads) != 2 {
		t.Fatalf("isolated uploads = %v", uploads)
	}

	runCLI(t, claimantHome, "share", "claim", "request", owner,
		"--share-id", created.ShareID, "--token", created.Token, "--action", "uploaded", "--json")
	ownerRequests, _ := runCLI(t, ownerHome, "requests", "--json")
	claimJSON := lifecyclePlaintext(t, ownerRequests, "sys.share.claim")
	claimFile := filepath.Join(t.TempDir(), "claim.json")
	if err := os.WriteFile(claimFile, []byte(claimJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	approvedRaw, _ := runCLI(t, ownerHome, "share", "claim", "approve",
		"--claim-file", claimFile, "--consume-link", "--perm", "rw", "--json")
	var approved struct {
		Grant struct {
			ShareID       string `json:"share_id"`
			SourceShareID string `json:"source_share_id"`
		} `json:"grant"`
		LinkRevoked bool `json:"link_revoked"`
	}
	if err := json.Unmarshal([]byte(approvedRaw), &approved); err != nil || approved.Grant.ShareID == "" || approved.Grant.SourceShareID != created.ShareID || !approved.LinkRevoked {
		t.Fatalf("claim approval output: %s err=%v", approvedRaw, err)
	}

	claimantRequests, _ := runCLI(t, claimantHome, "requests", "--json")
	offerJSON := lifecyclePlaintext(t, claimantRequests, "sys.share.offer")
	offerFile := filepath.Join(t.TempDir(), "offer.json")
	if err := os.WriteFile(offerFile, []byte(offerJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	acceptedRaw, _ := runCLI(t, claimantHome, "share", "accept", "--offer-file", offerFile, "--json")
	if !strings.Contains(acceptedRaw, `"owner_notified": true`) || !strings.Contains(acceptedRaw, `shared/`+owner+`/inbox`) {
		t.Fatalf("share accept output: %s", acceptedRaw)
	}

	claimantToken := mintTokenViaCLI(t, claimantHome, "--use-identity", claimant)
	resp = davDo(t, http.MethodGet, relayURL+"/dav/"+claimant+"/shared/"+owner+"/inbox/.poweur-mount.json", claimantToken, nil, nil)
	mountRaw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(mountRaw), approved.Grant.ShareID) {
		t.Fatalf("recipient mount: %d %s", resp.StatusCode, mountRaw)
	}

	visitorToken := mintTokenViaCLI(t, claimantHome, "--use-identity", claimant,
		"--audience", owner, "--scope", "dav:full", "--relay", relayURL)
	resp = davDo(t, http.MethodGet, ownerDAV+"/"+uploads[0], visitorToken, nil, nil)
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || (string(got) != "from-guest-a" && string(got) != "from-guest-b") {
		t.Fatalf("claimed guest upload %s: %d %q", uploads[0], resp.StatusCode, got)
	}
	resp = davDo(t, http.MethodGet, ownerDAV+"/shared/inbox/already-uploaded.txt", visitorToken, nil, nil)
	got, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(got) != "kept" {
		t.Fatalf("claimed existing upload: %d %q", resp.StatusCode, got)
	}
	resp = davDo(t, http.MethodPut, ownerDAV+"/shared/inbox/identified-edit.txt", visitorToken, []byte("edited"), nil)
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("claimed share write: %d", resp.StatusCode)
	}

	resp, err = http.Get(relayURL + "/s/" + owner + "/" + created.Token)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("consumed public link: %d want 404", resp.StatusCode)
	}
}

func postFileRequest(t *testing.T, linkURL, filename, body string) *http.Response {
	t.Helper()
	var encoded bytes.Buffer
	form := multipart.NewWriter(&encoded)
	part, err := form.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(part, body); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, linkURL, &encoded)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func inboxUploads(t *testing.T, relayURL, owner, token string) []string {
	t.Helper()
	resp := davDo(t, http.MethodGet, relayURL+"/sync/"+url.PathEscape(owner)+"/manifest?paths="+url.QueryEscape("/shared/inbox/"), token, nil, nil)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("manifest: %d %s", resp.StatusCode, raw)
	}
	var names []string
	for _, line := range strings.Split(string(raw), "\n") {
		var entry struct {
			Path string `json:"path"`
		}
		if json.Unmarshal([]byte(line), &entry) != nil || !strings.HasSuffix(entry.Path, "-same.txt") {
			continue
		}
		names = append(names, strings.TrimPrefix(entry.Path, "/"))
	}
	return names
}

func lifecyclePlaintext(t *testing.T, raw, messageType string) string {
	t.Helper()
	var queue struct {
		Requests []struct {
			Type      string `json:"type"`
			Plaintext string `json:"plaintext"`
		} `json:"requests"`
	}
	if err := json.Unmarshal([]byte(raw), &queue); err != nil {
		t.Fatalf("decode requests: %v: %s", err, raw)
	}
	for _, request := range queue.Requests {
		if request.Type == messageType && request.Plaintext != "" {
			return request.Plaintext
		}
	}
	t.Fatalf("%s not found in requests: %s", messageType, raw)
	return ""
}

// TestINT_SHARE_01: alice (hosted on relay A) shares a folder with bob
// (DNS identity on relay B) via `poweur share add`; bob reads and writes
// through the share with a token minted at alice's relay; `poweur share
// revoke` kills the access.
func TestINT_SHARE_01_CrossRelayShareLifecycle(t *testing.T) {
	zone := newZone(t)
	tsA, addrA := newHostedRelay(t, zone, t.TempDir())
	defer tsA.Close()
	_, addrB := newRelay(t, zone)

	relayA := "http://" + addrA
	zone.SetHost("sharealice.poweur.net", addrA)
	clipkg.ConfigureIdentityResolver("http", true, addrA)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	aliceHome := t.TempDir()
	bobHome := t.TempDir()
	runCLI(t, aliceHome, "identity", "create", "sharealice.poweur.net", "--hosted", "--relay", relayA, "--json")
	runCLI(t, bobHome, "identity", "create", "bob",
		"--parent-domain", "example.org", "--relay", "http://"+addrB,
		"--dns-provider", "mock", "--dns-token", "integration")

	// Alice seeds the shared folder and a sibling that must stay closed.
	aliceToken := mintTokenViaCLI(t, aliceHome, "--use-identity", "sharealice.poweur.net")
	base := relayA + "/dav/sharealice.poweur.net"
	for path, body := range map[string]string{
		"/shared/project/plan.md":    "the plan",
		"/shared/internal/notes.txt": "internal only",
	} {
		segs := strings.Split(strings.Trim(path, "/"), "/")
		for i := 1; i < len(segs); i++ {
			resp := davDo(t, "MKCOL", base+"/"+strings.Join(segs[:i], "/"), aliceToken, nil, nil)
			resp.Body.Close()
		}
		resp := davDo(t, http.MethodPut, base+path, aliceToken, []byte(body), nil)
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			t.Fatalf("seed %s: %d", path, resp.StatusCode)
		}
	}

	// Share via CLI: rw for bob.
	stdout, _ := runCLI(t, aliceHome, "share", "add", "/shared/project",
		"--with", "bob.example.org", "--perm", "rw", "--json")
	if !strings.Contains(stdout, "shared/project") {
		t.Fatalf("share add output: %s", stdout)
	}
	lsOut, _ := runCLI(t, aliceHome, "share", "ls")
	if !strings.Contains(lsOut, "bob.example.org") || !strings.Contains(lsOut, "perm=read,write") {
		t.Fatalf("share ls output: %s", lsOut)
	}
	shareID := strings.Fields(lsOut)[0]
	if !strings.HasPrefix(shareID, "shr_") {
		t.Fatalf("share id not found in ls output: %s", lsOut)
	}

	// Bob (relay B identity) mints a writable visitor token at alice's relay.
	bobToken := mintTokenViaCLI(t, bobHome,
		"--use-identity", "bob.example.org",
		"--audience", "sharealice.poweur.net",
		"--scope", "dav:full",
		"--relay", relayA)

	// Read through the share.
	resp := davDo(t, http.MethodGet, base+"/shared/project/plan.md", bobToken, nil, nil)
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(got) != "the plan" {
		t.Fatalf("cross-relay share read: %d %q", resp.StatusCode, got)
	}
	// Write through the share; alice sees it.
	resp = davDo(t, http.MethodPut, base+"/shared/project/from-bob.md", bobToken, []byte("bob's edit"), nil)
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("cross-relay share write: %d", resp.StatusCode)
	}
	resp = davDo(t, http.MethodGet, base+"/shared/project/from-bob.md", aliceToken, nil, nil)
	got, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(got) != "bob's edit" {
		t.Fatalf("owner read of shared write: %q", got)
	}
	// The ungranted sibling stays closed.
	resp = davDo(t, http.MethodGet, base+"/shared/internal/notes.txt", bobToken, nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("sibling read: %d want 403", resp.StatusCode)
	}

	// Revoke via CLI; bob loses access on the next request.
	runCLI(t, aliceHome, "share", "revoke", shareID)
	resp = davDo(t, http.MethodGet, base+"/shared/project/plan.md", bobToken, nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("post-revoke read: %d want 403", resp.StatusCode)
	}
}

// TestINT_SHARE_02: group shares via CLI — membership changes take effect
// without touching the grant.
func TestINT_SHARE_02_GroupShare(t *testing.T) {
	zone := newZone(t)
	tsA, addrA := newHostedRelay(t, zone, t.TempDir())
	defer tsA.Close()

	relayA := "http://" + addrA
	clipkg.ConfigureIdentityResolver("http", true, addrA)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	aliceHome := t.TempDir()
	bobHome := t.TempDir()
	carolHome := t.TempDir()
	runCLI(t, aliceHome, "identity", "create", "groupalice.poweur.net", "--hosted", "--relay", relayA, "--json")
	runCLI(t, bobHome, "identity", "create", "groupbob.poweur.net", "--hosted", "--relay", relayA, "--json")
	runCLI(t, carolHome, "identity", "create", "groupcarol.poweur.net", "--hosted", "--relay", relayA, "--json")

	aliceToken := mintTokenViaCLI(t, aliceHome, "--use-identity", "groupalice.poweur.net")
	base := relayA + "/dav/groupalice.poweur.net"
	resp := davDo(t, "MKCOL", base+"/shared/team-docs", aliceToken, nil, nil)
	resp.Body.Close()
	resp = davDo(t, http.MethodPut, base+"/shared/team-docs/handbook.md", aliceToken, []byte("team handbook"), nil)
	resp.Body.Close()

	// Group with bob only; grant to the group.
	runCLI(t, aliceHome, "share", "group", "set", "team", "--members", "groupbob.poweur.net")
	runCLI(t, aliceHome, "share", "add", "/shared/team-docs", "--with-group", "team", "--perm", "read")

	bobToken := mintTokenViaCLI(t, bobHome,
		"--use-identity", "groupbob.poweur.net", "--audience", "groupalice.poweur.net", "--relay", relayA)
	carolToken := mintTokenViaCLI(t, carolHome,
		"--use-identity", "groupcarol.poweur.net", "--audience", "groupalice.poweur.net", "--relay", relayA)

	resp = davDo(t, http.MethodGet, base+"/shared/team-docs/handbook.md", bobToken, nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("member read: %d", resp.StatusCode)
	}
	resp = davDo(t, http.MethodGet, base+"/shared/team-docs/handbook.md", carolToken, nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-member read: %d want 403", resp.StatusCode)
	}

	// One membership update: carol in, bob out.
	runCLI(t, aliceHome, "share", "group", "set", "team", "--members", "groupcarol.poweur.net")
	resp = davDo(t, http.MethodGet, base+"/shared/team-docs/handbook.md", carolToken, nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("added member read: %d", resp.StatusCode)
	}
	resp = davDo(t, http.MethodGet, base+"/shared/team-docs/handbook.md", bobToken, nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("removed member read: %d want 403", resp.StatusCode)
	}
}

// TestINT_SHARE_03: public-link shares (E05-T4) end to end — `poweur share
// link add` issues a capability URL, a plain HTTP client with no Poweur
// identity and no credentials browses and downloads through it, the
// password gate and download cap hold, and `poweur share revoke` kills it.
func TestINT_SHARE_03_PublicLinkShare(t *testing.T) {
	zone := newZone(t)
	tsA, addrA := newHostedRelay(t, zone, t.TempDir())
	defer tsA.Close()

	relayA := "http://" + addrA
	zone.SetHost("linkalice.poweur.net", addrA)
	clipkg.ConfigureIdentityResolver("http", true, addrA)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	aliceHome := t.TempDir()
	runCLI(t, aliceHome, "identity", "create", "linkalice.poweur.net", "--hosted", "--relay", relayA, "--json")

	aliceToken := mintTokenViaCLI(t, aliceHome, "--use-identity", "linkalice.poweur.net")
	base := relayA + "/dav/linkalice.poweur.net"
	resp := davDo(t, "MKCOL", base+"/shared/handouts", aliceToken, nil, nil)
	resp.Body.Close()
	for path, body := range map[string]string{
		"/shared/handouts/agenda.md": "# agenda",
		"/shared/private-notes.txt":  "not for the link",
	} {
		resp := davDo(t, http.MethodPut, base+path, aliceToken, []byte(body), nil)
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			t.Fatalf("seed %s: %d", path, resp.StatusCode)
		}
	}

	// `share link add` reports the capability URL exactly once.
	stdout, _ := runCLI(t, aliceHome, "share", "link", "add", "/shared/handouts", "--json")
	var created struct {
		ShareID string `json:"share_id"`
		URL     string `json:"url"`
		Token   string `json:"token"`
	}
	if err := json.Unmarshal([]byte(stdout), &created); err != nil {
		t.Fatalf("share link add --json: %v (%s)", err, stdout)
	}
	if !strings.HasPrefix(created.ShareID, "shr_") || created.Token == "" {
		t.Fatalf("share link add: %+v", created)
	}
	if want := "https://linkalice.poweur.net/s/" + created.Token; created.URL != want {
		t.Fatalf("url %q want %q", created.URL, want)
	}

	// `share link ls` finds it; plain `share ls` shows it without the token.
	linkLs, _ := runCLI(t, aliceHome, "share", "link", "ls")
	if !strings.Contains(linkLs, created.ShareID) || !strings.Contains(linkLs, created.Token) {
		t.Fatalf("share link ls: %s", linkLs)
	}
	shareLs, _ := runCLI(t, aliceHome, "share", "ls")
	if !strings.Contains(shareLs, created.ShareID) {
		t.Fatalf("share ls should list link shares: %s", shareLs)
	}
	if strings.Contains(shareLs, created.Token) {
		t.Fatalf("share ls must not print the token: %s", shareLs)
	}

	// A visitor with no identity, no token and no Poweur software at all.
	anon := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	visit := func(t *testing.T, path string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, relayA+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		// The Host header is what a real `https://<identity>/s/<token>`
		// visit carries; the relay routes the owner off it.
		req.Host = "linkalice.poweur.net"
		resp, err := anon.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp.StatusCode, string(raw)
	}

	code, body := visit(t, "/s/"+created.Token)
	if code != http.StatusOK || !strings.Contains(body, "agenda.md") {
		t.Fatalf("anonymous browse: %d %s", code, body)
	}
	if strings.Contains(body, "private-notes") {
		t.Fatalf("the link leaked a sibling: %s", body)
	}
	code, body = visit(t, "/s/"+created.Token+"/agenda.md")
	if code != http.StatusOK || body != "# agenda" {
		t.Fatalf("anonymous download: %d %q", code, body)
	}
	// Nothing outside the shared folder is reachable through it.
	if code, _ = visit(t, "/s/"+created.Token+"/../private-notes.txt"); code == http.StatusOK {
		t.Fatal("the link reached outside its folder")
	}

	// Revocation is the ordinary verb, and it lands on the next request.
	runCLI(t, aliceHome, "share", "revoke", created.ShareID)
	if code, _ = visit(t, "/s/"+created.Token+"/agenda.md"); code != http.StatusNotFound {
		t.Fatalf("post-revoke link: %d want 404", code)
	}

	// A password-protected, download-capped link.
	stdout, _ = runCLI(t, aliceHome, "share", "link", "add", "/shared/handouts/agenda.md",
		"--password", "correct horse", "--max-downloads", "1", "--json")
	var guarded struct {
		ShareID string `json:"share_id"`
		Token   string `json:"token"`
	}
	if err := json.Unmarshal([]byte(stdout), &guarded); err != nil {
		t.Fatalf("guarded share link add: %v (%s)", err, stdout)
	}

	guardedPath := "/s/" + guarded.Token
	code, body = visit(t, guardedPath)
	if code != http.StatusUnauthorized || !strings.Contains(body, `type="password"`) {
		t.Fatalf("password gate: %d %s", code, body)
	}
	if strings.Contains(body, "# agenda") {
		t.Fatal("the password gate served the file anyway")
	}

	// A wrong password stays out; the right one gets a session cookie.
	postPassword := func(t *testing.T, password string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, relayA+guardedPath,
			strings.NewReader(url.Values{"password": {password}}.Encode()))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "linkalice.poweur.net"
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := anon.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		resp.Body.Close()
		return resp
	}
	if got := postPassword(t, "wrong"); got.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d want 401", got.StatusCode)
	}
	unlocked := postPassword(t, "correct horse")
	if unlocked.StatusCode != http.StatusSeeOther {
		t.Fatalf("correct password: %d want 303", unlocked.StatusCode)
	}
	var session *http.Cookie
	for _, c := range unlocked.Cookies() {
		if strings.HasPrefix(c.Name, "poweur_link_") {
			session = c
		}
	}
	if session == nil {
		t.Fatal("a correct password must produce a session cookie")
	}

	withSession := func(t *testing.T) (int, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, relayA+guardedPath, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "linkalice.poweur.net"
		req.AddCookie(session)
		resp, err := anon.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp.StatusCode, string(raw)
	}

	// The single download the cap allows…
	if code, body = withSession(t); code != http.StatusOK || body != "# agenda" {
		t.Fatalf("unlocked download: %d %q", code, body)
	}
	// …and no more.
	if code, _ = withSession(t); code != http.StatusGone {
		t.Fatalf("past the download cap: %d want 410", code)
	}
}

// TestINT_SHARE_04: addressable group identities (E05-T5) end to end.
//
// The group is a hosted identity of its own. `poweur group create` registers
// it and signs poweur-sys/relay/groups/self.json with the *group's* key;
// alice's grant names the group by its Poweur ID; the relay resolves the
// membership out of the group's own tree and lets members through. Adding
// and removing a member is one signed update against the group, never a
// change to alice's grant.
func TestINT_SHARE_04_GroupIdentityShare(t *testing.T) {
	zone := newZone(t)
	tsA, addrA := newHostedRelay(t, zone, t.TempDir())
	defer tsA.Close()

	relayA := "http://" + addrA
	clipkg.ConfigureIdentityResolver("http", true, addrA)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	aliceHome := t.TempDir()
	bobHome := t.TempDir()
	carolHome := t.TempDir()
	daveHome := t.TempDir()
	for home, id := range map[string]string{
		aliceHome: "gidalice.poweur.net",
		bobHome:   "gidbob.poweur.net",
		carolHome: "gidcarol.poweur.net",
		daveHome:  "giddave.poweur.net",
	} {
		runCLI(t, home, "identity", "create", id, "--hosted", "--relay", relayA, "--json")
	}

	aliceToken := mintTokenViaCLI(t, aliceHome, "--use-identity", "gidalice.poweur.net")
	base := relayA + "/dav/gidalice.poweur.net"
	resp := davDo(t, "MKCOL", base+"/shared/crew-docs", aliceToken, nil, nil)
	resp.Body.Close()
	resp = davDo(t, http.MethodPut, base+"/shared/crew-docs/handbook.md", aliceToken, []byte("crew handbook"), nil)
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("seed: %d", resp.StatusCode)
	}

	// Alice creates the group identity with bob in it. Creating a group must
	// not switch which identity the next command speaks as.
	createOut, _ := runCLI(t, aliceHome, "group", "create", "gidcrew.poweur.net",
		"--member", "gidbob.poweur.net", "--relay", relayA)
	if !strings.Contains(createOut, "gidcrew.poweur.net") {
		t.Fatalf("group create output: %s", createOut)
	}
	whoami, _ := runCLI(t, aliceHome, "identity", "show", "--json")
	if !strings.Contains(whoami, "gidalice.poweur.net") {
		t.Fatalf("creating a group must leave the active identity alone: %s", whoami)
	}

	// The group's membership document lives in the *group's* tree, signed by
	// the group's own key — not in alice's.
	showOut, _ := runCLI(t, aliceHome, "group", "show", "gidcrew.poweur.net", "--json")
	var doc struct {
		Group   string   `json:"group"`
		Owner   string   `json:"owner"`
		Members []string `json:"members"`
		Admins  []string `json:"admins"`
		Epoch   int      `json:"epoch"`
	}
	if err := json.Unmarshal([]byte(showOut), &doc); err != nil {
		t.Fatalf("group show --json: %v (%s)", err, showOut)
	}
	if doc.Group != "gidcrew.poweur.net" || doc.Owner != "gidcrew.poweur.net" {
		t.Fatalf("a group identity is its own owner, got %q/%q", doc.Group, doc.Owner)
	}
	if doc.Epoch != 1 {
		t.Fatalf("fresh group epoch = %d want 1", doc.Epoch)
	}
	if strings.Join(doc.Admins, ",") != "gidalice.poweur.net" {
		t.Fatalf("admins = %v want alice", doc.Admins)
	}
	// `admins` is an authority list, not a membership list: alice can change
	// the group without being in it, and access follows `members` alone.
	if strings.Join(doc.Members, ",") != "gidbob.poweur.net" {
		t.Fatalf("members = %v want bob alone", doc.Members)
	}

	// One grant, addressed to the group by its Poweur ID.
	runCLI(t, aliceHome, "share", "add", "/shared/crew-docs",
		"--with-group", "gidcrew.poweur.net", "--perm", "read")

	visitorToken := func(home, id string) string {
		return mintTokenViaCLI(t, home, "--use-identity", id,
			"--audience", "gidalice.poweur.net", "--relay", relayA)
	}
	bobToken := visitorToken(bobHome, "gidbob.poweur.net")
	carolToken := visitorToken(carolHome, "gidcarol.poweur.net")
	daveToken := visitorToken(daveHome, "giddave.poweur.net")

	read := func(t *testing.T, token string) int {
		t.Helper()
		r := davDo(t, http.MethodGet, base+"/shared/crew-docs/handbook.md", token, nil, nil)
		r.Body.Close()
		return r.StatusCode
	}

	if code := read(t, bobToken); code != http.StatusOK {
		t.Fatalf("group member read: %d want 200", code)
	}
	if code := read(t, carolToken); code != http.StatusForbidden {
		t.Fatalf("non-member read: %d want 403", code)
	}

	// Membership changes are signed updates against the group, and the grant
	// is never touched.
	runCLI(t, aliceHome, "group", "add", "gidcrew.poweur.net", "--member", "gidcarol.poweur.net")
	if code := read(t, carolToken); code != http.StatusOK {
		t.Fatalf("added member read: %d want 200", code)
	}
	runCLI(t, aliceHome, "group", "remove", "gidcrew.poweur.net", "--member", "gidbob.poweur.net")
	if code := read(t, bobToken); code != http.StatusForbidden {
		t.Fatalf("removed member read: %d want 403", code)
	}
	if code := read(t, daveToken); code != http.StatusForbidden {
		t.Fatalf("outsider read: %d want 403", code)
	}

	// Two real changes, so the epoch has moved twice. E09-T5 binds group keys
	// to this number, so a no-op update must not move it.
	showOut, _ = runCLI(t, aliceHome, "group", "show", "gidcrew.poweur.net", "--json")
	if err := json.Unmarshal([]byte(showOut), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Epoch != 3 {
		t.Fatalf("epoch = %d want 3", doc.Epoch)
	}
	noop, _ := runCLI(t, aliceHome, "group", "add", "gidcrew.poweur.net", "--member", "gidcarol.poweur.net")
	if !strings.Contains(noop, "no change") {
		t.Fatalf("re-adding an existing member should be a no-op: %s", noop)
	}
	showOut, _ = runCLI(t, aliceHome, "group", "show", "gidcrew.poweur.net", "--json")
	if err := json.Unmarshal([]byte(showOut), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Epoch != 3 {
		t.Fatalf("a no-op update moved the epoch to %d", doc.Epoch)
	}

	// Dave holds no group key, so he cannot administer the group — the CLI
	// stops before it signs anything, and the membership is unchanged.
	if code := runCLICode(t, daveHome, "group", "add", "gidcrew.poweur.net", "--member", "giddave.poweur.net"); code == 0 {
		t.Fatal("a non-admin without the group key must not be able to update the group")
	}
	if code := read(t, daveToken); code != http.StatusForbidden {
		t.Fatalf("outsider read after a refused update: %d want 403", code)
	}

	// A grant naming a group this relay cannot resolve fails closed rather
	// than opening up: cross-relay group resolution is deferred.
	runCLI(t, aliceHome, "share", "add", "/shared/crew-docs",
		"--with-group", "nosuchgroup.poweur.net", "--perm", "read")
	if code := read(t, daveToken); code != http.StatusForbidden {
		t.Fatalf("unresolvable group grant: %d want 403", code)
	}
}
