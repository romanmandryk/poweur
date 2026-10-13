package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	idpkg "github.com/poweur/identity"
)

func TestParseMailbox(t *testing.T) {
	tests := []struct {
		in, name, addr string
		ok             bool
	}{
		{"ann@example.com", "", "ann@example.com", true},
		{"<ann@example.com>", "", "ann@example.com", true},
		{"Ann Example <ann@example.com>", "Ann Example", "ann@example.com", true},
		{`"Ann, E." <ann@example.com>`, "Ann, E.", "ann@example.com", true},
		{"not an address", "", "", false},
		{"Eve <eve@example.com>\r\nBcc: x@example.com", "", "", false},
	}
	for _, tt := range tests {
		got, err := parseMailbox(tt.in)
		if (err == nil) != tt.ok {
			t.Errorf("parseMailbox(%q) err = %v", tt.in, err)
			continue
		}
		if tt.ok && (got.Name != tt.name || got.Address != tt.addr) {
			t.Errorf("parseMailbox(%q) = %+v", tt.in, got)
		}
	}
	list, err := parseAddressList([]string{"a@example.com, b@example.com", "c@example.com"})
	if err != nil || len(list) != 3 {
		t.Fatalf("parseAddressList = %v, %v", list, err)
	}
}

func TestDescribeEmail(t *testing.T) {
	m, _ := json.Marshal(idpkg.EmailMessage{Version: 1, From: idpkg.EmailAddress{Name: "Ann", Address: "ann@example.com"}, Subject: "Hi", Auth: idpkg.EmailAuth{Verdict: idpkg.EmailVerified}})
	if got := describeEmail(idpkg.MsgTypeEmailMessage, string(m)); got != `📧 email from "Ann" <ann@example.com>: Hi` {
		t.Fatalf("describeEmail = %q", got)
	}
	m, _ = json.Marshal(idpkg.EmailMessage{Version: 1, From: idpkg.EmailAddress{Address: "x@example.com"}, Subject: "Win", Auth: idpkg.EmailAuth{Verdict: idpkg.EmailUnverified, Spam: "spam"}})
	if got := describeEmail(idpkg.MsgTypeEmailMessage, string(m)); !strings.HasSuffix(got, "[spam]") {
		t.Fatalf("spam not flagged: %q", got)
	}
	st, _ := json.Marshal(idpkg.EmailStatus{Version: 1, SendID: "S1", State: idpkg.EmailStateBounced, Reason: "no such user", At: "2026-10-13T12:00:00Z"})
	if got := describeEmail(idpkg.MsgTypeEmailStatus, string(st)); got != "📧 email S1 bounced: no such user" {
		t.Fatalf("status = %q", got)
	}
	if got := describeEmail(idpkg.MsgTypeEmailMessage, "{"); got != "📧 unreadable email" {
		t.Fatalf("garbage = %q", got)
	}
}

func TestWithSubjectSplitsSentBody(t *testing.T) {
	body, md, _ := idpkg.FormatBody("Q3", "numbers")
	r := withSubject(idpkg.HistoryRecord{Body: body, Metadata: md})
	if r.Subject != "Q3" || r.Body != "numbers" {
		t.Fatalf("withSubject = %+v", r)
	}
	plain := withSubject(idpkg.HistoryRecord{Body: "Subject: x\n\ny"})
	if plain.Subject != "" || plain.Body != "Subject: x\n\ny" {
		t.Fatalf("unflagged body parsed: %+v", plain)
	}
}

func TestPrintBySubject(t *testing.T) {
	const me = "alice.example.org"
	records := []idpkg.HistoryRecord{
		{ID: "1", Sender: "bob.example.org", Recipient: me, Timestamp: "2026-01-01T10:00:00Z", ThreadID: "T1", Subject: "Q3 budget", Queue: "inbox", Body: "a"},
		{ID: "2", Sender: me, Recipient: "bob.example.org", Timestamp: "2026-01-01T11:00:00Z", ThreadID: "T1", Queue: "sent", Body: "b"},
		{ID: "3", Sender: "bob.example.org", Recipient: me, Timestamp: "2026-01-01T12:00:00Z", Queue: "inbox", Body: "c"},
		{ID: "4", Sender: "bob.example.org", Recipient: me, Timestamp: "2026-01-01T13:00:00Z", ThreadID: "named", Queue: "inbox", Body: "d"},
	}
	var out bytes.Buffer
	if code := printBySubject(&out, false, me, records, idpkg.ReadState{Conversations: map[string]idpkg.ReadMark{}}); code != 0 {
		t.Fatal(code)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 || !strings.Contains(lines[0], "#named") || !strings.Contains(lines[1], "(no subject)") || !strings.Contains(lines[2], "Q3 budget (2, 1 unread)") {
		t.Fatalf("by subject:\n%s", out.String())
	}
}

func TestSendSubjectFlagValidation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var stderr bytes.Buffer
	if code := runSend([]string{"bob.example.org", "hi", "--subject=a\nb"}, &bytes.Buffer{}, &stderr); code == 0 {
		t.Fatal("subject with a newline accepted")
	}
	stderr.Reset()
	if code := runSend([]string{"bob.example.org", "hi", "--subject=x", "--anon"}, &bytes.Buffer{}, &stderr); code == 0 || !strings.Contains(stderr.String(), "--subject") {
		t.Fatalf("--subject with --anon: %d %s", code, stderr.String())
	}
	stderr.Reset()
	if code := runSend([]string{"bob.example.org", "hi", "--subject=x", "--type=net.example.thing"}, &bytes.Buffer{}, &stderr); code == 0 || !strings.Contains(stderr.String(), "--subject") {
		t.Fatalf("--subject with --type: %d %s", code, stderr.String())
	}
}
