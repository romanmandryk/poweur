package bridge

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"

	"github.com/poweur/identity"
)

func crlf(s string) string { return strings.ReplaceAll(s, "\n", "\r\n") }

func TestParseDomainsAndAliases(t *testing.T) {
	d, err := ParseDomains("Poweur.net, poweur.org=poweur.net")
	if err != nil || len(d) != 2 || d[1] != (DomainMap{Mail: "poweur.org", Identity: "poweur.net"}) {
		t.Fatalf("ParseDomains = %v, %v", d, err)
	}
	if _, err := ParseDomains("not a domain"); err == nil {
		t.Fatal("bad domain accepted")
	}
	a, err := ParseAliases("postmaster@poweur.net=support.poweur.net")
	if err != nil || a["postmaster@poweur.net"] != "support.poweur.net" {
		t.Fatalf("ParseAliases = %v, %v", a, err)
	}
	if _, err := ParseAliases("postmaster=support.poweur.net"); err == nil {
		t.Fatal("alias without @ accepted")
	}
}

func TestConfigValidate(t *testing.T) {
	var c Config
	err := c.Validate()
	for _, want := range []string{"EMAIL_BRIDGE_ID", "EMAIL_BRIDGE_SEED", "EMAIL_RELAY_URL", "EMAIL_DOMAINS", "EMAIL_INBOUND", "EMAIL_TRANSPORT"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("Validate() missing %s: %v", want, err)
		}
	}
	// Each inbound mode asks only for what it uses.
	behind := Config{Inbound: "lmtp"}
	err = behind.Validate()
	for _, want := range []string{"EMAIL_HOOK_SECRET", "EMAIL_LMTP_USERNAME", "EMAIL_LMTP_TLS_CERT_FILE"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("lmtp mode missing %s: %v", want, err)
		}
	}
	direct := Config{Inbound: "smtp"}
	err = direct.Validate()
	if err == nil || !strings.Contains(err.Error(), "EMAIL_MX_HOSTNAME") || strings.Contains(err.Error(), "EMAIL_HOOK_SECRET") {
		t.Fatalf("smtp mode: %v", err)
	}
}

// --- RCPT hook ---------------------------------------------------------

func postHook(t *testing.T, b *testBridge, auth, stage, to string) (int, hookResponse) {
	t.Helper()
	body := fmt.Sprintf(`{"context":{"stage":%q,"client":{"ip":"192.0.2.1"},"protocol":{"version":1}},"envelope":{"from":{"address":"sender@example.com"},"to":[{"address":"first@poweur.test"},{"address":%q}]}}`, stage, to)
	req, _ := http.NewRequest(http.MethodPost, b.public.URL+"/stalwart/hook", strings.NewReader(body))
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out hookResponse
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestHookDecidesAtRcpt(t *testing.T) {
	b := newTestBridge(t)
	b.net.add("john.poweur.test", b.self.name)
	b.net.add("elsewhere.poweur.test", "other-bridge.poweur.test")
	b.net.add("quiet.poweur.test", "")
	bearer := "Bearer hook-secret-0123456789"

	if code, _ := postHook(t, b, "", "rcpt", "john@poweur.test"); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated hook = %d", code)
	}
	if code, _ := postHook(t, b, "Bearer wrong-secret-000000", "rcpt", "john@poweur.test"); code != http.StatusUnauthorized {
		t.Fatalf("wrong secret = %d", code)
	}
	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte("stalwart:hook-secret-0123456789"))
	tests := []struct {
		name, auth, stage, to, action string
		status                        int
		enhanced                      string
	}{
		{"opted in", bearer, "rcpt", "john@poweur.test", "accept", 0, ""},
		{"basic auth works too", basic, "rcpt", "john@poweur.test", "accept", 0, ""},
		{"subaddress", bearer, "rcpt", "John+News@poweur.test", "accept", 0, ""},
		{"alias domain", bearer, "rcpt", "john@alias.test", "accept", 0, ""},
		{"no such identity", bearer, "rcpt", "nobody@poweur.test", "reject", 550, "5.1.1"},
		{"opted into another bridge", bearer, "rcpt", "elsewhere@poweur.test", "reject", 550, "5.1.1"},
		{"not opted in", bearer, "rcpt", "quiet@poweur.test", "reject", 550, "5.1.1"},
		{"dots are not folded", bearer, "rcpt", "jo.hn@poweur.test", "reject", 550, "5.1.1"},
		{"the bridge itself", bearer, "rcpt", "email@poweur.test", "reject", 550, "5.1.1"},
		{"not our domain", bearer, "rcpt", "john@example.com", "reject", 550, "5.7.1"},
		{"other stages pass", bearer, "mail", "anything@example.com", "accept", 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, out := postHook(t, b, tt.auth, tt.stage, tt.to)
			if code != 200 || out.Action != tt.action {
				t.Fatalf("hook = %d %+v", code, out)
			}
			if tt.status != 0 && (out.Response == nil || out.Response.Status != tt.status || out.Response.EnhancedStatus != tt.enhanced) {
				t.Fatalf("response = %+v", out.Response)
			}
		})
	}
}

