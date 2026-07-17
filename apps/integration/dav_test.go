// Integration tests for the per-identity file layer (EPIC-003): WebDAV
// access with Poweur-key auth, cross-identity /public reads across relays,
// app passwords, and public web serving.
package integration_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	clipkg "github.com/poweur/cli/pkg/cli"
)

func davDo(t *testing.T, method, url, token string, body []byte, hdr map[string]string) *http.Response {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rdr)
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

func mintTokenViaCLI(t *testing.T, home string, extra ...string) string {
	t.Helper()
	args := append([]string{"dav", "token", "--json"}, extra...)
	stdout, _ := runCLI(t, home, args...)
	var tok struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(stdout), &tok); err != nil || tok.Token == "" {
		t.Fatalf("dav token output: %s (%v)", stdout, err)
	}
	return tok.Token
}

// TestINT_DAV_01: full owner lifecycle over WebDAV with a CLI-minted token —
// upload, download with content-hash etag, PROPFIND, mkdir/move/delete,
// and the poweur-sys protection rules.
func TestINT_DAV_01_OwnerFileLifecycle(t *testing.T) {
	zone := newZone(t)
	ts, addr := newHostedRelay(t, zone, t.TempDir())
	defer ts.Close()
	relayURL := "http://" + addr
	zone.SetHost("davowner.poweur.net", addr)
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	home := t.TempDir()
	runCLI(t, home, "identity", "create", "davowner.poweur.net", "--hosted", "--relay", relayURL, "--json")
	token := mintTokenViaCLI(t, home, "--use-identity", "davowner.poweur.net")
	base := relayURL + "/dav/davowner.poweur.net"

	resp := davDo(t, http.MethodPut, base+"/private/hello.txt", token, []byte("file layer works"), nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT: %d", resp.StatusCode)
	}

	resp = davDo(t, http.MethodGet, base+"/private/hello.txt", token, nil, nil)
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(got) != "file layer works" {
		t.Fatalf("GET body %q", got)
	}
	if !strings.HasPrefix(resp.Header.Get("ETag"), `"sha256-`) {
		t.Fatalf("expected content-hash etag, got %q", resp.Header.Get("ETag"))
	}

	resp = davDo(t, "PROPFIND", base+"/", token, nil, map[string]string{"Depth": "1"})
	listing, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 207 || !strings.Contains(string(listing), "poweur-sys") {
		t.Fatalf("PROPFIND: %d %s", resp.StatusCode, listing)
	}

	resp = davDo(t, "MKCOL", base+"/private/docs", token, nil, nil)
	resp.Body.Close()
	resp = davDo(t, "MOVE", base+"/private/hello.txt", token, nil,
		map[string]string{"Destination": base + "/private/docs/hello.txt"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		t.Fatalf("MOVE: %d", resp.StatusCode)
	}
	resp = davDo(t, http.MethodDelete, base+"/private/docs", token, nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE: %d", resp.StatusCode)
	}

	// id.json is relay-managed.
	resp = davDo(t, http.MethodPut, base+"/poweur-sys/public/id.json", token, []byte("{}"), nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("id.json overwrite: %d want 403", resp.StatusCode)
	}
}

// TestINT_DAV_02: bob (hosted on relay B) reads alice's /public on relay A
// over WebDAV using a token issued by alice's relay — visitor auth resolves
// bob's key via the resolver chain, so visitors from any relay/domain work.
func TestINT_DAV_02_CrossRelayPublicRead(t *testing.T) {
	zone := newZone(t)
	tsA, addrA := newHostedRelay(t, zone, t.TempDir())
	defer tsA.Close()
	_, addrB := newRelay(t, zone)

	relayA := "http://" + addrA
	zone.SetHost("davalice.poweur.net", addrA)
	clipkg.ConfigureIdentityResolver("http", true, addrA)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	aliceHome := t.TempDir()
	bobHome := t.TempDir()
	runCLI(t, aliceHome, "identity", "create", "davalice.poweur.net", "--hosted", "--relay", relayA, "--json")
	// bob is DNS-registered on relay B: relay A can resolve his key via TXT.
	runCLI(t, bobHome, "identity", "create", "bob",
		"--parent-domain", "example.org", "--relay", "http://"+addrB,
		"--dns-provider", "mock", "--dns-token", "integration")

	// Alice publishes a public file.
	aliceToken := mintTokenViaCLI(t, aliceHome, "--use-identity", "davalice.poweur.net")
	base := relayA + "/dav/davalice.poweur.net"
	resp := davDo(t, http.MethodPut, base+"/public/announcement.txt", aliceToken, []byte("hello visitors"), nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("alice PUT: %d", resp.StatusCode)
	}
	resp = davDo(t, http.MethodPut, base+"/private/secret.txt", aliceToken, []byte("owner only"), nil)
	resp.Body.Close()

	// Bob mints a visitor token from alice's relay for alice's tree.
	bobToken := mintTokenViaCLI(t, bobHome,
		"--use-identity", "bob.example.org",
		"--audience", "davalice.poweur.net",
		"--relay", relayA)

	resp = davDo(t, http.MethodGet, base+"/public/announcement.txt", bobToken, nil, nil)
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(got) != "hello visitors" {
		t.Fatalf("cross-relay /public read: %d %q", resp.StatusCode, got)
	}

	// Layout denials, per root.
	for path, want := range map[string]int{
		"/private/secret.txt":              http.StatusForbidden,
		"/shared/anything":                 http.StatusForbidden,
		"/poweur-sys/relay/contacts.json":  http.StatusForbidden,
		"/poweur-sys/private/sealed-thing": http.StatusForbidden,
		"/apps/net.poweur.tasks/todo.json": http.StatusForbidden,
	} {
		resp = davDo(t, http.MethodGet, base+path, bobToken, nil, nil)
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("visitor GET %s: %d want %d", path, resp.StatusCode, want)
		}
	}

	// Writes are refused for visitors.
	resp = davDo(t, http.MethodPut, base+"/public/graffiti.txt", bobToken, []byte("x"), nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("visitor PUT: %d want 403", resp.StatusCode)
	}
}

