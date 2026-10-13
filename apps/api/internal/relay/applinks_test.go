package relay

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestAppLinksServedOnlyWhenConfigured(t *testing.T) {
	server, ts := newTestRelay(t)
	for _, path := range []string{"/.well-known/apple-app-site-association", "/.well-known/assetlinks.json"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s without config = %d", path, resp.StatusCode)
		}
	}

	server.cfg.AppLinksIOSAppIDs = []string{"ABCDE12345.net.poweur.app"}
	server.cfg.AppLinksAndroidPackage = "net.poweur.app"
	server.cfg.AppLinksAndroidSHA256 = []string{"AA:BB:CC"}

	get := func(path string) (string, string) {
		t.Helper()
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
			t.Fatalf("%s = %d %q", path, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		return string(body), resp.Header.Get("Content-Type")
	}

	aasa, _ := get("/.well-known/apple-app-site-association")
	var apple struct {
		Applinks struct {
			Details []struct {
				AppIDs     []string            `json:"appIDs"`
				Components []map[string]string `json:"components"`
			} `json:"details"`
		} `json:"applinks"`
	}
	if err := json.Unmarshal([]byte(aasa), &apple); err != nil || len(apple.Applinks.Details) != 1 {
		t.Fatalf("aasa = %s %v", aasa, err)
	}
	d := apple.Applinks.Details[0]
	if d.AppIDs[0] != "ABCDE12345.net.poweur.app" {
		t.Fatalf("app ids = %v", d.AppIDs)
	}
	// Only the approve page: the rest of the identity's host stays in the browser.
	for _, c := range d.Components {
		if !strings.HasPrefix(c["/"], "/id/approve") {
			t.Fatalf("claims more than the approve page: %v", d.Components)
		}
	}

	links, _ := get("/.well-known/assetlinks.json")
	if !strings.Contains(links, `"package_name":"net.poweur.app"`) || !strings.Contains(links, `"AA:BB:CC"`) ||
		!strings.Contains(links, "delegate_permission/common.handle_all_urls") {
		t.Fatalf("assetlinks = %s", links)
	}
}