// --- inbound LMTP ------------------------------------------------------

func startLMTP(t *testing.T, b *testBridge) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = b.lmtp.Serve(l) }()
	t.Cleanup(func() { _ = b.lmtp.Close() })
	return l.Addr().String()
}

type lmtpResult struct {
	rcptErrs map[string]error
	data     map[string]*smtp.DataResponse
	dataErr  error
}

func deliverLMTP(t *testing.T, addr, password string, rcpts []string, msg string) lmtpResult {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	c := smtp.NewClientLMTP(conn)
	defer c.Close()
	if err := c.Hello("mail.poweur.test"); err != nil {
		t.Fatal(err)
	}
	res := lmtpResult{rcptErrs: map[string]error{}}
	if err := c.Auth(sasl.NewPlainClient("", "stalwart", password)); err != nil {
		res.dataErr = err
		return res
	}
	if err := c.Mail("sender@example.com", nil); err != nil {
		t.Fatal(err)
	}
	accepted := 0
	for _, r := range rcpts {
		res.rcptErrs[r] = c.Rcpt(r, nil)
		if res.rcptErrs[r] == nil {
			accepted++
		}
	}
	if accepted == 0 {
		return res
	}
	w, err := c.Data()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte(crlf(msg)))
	res.data, res.dataErr = w.CloseWithLMTPResponse()
	return res
}

const sampleMail = `Authentication-Results: mail.poweur.test; dkim=pass header.d=example.com; spf=pass smtp.mailfrom=example.com; dmarc=pass header.from=example.com
Authentication-Results: forged.example; dmarc=fail
X-Spam-Status: No, score=-1.5
From: Ann Example <ann@example.com>
To: John <john@poweur.test>
Subject: =?utf-8?q?Q3_budget_=E2=9C=93?=
Date: Tue, 13 Oct 2026 10:00:00 +0000
Message-ID: <first@example.com>
MIME-Version: 1.0
Content-Type: multipart/mixed; boundary="mix"

--mix
Content-Type: multipart/alternative; boundary="alt"

--alt
Content-Type: text/plain; charset=utf-8

The numbers are attached.
--alt
Content-Type: text/html; charset=utf-8

<html><body><p onclick="steal()">The <b>numbers</b></p><script>alert(1)</script><img src="cid:logo@x"><img src="https://tracker.example/p.gif"><a href="javascript:alert(1)">x</a></body></html>
--alt--
--mix
Content-Type: image/png
Content-ID: <logo@x>
Content-Disposition: inline; filename="logo.png"
Content-Transfer-Encoding: base64

iVBORw0KGgo=
--mix
Content-Type: text/csv; name="q3.csv"
Content-Disposition: attachment; filename="../../q3.csv"

a,b
1,2
--mix--
`

