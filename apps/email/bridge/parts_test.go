package bridge

import (
	"strings"
	"testing"
	"time"

	"github.com/poweur/identity"
)

func TestSanitizeHTMLCorpus(t *testing.T) {
	tests := []struct {
		name, in string
		banned   []string
		kept     []string
	}{
		{"script", `<p>hi</p><script>alert(1)</script>`, []string{"<script", "alert"}, []string{"<p>hi</p>"}},
		{"event handler", `<a href="https://x.example" onmouseover="steal()">x</a>`, []string{"onmouseover"}, []string{`href="https://x.example"`, `target="_blank"`, "noreferrer"}},
		{"javascript url", `<a href="javascript:alert(1)">x</a><a href="JaVaScRiPt:alert(1)">y</a>`, []string{"javascript", "JaVaScRiPt"}, nil},
		{"iframe object embed form", `<iframe src="https://x"></iframe><object data="x"></object><embed src="x"><form action="https://x"><input name="p"></form>`, []string{"<iframe", "<object", "<embed", "<form", "<input"}, nil},
		{"base and meta", `<base href="https://evil.example/"><meta http-equiv="refresh" content="0;url=https://evil.example">`, []string{"<base", "<meta", "refresh"}, nil},
		{"style block", `<style>body{background:url(https://t.example/p)}</style><p>x</p>`, []string{"<style", "url("}, []string{"<p>x</p>"}},
		{"css url in style attr", `<p style="color:red;background-image:url(https://t.example/p)">x</p>`, []string{"url(", "background-image"}, []string{"color: red"}},
		{"svg", `<svg onload="alert(1)"><script>alert(2)</script></svg>`, []string{"<svg", "onload", "alert"}, nil},
		{"data html", `<img src="data:text/html;base64,PHNjcmlwdD4=">`, []string{"data:text/html"}, nil},
		{"data image kept", `<img src="data:image/png;base64,iVBORw0KGgo=">`, nil, []string{"data:image/png;base64,iVBORw0KGgo="}},
		{"cid and remote images kept", `<img src="cid:logo@x"><img src="https://img.example/a.png">`, nil, []string{"cid:logo@x", "https://img.example/a.png"}},
		// Remote images stay (the client blocks them until asked); only the
		// schemes matter here.
		{"remote http image kept for the client to block", `<img src="http://img.example/a.png">`, nil, []string{"http://img.example/a.png"}},
		{"ftp image dropped", `<img src="ftp://img.example/a.png">`, []string{"ftp://"}, nil},
		{"tables survive", `<table width="600" cellpadding="0"><tr><td align="center" bgcolor="#fff">x</td></tr></table>`, nil, []string{"<table", `width="600"`, `align="center"`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := SanitizeHTML(tt.in)
			for _, b := range tt.banned {
				if strings.Contains(out, b) {
					t.Fatalf("kept %q: %s", b, out)
				}
			}
			for _, k := range tt.kept {
				if !strings.Contains(out, k) {
					t.Fatalf("lost %q: %s", k, out)
				}
			}
		})
	}
}

func TestAuthResultsTrustOnlyOurServer(t *testing.T) {
	values := []string{
		"evil.example; dmarc=pass",
		"mail.poweur.test; spf=softfail smtp.mailfrom=x; dkim=fail; dkim=pass header.d=x; dmarc=fail header.from=x",
	}
	a := authResults(values, "mail.poweur.test")
	if a.SPF != "softfail" || a.DKIM != "pass" || a.DMARC != "fail" || a.Verdict != identity.EmailFailed {
		t.Fatalf("auth = %+v", a)
	}
	if a := authResults(values, ""); a.Verdict != identity.EmailUnverified || a.DMARC != "" {
		t.Fatalf("no trusted server must mean unverified: %+v", a)
	}
	if a := authResults([]string{"evil.example; dmarc=pass"}, "mail.poweur.test"); a.Verdict != identity.EmailUnverified {
		t.Fatalf("forged results trusted: %+v", a)
	}
	if v, s := spamVerdict("Yes, score=12.5 required=5", ""); v != "spam" || s != 12.5 {
		t.Fatalf("spam = %s %v", v, s)
	}
}

