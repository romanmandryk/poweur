package integration_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"

	clipkg "github.com/poweur/cli/pkg/cli"
	emailbridge "github.com/poweur/email/bridge"
	"github.com/poweur/integration/fakedns"

	relaypkg "github.com/poweur/api/pkg/relay"
	idpkg "github.com/poweur/identity"
)

// emailFixture is a hosted relay that knows its email bridge, the bridge
// itself (real process, real relay protocol), and an LMTP client standing in
// for the mail server.
type emailFixture struct {
	relayURL string
	relayDir string
	lmtpAddr string
	sentDir  string
	bridge   *emailbridge.Server
	cancel   context.CancelFunc
}

const lmtpPassword = "integration-lmtp-password-0123"

func newEmailFixture(t *testing.T) emailFixture {
	t.Helper()
	zone := newZone(t)
	ts := httptest.NewUnstartedServer(nil)
	addr := ts.Listener.Addr().String()
	relayDir := t.TempDir()
	cfg := relaypkg.Config{
		ListenAddr: addr, RelayAddress: addr, RelayScheme: "http",
		DNSTTL: time.Minute, ChallengeTTL: time.Minute, Version: "integration-test",
		DataDir: relayDir, HostedDomains: []string{"poweur.net"}, ResolverAllowPrivate: true,
		OperatorToken: integrationOperatorToken, EmailBridgeID: "email.poweur.net",
		RateLimits:        relaypkg.RateLimits{PerMinute: 1000, PerHour: 10000, PerDay: 100000},
		EmailBridgeLimits: relaypkg.RateLimits{PerMinute: 1000, PerHour: 10000, PerDay: 100000},
	}
	providers := relaypkg.NewProviderFactory(cfg)
	relaypkg.RegisterProvider(providers, "mock", zone.Provider())
	server := relaypkg.NewServer(cfg, zone, providers)
	ts.Config.Handler = relaypkg.Router(server)
	ts.Start()
	t.Cleanup(ts.Close)
	relayURL := "http://" + addr
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })
	for _, name := range []string{"email.poweur.net", "john.poweur.net", "jane.poweur.net"} {
		zone.SetHost(name, addr)
	}

	// The bridge's own identity: a reserved name, so the operator creates it.
	seed, err := idpkg.NewSeed()
	if err != nil {
		t.Fatal(err)
	}
	runCLI(t, t.TempDir(), "identity", "create", "email.poweur.net", "--hosted",
		"--seed", idpkg.EncodeSeed(seed), "--operator-token", integrationOperatorToken, "--relay", relayURL, "--json")

	httpL, _ := net.Listen("tcp", "127.0.0.1:0")
	lmtpL, _ := net.Listen("tcp", "127.0.0.1:0")
	sentDir := t.TempDir()
	bcfg := emailbridge.Config{
		PublicURL: "http://" + httpL.Addr().String(),
		BridgeID:  "email.poweur.net", Seed: seed, RelayURL: relayURL,
		Domains:    []emailbridge.DomainMap{{Mail: "poweur.net", Identity: "poweur.net"}},
		HookSecret: "integration-hook-secret-01", LMTPUsername: "stalwart", LMTPPassword: lmtpPassword,
		LMTPInsecure: true, AuthServID: "mail.poweur.net",
		Transport: "log", LogDir: sentDir, DataDir: t.TempDir(),
		Footer: "first", SignupURL: "https://poweur.net", OptInCacheTTL: time.Millisecond,
		OutDailyLimit: 100, OutNewRecipients: 20,
		Resolve: idpkg.ResolveOptions{Scheme: "http", AllowPrivate: true, TXT: zone, HTTPClient: routingClient(map[string]string{"poweur.net": addr})},
	}
	b, err := emailbridge.New(bcfg, emailbridge.Options{
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		HTTPListener: httpL, LMTPListener: lmtpL,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = b.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return emailFixture{relayURL: relayURL, relayDir: relayDir, lmtpAddr: lmtpL.Addr().String(), sentDir: sentDir, bridge: b, cancel: cancel}
}

// lmtpDeliver plays the mail server's queue: RCPT (the bridge decides) and
// DATA, returning the RCPT error if the bridge refused the recipient.
func lmtpDeliver(t *testing.T, addr, to, msg string) error {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	c := smtp.NewClientLMTP(conn)
	defer c.Close()
	if err := c.Hello("mail.poweur.net"); err != nil {
		t.Fatal(err)
	}
	if err := c.Auth(sasl.NewPlainClient("", "stalwart", lmtpPassword)); err != nil {
		t.Fatal(err)
	}
	if err := c.Mail("ann@example.com", nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Rcpt(to, nil); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte(strings.ReplaceAll(msg, "\n", "\r\n")))
	if _, err := w.CloseWithLMTPResponse(); err != nil {
		t.Fatal(err)
	}
	return nil
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

const annMail = `Authentication-Results: mail.poweur.net; spf=pass smtp.mailfrom=example.com; dkim=pass header.d=example.com; dmarc=pass header.from=example.com
From: Ann <ann@example.com>
To: john@poweur.net
Subject: Lunch on Friday?
Message-ID: <lunch-1@example.com>
Date: Tue, 13 Oct 2026 09:00:00 +0000
Content-Type: text/plain; charset=utf-8

Shall we try the new place?
`

// TestINT_EMAIL_01: mail in through the bridge lands, encrypted, in the
// recipient's inbox and is readable by the CLI; a reply goes out through the
// bridge as john@poweur.net and its status comes back.
func TestINT_EMAIL_01_InboundAndOutbound(t *testing.T) {
	fx := newEmailFixture(t)
	john := t.TempDir()
	runCLI(t, john, "identity", "create", "john.poweur.net", "--hosted", "--relay", fx.relayURL, "--json")

	// Before opting in the address does not exist.
	if err := lmtpDeliver(t, fx.lmtpAddr, "john@poweur.net", annMail); err == nil {
		t.Fatal("mail accepted for an identity that did not opt in")
	}
	status, _ := runCLI(t, john, "email", "status")
	if !strings.Contains(status, "email.poweur.net") {
		t.Fatalf("relay does not offer its bridge: %s", status)
	}
	out, _ := runCLI(t, john, "email", "enable", "--json")
	if !strings.Contains(out, `"address": "john@poweur.net"`) {
		t.Fatalf("enable: %s", out)
	}

	if err := lmtpDeliver(t, fx.lmtpAddr, "john@poweur.net", annMail); err != nil {
		t.Fatalf("delivery refused after opting in: %v", err)
	}
	ls, _ := runCLI(t, john, "email", "ls")
	if !strings.Contains(ls, "Lunch on Friday?") || !strings.Contains(ls, "ann@example.com") || strings.Contains(ls, "unverified") {
		t.Fatalf("email ls:\n%s", ls)
	}
	read, _ := runCLI(t, john, "email", "read", "1")
	if !strings.Contains(read, "Shall we try the new place?") || !strings.Contains(read, "Message-ID: lunch-1@example.com") {
		t.Fatalf("email read:\n%s", read)
	}

	// The relay stored only ciphertext: the subject is nowhere in its data.
	assertNotOnDisk(t, fx.relayDir, "Lunch on Friday")

	// Reply.
	runCLI(t, john, "email", "send", "--to=Ann <ann@example.com>", "--subject=Re: Lunch on Friday?",
		"--text=Yes, 12:30!", "--in-reply-to=lunch-1@example.com", "--references=lunch-1@example.com", "--from-name=John")
	var eml string
	waitFor(t, "the reply to reach the mail server", func() bool {
		entries, _ := os.ReadDir(fx.sentDir)
		if len(entries) == 0 {
			return false
		}
		raw, _ := os.ReadFile(filepath.Join(fx.sentDir, entries[0].Name()))
		eml = string(raw)
		return true
	})
	for _, want := range []string{`From: "John" <john@poweur.net>`, "In-Reply-To: <lunch-1@example.com>", "Yes, 12:30!", "Sent with Poweur ID john.poweur.net"} {
		if !strings.Contains(eml, want) {
			t.Fatalf("outbound lacks %q:\n%s", want, eml)
		}
	}
	waitFor(t, "the sent status", func() bool {
		ls, _ := runCLI(t, john, "email", "ls")
		return strings.Contains(ls, "[sent]")
	})
	assertNotOnDisk(t, fx.relayDir, "12:30")
}

// TestINT_EMAIL_02: opting out stops delivery within the opt-in cache.
func TestINT_EMAIL_02_DisableRefusesMail(t *testing.T) {
	fx := newEmailFixture(t)
	jane := t.TempDir()
	runCLI(t, jane, "identity", "create", "jane.poweur.net", "--hosted", "--relay", fx.relayURL, "--json")
	runCLI(t, jane, "email", "enable")
	mail := strings.ReplaceAll(annMail, "john@", "jane@")
	if err := lmtpDeliver(t, fx.lmtpAddr, "jane@poweur.net", mail); err != nil {
		t.Fatal(err)
	}
	runCLI(t, jane, "email", "disable")
	time.Sleep(5 * time.Millisecond)
	err := lmtpDeliver(t, fx.lmtpAddr, "jane@poweur.net", strings.ReplaceAll(mail, "lunch-1", "lunch-2"))
	var se *smtp.SMTPError
	if err == nil || !asSMTP(err, &se) || se.Code != 550 {
		t.Fatalf("mail after opting out: %v", err)
	}
	// The policy block is closed too, so even a stale bridge cannot deliver.
	pol, _ := runCLI(t, jane, "policy", "show", "--json")
	var p idpkg.InboxPolicy
	_ = json.Unmarshal([]byte(pol), &p)
	if p.Email == nil || p.Email.Allow {
		t.Fatalf("policy after disable: %s", pol)
	}
}

func asSMTP(err error, target **smtp.SMTPError) bool {
	se, ok := err.(*smtp.SMTPError)
	if ok {
		*target = se
	}
	return ok
}

// assertNotOnDisk walks a relay's data directory for a plaintext string.
func assertNotOnDisk(t *testing.T, dir, needle string) {
	t.Helper()
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		raw, _ := os.ReadFile(path)
		if strings.Contains(string(raw), needle) || strings.Contains(string(raw), base64.StdEncoding.EncodeToString([]byte(needle))) {
			t.Fatalf("%q found in plaintext at %s", needle, path)
		}
		return nil
	})
}

