package relay

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/poweur/api/internal/config"
	"github.com/poweur/api/internal/dns"
	"github.com/poweur/api/internal/storage"
)

const pageIdentity = "alice.poweur.net"

func newIdentityPageServer(t *testing.T) *Server {
	t.Helper()
	cfg := config.Config{
		RelayAddress:   "relay.test",
		HostedDomains:  []string{"poweur.net"},
		OAuthBridgeURL: "https://auth.poweur.net",
		Version:        "test",
	}
	s := NewServer(cfg, &fakeResolver{}, dns.NewProviderFactory(cfg))
	s.identities.Add(storage.Identity{Identity: pageIdentity})
	return s
}

func pageRequest(t *testing.T, s *Server, accept string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "https://"+pageIdentity+"/", nil)
	req.Host = pageIdentity
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	w := httptest.NewRecorder()
	s.handleRoot(w, req)
	return w
}

func writePageProfile(t *testing.T, s *Server, raw string) {
	t.Helper()
	if err := s.sysFiles.Write(t.Context(), pageIdentity, profilePath, []byte(raw)); err != nil {
		t.Fatal(err)
	}
}

func TestIdentityPageDefaultAndRootNegotiation(t *testing.T) {
	s := newIdentityPageServer(t)

	html := pageRequest(t, s, "text/html,application/xhtml+xml")
	if html.Code != http.StatusOK {
		t.Fatalf("HTML status = %d: %s", html.Code, html.Body.String())
	}
	body := html.Body.String()
	if !strings.Contains(body, pageIdentity) || !strings.Contains(body, "Copy ID") {
		t.Fatalf("default page is missing identity content: %s", body)
	}
	// Search indexing is opt-in: a default profile's page says noindex.
	if !strings.Contains(body, `name="robots"`) || html.Header().Get("X-Robots-Tag") != "noindex, nofollow" {
		t.Fatal("old profiles must not be indexable by default")
	}
	if got := html.Header().Get("Vary"); !strings.Contains(got, "Accept") {
		t.Fatalf("Vary = %q", got)
	}
	if got := html.Header().Get("Content-Security-Policy"); !strings.Contains(got, "default-src 'none'") {
		t.Fatalf("CSP = %q", got)
	}
	if got := html.Header().Values("Link"); len(got) == 0 || !strings.Contains(got[0], "indieauth-metadata") {
		t.Fatalf("IndieAuth Link missing: %v", got)
	}

	jsonResponse := pageRequest(t, s, "application/json")
	if jsonResponse.Code != http.StatusOK || !strings.Contains(jsonResponse.Body.String(), `"service":"poweur-relay"`) {
		t.Fatalf("JSON root changed: %d %s", jsonResponse.Code, jsonResponse.Body.String())
	}
	curl := pageRequest(t, s, "*/*")
	if !strings.Contains(curl.Body.String(), `"service":"poweur-relay"`) {
		t.Fatalf("ambiguous root should stay JSON: %s", curl.Body.String())
	}
}

func TestIdentityPageEscapesFiltersAndAdvertisesAnonymous(t *testing.T) {
	s := newIdentityPageServer(t)
	writePageProfile(t, s, `{"version":1,"display_name":"<script>alert(1)</script>","bio":"hello <img src=x>","links":[{"label":"safe","url":"https://example.org/path"},{"label":"bad","url":"javascript:alert(1)"}],"identity_page":{"advertise_anonymous_messages":true}}`)

	w := pageRequest(t, s, "text/html")
	body := w.Body.String()
	for _, unsafe := range []string{"<script>alert(1)</script>", "<img src=x>", "javascript:alert(1)"} {
		if strings.Contains(body, unsafe) {
			t.Fatalf("unsafe profile content reached HTML: %q in %s", unsafe, body)
		}
	}
	for _, wanted := range []string{"&lt;script&gt;alert(1)&lt;/script&gt;", "https://example.org/path", "/app/?anonymous=1", "Write anonymously"} {
		if !strings.Contains(body, wanted) {
			t.Fatalf("page missing %q: %s", wanted, body)
		}
	}
}

func TestIdentityPageOptOutsAndConditionalGet(t *testing.T) {
	s := newIdentityPageServer(t)
	writePageProfile(t, s, `{"version":1,"display_name":"Private Alice","identity_page":{"indexable":false}}`)
	w := pageRequest(t, s, "text/html")
	if w.Code != http.StatusOK || w.Header().Get("X-Robots-Tag") != "noindex, nofollow" || !strings.Contains(w.Body.String(), `name="robots"`) {
		t.Fatalf("index opt-out not applied: %d %q %s", w.Code, w.Header().Get("X-Robots-Tag"), w.Body.String())
	}
	etag := w.Header().Get("ETag")
	req := httptest.NewRequest(http.MethodGet, "https://"+pageIdentity+"/", nil)
	req.Host = pageIdentity
	req.Header.Set("Accept", "text/html")
	req.Header.Set("If-None-Match", etag)
	conditional := httptest.NewRecorder()
	s.handleRoot(conditional, req)
	if conditional.Code != http.StatusNotModified || conditional.Body.Len() != 0 {
		t.Fatalf("conditional GET = %d %q", conditional.Code, conditional.Body.String())
	}

	writePageProfile(t, s, `{"version":1,"display_name":"Must not leak","identity_page":{"enabled":false}}`)
	disabled := pageRequest(t, s, "text/html")
	if disabled.Code != http.StatusNotFound || strings.Contains(disabled.Body.String(), "Must not leak") {
		t.Fatalf("disabled page leaked profile: %d %s", disabled.Code, disabled.Body.String())
	}
	if disabled.Header().Get("X-Robots-Tag") != "noindex, nofollow" {
		t.Fatalf("disabled X-Robots-Tag = %q", disabled.Header().Get("X-Robots-Tag"))
	}
	if jsonResponse := pageRequest(t, s, "application/json"); jsonResponse.Code != http.StatusOK {
		t.Fatalf("page opt-out disabled machine root: %d", jsonResponse.Code)
	}
}