func TestLMTPDeliversEncryptedPerRecipient(t *testing.T) {
	b := newTestBridge(t)
	john := b.net.add("john.poweur.test", b.self.name)
	addr := startLMTP(t, b)

	if res := deliverLMTP(t, addr, "wrong-password-000000000", []string{"john@poweur.test"}, sampleMail); res.dataErr == nil {
		t.Fatal("LMTP accepted a wrong password")
	}
	res := deliverLMTP(t, addr, "lmtp-password-0123456789", []string{"john+news@poweur.test", "nobody@poweur.test"}, sampleMail)
	if res.rcptErrs["john+news@poweur.test"] != nil {
		t.Fatalf("rcpt john: %v", res.rcptErrs["john+news@poweur.test"])
	}
	if se, ok := res.rcptErrs["nobody@poweur.test"].(*smtp.SMTPError); !ok || se.Code != 550 {
		t.Fatalf("rcpt nobody: %v", res.rcptErrs["nobody@poweur.test"])
	}
	if res.dataErr != nil {
		t.Fatalf("data: %v", res.dataErr)
	}
	got := b.net.delivered(john, identity.MsgTypeEmailMessage)
	if len(got) != 1 {
		t.Fatalf("delivered %d messages", len(got))
	}
	var m identity.EmailMessage
	if err := json.Unmarshal(got[0], &m); err != nil {
		t.Fatal(err)
	}
	if m.Subject != "Q3 budget ✓" || m.From.Address != "ann@example.com" || m.From.Name != "Ann Example" || m.Tag != "news" || m.MessageID != "first@example.com" {
		t.Fatalf("headers: %+v", m)
	}
	if m.Auth.Verdict != identity.EmailVerified || m.Auth.DKIM != "pass" || m.Auth.Spam != "ham" || m.Auth.SpamScore != -1.5 {
		t.Fatalf("auth: %+v", m.Auth)
	}
	if !strings.Contains(m.Text, "numbers are attached") {
		t.Fatalf("text: %q", m.Text)
	}
	for _, bad := range []string{"<script", "onclick", "javascript:"} {
		if strings.Contains(m.HTML, bad) {
			t.Fatalf("html kept %s: %s", bad, m.HTML)
		}
	}
	if !strings.Contains(m.HTML, `src="cid:logo@x"`) || !strings.Contains(m.HTML, "https://tracker.example/p.gif") {
		t.Fatalf("html lost its images: %s", m.HTML)
	}
	if len(m.Attachments) != 2 {
		t.Fatalf("attachments: %+v", m.Attachments)
	}
	for _, a := range m.Attachments {
		switch a.Name {
		case "logo.png":
			if !a.Inline || a.ContentID != "logo@x" || a.Data == "" {
				t.Fatalf("inline image: %+v", a)
			}
		case "q3.csv":
			data, _ := base64.StdEncoding.DecodeString(a.Data)
			if !strings.Contains(string(data), "1,2") {
				t.Fatalf("csv: %+v", a)
			}
		default:
			t.Fatalf("unexpected attachment name %q (paths must be stripped)", a.Name)
		}
	}
	if m.Raw == nil || m.Raw.URL == "" {
		t.Fatal("original not kept as a blob")
	}
	// The same message again (a mail server retry) is not delivered twice.
	deliverLMTP(t, addr, "lmtp-password-0123456789", []string{"john@poweur.test"}, sampleMail)
	if n := len(b.net.delivered(john, identity.MsgTypeEmailMessage)); n != 1 {
		t.Fatalf("retry delivered again: %d", n)
	}
}

func TestLMTPRelayFailures(t *testing.T) {
	b := newTestBridge(t)
	b.net.add("john.poweur.test", b.self.name)
	b.net.add("jane.poweur.test", b.self.name)
	addr := startLMTP(t, b)
	b.net.refuse = func(env Envelope) int {
		if env.Recipient == "john.poweur.test" {
			return http.StatusServiceUnavailable
		}
		return http.StatusForbidden
	}
	res := deliverLMTP(t, addr, "lmtp-password-0123456789", []string{"john@poweur.test", "jane@poweur.test"}, sampleMail)
	var lerr smtp.LMTPDataError
	if res.dataErr == nil {
		t.Fatal("no per-recipient failure reported")
	}
	if e, ok := res.dataErr.(smtp.LMTPDataError); ok {
		lerr = e
	}
	john, jane := lerr["john@poweur.test"], lerr["jane@poweur.test"]
	if john == nil || john.Code != 451 {
		t.Fatalf("relay outage should be temporary: %+v", john)
	}
	if jane == nil || jane.Code != 550 {
		t.Fatalf("relay refusal should be permanent: %+v", jane)
	}
}

