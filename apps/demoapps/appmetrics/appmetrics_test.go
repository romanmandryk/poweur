package appmetrics

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func render(r *Registry) string {
	var b strings.Builder
	r.Write(&b)
	return b.String()
}

func TestCounterStartsAtZeroAndFoldsUnknownValuesIntoOther(t *testing.T) {
	r := New("poweur_demo")
	c := r.Counter("events_total", "Things that happened.", "kind", "a", "b")

	out := render(r)
	for _, want := range []string{
		"# TYPE poweur_demo_events_total counter",
		`poweur_demo_events_total{kind="a"} 0`,
		`poweur_demo_events_total{kind="b"} 0`,
		"poweur_demo_start_time_seconds ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, `kind="other"`) {
		t.Errorf("an unused other series was published:\n%s", out)
	}

	c.Inc("a")
	c.Inc("a")
	c.Inc("whatever a visitor typed")
	c.Inc("another one")
	if c.Value("a") != 2 || c.Value("b") != 0 || c.Value(Other) != 2 {
		t.Fatalf("a=%d b=%d other=%d", c.Value("a"), c.Value("b"), c.Value(Other))
	}
	out = render(r)
	if !strings.Contains(out, `poweur_demo_events_total{kind="other"} 2`) || strings.Contains(out, "typed") {
		t.Errorf("unknown values must be folded, never published:\n%s", out)
	}
}

func TestNilRegistryAndCounterRecordNothing(t *testing.T) {
	var r *Registry
	c := r.Counter("x_total", "x", "kind", "a")
	c.Inc("a")
	if c.Value("a") != 0 {
		t.Fatal("a nil counter counted")
	}
	r.Write(io.Discard)
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	rec := httptest.NewRecorder()
	r.Instrument(h).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusTeapot {
		t.Fatalf("a nil registry must pass requests through, got %d", rec.Code)
	}
	if err := r.Serve(context.Background(), "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
}

func TestInstrumentLabelsByPatternAndStatusClass(t *testing.T) {
	r := New("poweur_demo")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /things/{id}", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok") })
	mux.HandleFunc("POST /boom", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "no", http.StatusInternalServerError) })
	h := r.Instrument(mux)

	for _, tc := range []struct{ method, path string }{
		{"GET", "/things/alice.poweur.net"},
		{"GET", "/things/bob.poweur.net?token=secret"},
		{"POST", "/boom"},
		{"GET", "/nope/at/all"},
	} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(tc.method, tc.path, nil))
	}

	out := render(r)
	for _, want := range []string{
		`poweur_demo_http_requests_total{route="GET /things/{id}",code="2xx"} 2`,
		`poweur_demo_http_requests_total{route="POST /boom",code="5xx"} 1`,
		`poweur_demo_http_requests_total{route="unmatched",code="4xx"} 1`,
		`poweur_demo_http_request_duration_seconds_count{route="GET /things/{id}"} 2`,
		`poweur_demo_http_request_duration_seconds_bucket{route="POST /boom",le="+Inf"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, leak := range []string{"alice", "bob", "secret", "token"} {
		if strings.Contains(out, leak) {
			t.Errorf("%q leaked into the metrics:\n%s", leak, out)
		}
	}
}

func TestHandlerServesTextFormat(t *testing.T) {
	r := New("poweur_demo")
	r.Counter("x_total", "x", "kind", "a").Inc("a")
	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain; version=0.0.4") {
		t.Errorf("content type = %q", ct)
	}
	if !strings.Contains(rec.Body.String(), `poweur_demo_x_total{kind="a"} 1`) {
		t.Errorf("body:\n%s", rec.Body.String())
	}
}

func TestServeAnswersOnlyMetricsAndStopsWithItsContext(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()

	r := New("poweur_demo")
	r.Counter("x_total", "x", "kind", "a").Inc("a")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Serve(ctx, addr) }()

	var resp *http.Response
	for deadline := time.Now().Add(5 * time.Second); ; {
		resp, err = http.Get("http://" + addr + "/metrics")
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("metrics listener never came up: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `poweur_demo_x_total{kind="a"} 1`) {
		t.Fatalf("GET /metrics = %d\n%s", resp.StatusCode, body)
	}
	other, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatal(err)
	}
	other.Body.Close()
	if other.StatusCode != http.StatusNotFound {
		t.Errorf("GET / on the metrics listener = %d, want 404", other.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not stop with its context")
	}
}

func TestServeReportsABadAddress(t *testing.T) {
	if err := New("poweur_demo").Serve(context.Background(), "256.0.0.1:99999"); err == nil {
		t.Fatal("a bad listen address was accepted")
	}
}
