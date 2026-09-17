package signin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFetchContext(t *testing.T) {
	started := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	var served SignInContext
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/moved" {
			http.Redirect(w, r, "https://elsewhere.example/", http.StatusFound)
			return
		}
		served.RequestID = r.URL.Query().Get("request_id")
		_ = json.NewEncoder(w).Encode(served)
	}))
	defer ts.Close()
	meta := Metadata{Origin: ts.URL, ContextURI: ts.URL + "/ctx"}

	served = SignInContext{StartedAt: started, Browser: "Chrome on macOS\x00", Client: "Team dashboard", ClientHost: "grafana.example.org"}
	c, err := FetchContext(context.Background(), meta, "req_1", FetchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if c.RequestID != "req_1" || strings.ContainsAny(c.Browser, "\x00") {
		t.Fatalf("context = %+v", c)
	}
	got := DescribeContext(c, started.Add(12*time.Second))
	if !strings.Contains(got, "12 seconds ago") || !strings.Contains(got, "Team dashboard (grafana.example.org)") {
		t.Fatalf("describe = %q", got)
	}

	// No context_uri: nothing, no error.
	if c, err := FetchContext(context.Background(), Metadata{Origin: ts.URL}, "req_1", FetchOptions{}); err != nil || !c.StartedAt.IsZero() {
		t.Fatalf("no uri = %+v %v", c, err)
	}
	// Off-origin, redirects and mismatched ids are refused.
	for name, m := range map[string]Metadata{
		"off-origin": {Origin: ts.URL, ContextURI: "https://evil.example/ctx"},
		"redirect":   {Origin: ts.URL, ContextURI: ts.URL + "/moved"},
	} {
		if _, err := FetchContext(context.Background(), m, "req_1", FetchOptions{}); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	liar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(SignInContext{RequestID: "req_other", StartedAt: started})
	}))
	defer liar.Close()
	if _, err := FetchContext(context.Background(), Metadata{Origin: liar.URL, ContextURI: liar.URL}, "req_1", FetchOptions{}); err == nil {
		t.Error("a context for another request was accepted")
	}
}

func TestDescribeContext(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	for d, want := range map[time.Duration]string{
		time.Second:      "just now",
		30 * time.Second: "30 seconds ago",
		90 * time.Second: "a minute ago",
		5 * time.Minute:  "5 minutes ago",
		2 * time.Hour:    "over an hour ago",
	} {
		if got := DescribeContext(SignInContext{StartedAt: now.Add(-d)}, now); got != "Started "+want+"." {
			t.Errorf("%v: %q", d, got)
		}
	}
	if DescribeContext(SignInContext{}, now) != "" {
		t.Error("an empty context was described")
	}
	if got := DescribeContext(SignInContext{StartedAt: now, Browser: "Safari on iOS", Client: "x.example", ClientHost: "X.example"}, now); got != "Started just now in Safari on iOS, to sign in to x.example." {
		t.Errorf("got %q", got)
	}
}

func TestMetadataContextURIMustBeSameOrigin(t *testing.T) {
	m := Metadata{PoweurAuth: "1", Origin: "https://rp.example", Name: "RP", ContextURI: "https://evil.example/ctx"}
	if err := m.Validate("https://rp.example"); err == nil {
		t.Fatal("off-origin context_uri accepted")
	}
	m.ContextURI = "https://rp.example/ctx"
	if err := m.Validate("https://rp.example"); err != nil {
		t.Fatal(err)
	}
}
