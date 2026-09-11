package identity

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func authLogResponse() SignInResponse {
	return SignInResponse{
		Audience:  "https://tasks.example",
		Action:    SignInActionLink,
		RequestID: "req_1",
		KeyID:     SignInKeyIDIdentity,
		Scopes:    []string{"messages:send", "profile:read"},
		Statement: "Connect Tasks",
	}
}

func TestAuthLogRecordRoundTripAndGrouping(t *testing.T) {
	rec := NewAuthLogRecord(authLogResponse(), "Tasks", "cli", true,
		time.Date(2026, 9, 10, 9, 0, 0, 0, time.FixedZone("west", -3600)))
	if rec.At != "2026-09-10T10:00:00Z" || rec.AppID != "example.tasks" || !rec.Verified {
		t.Fatalf("record = %#v", rec)
	}
	raw, err := AppendAuthLog(nil, rec)
	if err != nil {
		t.Fatal(err)
	}
	badThenGood := append([]byte("not-json\n"), raw...)
	got := ParseAuthLog(badThenGood)
	if len(got) != 1 || got[0].RequestID != "req_1" {
		t.Fatalf("parsed = %#v", got)
	}
	byApp := AuthLogByApp(got)
	if len(byApp["example.tasks"]) != 1 {
		t.Fatalf("grouped = %#v", byApp)
	}
}

func TestAuthLogValidation(t *testing.T) {
	rec := NewAuthLogRecord(authLogResponse(), "", "web", false, time.Now())
	for _, mutate := range []func(*AuthLogRecord){
		func(r *AuthLogRecord) { r.At = "" },
		func(r *AuthLogRecord) { r.At = "yesterday" },
		func(r *AuthLogRecord) { r.Audience = "" },
		func(r *AuthLogRecord) { r.Audience = "javascript:alert(1)" },
	} {
		bad := rec
		mutate(&bad)
		if _, err := AppendAuthLog(nil, bad); err == nil {
			t.Fatalf("accepted invalid record: %#v", bad)
		}
	}
}

func TestAuthLogTrimsOldestWholeRecords(t *testing.T) {
	rec := NewAuthLogRecord(authLogResponse(), "Tasks", "cli", true, time.Now())
	rec.Statement = strings.Repeat("x", 2048)
	var raw []byte
	for i := 0; i < 180; i++ {
		rec.RequestID = "req_" + strings.Repeat("0", i%7) + string(rune('a'+i%26))
		var err error
		raw, err = AppendAuthLog(raw, rec)
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(raw) > MaxAuthLogBytes {
		t.Fatalf("log is %d bytes, cap %d", len(raw), MaxAuthLogBytes)
	}
	if !bytes.HasSuffix(raw, []byte("\n")) {
		t.Fatal("log must end at a record boundary")
	}
	parsed := ParseAuthLog(raw)
	if len(parsed) == 0 || len(parsed) >= 180 {
		t.Fatalf("trimming kept %d records", len(parsed))
	}
}
