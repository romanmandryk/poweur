package bridge

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Page data carries text others wrote (application names, IDs); none of it
// may end the data block and run a script.
func TestPageDataCannotBreakOutOfItsBlock(t *testing.T) {
	hostile := `</script><script>alert(1)</script><!--`
	rec := httptest.NewRecorder()
	if err := writePage(rec, http.StatusOK, pagePayload{
		Page: "error", Title: hostile, Service: serviceInfo{Name: "Bridge"}, Data: messagePage{Message: hostile},
	}); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	if strings.Contains(body, "<script>alert") || strings.Count(body, "</script>") != strings.Count(uiIndex, "</script>")+1 {
		t.Fatalf("page data escaped its block: %s", body)
	}
	var mp messagePage
	if (page{body: body}).data(t, &mp); mp.Message != hostile {
		t.Fatalf("message did not round-trip: %q", mp.Message)
	}
	if !strings.Contains(body, "<title>&lt;/script&gt;&lt;script&gt;alert(1)&lt;/script&gt;&lt;!-- · Bridge</title>") {
		t.Errorf("title not escaped: %s", body)
	}
}

func TestPagesKeepTheStrictCSP(t *testing.T) {
	h := newHarness(t)
	p := h.browser().get("/")
	csp := p.header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self';") || strings.Contains(csp, "unsafe") {
		t.Fatalf("CSP = %q", csp)
	}
	if p.header.Get("Cache-Control") != "no-store" || !strings.HasPrefix(p.header.Get("Content-Type"), "text/html") {
		t.Fatalf("page headers = %v", p.header)
	}
}

func TestAssets(t *testing.T) {
	h := newHarness(t)
	for _, bad := range []string{"/assets/..%2Fweb.go", "/assets/%2e%2e", "/assets/missing.js", "/assets/"} {
		rec := httptest.NewRecorder()
		h.srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, bad, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s = %d", bad, rec.Code)
		}
	}
	if !uiBuilt {
		t.Skip("UI not built (pnpm --filter @poweur/oauth-ui build); nothing to serve")
	}
	entries, _ := webFS.ReadDir("web/dist/assets")
	for _, e := range entries {
		rec := httptest.NewRecorder()
		h.srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/"+e.Name(), nil))
		if rec.Code != 200 || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") || rec.Header().Get("Content-Type") == "" {
			t.Errorf("%s = %d %v", e.Name(), rec.Code, rec.Header())
		}
		if !strings.Contains(uiIndex, "/assets/"+e.Name()) && strings.HasSuffix(e.Name(), ".js") {
			t.Errorf("index.html does not load %s", e.Name())
		}
	}
}

func TestAnalyticsOnlyOnPublicPages(t *testing.T) {
	// Off by default: no script, no route, the strict CSP everywhere.
	off := newHarness(t)
	if p := off.browser().get("/"); strings.Contains(p.body, "analytics.js") || strings.Contains(p.header.Get("Content-Security-Policy"), "betterstack") {
		t.Fatalf("analytics without a token: %s", p.header.Get("Content-Security-Policy"))
	}
	rec := httptest.NewRecorder()
	off.srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/analytics.js", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("/analytics.js without a token = %d", rec.Code)
	}

	h := newHarness(t, func(c *Config) { c.AnalyticsToken = "testToken123" })
	for _, path := range []string{"/", "/privacy", "/security", "/abuse"} {
		p := h.browser().get(path)
		csp := p.header.Get("Content-Security-Policy")
		if !strings.Contains(p.body, `<script src="/analytics.js" async></script></head>`) ||
			!strings.Contains(csp, "script-src 'self' https://betterstack.net;") || strings.Contains(csp, "unsafe") {
			t.Errorf("%s: analytics missing or CSP wrong: %s", path, csp)
		}
	}
	// Anything that is part of signing in or managing access stays first-party only.
	for _, name := range []string{"identify", "await", "handoff", "consent", "account", "developers", "client_new", "client", "error"} {
		rec := httptest.NewRecorder()
		rec.Header().Set("Content-Security-Policy", h.srv.csp)
		h.srv.render(rec, httptest.NewRequest(http.MethodGet, "/", nil), http.StatusOK, name+".html", "", nil)
		if strings.Contains(rec.Body.String(), "analytics.js") || rec.Header().Get("Content-Security-Policy") != h.srv.csp {
			t.Errorf("%s page loads analytics", name)
		}
	}

	rec = httptest.NewRecorder()
	h.srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/analytics.js", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `'betterstack',"testToken123")`) ||
		!strings.HasPrefix(rec.Header().Get("Content-Type"), "text/javascript") {
		t.Fatalf("/analytics.js = %d %q", rec.Code, rec.Body.String())
	}
}

func TestAnalyticsTokenIsValidated(t *testing.T) {
	cfg := Config{AnalyticsToken: `x");alert(1)//`}
	if _, err := New(t.Context(), cfg); err == nil || !strings.Contains(err.Error(), "AnalyticsToken") {
		t.Fatalf("hostile token accepted: %v", err)
	}
}