func TestIdentityPageAssetCaching(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/identity-page/assets/page.css?v=1", nil)
	w := httptest.NewRecorder()
	serveIdentityPageAsset(w, req)
	result := w.Result()
	defer result.Body.Close()
	if result.StatusCode != http.StatusOK || result.Header.Get("ETag") == "" || !strings.Contains(result.Header.Get("Cache-Control"), "immutable") {
		t.Fatalf("asset headers: %d %v", result.StatusCode, result.Header)
	}
	if body, _ := io.ReadAll(result.Body); !strings.Contains(string(body), ".identity-card") {
		t.Fatalf("unexpected stylesheet: %s", body)
	}
}

func TestIdentityPageOnlyOnExactRoot(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("web-app-shell"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newIdentityPageServer(t)
	s.cfg.WebStaticDir = dir
	router := s.Router()

	unknown := httptest.NewRequest(http.MethodGet, "https://"+pageIdentity+"/not-a-page", nil)
	unknown.Host = pageIdentity
	unknown.Header.Set("Accept", "text/html")
	unknownRec := httptest.NewRecorder()
	router.ServeHTTP(unknownRec, unknown)
	if strings.Contains(unknownRec.Body.String(), "Copy ID") || !strings.Contains(unknownRec.Body.String(), `"service":"poweur-relay"`) {
		t.Fatalf("unknown path became the identity page: %d %s", unknownRec.Code, unknownRec.Body.String())
	}

	app := httptest.NewRequest(http.MethodGet, "https://"+pageIdentity+"/app/", nil)
	app.Host = pageIdentity
	app.Header.Set("Accept", "text/html")
	appRec := httptest.NewRecorder()
	router.ServeHTTP(appRec, app)
	if appRec.Code != http.StatusOK || !strings.Contains(appRec.Body.String(), "web-app-shell") || strings.Contains(appRec.Body.String(), "Copy ID") {
		t.Fatalf("/app/ was not the web shell: %d %s", appRec.Code, appRec.Body.String())
	}
}

func TestIdentityPageRendersEachRequestFromTheProfile(t *testing.T) {
	s := newIdentityPageServer(t)
	for i := 0; i < 8; i++ {
		id := fmt.Sprintf("n%d.poweur.net", i)
		s.identities.Add(storage.Identity{Identity: id})
		raw := fmt.Sprintf(`{"version":1,"display_name":"Person %d"}`, i)
		if err := s.sysFiles.Write(t.Context(), id, profilePath, []byte(raw)); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodGet, "https://"+id+"/", nil)
		req.Host = id
		req.Header.Set("Accept", "text/html")
		w := httptest.NewRecorder()
		s.handleRoot(w, req)
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, fmt.Sprintf("Person %d", i)) {
			t.Fatalf("host %s: %d %s", id, w.Code, body)
		}
		if i > 0 && strings.Contains(body, "Person 0") {
			t.Fatalf("host %s reused another identity's page: %s", id, body)
		}
	}

	updated := `{"version":1,"display_name":"Person 0 revised"}`
	if err := s.sysFiles.Write(t.Context(), "n0.poweur.net", profilePath, []byte(updated)); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "https://n0.poweur.net/", nil)
	req.Host = "n0.poweur.net"
	req.Header.Set("Accept", "text/html")
	w := httptest.NewRecorder()
	s.handleRoot(w, req)
	if !strings.Contains(w.Body.String(), "Person 0 revised") || strings.Contains(w.Body.String(), ">Person 0<") {
		t.Fatalf("a later request reused the previous render: %s", w.Body.String())
	}
}

func TestSafeProfileLink(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"https://example.org/path", true},
		{"http://example.org", true},
		{"javascript:alert(1)", false},
		{"//example.org/path", false},
		{"https://user:password@example.org", false},
		{"https:///missing-host", false},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			_, got := safeProfileLink(tt.url)
			if got != tt.want {
				t.Fatalf("safeProfileLink(%q) allowed = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

func TestIdentityPageCallsToAction(t *testing.T) {
	s := newIdentityPageServer(t)
	writePageProfile(t, s, `{"version":1,"display_name":"Alice Smith"}`)
	// No launcher: nowhere to claim or message from, so no buttons that lead nowhere.
	if body := pageRequest(t, s, "text/html").Body.String(); strings.Contains(body, `id="btn-message"`) || strings.Contains(body, `id="btn-claim"`) {
		t.Fatalf("CTA without a launcher: %s", body)
	}

	s.cfg.LauncherHost = "poweur.net"
	body := pageRequest(t, s, "text/html").Body.String()
	for _, wanted := range []string{
		`href="https://poweur.net/app/?to=alice.poweur.net"`,
		"Message Alice",
		`id="btn-claim"`,
		"What is a Poweur ID?",
		`src="/identity-page/assets/logo.svg"`,
	} {
		if !strings.Contains(body, wanted) {
			t.Fatalf("page missing %q: %s", wanted, body)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/identity-page/assets/logo.svg", nil)
	w := httptest.NewRecorder()
	serveIdentityPageAsset(w, req)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/svg+xml" || !strings.Contains(w.Body.String(), "<svg") {
		t.Fatalf("logo asset: %d %q", w.Code, w.Header().Get("Content-Type"))
	}
}
