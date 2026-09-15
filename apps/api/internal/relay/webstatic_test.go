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

// EPIC-021 E21-T2: the preview build is served at /newapp/ beside the legacy
// app at /app/, from its own directory, with the same SPA fallback.
func TestWebStaticServesLegacyAndNextSideBySide(t *testing.T) {
	legacy := writeStaticApp(t, "legacy")
	next := writeStaticApp(t, "next")
	mux := http.NewServeMux()
	mountWebStatic(mux, "/app", legacy)
	mountWebStatic(mux, "/newapp", next)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	cases := []struct {
		path     string
		status   int
		location string
		contains string
	}{
		{"/app", http.StatusMovedPermanently, "/app/", ""},
		{"/newapp", http.StatusMovedPermanently, "/newapp/", ""},
		{"/app/", http.StatusOK, "", "legacy"},
		{"/newapp/", http.StatusOK, "", "next"},
		{"/newapp/assets/main.js", http.StatusOK, "", "// next"},
		{"/app/assets/main.js", http.StatusOK, "", "// legacy"},
		{"/newapp/some/deep/route", http.StatusOK, "", "next"},
		{"/newapp/assets", http.StatusOK, "", "next"},
	}
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

func TestWebStaticUnsetDirMountsNothing(t *testing.T) {
	mux := http.NewServeMux()
	mountWebStatic(mux, "/newapp", "")
	ts := httptest.NewServer(mux)
	defer ts.Close()
	if status, _, _ := staticGet(t, ts, "/newapp/"); status != http.StatusNotFound {
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
	mountWebStatic(mux, "/newapp", app)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	// ServeMux answers a dot-segment path with a redirect to the cleaned one,
	// so the check is on the file's content, not on the path appearing anywhere.
	for _, path := range []string{"/newapp/../secret.txt", "/newapp/%2e%2e/secret.txt", "/newapp/..%2fsecret.txt"} {
		_, _, body := staticGet(t, ts, path)
		if strings.Contains(body, secret) {
			t.Errorf("%s leaked a file outside the app dir", path)
		}
	}
}
