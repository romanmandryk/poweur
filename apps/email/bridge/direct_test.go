package bridge

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-smtp"

	"github.com/poweur/identity"
)

// fakeTXT answers SPF, DMARC and DKIM key lookups for the tests.
type fakeTXT map[string][]string

func (f fakeTXT) lookup(_ context.Context, name string) ([]string, error) {
	if v, ok := f[strings.TrimSuffix(strings.ToLower(name), ".")]; ok {
		return v, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func startDirect(t *testing.T, b *testBridge, txt fakeTXT) string {
	t.Helper()
	b.Inbound.authenticator = Authenticator{LookupTXT: txt.lookup}
	srv, err := b.Inbound.NewSMTPServer()
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	return l.Addr().String()
}

func smtpSend(t *testing.T, addr, from, to, msg string) error {
	t.Helper()
	c, err := smtp.Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Hello("sender.example.com"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := c.Extension("AUTH"); ok {
		t.Fatal("the public MX offers AUTH")
	}
	if err := c.Mail(from, nil); err != nil {
		return err
	}
	if err := c.Rcpt(to, nil); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	_, _ = w.Write([]byte(crlf(strings.ReplaceAll(msg, "\r\n", "\n"))))
	return w.Close()
}

func TestDirectMXChecksAuthenticationItself(t *testing.T) {
	b := newTestBridge(t, func(c *Config) { c.Inbound = "smtp"; c.MXHostname = "mx.poweur.test"; c.AuthServID = "" })
	john := b.net.add("john.poweur.test", b.self.name)

	// The sender's own DKIM key, published like any domain would.
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(priv)
	keyFile := filepath.Join(t.TempDir(), "dkim.pem")
	_ = os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600)
	signer, err := LoadDKIMSigner("s1", keyFile, []DomainMap{{Mail: "example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	txt := fakeTXT{
		"example.com":               {"v=spf1 -all"}, // SPF fails: DKIM must carry DMARC
		"_dmarc.example.com":        {"v=DMARC1; p=none"},
		"s1._domainkey.example.com": {"v=DKIM1; k=ed25519; p=" + base64.StdEncoding.EncodeToString(pub)},
		"strict.test":               {"v=spf1 -all"},
		"_dmarc.strict.test":        {"v=DMARC1; p=reject"},
		"spfonly.test":              {"v=spf1 ip4:127.0.0.1 -all"},
		"_dmarc.spfonly.test":       {"v=DMARC1; p=quarantine"},
	}
	addr := startDirect(t, b, txt)

	body := "From: Ann <ann@example.com>\nTo: john@poweur.test\nSubject: signed\nMessage-ID: <s1@example.com>\nDate: Wed, 14 Oct 2026 09:00:00 +0000\n\nhello\n"
	signed, err := signer.Sign([]byte(crlf(body)), "example.com")
	if err != nil {
		t.Fatal(err)
	}
	// A forged Authentication-Results must not count.
	forged := "Authentication-Results: mx.poweur.test; dmarc=pass\r\n" + string(signed)
	if err := smtpSend(t, addr, "ann@example.com", "john@poweur.test", forged); err != nil {
		t.Fatalf("signed mail refused: %v", err)
	}
	// SPF alone, aligned.
	if err := smtpSend(t, addr, "bob@spfonly.test", "john@poweur.test", "From: bob@spfonly.test\nTo: john@poweur.test\nSubject: spf\n\nhi\n"); err != nil {
		t.Fatal(err)
	}
	// Unauthenticated, from a domain that asks for rejection.
	err = smtpSend(t, addr, "eve@strict.test", "john@poweur.test", "From: ceo@strict.test\nTo: john@poweur.test\nSubject: wire money\n\nnow\n")
	if se, ok := err.(*smtp.SMTPError); !ok || se.Code != 550 {
		t.Fatalf("DMARC reject not enforced: %v", err)
	}
	// Unknown recipients are refused at RCPT here too.
	if err := smtpSend(t, addr, "ann@example.com", "nobody@poweur.test", body); err == nil {
		t.Fatal("unknown recipient accepted")
	}

	got := b.net.delivered(john, identity.MsgTypeEmailMessage)
	if len(got) != 2 {
		t.Fatalf("delivered %d", len(got))
	}
	verdicts := map[string]identity.EmailAuth{}
	for _, raw := range got {
		var m identity.EmailMessage
		_ = json.Unmarshal(raw, &m)
		verdicts[m.Subject] = m.Auth
	}
	if a := verdicts["signed"]; a.DKIM != "pass" || a.SPF != "fail" || a.DMARC != "pass" || a.Verdict != identity.EmailVerified {
		t.Fatalf("signed: %+v", a)
	}
	if a := verdicts["spf"]; a.SPF != "pass" || a.DMARC != "pass" || a.Verdict != identity.EmailVerified {
		t.Fatalf("spf: %+v", a)
	}
}

func TestDirectMXLimitsConnectionsPerIP(t *testing.T) {
	b := newTestBridge(t, func(c *Config) { c.Inbound = "smtp"; c.MXHostname = "mx.poweur.test" })
	backend := &listenerBackend{in: b.Inbound, direct: true, active: map[string]int{}, recent: map[string][]time.Time{}}
	ip := net.ParseIP("192.0.2.7")
	var releases []func()
	for i := 0; i < maxSessionsPerIP; i++ {
		r, err := backend.admit(ip)
		if err != nil {
			t.Fatalf("session %d refused: %v", i, err)
		}
		releases = append(releases, r)
	}
	if _, err := backend.admit(ip); err == nil {
		t.Fatal("too many concurrent sessions admitted")
	}
	releases[0]()
	if _, err := backend.admit(ip); err != nil {
		t.Fatalf("a freed slot was not reused: %v", err)
	}
	// Another IP is unaffected.
	if _, err := backend.admit(net.ParseIP("192.0.2.8")); err != nil {
		t.Fatal(err)
	}
}
