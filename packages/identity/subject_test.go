package identity

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestFormatAndParseBody(t *testing.T) {
	tests := []struct {
		name, subject, text string
		wantFlag            bool
		wantErr             bool
	}{
		{"no subject is today's message", "", "hello", false, false},
		{"whitespace subject is no subject", "   ", "hello", false, false},
		{"subject", "Q3 budget", "numbers", true, false},
		{"empty text", "Only subject", "", true, false},
		{"newline refused", "a\nb", "x", false, true},
		{"carriage return refused", "a\rb", "x", false, true},
		{"line separator refused", "a b", "x", false, true},
		{"C1 control refused", "a\u0085b", "x", false, true},
		{"too long", strings.Repeat("x", MaxSubjectBytes+1), "x", false, true},
		{"exactly max", strings.Repeat("x", MaxSubjectBytes), "x", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plaintext, md, err := FormatBody(tt.subject, tt.text)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if got := md[MetaBodyFormat] == BodyFormatHeadersV1; got != tt.wantFlag {
				t.Fatalf("flag = %v, want %v", got, tt.wantFlag)
			}
			if !tt.wantFlag && (plaintext != tt.text || md != nil) {
				t.Fatalf("no-subject body changed: %q %v", plaintext, md)
			}
			subject, text := ParseBody(plaintext, md)
			if subject != strings.TrimSpace(tt.subject) || text != tt.text {
				t.Fatalf("round trip = (%q, %q)", subject, text)
			}
		})
	}
}

func TestParseBodyNeverDropsContent(t *testing.T) {
	flag := map[string]string{MetaBodyFormat: BodyFormatHeadersV1}
	for _, plaintext := range []string{
		"Subject: s\nno blank line",
		"Subject: s\nbad line\n\nbody",
		"",
		"\n\nonly body",
	} {
		subject, text := ParseBody(plaintext, flag)
		if plaintext == "\n\nonly body" {
			// An empty header block is malformed (an empty line is not a header).
			if subject != "" || text != plaintext {
				t.Fatalf("%q -> (%q, %q)", plaintext, subject, text)
			}
			continue
		}
		if subject != "" || text != plaintext {
			t.Fatalf("%q -> (%q, %q), want whole text", plaintext, subject, text)
		}
	}
	// Unflagged: a body that starts with Subject: stays body.
	if s, text := ParseBody("Subject: hi\n\nthere", nil); s != "" || text != "Subject: hi\n\nthere" {
		t.Fatalf("unflagged parsed: %q %q", s, text)
	}
}

func TestNormalizeSubject(t *testing.T) {
	for in, want := range map[string]string{
		"Q3 budget":              "q3 budget",
		"  Q3\t  Budget ":        "q3 budget",
		"Re: Q3 budget":          "q3 budget",
		"RE: re: Fwd: Q3 budget": "q3 budget",
		"Re[12]: x":              "x",
		"Re[]: x":                "re[]: x",
		"Aw: Sv: Fw: x":          "x",
		"Reading list":           "reading list",
		"Re:":                    "",
		"Re : x":                 "x",
		"İstanbul":               "istanbul",
	} {
		if got := NormalizeSubject(in); got != want {
			t.Errorf("NormalizeSubject(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestConversationKeyOrder(t *testing.T) {
	if got := ConversationKey("T1", "Q3"); got != "T1" {
		t.Fatalf("thread first: %q", got)
	}
	if got := ConversationKey("", "Re: Q3"); got != "subj:q3" {
		t.Fatalf("subject second: %q", got)
	}
	if got := ConversationKey("", "Re:"); got != "" {
		t.Fatalf("empty normalized subject is default thread: %q", got)
	}
}

func TestNewThreadID(t *testing.T) {
	a := NewThreadID()
	if len(a) != 26 || ValidateThreadID(a) != nil {
		t.Fatalf("bad ulid %q", a)
	}
	if b := NewThreadID(); a == b {
		t.Fatal("thread ids repeat")
	}
	early := newULID(time.UnixMilli(1))
	late := newULID(time.UnixMilli(1 << 40))
	if early[:10] >= late[:10] {
		t.Fatalf("ulid time prefix does not sort: %q %q", early, late)
	}
	if err := ValidateGroupThreadID("team.example.org", GroupThreadID("team.example.org", a)); err != nil {
		t.Fatalf("group subject thread: %v", err)
	}
}

func TestHistoryRecordSubjectCompat(t *testing.T) {
	r := HistoryRecord{Version: 1, ID: "m1", Sender: "b.example.org", Recipient: "a.example.org", Timestamp: "2026-01-01T00:00:00Z", Queue: HistoryQueueInbox, Body: "x"}
	raw, _ := json.Marshal(r)
	if strings.Contains(string(raw), "subject") {
		t.Fatalf("record without subject changed shape: %s", raw)
	}
	r.Subject = "bad\nsubject"
	if r.Validate() == nil {
		t.Fatal("invalid subject accepted")
	}
}

func TestReadStateThreads(t *testing.T) {
	const peer = "bob.example.org"
	s := ReadState{Version: 1, Conversations: map[string]ReadMark{}}
	s = s.MarkThreadRead(peer, "A", "2026-01-01T10:00:00Z", "m2")
	if !s.Covers(peer, "A", "2026-01-01T09:00:00Z", "m1") {
		t.Fatal("thread mark does not cover")
	}
	if s.Covers(peer, "B", "2026-01-01T09:00:00Z", "m1") {
		t.Fatal("thread mark leaks to another thread")
	}
	// A rewind is ignored.
	s = s.MarkThreadRead(peer, "A", "2026-01-01T08:00:00Z", "m0")
	if s.Threads[peer]["A"].ID != "m2" {
		t.Fatal("thread mark rewound")
	}
	// Advancing the peer mark past it prunes it.
	s = s.MarkRead(peer, "2026-01-01T11:00:00Z", "m3")
	if s.Threads != nil {
		t.Fatalf("covered thread marks kept: %v", s.Threads)
	}
	// A thread mark the peer mark already covers is not stored.
	s = s.MarkThreadRead(peer, "A", "2026-01-01T10:30:00Z", "m9")
	if s.Threads != nil {
		t.Fatalf("redundant thread mark stored: %v", s.Threads)
	}
	raw, _ := json.Marshal(s)
	if _, err := ParseReadState(raw); err != nil {
		t.Fatal(err)
	}
	// Without threads the document is byte-identical to the older form.
	if strings.Contains(string(raw), "threads") {
		t.Fatalf("empty threads serialized: %s", raw)
	}
}

func TestReadStateThreadMarkCap(t *testing.T) {
	s := ReadState{Version: 1, Conversations: map[string]ReadMark{}}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < MaxThreadMarks+5; i++ {
		s = s.MarkThreadRead("p.example.org", NewThreadID()+string(rune('a'+i%26)), base.Add(time.Duration(i)*time.Second).Format(time.RFC3339), "m")
	}
	if n := len(s.Threads["p.example.org"]); n != MaxThreadMarks {
		t.Fatalf("thread marks = %d, want %d", n, MaxThreadMarks)
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
}