// TestINT_DAV_03: app passwords via the CLI — add, use with HTTP Basic,
// list, remove (revocation is immediate).
func TestINT_DAV_03_AppPasswordBasicAuth(t *testing.T) {
	zone := newZone(t)
	ts, addr := newHostedRelay(t, zone, t.TempDir())
	defer ts.Close()
	relayURL := "http://" + addr
	zone.SetHost("davbasic.poweur.net", addr)
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	home := t.TempDir()
	runCLI(t, home, "identity", "create", "davbasic.poweur.net", "--hosted", "--relay", relayURL, "--json")

	stdout, _ := runCLI(t, home, "dav", "password", "add", "--name", "finder", "--json")
	var added struct {
		Password string `json:"password"`
		Username string `json:"username"`
	}
	if err := json.Unmarshal([]byte(stdout), &added); err != nil || added.Password == "" {
		t.Fatalf("password add output: %s (%v)", stdout, err)
	}

	basic := base64.StdEncoding.EncodeToString([]byte(added.Username + ":" + added.Password))
	req, _ := http.NewRequest("PROPFIND", relayURL+"/dav/davbasic.poweur.net/private/", nil)
	req.Header.Set("Authorization", "Basic "+basic)
	req.Header.Set("Depth", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 207 {
		t.Fatalf("Basic PROPFIND: %d want 207", resp.StatusCode)
	}

	listOut, _ := runCLI(t, home, "dav", "password", "list")
	if !strings.Contains(listOut, "finder") {
		t.Fatalf("password list: %s", listOut)
	}

	runCLI(t, home, "dav", "password", "remove", "--name", "finder")
	req, _ = http.NewRequest("PROPFIND", relayURL+"/dav/davbasic.poweur.net/private/", nil)
	req.Header.Set("Authorization", "Basic "+basic)
	req.Header.Set("Depth", "1")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked app password: %d want 401", resp.StatusCode)
	}
}

// TestINT_DAV_04: public web serving — a marked folder under /public is
// browsable anonymously via Host-routed /pub/, unmarked paths are not.
func TestINT_DAV_04_PubWebServing(t *testing.T) {
	zone := newZone(t)
	ts, addr := newHostedRelay(t, zone, t.TempDir())
	defer ts.Close()
	relayURL := "http://" + addr
	zone.SetHost("davpub.poweur.net", addr)
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	home := t.TempDir()
	runCLI(t, home, "identity", "create", "davpub.poweur.net", "--hosted", "--relay", relayURL, "--json")
	token := mintTokenViaCLI(t, home, "--use-identity", "davpub.poweur.net")
	base := relayURL + "/dav/davpub.poweur.net"

	resp := davDo(t, "MKCOL", base+"/public/downloads", token, nil, nil)
	resp.Body.Close()
	resp = davDo(t, http.MethodPut, base+"/public/downloads/readme.txt", token, []byte("public artifact"), nil)
	resp.Body.Close()
	resp = davDo(t, http.MethodPut, base+"/public/downloads/.poweur-web-public", token, []byte(""), nil)
	resp.Body.Close()
	resp = davDo(t, http.MethodPut, base+"/public/notmarked.txt", token, []byte("id-auth only"), nil)
	resp.Body.Close()

	pubGet := func(path string) *http.Response {
		req, _ := http.NewRequest(http.MethodGet, relayURL+path, nil)
		req.Host = "davpub.poweur.net"
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}

	r := pubGet("/pub/downloads/readme.txt")
	body, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != http.StatusOK || string(body) != "public artifact" {
		t.Fatalf("pub marked file: %d %q", r.StatusCode, body)
	}
	r = pubGet("/pub/notmarked.txt")
	r.Body.Close()
	if r.StatusCode != http.StatusNotFound {
		t.Fatalf("pub unmarked file: %d want 404", r.StatusCode)
	}
	// Listings default off (empty marker).
	r = pubGet("/pub/downloads/")
	r.Body.Close()
	if r.StatusCode != http.StatusNotFound {
		t.Fatalf("pub listing default: %d want 404", r.StatusCode)
	}
}
