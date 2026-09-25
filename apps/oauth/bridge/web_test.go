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

func TestTelemetryIsFirstPartyAndOptional(t *testing.T) {
	// Off by default: no telemetry in the page, and no third-party anything.
	off := newHarness(t)
	p := off.browser().get("/")
	if strings.Contains(p.body, `"telemetry"`) || strings.Contains(p.body, "betterstack") {
		t.Fatalf("telemetry without a URL: %s", p.body)
	}
	rec := httptest.NewRecorder()
	off.srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/analytics.js", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("/analytics.js = %d, the Better Stack loader is gone", rec.Code)
	}

	// On: every page tells the bundled UI where to send, same origin, and the
	// CSP is the strict one everywhere — nothing but 'self' to connect to.
	h := newHarness(t, func(c *Config) { c.TelemetryURL = "/faro/collect" })
	for _, name := range []string{"home", "identify", "await", "consent", "account", "developers", "error"} {
		rec := httptest.NewRecorder()
		rec.Header().Set("Content-Security-Policy", h.srv.csp)
		h.srv.render(rec, httptest.NewRequest(http.MethodGet, "/", nil), http.StatusOK, name+".html", "", nil)
		body := rec.Body.String()
		if !strings.Contains(body, `"telemetry":{"url":"/faro/collect","version":"`+Version+`"}`) {
			t.Errorf("%s: no telemetry in the page: %s", name, body)
		}
		if strings.Contains(body, "<script src=") && !strings.Contains(body, `src="/assets/`) {
			t.Errorf("%s loads a script from elsewhere", name)
		}
	}
	if csp := h.srv.csp; strings.Contains(csp, "betterstack") || !strings.Contains(csp, "script-src 'self';") {
		t.Fatalf("CSP widened: %s", csp)
	}

	// Only a same-origin path is accepted.
	for _, bad := range []string{"https://collect.example/faro", "//collect.example/faro", "faro/collect"} {
		if _, err := New(t.Context(), Config{TelemetryURL: bad}); err == nil || !strings.Contains(err.Error(), "TelemetryURL") {
			t.Errorf("TelemetryURL %q: %v", bad, err)
		}
	}
}
