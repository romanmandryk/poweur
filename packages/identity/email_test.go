package identity

import (
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestEmailAddressFor(t *testing.T) {
	tests := []struct {
		identity, want string
		ok             bool
	}{
		{"john.poweur.net", "john@poweur.net", true},
		{"John.Poweur.NET", "john@poweur.net", true},
		{"a.b.example.org", "a@b.example.org", true},
		{"poweur.net", "", false},
		{"not a name", "", false},
	}
	for _, tt := range tests {
		got, err := EmailAddressFor(tt.identity)
		if (err == nil) != tt.ok || got != tt.want {
			t.Errorf("EmailAddressFor(%q) = %q, %v", tt.identity, got, err)
		}
	}
}

func TestParseEmailAddress(t *testing.T) {
	tests := []struct {
		in                 string
		local, tag, domain string
		ok                 bool
	}{
		{"john@poweur.net", "john", "", "poweur.net", true},
		{"John+News@Poweur.Net", "john", "news", "poweur.net", true},
		{"<john@poweur.net>", "john", "", "poweur.net", true},
		{"john+@poweur.net", "john", "", "poweur.net", true},
		{"mailer-daemon@poweur.net", "mailer-daemon", "", "poweur.net", true},
		{"john.doe@poweur.net", "", "", "", false}, // dots are not ignored: no such label
		{"-john@poweur.net", "", "", "", false},
		{"jöhn@poweur.net", "", "", "", false},
		{"john+a b@poweur.net", "", "", "", false},
		{"john@localhost", "", "", "", false},
		{"@poweur.net", "", "", "", false},
		{"john@", "", "", "", false},
		{"john+" + strings.Repeat("t", MaxEmailTagLen+1) + "@poweur.net", "", "", "", false},
	}
	for _, tt := range tests {
		r, err := ParseEmailAddress(tt.in)
		if (err == nil) != tt.ok {
			t.Errorf("ParseEmailAddress(%q) err = %v", tt.in, err)
			continue
		}
		if tt.ok && (r.Local != tt.local || r.Tag != tt.tag || r.Domain != tt.domain) {
			t.Errorf("ParseEmailAddress(%q) = %+v", tt.in, r)
		}
	}
}

func TestEmailOptInSignVerify(t *testing.T) {
	priv := vectorKey()
	pub := priv.Public().(ed25519.PublicKey)
	o, err := NewEmailOptIn("John.poweur.net", "email.poweur.net", time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if o.Address != "john@poweur.net" {
		t.Fatalf("address %q", o.Address)
	}
	if err := o.Sign(priv); err != nil {
		t.Fatal(err)
	}
	if err := o.VerifySignature(pub); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(o)
	parsed, err := ParseEmailOptIn(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := parsed.VerifySignature(pub); err != nil {
		t.Fatal(err)
	}
	tampered := parsed
	tampered.Bridge = "evil.example.org"
	if tampered.VerifySignature(pub) == nil {
		t.Fatal("tampered bridge verified")
	}
	wrongAddr := parsed
	wrongAddr.Address = "boss@poweur.net"
	if wrongAddr.Validate() == nil {
		t.Fatal("address not matching the identity accepted")
	}
	unsigned := o
	unsigned.Signature = ""
	raw, _ = json.Marshal(unsigned)
	if _, err := ParseEmailOptIn(raw); err == nil {
		t.Fatal("unsigned email.json accepted")
	}
}

func TestInboxPolicyEmailBlock(t *testing.T) {
	p, err := ParseInboxPolicy([]byte(`{"version":1,"mode":"contacts_only","email":{"allow":true,"bridge":"Email.poweur.net"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !p.TrustsEmailBridge("email.poweur.net") || p.TrustsEmailBridge("other.poweur.net") || p.TrustsEmailBridge("") {
		t.Fatal("TrustsEmailBridge wrong")
	}
	off, _ := ParseInboxPolicy([]byte(`{"version":1,"mode":"open","email":{"allow":false,"bridge":"email.poweur.net"}}`))
	if off.TrustsEmailBridge("email.poweur.net") {
		t.Fatal("disabled block trusted")
	}
	if _, err := ParseInboxPolicy([]byte(`{"version":1,"mode":"open","email":{"allow":true,"bridge":"no"}}`)); err == nil {
		t.Fatal("bad bridge accepted")
	}
	if (InboxPolicy{Mode: InboxOpen}).TrustsEmailBridge("email.poweur.net") {
		t.Fatal("absent block trusted")
	}
}

func validSend() EmailSend {
	return EmailSend{Version: 1, SendID: "01J9Z", To: []EmailAddress{{Name: "Ann", Address: "ann@example.com"}}, Subject: "Hi", Text: "hello"}
}

func TestEmailSendValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*EmailSend)
		ok     bool
	}{
		{"valid", func(*EmailSend) {}, true},
		{"no send id", func(s *EmailSend) { s.SendID = "" }, false},
		{"bad send id", func(s *EmailSend) { s.SendID = "a b" }, false},
		{"no recipients", func(s *EmailSend) { s.To = nil }, false},
		{"bcc only", func(s *EmailSend) { s.To = nil; s.Bcc = []EmailAddress{{Address: "b@example.com"}} }, true},
		{"bad address", func(s *EmailSend) { s.To[0].Address = "not an address" }, false},
		{"address with name smuggled", func(s *EmailSend) { s.To[0].Address = "Eve <e@example.com>" }, false},
		{"header injection in subject", func(s *EmailSend) { s.Subject = "hi\r\nBcc: x@example.com" }, false},
		{"header injection in name", func(s *EmailSend) { s.FromName = "A\nB" }, false},
		{"too many", func(s *EmailSend) {
			for i := 0; i < MaxEmailRecipients; i++ {
				s.Cc = append(s.Cc, EmailAddress{Address: "c@example.com"})
			}
		}, false},
		{"attachment data", func(s *EmailSend) {
			s.Attachments = []EmailOutAttachment{{Name: "a.txt", MIME: "text/plain", Size: 2, Data: "aGk="}}
		}, true},
		{"attachment both", func(s *EmailSend) {
			s.Attachments = []EmailOutAttachment{{Name: "a.txt", MIME: "text/plain", Data: "aGk=", Upload: "u1"}}
		}, false},
		{"attachment path name", func(s *EmailSend) { s.Attachments = []EmailOutAttachment{{Name: "../a", Data: "aGk="}} }, false},
		{"too large", func(s *EmailSend) {
			s.Attachments = []EmailOutAttachment{{Name: "big", Size: MaxEmailOutboundBytes, Upload: "u"}}
		}, false},
		{"bad reference", func(s *EmailSend) { s.References = []string{"<a> <b>"} }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := validSend()
			tt.mutate(&s)
			if err := s.Validate(); (err == nil) != tt.ok {
				t.Fatalf("Validate() = %v, ok want %v", err, tt.ok)
			}
		})
	}
}

func TestEmailStatusAndMessageValidate(t *testing.T) {
	st := EmailStatus{Version: 1, SendID: "s", State: EmailStateSent, At: "2026-10-13T12:00:00Z"}
	if err := st.Validate(); err != nil {
		t.Fatal(err)
	}
	st.State = "lost"
	if st.Validate() == nil {
		t.Fatal("bad state accepted")
	}
	m := EmailMessage{Version: 1, From: EmailAddress{Address: "a@example.com"}, Auth: EmailAuth{Verdict: EmailUnverified}}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	m.From.Address = "nope"
	if m.Validate() == nil {
		t.Fatal("bad from accepted")
	}
}

func TestEmailConversationRoot(t *testing.T) {
	if got := EmailConversationRoot("<c>", "<b>", []string{"<a>", "<b>"}); got != "<a>" {
		t.Fatal(got)
	}
	if got := EmailConversationRoot("<c>", "<b>", nil); got != "<b>" {
		t.Fatal(got)
	}
	if got := EmailConversationRoot("<c>", "", nil); got != "<c>" {
		t.Fatal(got)
	}
}

func TestEmailBlobSealRoundTrip(t *testing.T) {
	pub, priv, err := GenerateX25519Keypair()
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := Seal(pub, []byte("attachment"), EmailBlobSealDomain, EmailBlobContext("b1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenSeal(priv, sealed, EmailBlobSealDomain, EmailBlobContext("b2")); err == nil {
		t.Fatal("blob opened under another id")
	}
	out, err := OpenSeal(priv, sealed, EmailBlobSealDomain, EmailBlobContext("b1"))
	if err != nil || string(out) != "attachment" {
		t.Fatalf("open: %v %q", err, out)
	}
	if _, err := Seal(pub, []byte("x"), EmailBlobSealDomain, nil); err == nil {
		t.Fatal("blob seal without context accepted")
	}
}