func TestParseCaps(t *testing.T) {
	var b strings.Builder
	b.WriteString("From: a@example.com\r\nSubject: parts\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=b\r\n\r\n")
	for i := 0; i < maxParts+5; i++ {
		b.WriteString("--b\r\nContent-Type: text/plain\r\n\r\nx\r\n")
	}
	b.WriteString("--b--\r\n")
	if _, err := ParseMessage([]byte(b.String()), ""); err != errTooManyParts {
		t.Fatalf("part bomb: %v", err)
	}
	// Header values are single lines however they were folded or encoded.
	p, err := ParseMessage([]byte("From: a@example.com\r\nSubject: =?utf-8?q?two=0Alines?=\r\n\r\nbody"), "")
	if err != nil || strings.ContainsAny(p.Email.Subject, "\r\n") {
		t.Fatalf("subject %q, %v", p.Email.Subject, err)
	}
}

func TestBuildMIMEAndFooter(t *testing.T) {
	raw, err := BuildMIME(Outgoing{
		From: identity.EmailAddress{Name: "Jöhn", Address: "john@poweur.net"}, To: []identity.EmailAddress{{Address: "a@example.com"}},
		Subject: "Grüße", Text: "Thanks!\n\nOn Mon, Ann wrote:\n> hello", MessageID: "abc@poweur.net", Date: time.Unix(0, 0),
		FooterText: "Sent with Poweur ID john.poweur.net",
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, "Subject: =?utf-8?") || !strings.Contains(s, "From: =?utf-8?") {
		t.Fatalf("non-ASCII headers not encoded:\n%s", s)
	}
	parsed, err := ParseMessage(raw, "")
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Email.Subject != "Grüße" || parsed.Email.From.Name != "Jöhn" || parsed.Email.MessageID != "abc@poweur.net" {
		t.Fatalf("round trip: %+v", parsed.Email)
	}
	footer := strings.Index(parsed.Email.Text, "Sent with Poweur ID")
	quote := strings.Index(parsed.Email.Text, "On Mon, Ann wrote:")
	if footer < 0 || quote < 0 || footer > quote {
		t.Fatalf("footer not above the quote:\n%s", parsed.Email.Text)
	}
	if got := insertHTMLFooter("<p>x</p><blockquote>q</blockquote>", "F"); strings.Index(got, "F") > strings.Index(got, "<blockquote") {
		t.Fatalf("html footer below quote: %s", got)
	}
}

func TestStoreCountersAndClaims(t *testing.T) {
	s, err := OpenStore(t.TempDir()+"/t.db", []byte("key"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := t.Context()
	for i := 1; i <= 3; i++ {
		if n, _ := s.Count(ctx, "out:a", 1); n != i {
			t.Fatalf("count = %d", n)
		}
	}
	tomorrow := time.Now().Add(25 * time.Hour)
	s.now = func() time.Time { return tomorrow }
	if n, _ := s.Peek(ctx, "out:a"); n != 0 {
		t.Fatalf("counter did not reset at midnight: %d", n)
	}
	r, fresh, _ := s.ClaimSend(ctx, "A.example.org", "S")
	if !fresh || r.State != "accepted" {
		t.Fatal("first claim")
	}
	_ = s.UpdateSend(ctx, "a.example.org", "S", "sent", "mid@x")
	r, fresh, _ = s.ClaimSend(ctx, "a.example.org", "S")
	if fresh || r.State != "sent" || r.MessageID != "mid@x" {
		t.Fatalf("second claim: %+v %v", r, fresh)
	}
	if got, ok, _ := s.SendByMessageID(ctx, "mid@x"); !ok || got.SendID != "S" {
		t.Fatal("send by message id")
	}
	// Addresses are not stored in the clear.
	_ = s.Suppress(ctx, "Gone@Example.com", "bounced")
	if _, ok, _ := s.Suppressed(ctx, "gone@example.com"); !ok {
		t.Fatal("suppression is case-sensitive")
	}
	var k string
	_ = s.db.QueryRow(`SELECT k FROM suppressed`).Scan(&k)
	if strings.Contains(k, "example") {
		t.Fatal("address stored in the clear")
	}
	if first, _ := s.FooterSent(ctx, "a", "b"); first {
		t.Fatal("footer reported sent before it was")
	}
	if again, _ := s.FooterSent(ctx, "a", "b"); !again {
		t.Fatal("footer ledger forgot")
	}
}