func TestLargeAttachmentBecomesBlob(t *testing.T) {
	b := newTestBridge(t)
	john := b.net.add("john.poweur.test", b.self.name)
	addr := startLMTP(t, b)
	big := make([]byte, 600*1024)
	_, _ = rand.Read(big)
	enc := base64.StdEncoding.EncodeToString(big)
	var lines []string
	for len(enc) > 76 {
		lines, enc = append(lines, enc[:76]), enc[76:]
	}
	lines = append(lines, enc)
	msg := "From: a@example.com\nTo: john@poweur.test\nSubject: big\nMIME-Version: 1.0\nContent-Type: multipart/mixed; boundary=b\n\n--b\nContent-Type: text/plain\n\nsee attached\n--b\nContent-Type: application/octet-stream\nContent-Disposition: attachment; filename=big.bin\nContent-Transfer-Encoding: base64\n\n" + strings.Join(lines, "\n") + "\n--b--\n"
	if res := deliverLMTP(t, addr, "lmtp-password-0123456789", []string{"john@poweur.test"}, msg); res.dataErr != nil {
		t.Fatal(res.dataErr)
	}
	got := b.net.delivered(john, identity.MsgTypeEmailMessage)
	if len(got) != 1 || len(got[0]) > envelopeBudget {
		t.Fatalf("payload %d bytes", len(got[0]))
	}
	var m identity.EmailMessage
	_ = json.Unmarshal(got[0], &m)
	if len(m.Attachments) != 1 || m.Attachments[0].Blob == nil || m.Attachments[0].Data != "" {
		t.Fatalf("attachment: %+v", m.Attachments)
	}
	ref := m.Attachments[0].Blob
	resp, err := http.Get(ref.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var sealed identity.SealedPayload
	_ = json.NewDecoder(resp.Body).Decode(&sealed)
	plain, err := identity.OpenSeal(john.encPriv, sealed, identity.EmailBlobSealDomain, identity.EmailBlobContext(ref.ID))
	if err != nil || !bytes.Equal(plain, big) {
		t.Fatalf("blob does not open to the attachment: %v", err)
	}
	sum := sha256.Sum256(big)
	if ref.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatal("blob hash wrong")
	}
	// Unknown and malformed ids are not served.
	for _, path := range []string{"/blobs/AAAAAAAAAAAAAAAAAAAAAAAAAA", "/blobs/../email.db"} {
		r, _ := http.Get(b.public.URL + path)
		if r.StatusCode == 200 {
			t.Fatalf("%s served", path)
		}
		r.Body.Close()
	}
}

// --- outbound ----------------------------------------------------------

func readSent(t *testing.T, dir string) []string {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	var out []string
	for _, e := range entries {
		raw, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		out = append(out, string(raw))
	}
	return out
}

func statuses(t *testing.T, n *fakeNet, to *fakeID) []identity.EmailStatus {
	var out []identity.EmailStatus
	for _, raw := range n.delivered(to, identity.MsgTypeEmailStatus) {
		var st identity.EmailStatus
		_ = json.Unmarshal(raw, &st)
		out = append(out, st)
	}
	return out
}

func runOutbound(t *testing.T, b *testBridge) {
	t.Helper()
	ctx := context.Background()
	if err := b.Outbound.Pickup(ctx); err != nil {
		t.Fatal(err)
	}
	b.Outbound.ProcessDue(ctx)
}

