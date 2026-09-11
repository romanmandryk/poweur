package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOutboxQueuesAndRetries(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	received := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received++
		w.WriteHeader(http.StatusAccepted)
	}))
	defer ts.Close()
	msg := Message{ID: "msg_1", Sender: "alice.example", Recipient: "bob.example",
		Timestamp: time.Now().UTC().Format(time.RFC3339), Payload: "cipher", Signature: "signature"}
	if err := queueOutbox("alice.example", ts.URL, msg, false, context.DeadlineExceeded); err != nil {
		t.Fatal(err)
	}
	entries, err := loadOutbox("alice.example")
	if err != nil || len(entries) != 1 {
		t.Fatalf("queued: entries=%d err=%v", len(entries), err)
	}
	var out strings.Builder
	sent, pending := retryOutboxForIdentity(context.Background(), "alice.example", true, &out, io.Discard)
	if sent != 1 || pending != 0 || received != 1 {
		t.Fatalf("sent=%d pending=%d received=%d", sent, pending, received)
	}
	if entries, _ := loadOutbox("alice.example"); len(entries) != 0 {
		t.Fatalf("outbox not drained: %+v", entries)
	}
}

func TestOutboxDropsExpiredWithoutPosting(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	msg := Message{ID: "msg_expired", Sender: "alice.example", Recipient: "bob.example",
		ExpiresAt: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)}
	if err := queueOutbox("alice.example", "http://127.0.0.1:1", msg, false, context.DeadlineExceeded); err != nil {
		t.Fatal(err)
	}
	_, pending := retryOutboxForIdentity(context.Background(), "alice.example", true, io.Discard, io.Discard)
	if pending != 0 {
		t.Fatal("expired message remained queued")
	}
}
