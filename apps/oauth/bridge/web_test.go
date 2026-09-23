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
