package integration_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	relaypkg "github.com/poweur/api/pkg/relay"
	clipkg "github.com/poweur/cli/pkg/cli"
	collectlog "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	"google.golang.org/protobuf/proto"
)

func TestINT_TELEMETRY_ConsentAndCollectorOutage(t *testing.T) {
	var mu sync.Mutex
	var events []map[string]any
	var fail atomic.Bool
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(503)
			return
		}
		b, _ := io.ReadAll(r.Body)
		if r.URL.Path == "/v1/logs" {
			var req collectlog.ExportLogsServiceRequest
			if err := proto.Unmarshal(b, &req); err != nil {
				t.Error(err)
			}
			mu.Lock()
			defer mu.Unlock()
			for _, res := range req.ResourceLogs {
				for _, scope := range res.ScopeLogs {
					for _, record := range scope.LogRecords {
						var e map[string]any
						_ = json.Unmarshal([]byte(record.Body.GetStringValue()), &e)
						events = append(events, e)
					}
				}
			}
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	defer collector.Close()
	zone := newZone(t)
	ts := httptest.NewUnstartedServer(nil)
	addr := ts.Listener.Addr().String()
	cfg := relaypkg.Config{ListenAddr: addr, RelayAddress: addr, RelayScheme: "http", DNSTTL: time.Minute, ChallengeTTL: time.Minute, DataDir: t.TempDir(), HostedDomains: []string{"poweur.net"}, ResolverAllowPrivate: true, MaxInboxPerIdentity: 50, RateLimits: relaypkg.RateLimits{PerMinute: 1000, PerHour: 10000, PerDay: 100000}, Telemetry: relaypkg.TelemetryConfig{Endpoint: collector.URL, AllowHTTP: true, HashKey: strings.Repeat("x", 32), Environment: "test"}}
	s := relaypkg.NewServer(cfg, zone, relaypkg.NewProviderFactory(cfg))
	if err := s.StartTelemetry(context.Background(), io.Discard); err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	ts.Config.Handler = s.Router()
	ts.Start()
	defer ts.Close()
	zone.SetHost("telealice.poweur.net", addr)
	zone.SetHost("telebob.poweur.net", addr)
	clipkg.ConfigureIdentityResolver("http", true, addr)
	defer clipkg.ConfigureIdentityResolver("https", false, "")
	alice, bob := t.TempDir(), t.TempDir()
	runCLI(t, alice, "identity", "create", "telealice.poweur.net", "--hosted", "--relay", ts.URL)
	runCLI(t, bob, "identity", "create", "telebob.poweur.net", "--hosted", "--relay", ts.URL)
	runCLI(t, alice, "analytics", "off")
	runCLI(t, alice, "send", "telebob.poweur.net", "SECRET_MESSAGE_CONTENT")
	wait := func(wantRaw bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			found := false
			for _, e := range events {
				if e["kind"] == "request" && e["route"] == "POST /messages" && e["identity_mode"] == map[bool]string{true: "raw", false: "hashed"}[wantRaw] {
					if wantRaw {
						found = e["actor_id"] == "telealice.poweur.net" && e["client_ip"] != ""
					} else {
						_, hasIP := e["client_ip"]
						found = e["actor_id"] != "telealice.poweur.net" && !hasIP
					}
				}
			}
			mu.Unlock()
			if found {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("missing request mode raw=%v", wantRaw)
	}
	wait(false)
	runCLI(t, alice, "analytics", "on")
	runCLI(t, alice, "send", "telebob.poweur.net", "SECOND_SECRET")
	wait(true)
	runCLI(t, alice, "analytics", "off")
	fail.Store(true)
	runCLI(t, alice, "send", "telebob.poweur.net", "OUTAGE_STILL_DELIVERED")
	got, _ := runCLI(t, bob, "inbox")
	assertDecryptedInbox(t, got, "telealice.poweur.net", "OUTAGE_STILL_DELIVERED")
	// Invalid signatures/claimed consent must never become verified actor telemetry.
	req, _ := http.NewRequest("GET", ts.URL+"/messages/telealice.poweur.net", nil)
	req.Header.Set("X-Poweur-Identity", "telealice.poweur.net")
	req.Header.Set("X-Poweur-Consent", "true")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatal("forged request accepted")
	}
	fail.Store(false)
	mu.Lock()
	b, _ := json.Marshal(events)
	mu.Unlock()
	if strings.Contains(string(b), "SECRET_MESSAGE_CONTENT") || strings.Contains(string(b), "SECOND_SECRET") {
		t.Fatal("message contents leaked")
	}
}
