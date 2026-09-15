package telemetry

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestActionDetailIsRecorded(t *testing.T) {
	var buf bytes.Buffer
	r, err := New(context.Background(), Config{}, "test", nil, &buf)
	if err != nil {
		t.Fatal(err)
	}
	r.Record(context.Background(), Event{Kind: "action", Action: "message.submit", Detail: "chat", Outcome: "success"}, "alice.example", "")
	r.Record(context.Background(), Event{Kind: "action", Action: "registration.create", Outcome: "success"}, "", "")
	out := buf.String()
	if !strings.Contains(out, `"action":"message.submit","detail":"chat"`) {
		t.Fatalf("detail missing: %s", out)
	}
	if strings.Contains(out, `"action":"registration.create","detail"`) {
		t.Fatal("empty detail must be omitted")
	}
}
