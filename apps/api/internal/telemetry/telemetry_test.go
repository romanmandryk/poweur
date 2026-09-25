package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	collectlog "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	"google.golang.org/protobuf/proto"

	"github.com/getsentry/sentry-go"
)

func TestConfig(t *testing.T) {
	for _, tt := range []struct {
		name string
		c    Config
		ok   bool
	}{
		{"disabled ignores exporter env", Config{}, true},
		{"https", Config{Endpoint: "https://example.org", HashKey: strings.Repeat("x", 32)}, true},
		{"http denied", Config{Endpoint: "http://example.org", HashKey: strings.Repeat("x", 32)}, false},
		{"credentials in URL", Config{Endpoint: "https://secret@example.org", HashKey: strings.Repeat("x", 32)}, false},
		{"weak hash", Config{Endpoint: "https://example.org", HashKey: "short"}, false},
		{"protocol", Config{Endpoint: "https://example.org", HashKey: strings.Repeat("x", 32), Protocol: "grpc"}, false},
		{"header injection", Config{Endpoint: "https://example.org", HashKey: strings.Repeat("x", 32), Headers: "Authorization=abc%0a"}, false},
		{"secondary http denied", Config{SecondaryEndpoint: "http://example.org", HashKey: strings.Repeat("x", 32)}, false},
		{"secondary https", Config{SecondaryEndpoint: "https://example.org", HashKey: strings.Repeat("x", 32)}, true},
		{"uptime http denied", Config{UptimeURL: "http://example.org/hb"}, false},
		{"sentry https", Config{SentryDSN: "https://key@s.example.org/1"}, true},
		{"sentry missing key", Config{SentryDSN: "https://s.example.org/1"}, false},
		{"sentry http denied", Config{SentryDSN: "http://key@s.example.org/1"}, false},
		{"bad level", Config{Level: "garbage"}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if (tt.c.Validate() == nil) != tt.ok {
				t.Fatal("unexpected validation")
			}
		})
	}
	if _, err := ParseTrustedProxies("*"); err == nil {
		t.Fatal("trusted wildcard accepted")
	}
}
func TestPrivacyAndProxy(t *testing.T) {
	var consent atomic.Bool
	proxies, _ := ParseTrustedProxies("10.0.0.0/8")
	r, _ := New(context.Background(), Config{HashKey: strings.Repeat("x", 32), TrustedProxies: proxies}, "test", func(string) bool { return consent.Load() }, io.Discard)
	for _, tt := range []struct{ peer, xff, want string }{
		{"192.0.2.4:123", "1.2.3.4", "192.0.2.4"},
		{"10.0.0.1:123", "1.2.3.4, 192.0.2.4, 10.0.0.2", "192.0.2.4"},
		{"10.0.0.1:123", "bad", "10.0.0.1"},
	} {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = tt.peer
		req.Header.Set("X-Forwarded-For", tt.xff)
		if got := r.ClientIP(req); got != tt.want {
			t.Fatalf("IP %s != %s", got, tt.want)
		}
	}
	if r.Hash("Alice.example.") != r.Hash("alice.example") || strings.Contains(r.Hash("alice.example"), "alice") {
		t.Fatal("bad canonical hash")
	}
	e := Event{ActorID: "alice.example", IdentityMode: "raw", ClientIP: "192.0.2.4"}
	consent.Store(true)
	if r.sanitize(e).ActorID != "alice.example" {
		t.Fatal("consented actor redacted")
	}
	consent.Store(false)
	out := r.sanitize(e)
	if out.ClientIP != "" || out.IdentityMode != "hashed" || out.ActorID == e.ActorID {
		t.Fatal("withdrawal failed")
	}
}
func TestDisabledNoNetworkAndLocalModes(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", "http://127.0.0.1:1")
	var buf bytes.Buffer
	r, err := New(context.Background(), Config{}, "test", nil, &buf)
	if err != nil {
		t.Fatal(err)
	}
	r.Record(context.Background(), Event{Kind: "action", Action: "registration.create"}, "alice.example", "192.0.2.1")
	if r.queue != nil || r.mp != nil || strings.Contains(buf.String(), "alice.example") || strings.Contains(buf.String(), "192.0.2.1") {
		t.Fatal("disabled export/privacy failure")
	}
}
func TestOTLPExportAndWithdrawal(t *testing.T) {
	var mu sync.Mutex
	var events []Event
	var authOK bool
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path == "/v1/logs" {
			var req collectlog.ExportLogsServiceRequest
			if err := proto.Unmarshal(body, &req); err != nil {
				t.Error(err)
			}
			mu.Lock()
			authOK = r.Header.Get("Authorization") == "Basic test"
			for _, res := range req.ResourceLogs {
				for _, scope := range res.ScopeLogs {
					for _, l := range scope.LogRecords {
						var e Event
						if json.Unmarshal([]byte(l.Body.GetStringValue()), &e) != nil {
							t.Error("bad event")
						}
						events = append(events, e)
					}
				}
			}
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	defer collector.Close()
	var consent atomic.Bool
	consent.Store(true)
	r, err := New(context.Background(), Config{Endpoint: collector.URL, AllowHTTP: true, HashKey: strings.Repeat("x", 32), Headers: "Authorization=Basic%20test", Level: "error"}, "test", func(string) bool { return consent.Load() }, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	r.Record(context.Background(), Event{Kind: "action", Action: "message.submit"}, "alice.example", "192.0.2.1")
	consent.Store(false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !authOK || len(events) != 1 {
		t.Fatalf("missing event/auth: %v %d", authOK, len(events))
	}
	if events[0].IdentityMode != "hashed" || events[0].ClientIP != "" || events[0].Timestamp.IsZero() {
		t.Fatal("queued withdrawal not applied")
	}
}
func TestUnavailableNonBlockingAndDrops(t *testing.T) {
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer collector.Close()
	r, err := New(context.Background(), Config{Endpoint: collector.URL, AllowHTTP: true, HashKey: strings.Repeat("x", 32)}, "test", nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for i := 0; i < 5000; i++ {
		r.Record(context.Background(), Event{Kind: "diagnostic", Action: "server.error", Level: slog.LevelError}, "", "")
	}
	if time.Since(start) > time.Second {
		t.Fatal("enqueue waited for collector")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = r.Shutdown(ctx)
	if r.dropped.Load() == 0 {
		t.Fatal("loss unreported")
	}
}

func TestSlowCollectorBoundedShutdown(t *testing.T) {
	started := make(chan struct{}, 1)
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case started <- struct{}{}:
		default:
		}
		<-r.Context().Done()
	}))
	defer collector.Close()
	r, err := New(context.Background(), Config{Endpoint: collector.URL, AllowHTTP: true, HashKey: strings.Repeat("x", 32)}, "test", nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 128; i++ {
		r.Record(context.Background(), Event{Kind: "action", Action: "test"}, "", "")
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("export did not start")
	}
	for i := 0; i < 1000; i++ {
		r.Record(context.Background(), Event{Kind: "action", Action: "test"}, "", "")
	}
	if r.dropped.Load() == 0 {
		t.Fatal("full queue did not report drops")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if r.Shutdown(ctx) == nil {
		t.Fatal("expected shutdown deadline")
	}
	if time.Since(start) > time.Second {
		t.Fatal("shutdown exceeded bounded deadline")
	}
}

func TestDualOTLPExport(t *testing.T) {
	var mu sync.Mutex
	counts := map[string]int{}
	handler := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/logs" {
				mu.Lock()
				counts[name]++
				mu.Unlock()
			}
			w.Header().Set("Content-Type", "application/x-protobuf")
		}
	}
	a := httptest.NewServer(handler("a"))
	defer a.Close()
	b := httptest.NewServer(handler("b"))
	defer b.Close()
	r, err := New(context.Background(), Config{Endpoint: a.URL, SecondaryEndpoint: b.URL, AllowHTTP: true, HashKey: strings.Repeat("x", 32)}, "test", nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	r.Record(context.Background(), Event{Kind: "action", Action: "registration.create"}, "", "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if counts["a"] == 0 || counts["b"] == 0 {
		t.Fatalf("both sinks must receive logs: %v", counts)
	}
}

func TestUptimeHeartbeat(t *testing.T) {
	got := make(chan struct{}, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			select {
			case got <- struct{}{}:
			default:
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()
	r, err := New(context.Background(), Config{UptimeURL: ts.URL, AllowHTTP: true}, "test", nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("heartbeat was not sent")
	}
	if err := r.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestBrowserConfig(t *testing.T) {
	empty := Config{}.BrowserConfig("0.1.0")
	if !strings.Contains(string(empty), `"providers":[]`) || strings.Contains(string(empty), "token") {
		t.Fatalf("empty config: %s", empty)
	}
	on := Config{BrowserFaroURL: "/faro/collect", Environment: "production"}.BrowserConfig("0.1.11")
	if !strings.Contains(string(on), `"type":"faro"`) || !strings.Contains(string(on), `"url":"/faro/collect"`) || !strings.Contains(string(on), "0.1.11") {
		t.Fatalf("enabled config: %s", on)
	}
}

type captureTransport struct {
	mu     sync.Mutex
	events []*sentry.Event
}

func (t *captureTransport) Configure(sentry.ClientOptions) {}
func (t *captureTransport) SendEvent(event *sentry.Event) {
	t.mu.Lock()
	t.events = append(t.events, event)
	t.mu.Unlock()
}
func (t *captureTransport) Flush(time.Duration) bool              { return true }
func (t *captureTransport) FlushWithContext(context.Context) bool { return true }
func (t *captureTransport) Close()                                {}
func (t *captureTransport) snapshot() []*sentry.Event {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]*sentry.Event, len(t.events))
	copy(out, t.events)
	return out
}

func TestSentryCapturesErrorDiagnosticsOnly(t *testing.T) {
	tr := &captureTransport{}
	r, err := New(context.Background(), Config{
		SentryDSN:       "https://key@example.invalid/1",
		sentryTransport: tr,
		Environment:     "test",
	}, "0.1.12", nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	r.Record(context.Background(), Event{Kind: "action", Action: "message.submit"}, "alice.example", "192.0.2.1")
	r.Record(context.Background(), Event{Kind: "diagnostic", Action: "server.error", ErrorCode: "rate_limited", Level: slog.LevelWarn}, "alice.example", "192.0.2.1")
	r.Record(context.Background(), Event{
		Kind: "diagnostic", Action: "server.error", Outcome: "failure", Route: "GET /panic",
		ErrorCode: "panic", Stack: "github.com/poweur/api/internal/relay.instrument\n", Level: slog.LevelError,
	}, "alice.example", "192.0.2.1")
	if err := r.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := tr.snapshot()
	if len(got) != 1 {
		t.Fatalf("captured %d events, want 1", len(got))
	}
	ev := got[0]
	if ev.Level != sentry.LevelError || ev.Tags["error_code"] != "panic" || ev.Tags["route"] != "GET /panic" {
		t.Fatalf("unexpected event: %+v tags=%v", ev, ev.Tags)
	}
	if strings.Contains(ev.Message, "alice") || ev.Request != nil || ev.User.ID != "" || ev.User.Email != "" {
		t.Fatal("sentry event leaked identity or request")
	}
}

func TestFromEnvReadsSentryDSN(t *testing.T) {
	t.Setenv("SENTRY_DSN", "https://key@s.example.org/1")
	if FromEnv().SentryDSN != "https://key@s.example.org/1" {
		t.Fatal("SENTRY_DSN not loaded")
	}
}
