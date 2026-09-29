package integration_test

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	clipkg "github.com/poweur/cli/pkg/cli"
)

// INT_DRIVE_09 (E20-T5): a public folder made with the CLI is readable by
// anyone at https://<identity>/pub/…, while the rest of the drive stays
// encrypted and unreachable there.
func TestINT_DRIVE_09_PublicFolder(t *testing.T) {
	zone := newZone(t)
	ts, addr := newHostedRelay(t, zone, t.TempDir())
	const alice = "pubcli.poweur.net"
	zone.SetHost(alice, addr)
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })
	home := t.TempDir()
	runCLI(t, home, "identity", "create", alice, "--hosted", "--relay", ts.URL, "--json")

	dir := t.TempDir()
	page, secret := filepath.Join(dir, "index.html"), filepath.Join(dir, "secret.txt")
	_ = os.WriteFile(page, []byte("<h1>made with the CLI</h1>"), 0600)
	_ = os.WriteFile(secret, []byte("not for the web"), 0600)
	runCLI(t, home, "drive", "mkdir", "/site", "--public", "--json")
	runCLI(t, home, "drive", "put", page, "/site/index.html", "--json")
	runCLI(t, home, "drive", "put", secret, "/secret.txt", "--json")

	get := func(path string) (int, string) {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		req.Host = alice
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}
	if status, body := get("/pub/site/index.html"); status != http.StatusOK || body != "<h1>made with the CLI</h1>" {
		t.Fatalf("public page: %d %q", status, body)
	}
	if status, body := get("/pub/"); status != http.StatusOK || !strings.Contains(body, `"site"`) || strings.Contains(body, "secret") {
		t.Fatalf("public index: %d %s", status, body)
	}
	if status, _ := get("/pub/secret.txt"); status != http.StatusNotFound {
		t.Fatalf("private file under /pub: %d", status)
	}
	// The owner's own client reads the public file like any other.
	out := filepath.Join(t.TempDir(), "back.html")
	runCLI(t, home, "drive", "get", "/site/index.html", out, "--json")
	if got, _ := os.ReadFile(out); string(got) != "<h1>made with the CLI</h1>" {
		t.Fatalf("owner read: %q", got)
	}
	// Publishing is explicit: a private file cannot be moved in.
	if code, _, stderr := runCLIFull(t, home, "drive", "mv", "/secret.txt", "/site/secret.txt"); code == 0 || !strings.Contains(stderr, "publishing needs a copy") {
		t.Fatalf("private file moved into a public folder: %d %s", code, stderr)
	}
}
