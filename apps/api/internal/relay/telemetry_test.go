package relay

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/poweur/api/internal/config"
	"github.com/poweur/api/internal/storage"
)

func TestAnalyticsPreference(t *testing.T) {
	for _, b := range []string{`{}`, `{"version":1,"granted":null,"updated_at":"2026-09-10T12:00:00Z"}`, `{"version":2,"granted":true,"updated_at":"2026-09-10T12:00:00Z"}`, `{"version":1,"granted":true,"updated_at":"bad"}`} {
		if _, err := parseAnalytics([]byte(b)); err == nil {
			t.Fatal("accepted invalid preference")
		}
	}
	for _, granted := range []string{"true", "false"} {
		p, err := parseAnalytics([]byte(`{"version":1,"granted":` + granted + `,"updated_at":"2026-09-10T12:00:00Z"}`))
		if err != nil || p.Granted != (granted == "true") {
			t.Fatal("valid preference rejected")
		}
	}
}
func TestTelemetryMiddlewarePanicAndStream(t *testing.T) {
	s := NewServer(config.Config{}, nil, nil)
	if err := s.StartTelemetry(context.Background(), io.Discard); err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	mux := http.NewServeMux()
	mux.HandleFunc("GET /stream/{identity}", func(w http.ResponseWriter, r *http.Request) {
		verifiedActor(r, r.PathValue("identity"))
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte("ready"))
	})
	mux.HandleFunc("GET /panic", func(w http.ResponseWriter, r *http.Request) { panic("SECRET user payload") })
	for _, path := range []string{"/stream/alice.example", "/panic"} {
		w := httptest.NewRecorder()
		s.instrument(mux, mux).ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if path == "/panic" && (w.Code != 500 || strings.Contains(w.Body.String(), "SECRET")) {
			t.Fatal("panic leaked")
		}
		if strings.HasPrefix(path, "/stream") && (!w.Flushed || w.Body.String() != "ready") {
			t.Fatal("stream broken")
		}
	}
}
func TestTelemetryStateSnapshots(t *testing.T) {
	ids := storage.NewIdentityStore()
	if len(ids.Names()) != 0 {
		t.Fatal("empty identities")
	}
	inbox := storage.NewInboxStore()
	if inbox.Depth() != 0 {
		t.Fatal("empty inbox")
	}
	inbox.Add("alice.example", storage.StoredMessage{ID: "a", Timestamp: time.Now().Format(time.RFC3339)}, 10)
	if inbox.Depth() != 1 {
		t.Fatal("wrong depth")
	}
	inbox.Drain("alice.example")
	if inbox.Depth() != 0 {
		t.Fatal("drain depth")
	}
}

func TestTelemetryReportsStorageFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	s := NewServer(config.Config{DataDir: path}, nil, nil)
	var logs bytes.Buffer
	if err := s.StartTelemetry(context.Background(), &logs); err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	if !strings.Contains(logs.String(), "drive_storage_open_failed") || strings.Contains(logs.String(), path) {
		t.Fatal("storage fallback missing or leaked path")
	}
}
