package relay

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Installable per-identity app (EPIC-009 E09-T12).

const testIndexHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <title>Poweur ID</title>
  <link rel="apple-touch-icon" href="./apple-touch-icon.png" />
  <link rel="manifest" href="./manifest.webmanifest" />
</head>
<body><div id="root"></div></body>
</html>`

const testManifest = `{"name":"Poweur ID","short_name":"Poweur","id":".","start_url":".","scope":".","display":"standalone",
"icons":[{"src":"icon-192.png","sizes":"192x192","type":"image/png","purpose":"any"}]}`

func pwaRelay(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	server, ts := newTestRelay(t)
	dir := t.TempDir()
	for name, body := range map[string]string{
		"index.html": testIndexHTML, "manifest.webmanifest": testManifest, "sw.js": "// sw",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	server.cfg.WebStaticDir = dir
	ts.Config.Handler = server.Router()
	return server, ts
}

func hostGet(t *testing.T, ts *httptest.Server, host, path string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
	req.Host = host
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

func TestPWAManifestNamedAfterIdentity(t *testing.T) {
	server, ts := pwaRelay(t)
	registerTestIdentity(t, server, ts, "alice.poweur.net")
	registerTestIdentity(t, server, ts, "bob.poweur.net")

	resp, body := hostGet(t, ts, "alice.poweur.net", "/id/manifest.webmanifest")
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/manifest+json") {
		t.Fatalf("manifest: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatal(err)
	}
	// "alice.poweur.net" is longer than a launcher label: the handle.
	if m["name"] != "alice.poweur.net" || m["short_name"] != "alice" {
		t.Fatalf("names %v / %v", m["name"], m["short_name"])
	}
	if m["id"] != "." || m["start_url"] != "." || m["scope"] != "." || m["icons"] == nil {
		t.Fatalf("manifest lost its origin-relative fields or icons: %s", body)
	}

	_, body = hostGet(t, ts, "bob.poweur.net", "/id/manifest.webmanifest")
	if !strings.Contains(body, `"name":"bob.poweur.net"`) {
		t.Fatalf("bob's manifest: %s", body)
	}

	// The launcher and unknown hosts keep the build's manifest.
	_, body = hostGet(t, ts, "id.poweur.net", "/id/manifest.webmanifest")
	if !strings.Contains(body, `"name":"Poweur ID"`) {
		t.Fatalf("launcher manifest: %s", body)
	}
}

func TestPWAPageNamedAfterIdentity(t *testing.T) {
	server, ts := pwaRelay(t)
	registerTestIdentity(t, server, ts, "alice.poweur.net")

	for _, path := range []string{"/id/", "/id/some/route"} {
		resp, body := hostGet(t, ts, "alice.poweur.net", path)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d", path, resp.StatusCode)
		}
		if !strings.Contains(body, "<title>alice.poweur.net</title>") ||
			!strings.Contains(body, `<meta name="apple-mobile-web-app-title" content="alice" />`) ||
			strings.Contains(body, "Poweur ID") {
			t.Fatalf("%s: page not personalized:\n%s", path, body)
		}
		if !strings.Contains(body, `href="./apple-touch-icon.png"`) {
			t.Fatalf("%s: no avatar, so the brand icon must stay", path)
		}
	}
	_, body := hostGet(t, ts, "id.poweur.net", "/id/")
	if !strings.Contains(body, "<title>Poweur ID</title>") {
		t.Fatalf("launcher page changed: %s", body)
	}
	resp, _ := hostGet(t, ts, "alice.poweur.net", "/id/sw.js")
	if resp.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("sw.js Cache-Control %q", resp.Header.Get("Cache-Control"))
	}
}

func TestPWAUsesAvatarAsIcon(t *testing.T) {
	server, ts := pwaRelay(t)
	registerTestIdentity(t, server, ts, "alice.poweur.net")
	setSysFile(t, server, "alice.poweur.net", profilePath, `{"version":1,"avatar":"avatar-1a2b.jpg"}`)

	_, body := hostGet(t, ts, "alice.poweur.net", "/id/")
	if !strings.Contains(body, `<link rel="apple-touch-icon" href="/.well-known/poweur/avatar-1a2b.jpg" />`) {
		t.Fatalf("apple-touch-icon not the avatar:\n%s", body)
	}
	_, body = hostGet(t, ts, "alice.poweur.net", "/id/manifest.webmanifest")
	var m struct {
		Icons []map[string]string `json:"icons"`
	}
	_ = json.Unmarshal([]byte(body), &m)
	if len(m.Icons) != 1 || m.Icons[0]["src"] != "/.well-known/poweur/avatar-1a2b.jpg" || m.Icons[0]["type"] != "image/jpeg" {
		t.Fatalf("icons %+v", m.Icons)
	}
}

func TestPersonalizePageEscapesAndShortHosts(t *testing.T) {
	out := string(personalizePage([]byte("<head></head>"), &pwaIdentity{Host: "a<b>.net", ShortName: "a<b>.net"}))
	if strings.Contains(out, "<b>") || !strings.Contains(out, "<title>a&lt;b&gt;.net</title>") {
		t.Fatalf("not escaped / not inserted: %s", out)
	}
}