// TestINT_SUBJECT_01: a subject travels encrypted, comes back apart from the
// text, groups by thread in history, and never reaches the relay's disk.
func TestINT_SUBJECT_01_SubjectIsEncryptedAndThreaded(t *testing.T) {
	zone := fakedns.NewZone()
	installZone(t, zone)
	dataDir := t.TempDir()
	_, addr := newHostedRelay(t, zone, dataDir)
	relayURL := "http://" + addr
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })
	zone.SetHost("alicesub.poweur.net", addr)
	zone.SetHost("bobsub.poweur.net", addr)
	alice, bob := t.TempDir(), t.TempDir()
	runCLI(t, alice, "identity", "create", "alicesub.poweur.net", "--hosted", "--relay", relayURL, "--json")
	runCLI(t, bob, "identity", "create", "bobsub.poweur.net", "--hosted", "--relay", relayURL, "--json")

	out, _ := runCLI(t, alice, "send", "bobsub.poweur.net", "numbers attached", "--subject", "Q3 budget review", "--json")
	var sent struct {
		Message struct {
			ThreadID string            `json:"thread_id"`
			Metadata map[string]string `json:"metadata"`
		} `json:"message"`
	}
	_ = json.Unmarshal([]byte(out), &sent)
	if len(sent.Message.ThreadID) != 26 || sent.Message.Metadata[idpkg.MetaBodyFormat] != idpkg.BodyFormatHeadersV1 {
		t.Fatalf("send: %s", out)
	}
	runCLI(t, alice, "send", "bobsub.poweur.net", "reply in thread", "--thread", sent.Message.ThreadID)
	runCLI(t, alice, "send", "bobsub.poweur.net", "Subject: not a header\n\nplain")
	assertNotOnDisk(t, dataDir, "Q3 budget review")

	inbox, _ := runCLI(t, bob, "inbox", "--json", "--decrypt")
	var doc struct {
		Messages []map[string]any `json:"messages"`
	}
	_ = json.Unmarshal([]byte(inbox), &doc)
	if len(doc.Messages) != 3 {
		t.Fatalf("inbox: %s", inbox)
	}
	if doc.Messages[0]["subject"] != "Q3 budget review" || doc.Messages[0]["body"] != "numbers attached" {
		t.Fatalf("subject message: %v", doc.Messages[0])
	}
	if _, has := doc.Messages[2]["subject"]; has || doc.Messages[2]["body"] != "Subject: not a header\n\nplain" {
		t.Fatalf("unflagged body parsed: %v", doc.Messages[2])
	}
	bySubject, _ := runCLI(t, bob, "history", "--by-subject", "--keep-unread")
	if !strings.Contains(bySubject, "Q3 budget review (2") || !strings.Contains(bySubject, "(no subject) (1") {
		t.Fatalf("history --by-subject:\n%s", bySubject)
	}
}