func TestOutboundSendsAndReportsStatus(t *testing.T) {
	b := newTestBridge(t)
	john := b.net.add("john.poweur.test", b.self.name)
	send := identity.EmailSend{Version: 1, SendID: "S1", FromName: "John", To: []identity.EmailAddress{{Name: "Ann", Address: "ann@example.com"}},
		Subject: "Hello", Text: "Hi Ann", HTML: `<p>Hi <script>x()</script>Ann</p>`, InReplyTo: "first@example.com", References: []string{"first@example.com"}}
	b.net.sendToBridge(john, b.self, identity.MsgTypeEmailSend, send)
	runOutbound(t, b)

	sent := readSent(t, b.logDir)
	if len(sent) != 1 {
		t.Fatalf("sent %d", len(sent))
	}
	eml := sent[0]
	for _, want := range []string{"X-Envelope-From: john@poweur.test", "From: \"John\" <john@poweur.test>", "To: \"Ann\" <ann@example.com>", "Subject: Hello",
		"In-Reply-To: <first@example.com>", "References: <first@example.com>", "Sent with Poweur ID john.poweur.test", "Message-Id: <"} {
		if !strings.Contains(eml, want) {
			t.Fatalf("message lacks %q:\n%s", want, eml)
		}
	}
	if strings.Contains(eml, "<script") {
		t.Fatal("outbound HTML not sanitized")
	}
	st := statuses(t, b.net, john)
	if len(st) != 1 || st[0].State != identity.EmailStateSent || st[0].SendID != "S1" || st[0].MessageID == "" {
		t.Fatalf("status: %+v", st)
	}

	// The same send again (a client retry) is answered, not resent.
	b.net.sendToBridge(john, b.self, identity.MsgTypeEmailSend, send)
	runOutbound(t, b)
	if n := len(readSent(t, b.logDir)); n != 1 {
		t.Fatalf("duplicate send_id sent again (%d)", n)
	}
	if st := statuses(t, b.net, john); len(st) != 2 || st[1].State != identity.EmailStateSent {
		t.Fatalf("repeat status: %+v", st)
	}

	// A second message to the same person carries no footer.
	send.SendID = "S2"
	b.net.sendToBridge(john, b.self, identity.MsgTypeEmailSend, send)
	runOutbound(t, b)
	all := readSent(t, b.logDir)
	if len(all) != 2 {
		t.Fatalf("second send: %d", len(all))
	}
	second := all[0]
	if strings.Contains(all[0], "S1") || all[0] == eml {
		second = all[1]
	}
	if strings.Contains(second, "Sent with Poweur ID") {
		t.Fatal("footer repeated for a known recipient")
	}
}

func TestOutboundRefusals(t *testing.T) {
	b := newTestBridge(t, func(c *Config) { c.OutDailyLimit = 1; c.Footer = "off" })
	john := b.net.add("john.poweur.test", b.self.name)
	stranger := b.net.add("stranger.poweur.test", "")
	mk := func(id, to string) identity.EmailSend {
		return identity.EmailSend{Version: 1, SendID: id, To: []identity.EmailAddress{{Address: to}}, Subject: "s", Text: "t"}
	}
	b.net.sendToBridge(stranger, b.self, identity.MsgTypeEmailSend, mk("X1", "a@example.com"))
	b.net.sendToBridge(john, b.self, identity.MsgTypeEmailSend, mk("J1", "a@example.com"))
	b.net.sendToBridge(john, b.self, identity.MsgTypeEmailSend, mk("J2", "b@example.com"))
	_ = b.Store.Suppress(context.Background(), "bounced@example.com", "bounced 5.1.1")
	runOutbound(t, b)

	if st := statuses(t, b.net, stranger); len(st) != 1 || st[0].State != identity.EmailStateRefused || !strings.Contains(st[0].Reason, "not enabled") {
		t.Fatalf("stranger: %+v", st)
	}
	st := statuses(t, b.net, john)
	// Over the daily limit a send waits for the window, it is not refused.
	if len(st) != 2 || st[0].State != identity.EmailStateSent || st[1].State != identity.EmailStateDeferred || !strings.Contains(st[1].Reason, "one email a day") {
		t.Fatalf("john: %+v", st)
	}
	if n := len(readSent(t, b.logDir)); n != 1 {
		t.Fatalf("sent %d", n)
	}

	b2 := newTestBridge(t, func(c *Config) { c.Footer = "off" })
	jane := b2.net.add("jane.poweur.test", b2.self.name)
	_ = b2.Store.Suppress(context.Background(), "bounced@example.com", "bounced 5.1.1")
	b2.net.sendToBridge(jane, b2.self, identity.MsgTypeEmailSend, mk("K1", "Bounced@example.com"))
	b2.net.sendToBridge(jane, b2.self, identity.MsgTypeEmailSend, map[string]any{"version": 1, "send_id": "K2", "to": []any{}})
	runOutbound(t, b2)
	st = statuses(t, b2.net, jane)
	if len(st) != 2 || !strings.Contains(st[0].Reason, "cannot receive") || st[1].State != identity.EmailStateRefused || !strings.Contains(st[1].Reason, "no recipients") {
		t.Fatalf("jane: %+v", st)
	}
}

