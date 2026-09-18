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

func TestWebStaticObservabilityJSON(t *testing.T) {
	mux := http.NewServeMux()
	mountWebStatic(mux, "/app", writeStaticApp(t, "app"), []byte(`{"providers":[{"type":"betterstack","token":"t"}]}`))
	ts := httptest.NewServer(mux)
	defer ts.Close()
	status, _, body := staticGet(t, ts, "/app/observability.json")
	if status != http.StatusOK || !strings.Contains(body, `"type":"betterstack"`) || strings.Contains(body, "<html>") {
		t.Fatalf("status=%d body=%q", status, body)
	}
}
