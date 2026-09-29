package s3

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A store that answers slower than a probe step fails the probe: the relay
// refuses to start on a degraded primary store.
func TestProbeFailsOnSlowStore(t *testing.T) {
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer slow.Close()
	defer close(release)
	store, err := New(Config{Endpoint: slow.URL, Bucket: "probe-bucket", Region: "us-east-1", AccessKey: "k", SecretKey: "s"})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err = store.probe(context.Background(), 100*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("slow store: %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("probe waited %v", time.Since(start))
	}
}
