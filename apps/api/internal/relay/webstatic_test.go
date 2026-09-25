package relay

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeStaticApp(t *testing.T, marker string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>"+marker+"</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "assets", "main.js"), []byte("// "+marker), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func staticGet(t *testing.T, ts *httptest.Server, path string) (int, string, string) {
	t.Helper()
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Get(ts.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("Location"), string(body)
}

type staticCase struct {
	path     string
	status   int
	location string
	contains string
}

func checkStatic(t *testing.T, ts *httptest.Server, cases []staticCase) {
	t.Helper()
	for _, tc := range cases {
		status, loc, body := staticGet(t, ts, tc.path)
		if status != tc.status {
			t.Errorf("%s: status = %d, want %d", tc.path, status, tc.status)
		}
		if loc != tc.location {
			t.Errorf("%s: Location = %q, want %q", tc.path, loc, tc.location)
		}
		if tc.contains != "" && !strings.Contains(body, tc.contains) {
			t.Errorf("%s: body %q does not contain %q", tc.path, body, tc.contains)
		}
	}
}

// The built app is served at /app/, with every unknown path answered by
// index.html so the SPA's own routes survive a reload.
func TestWebStaticServesAppWithSPAFallback(t *testing.T) {
	mux := http.NewServeMux()
	mountWebStatic(mux, "/app", writeStaticApp(t, "app"), nil)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	checkStatic(t, ts, []staticCase{
		{"/app", http.StatusMovedPermanently, "/app/", ""},
		{"/app/", http.StatusOK, "", "app"},
		{"/app/assets/main.js", http.StatusOK, "", "// app"},
		{"/app/some/deep/route", http.StatusOK, "", "<html>app"},
		{"/app/assets", http.StatusOK, "", "<html>app"},
		{"/app/observability.json", http.StatusOK, "", `"providers":[]`},
	})
}

func TestWebStaticUnsetDirMountsNothing(t *testing.T) {
	mux := http.NewServeMux()
	mountWebStatic(mux, "/app", "", nil)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	if status, _, _ := staticGet(t, ts, "/app/"); status != http.StatusNotFound {
		t.Errorf("status = %d, want 404", status)
	}
}

func TestWebStaticRejectsTraversal(t *testing.T) {
	parent := t.TempDir()
	app := filepath.Join(parent, "app")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app, "index.html"), []byte("index"), 0o644); err != nil {
		t.Fatal(err)
	}
	const secret = "TOP-SECRET-CONTENT"
	if err := os.WriteFile(filepath.Join(parent, "secret.txt"), []byte(secret), 0o644); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mountWebStatic(mux, "/app", app, nil)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	// ServeMux answers a dot-segment path with a redirect to the cleaned one,
	// so the check is on the file's content, not on the path appearing anywhere.
	for _, path := range []string{"/app/../secret.txt", "/app/%2e%2e/secret.txt", "/app/..%2fsecret.txt"} {
		_, _, body := staticGet(t, ts, path)
		if strings.Contains(body, secret) {
			t.Errorf("%s leaked a file outside the app dir", path)
		}
	}
}

// Browsers ask the host itself for /favicon.ico. That has to be the brand
// icon on the launcher and on every identity host, not the JSON service banner
// GET / would otherwise return.
func TestRootIconsOnLauncherAndIdentityHosts(t *testing.T) {
	dir := writeStaticApp(t, "app")
	files := map[string]string{
		"favicon.ico":          "ICO-BYTES",
		"favicon.svg":          "<svg>p</svg>",
		"apple-touch-icon.png": "PNG-BYTES",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ts := httptest.NewServer(launcherTestServer(t, dir).Router())
	defer ts.Close()

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	wantType := map[string]string{
		"/favicon.ico":          "image/x-icon",
		"/favicon.svg":          "image/svg+xml",
		"/apple-touch-icon.png": "image/png",
	}
	for _, host := range []string{"poweur.net", "id.poweur.net", "alice.poweur.net"} {
		for path, body := range map[string]string{
			"/favicon.ico":          "ICO-BYTES",
			"/favicon.svg":          "<svg>p</svg>",
			"/apple-touch-icon.png": "PNG-BYTES",
			"/app/favicon.ico":      "ICO-BYTES",
		} {
			req, err := http.NewRequest(http.MethodGet, ts.URL+path, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Host = host
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("%s %s: %v", host, path, err)
			}
			got, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("%s %s: status = %d, want 200; body %q", host, path, resp.StatusCode, got)
				continue
			}
			if string(got) != body {
				t.Errorf("%s %s: body = %q, want %q", host, path, got, body)
			}
			if ctype, ok := wantType[path]; ok && !strings.HasPrefix(resp.Header.Get("Content-Type"), ctype) {
				t.Errorf("%s %s: Content-Type = %q, want %s", host, path, resp.Header.Get("Content-Type"), ctype)
			}
		}
	}
}

// A host with the web app mounted but without an icon file must not hand the
// service banner back as if it were an image.
func TestRootIconMissingIsNotTheServiceBanner(t *testing.T) {
	ts := httptest.NewServer(launcherTestServer(t, writeStaticApp(t, "app")).Router())
	defer ts.Close()

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/favicon.ico", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "alice.poweur.net"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body %q", resp.StatusCode, body)
	}
	if strings.Contains(string(body), "poweur-relay") {
		t.Fatalf("missing icon returned the service banner: %s", body)
	}
}

func TestWebStaticObservabilityJSON(t *testing.T) {
	mux := http.NewServeMux()
	mountWebStatic(mux, "/app", writeStaticApp(t, "app"), []byte(`{"providers":[{"type":"faro","url":"/faro/collect"}]}`))
	ts := httptest.NewServer(mux)
	defer ts.Close()
	status, _, body := staticGet(t, ts, "/app/observability.json")
	if status != http.StatusOK || !strings.Contains(body, `"type":"faro"`) || strings.Contains(body, "<html>") {
		t.Fatalf("status=%d body=%q", status, body)
	}
}