func TestOutboundCountsSilentDrops(t *testing.T) {
	b := newTestBridge(t, func(c *Config) { c.Footer = "off" })
	john := b.net.add("john.poweur.test", b.self.name)
	stranger := b.net.add("stranger.poweur.test", "")
	// Sealed to someone else's key: the bridge cannot open it.
	b.net.sendToBridge(john, stranger, identity.MsgTypeEmailSend, map[string]any{"version": 1, "send_id": "A1"})
	b.net.mu.Lock()
	b.net.inbox[len(b.net.inbox)-1].Recipient = b.self.name
	b.net.mu.Unlock()
	// Opens, but is not an email.send document.
	b.net.sendToBridge(john, b.self, identity.MsgTypeEmailSend, "not a send")
	// Invalid, with no send id to answer.
	b.net.sendToBridge(john, b.self, identity.MsgTypeEmailSend, map[string]any{"version": 1, "to": []any{}})
	runOutbound(t, b)

	if st := statuses(t, b.net, john); len(st) != 0 {
		t.Fatalf("statuses for unanswerable sends: %+v", st)
	}
	rec := httptest.NewRecorder()
	b.Metrics.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, want := range []string{
		`email_outbound_dropped_total{reason="undecryptable"} 1`,
		`email_outbound_dropped_total{reason="malformed"} 1`,
		`email_outbound_dropped_total{reason="invalid_send_id"} 1`,
	} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("metrics missing %s:\n%s", want, rec.Body.String())
		}
	}
}

func TestOutboundAllowlistPausesOthers(t *testing.T) {
	b := newTestBridge(t, func(c *Config) {
		c.Footer = "off"
		c.SendMode = "allowlist"
		c.SendAllow = map[string]bool{"john.poweur.test": true}
	})
	john := b.net.add("john.poweur.test", b.self.name)
	jane := b.net.add("jane.poweur.test", b.self.name)
	mk := func(id string) identity.EmailSend {
		return identity.EmailSend{Version: 1, SendID: id, To: []identity.EmailAddress{{Address: "a@example.com"}}, Subject: "s", Text: "t"}
	}
	b.net.sendToBridge(john, b.self, identity.MsgTypeEmailSend, mk("J1"))
	b.net.sendToBridge(jane, b.self, identity.MsgTypeEmailSend, mk("N1"))
	runOutbound(t, b)

	if st := statuses(t, b.net, john); len(st) != 1 || st[0].State != identity.EmailStateSent {
		t.Fatalf("john (allowed): %+v", st)
	}
	if st := statuses(t, b.net, jane); len(st) != 1 || st[0].State != identity.EmailStateRefused || st[0].Reason != SendPausedReason {
		t.Fatalf("jane (paused): %+v", st)
	}
	if n := len(readSent(t, b.logDir)); n != 1 {
		t.Fatalf("sent %d", n)
	}

	// Clients learn that sending is paused, never who may send.
	if err := b.Publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw := string(b.net.sys["/identities/"+b.self.name+"/system/.poweur/public/capabilities.json"])
	if !strings.Contains(raw, `"email_send": "paused"`) || strings.Contains(raw, "john") {
		t.Fatalf("capabilities = %s", raw)
	}
}

