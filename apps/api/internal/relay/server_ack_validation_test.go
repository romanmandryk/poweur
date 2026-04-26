package relay

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPostAcksValidationErrors(t *testing.T) {
	s, _ := newTestServer(t)
	ts := httptest.NewServer(s.Router())
	defer ts.Close()
	url := ts.URL + "/acks"

	// not JSON
	resp, _ := http.Post(url, "application/json", bytes.NewReader([]byte("not-json")))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad json: %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// wrong ack type
	a := map[string]any{"type": "not_ack", "id": "1", "message_id": "m", "state": "delivered_client", "sender": "a", "recipient": "b", "timestamp": time.Now().UTC().Format(time.RFC3339), "signature": "e30="}
	if code := postJSON(t, url, a); code != http.StatusBadRequest {
		t.Fatalf("type: %d", code)
	}
	// wrong state
	a = map[string]any{"type": "ack", "id": "1", "message_id": "m", "state": "read", "sender": "a", "recipient": "b", "timestamp": time.Now().UTC().Format(time.RFC3339), "signature": "e30="}
	if code := postJSON(t, url, a); code != http.StatusBadRequest {
		t.Fatalf("state: %d", code)
	}
	// bad timestamp
	a = map[string]any{"type": "ack", "id": "1", "message_id": "m", "state": "delivered_client", "sender": "a", "recipient": "b", "timestamp": "not-rfc3339", "signature": "e30="}
	if code := postJSON(t, url, a); code != http.StatusBadRequest {
		t.Fatalf("ts: %d", code)
	}
}

func postJSON(t *testing.T, url string, v map[string]any) int {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func TestPostMessagesPayloadTooLarge(t *testing.T) {
	s, _ := newTestServer(t)
	ts := httptest.NewServer(s.Router())
	defer ts.Close()
	body := bytes.Repeat([]byte("a"), maxMessageBytes+1)
	resp, err := http.Post(ts.URL+"/messages", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d", resp.StatusCode)
	}
}
