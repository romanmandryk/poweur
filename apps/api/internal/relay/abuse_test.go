package relay

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/poweur/api/internal/config"
	"github.com/poweur/api/internal/dns"
	idpkg "github.com/poweur/identity"
)

// EPIC-007 E07-T5: `sys.abuse.report` — a signed complaint reaches the
// operator who is actually accountable for the subject, and nothing else does.

func postAbuseReport(t *testing.T, ts serverURL, report idpkg.AbuseReport) *http.Response {
	t.Helper()
	raw, _ := json.Marshal(report)
	resp, err := http.Post(ts.URL+"/abuse", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// serverURL is the tiny slice of *httptest.Server these helpers need.
type serverURL struct{ URL string }

func signedReport(t *testing.T, reporter davTestIdentity, subject, reason string, ids []string, note string) idpkg.AbuseReport {
	t.Helper()
	report := idpkg.NewAbuseReport(reporter.name, subject, reason, ids, note)
	if err := report.Sign(reporter.priv); err != nil {
		t.Fatal(err)
	}
	return report
}

func TestAbuseReportRecordedForLocalSubject(t *testing.T) {
	server, ts := newDAVTestServer(t, 0, 0)
	alice := registerDAVIdentity(t, server, ts, "alice.poweur.net")
	spammer := registerDAVIdentity(t, server, ts, "spammer.poweur.net")

	report := signedReport(t, alice, spammer.name, idpkg.AbuseReasonSpam, []string{"m-1", "m-2"}, "twelve identical messages")
	out := mustStatus(t, postAbuseReport(t, serverURL{ts.URL}, report), http.StatusAccepted, "abuse report")
	if out["status"] != "recorded" {
		t.Fatalf("response: %v", out)
	}

	entry, ok := server.abuse.Subject(spammer.name)
	if !ok || entry.Total != 1 || entry.Reporters != 1 || entry.Reasons[idpkg.AbuseReasonSpam] != 1 {
		t.Fatalf("counter = %+v (found %v)", entry, ok)
	}

	// The response must not disclose the count: a probe-able reputation
	// number is exactly what this endpoint refuses to be.
	if _, leaked := out["total"]; leaked {
		t.Fatalf("response leaks the counter: %v", out)
	}
}

func TestAbuseReportDedupPerReporterPerDay(t *testing.T) {
	server, ts := newDAVTestServer(t, 0, 0)
	alice := registerDAVIdentity(t, server, ts, "alice.poweur.net")
	bob := registerDAVIdentity(t, server, ts, "bob.poweur.net")
	spammer := registerDAVIdentity(t, server, ts, "spammer.poweur.net")

	first := signedReport(t, alice, spammer.name, idpkg.AbuseReasonSpam, nil, "again")
	mustStatus(t, postAbuseReport(t, serverURL{ts.URL}, first), http.StatusAccepted, "first report")

	second := signedReport(t, alice, spammer.name, idpkg.AbuseReasonHarassment, nil, "and again")
	out := mustStatus(t, postAbuseReport(t, serverURL{ts.URL}, second), http.StatusAccepted, "duplicate report")
	if out["status"] != "duplicate" {
		t.Fatalf("second report from the same reporter must not count twice: %v", out)
	}

	// A different reporter is a different data point — that is the whole
	// value of the counter.
	third := signedReport(t, bob, spammer.name, idpkg.AbuseReasonSpam, nil, "me too")
	mustStatus(t, postAbuseReport(t, serverURL{ts.URL}, third), http.StatusAccepted, "second reporter")

	entry, _ := server.abuse.Subject(spammer.name)
	if entry.Total != 2 || entry.Reporters != 2 {
		t.Fatalf("counter = %+v", entry)
	}
}

func TestAbuseReportRejections(t *testing.T) {
	server, ts := newDAVTestServer(t, 0, 0)
	alice := registerDAVIdentity(t, server, ts, "alice.poweur.net")
	mallory := registerDAVIdentity(t, server, ts, "mallory.poweur.net")
	spammer := registerDAVIdentity(t, server, ts, "spammer.poweur.net")

	t.Run("unsigned", func(t *testing.T) {
		report := idpkg.NewAbuseReport(alice.name, spammer.name, idpkg.AbuseReasonSpam, nil, "")
		mustStatus(t, postAbuseReport(t, serverURL{ts.URL}, report), http.StatusBadRequest, "unsigned report")
	})

	t.Run("tampered", func(t *testing.T) {
		report := signedReport(t, alice, spammer.name, idpkg.AbuseReasonSpam, nil, "")
		report.Reason = idpkg.AbuseReasonMalware
		mustStatus(t, postAbuseReport(t, serverURL{ts.URL}, report), http.StatusUnauthorized, "tampered report")
	})

	t.Run("signed by somebody else", func(t *testing.T) {
		report := idpkg.NewAbuseReport(alice.name, spammer.name, idpkg.AbuseReasonSpam, nil, "")
		if err := report.Sign(mallory.priv); err != nil {
			t.Fatal(err)
		}
		mustStatus(t, postAbuseReport(t, serverURL{ts.URL}, report), http.StatusUnauthorized, "forged reporter")
	})

	t.Run("subject not hosted here", func(t *testing.T) {
		report := signedReport(t, alice, "elsewhere.example.test", idpkg.AbuseReasonSpam, nil, "")
		out := mustStatus(t, postAbuseReport(t, serverURL{ts.URL}, report), http.StatusForbidden, "foreign subject")
		if out["error"] != "not_authorized" {
			t.Fatalf("error = %v", out)
		}
	})

	t.Run("self report", func(t *testing.T) {
		report := idpkg.NewAbuseReport(alice.name, alice.name, idpkg.AbuseReasonSpam, nil, "")
		report.Signature = "irrelevant"
		mustStatus(t, postAbuseReport(t, serverURL{ts.URL}, report), http.StatusBadRequest, "self report")
	})

	t.Run("malformed body", func(t *testing.T) {
		resp, err := http.Post(ts.URL+"/abuse", "application/json", bytes.NewReader([]byte("{")))
		if err != nil {
			t.Fatal(err)
		}
		mustStatus(t, resp, http.StatusBadRequest, "malformed body")
	})

	// Nothing above should have been counted.
	if _, ok := server.abuse.Subject(spammer.name); ok {
		t.Fatal("a rejected report must not reach the counter")
	}
}

func TestAbuseLogSubjectsOrdering(t *testing.T) {
	logStore := newAbuseLog()
	now := time.Now().UTC()
	for i, reporter := range []string{"a.test", "b.test", "c.test"} {
		report := idpkg.NewAbuseReport(reporter, "loud.test", idpkg.AbuseReasonSpam, nil, "")
		if !logStore.Record(report, now.Add(time.Duration(i)*time.Minute)) {
			t.Fatalf("distinct reporters must all count (%s)", reporter)
		}
	}
	quiet := idpkg.NewAbuseReport("a.test", "quiet.test", idpkg.AbuseReasonOther, nil, "")
	logStore.Record(quiet, now)

	subjects := logStore.Subjects()
	if len(subjects) != 2 || subjects[0].Subject != "loud.test" || subjects[0].Total != 3 {
		t.Fatalf("subjects = %+v", subjects)
	}

	// The dedup window is per pair, and it expires.
	repeat := idpkg.NewAbuseReport("a.test", "loud.test", idpkg.AbuseReasonSpam, nil, "")
	if logStore.Record(repeat, now.Add(time.Hour)) {
		t.Fatal("a repeat inside the window must not count")
	}
	if !logStore.Record(repeat, now.Add(abuseReportDedup+time.Hour)) {
		t.Fatal("a repeat after the window must count again")
	}
}

// senderRelayKey is what the per-relay meter buckets on, so its answers are
// worth pinning: our own users are exempt, a resolvable stranger keys on the
// host that serves them, and an unresolvable one still shares a bucket with
// everyone on their domain.
func TestSenderRelayKey(t *testing.T) {
	cfg := config.Config{
		ListenAddr:           ":0",
		RelayAddress:         "relay.test",
		RelayScheme:          "http",
		DNSTTL:               time.Minute,
		ChallengeTTL:         time.Minute,
		Version:              "test",
		DataDir:              t.TempDir(),
		HostedDomains:        []string{"poweur.net"},
		ResolverAllowPrivate: true,
		RateLimits:           config.RateLimits{PerMinute: 1000, PerHour: 1000, PerDay: 1000},
	}
	resolver := &fakeResolver{hosts: map[string][]string{
		"far.example.test":  {"peer.example.test"},
		"peer.example.test": {"203.0.113.9"},
	}}
	server := NewServer(cfg, resolver, dns.NewProviderFactory(cfg))
	pub, _, _ := ed25519.GenerateKey(nil)
	server.warmIdentityCache("local.poweur.net", pub)

	cases := []struct {
		name, sender, want string
	}{
		{"local identity is not metered", "local.poweur.net", ""},
		{"empty sender", "", ""},
		{"resolvable stranger keys on their relay host", "far.example.test", "peer.example.test"},
		{"unresolvable stranger falls back to their domain", "ghost.unknown.test", "unknown.test"},
		{"case is normalised", "FAR.example.test", "peer.example.test"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := server.senderRelayKey(context.Background(), tc.sender); got != tc.want {
				t.Fatalf("senderRelayKey(%q) = %q, want %q", tc.sender, got, tc.want)
			}
		})
	}
}

// The meter is off unless an operator turns it on, and a nil meter must never
// be the reason a contact request fails.
func TestMeterRequestRelayDisabledByDefault(t *testing.T) {
	server, ts := newDAVTestServer(t, 0, 0)
	_ = ts
	if server.requestRelayLimit != nil {
		t.Fatal("no configured windows should leave the meter off")
	}
	for i := 0; i < 100; i++ {
		if _, ok := server.meterRequestRelay(context.Background(), "someone.example.test"); !ok {
			t.Fatal("a disabled meter must allow every request")
		}
	}
}

// The counter is fed by remote input, so it has to forget. What it must not
// do is forget *asymmetrically*: dropping a reporter from the dedup ledger
// while keeping the subject's counter would make the next report lower the
// reporter count, which is the one number an operator would act on.
func TestAbuseLogPruneRetiresWholeSubjects(t *testing.T) {
	logStore := newAbuseLog()
	now := time.Now().UTC()

	stale := idpkg.NewAbuseReport("a.test", "stale.test", idpkg.AbuseReasonSpam, nil, "")
	logStore.Record(stale, now.Add(-abuseRetention-time.Hour))

	for _, reporter := range []string{"a.test", "b.test"} {
		report := idpkg.NewAbuseReport(reporter, "current.test", idpkg.AbuseReasonSpam, nil, "")
		if !logStore.Record(report, now.Add(-2*abuseReportDedup)) {
			t.Fatalf("distinct reporters must count (%s)", reporter)
		}
	}

	logStore.prune(now)

	if _, ok := logStore.Subject("stale.test"); ok {
		t.Fatal("a subject nobody has reported in the retention window must be retired")
	}
	// Its dedup ledger goes with it — otherwise the key leaks forever.
	if _, ok := logStore.lastSeen["stale.test"]; ok {
		t.Fatal("the retired subject's dedup ledger must go with it")
	}

	entry, ok := logStore.Subject("current.test")
	if !ok || entry.Total != 2 || entry.Reporters != 2 {
		t.Fatalf("a live subject must survive prune intact: %+v (found %v)", entry, ok)
	}

	// The ledger entries are past the dedup window, so a third reporter is
	// admitted — and must *raise* the reporter count, not reset it to 1.
	third := idpkg.NewAbuseReport("c.test", "current.test", idpkg.AbuseReasonSpam, nil, "")
	if !logStore.Record(third, now) {
		t.Fatal("a new reporter must be counted after prune")
	}
	entry, _ = logStore.Subject("current.test")
	if entry.Total != 3 || entry.Reporters != 3 {
		t.Fatalf("prune must not make the next report undercount reporters: %+v", entry)
	}
}