func TestParseSendAllow(t *testing.T) {
	got, err := ParseSendAllow(" Support.poweur.net, john.poweur.net ,")
	if err != nil || len(got) != 2 || !got["support.poweur.net"] || !got["john.poweur.net"] {
		t.Fatalf("got %v, %v", got, err)
	}
	if _, err := ParseSendAllow("not an id"); err == nil {
		t.Fatal("invalid ID accepted")
	}
	if !(Config{}).SendAllowed("anyone.poweur.net") {
		t.Fatal("open mode refused a sender")
	}
}

func TestOutboundTemporaryFailureRetries(t *testing.T) {
	failures := 1
	b := newTestBridge(t, func(c *Config) { c.Footer = "off" })
	lt := b.Outbound.transport.(*LogTransport)
	lt.Fail = func([]string) error {
		if failures > 0 {
			failures--
			return &SubmitError{Code: 451, Message: "try later"}
		}
		return nil
	}
	john := b.net.add("john.poweur.test", b.self.name)
	b.net.sendToBridge(john, b.self, identity.MsgTypeEmailSend, identity.EmailSend{Version: 1, SendID: "R1", To: []identity.EmailAddress{{Address: "a@example.com"}}, Subject: "s", Text: "t"})
	runOutbound(t, b)
	if st := statuses(t, b.net, john); len(st) != 1 || st[0].State != identity.EmailStateDeferred {
		t.Fatalf("first attempt: %+v", st)
	}
	// Make the retry due now.
	b.Store.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	b.Outbound.ProcessDue(context.Background())
	if st := statuses(t, b.net, john); len(st) != 2 || st[1].State != identity.EmailStateSent {
		t.Fatalf("retry: %+v", st)
	}
	if n := len(readSent(t, b.logDir)); n != 1 {
		t.Fatalf("sent %d", n)
	}
}

func signedUpload(t *testing.T, b *testBridge, who *fakeID, id string, data []byte) *http.Response {
	t.Helper()
	sealed, _ := identity.Seal(b.self.encPub, data, identity.EmailUploadSealDomain, identity.EmailBlobContext(id))
	body, _ := json.Marshal(sealed)
	sum := sha256.Sum256(body)
	issued := time.Now().UTC().Format(time.RFC3339)
	nonce := "nonce-" + id + "-0000000000"
	canonical := identity.CanonicalEmailUpload(who.name, id, hex.EncodeToString(sum[:]), int64(len(body)), issued, nonce)
	req, _ := http.NewRequest(http.MethodPut, b.public.URL+"/v1/uploads/"+id, bytes.NewReader(body))
	req.Header.Set("X-Poweur-Identity", who.name)
	req.Header.Set("X-Poweur-Timestamp", issued)
	req.Header.Set("X-Poweur-Nonce", nonce)
	req.Header.Set("X-Poweur-Signature", base64.RawURLEncoding.EncodeToString(ed25519.Sign(who.priv, []byte(canonical))))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestUploadedAttachmentIsSent(t *testing.T) {
	b := newTestBridge(t, func(c *Config) { c.Footer = "off" })
	john := b.net.add("john.poweur.test", b.self.name)
	mallory := b.net.add("mallory.poweur.test", "")
	data := []byte("%PDF-1.4 a large report")
	const id = "01J9ZUPLOAD000000000000000"
	if resp := signedUpload(t, b, mallory, "01J9ZUPLOAD000000000000001", data); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("upload from someone not opted in = %d", resp.StatusCode)
	}
	if resp := signedUpload(t, b, john, id, data); resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("upload = %d %s", resp.StatusCode, body)
	}
	if resp := signedUpload(t, b, john, id, data); resp.StatusCode == http.StatusCreated {
		t.Fatal("upload id reused")
	}
	sum := sha256.Sum256(data)
	b.net.sendToBridge(john, b.self, identity.MsgTypeEmailSend, identity.EmailSend{Version: 1, SendID: "U1",
		To: []identity.EmailAddress{{Address: "a@example.com"}}, Subject: "report", Text: "attached",
		Attachments: []identity.EmailOutAttachment{{Name: "report.pdf", MIME: "application/pdf", Size: int64(len(data)), Upload: id, SHA256: hex.EncodeToString(sum[:])}}})
	runOutbound(t, b)
	sent := readSent(t, b.logDir)
	if len(sent) != 1 || !strings.Contains(sent[0], `filename=report.pdf`) || !strings.Contains(sent[0], base64.StdEncoding.EncodeToString(data)[:20]) {
		t.Fatalf("attachment not in message: %v", sent)
	}
	// Someone else's send cannot use it.
	if _, err := b.Blobs.TakeUpload(context.Background(), "mallory.poweur.test", id, nil); err == nil {
		t.Fatal("another identity took the upload")
	}
}

func TestBounceReportsAndSuppresses(t *testing.T) {
	b := newTestBridge(t, func(c *Config) { c.Footer = "off" })
	john := b.net.add("john.poweur.test", b.self.name)
	b.net.sendToBridge(john, b.self, identity.MsgTypeEmailSend, identity.EmailSend{Version: 1, SendID: "B1", To: []identity.EmailAddress{{Address: "gone@example.com"}}, Subject: "s", Text: "t"})
	runOutbound(t, b)
	st := statuses(t, b.net, john)
	if len(st) != 1 || st[0].MessageID == "" {
		t.Fatalf("send: %+v", st)
	}
	dsn := `From: Mail Delivery System <mailer-daemon@mail.poweur.test>
To: john@poweur.test
Subject: Undelivered Mail Returned to Sender
MIME-Version: 1.0
Content-Type: multipart/report; report-type=delivery-status; boundary="r"

--r
Content-Type: text/plain

Your message could not be delivered.
--r
Content-Type: message/delivery-status

Reporting-MTA: dns; mail.poweur.test

Final-Recipient: rfc822; gone@example.com
Action: failed
Status: 5.1.1
Diagnostic-Code: smtp; 550 5.1.1 user unknown

--r
Content-Type: text/rfc822-headers

From: john@poweur.test
Message-Id: <` + st[0].MessageID + `>
Subject: s

--r--
`
	addr := startLMTP(t, b)
	if res := deliverLMTP(t, addr, "lmtp-password-0123456789", []string{"john@poweur.test"}, dsn); res.dataErr != nil {
		t.Fatal(res.dataErr)
	}
	st = statuses(t, b.net, john)
	if len(st) != 2 || st[1].State != identity.EmailStateBounced || st[1].Recipient != "gone@example.com" || !strings.Contains(st[1].Reason, "user unknown") {
		t.Fatalf("bounce status: %+v", st)
	}
	if _, suppressed, _ := b.Store.Suppressed(context.Background(), "gone@example.com"); !suppressed {
		t.Fatal("hard bounce not suppressed")
	}
	var m identity.EmailMessage
	got := b.net.delivered(john, identity.MsgTypeEmailMessage)
	_ = json.Unmarshal(got[len(got)-1], &m)
	if m.Bounce == nil || m.Bounce.SendID != "B1" {
		t.Fatalf("report not linked to the send: %+v", m.Bounce)
	}
}

func TestPublishCapabilities(t *testing.T) {
	b := newTestBridge(t)
	if err := b.Publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw := b.net.sys["/identities/"+b.self.name+"/system/.poweur/public/capabilities.json"]
	var caps struct {
		Endpoints map[string]string `json:"endpoints"`
	}
	_ = json.Unmarshal(raw, &caps)
	if caps.Endpoints["email_api"] != b.public.URL {
		t.Fatalf("capabilities = %s", raw)
	}
}
