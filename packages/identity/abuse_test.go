package identity

import (
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func validReport() AbuseReport {
	return AbuseReport{
		Version:    1,
		Type:       MsgTypeAbuseReport,
		Reporter:   "alice.poweur.net",
		Subject:    "spammer.poweur.net",
		Reason:     AbuseReasonSpam,
		MessageIDs: []string{"m-2", "m-1"},
		Note:       "twelve identical messages",
		CreatedAt:  "2026-01-01T00:00:00Z",
	}
}

func TestAbuseReportValidate(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*AbuseReport)
		wantOK bool
	}{
		{"valid", func(*AbuseReport) {}, true},
		{"version 0 tolerated", func(a *AbuseReport) { a.Version = 0 }, true},
		{"future version refused", func(a *AbuseReport) { a.Version = 2 }, false},
		{"wrong type", func(a *AbuseReport) { a.Type = "sys.contact.request" }, false},
		{"absent type tolerated", func(a *AbuseReport) { a.Type = "" }, true},
		{"no reporter", func(a *AbuseReport) { a.Reporter = "" }, false},
		{"no subject", func(a *AbuseReport) { a.Subject = " " }, false},
		{"self report", func(a *AbuseReport) { a.Subject = a.Reporter }, false},
		{"unknown reason", func(a *AbuseReport) { a.Reason = "vibes" }, false},
		{"empty reason", func(a *AbuseReport) { a.Reason = "" }, false},
		{"every listed reason", func(a *AbuseReport) { a.Reason = AbuseReasonImpersonate }, true},
		{"too many message ids", func(a *AbuseReport) {
			a.MessageIDs = make([]string, MaxAbuseMessageIDs+1)
			for i := range a.MessageIDs {
				a.MessageIDs[i] = "m"
			}
		}, false},
		{"empty message id", func(a *AbuseReport) { a.MessageIDs = []string{"m1", " "} }, false},
		{"note too long", func(a *AbuseReport) { a.Note = strings.Repeat("x", MaxAbuseNote+1) }, false},
		{"no created_at", func(a *AbuseReport) { a.CreatedAt = "" }, false},
		{"created_at not rfc3339", func(a *AbuseReport) { a.CreatedAt = "yesterday" }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := validReport()
			tc.mutate(&report)
			err := report.Validate()
			if tc.wantOK && err != nil {
				t.Fatalf("expected valid, got %v", err)
			}
			if !tc.wantOK && err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestAbuseReportSignAndVerify(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	report := validReport()
	if err := report.Sign(priv); err != nil {
		t.Fatal(err)
	}
	if err := report.VerifySignature(pub); err != nil {
		t.Fatal(err)
	}

	// Every signed field is actually covered: a report whose reason, subject
	// or evidence can be edited in transit is a report an operator cannot act
	// on.
	for _, tc := range []struct {
		name   string
		mutate func(*AbuseReport)
	}{
		{"subject", func(a *AbuseReport) { a.Subject = "innocent.poweur.net" }},
		{"reason", func(a *AbuseReport) { a.Reason = AbuseReasonMalware }},
		{"reporter", func(a *AbuseReport) { a.Reporter = "mallory.poweur.net" }},
		{"note", func(a *AbuseReport) { a.Note = "actually it was fine" }},
		{"message ids", func(a *AbuseReport) { a.MessageIDs = append(a.MessageIDs, "m-3") }},
		{"created_at", func(a *AbuseReport) { a.CreatedAt = time.Now().UTC().Format(time.RFC3339) }},
	} {
		t.Run("tampered "+tc.name, func(t *testing.T) {
			tampered := report
			tampered.MessageIDs = append([]string(nil), report.MessageIDs...)
			tc.mutate(&tampered)
			if err := tampered.VerifySignature(pub); err == nil {
				t.Fatalf("tampering with %s must break the signature", tc.name)
			}
		})
	}

	other, _, _ := ed25519.GenerateKey(nil)
	if err := report.VerifySignature(other); err == nil {
		t.Fatal("a different key must not verify")
	}
}

// Message-ID order is evidence, not meaning: two clients listing the same
// evidence differently must produce the same signature.
func TestAbuseReportCanonicalIsOrderIndependent(t *testing.T) {
	a := validReport()
	b := validReport()
	b.MessageIDs = []string{"m-1", "m-2"}
	if a.Canonical() != b.Canonical() {
		t.Fatalf("canonical differs by list order:\n%q\n%q", a.Canonical(), b.Canonical())
	}
}

func TestParseAbuseReport(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	report := validReport()
	_ = report.Sign(priv)
	raw, _ := json.Marshal(report)

	if _, err := ParseAbuseReport(raw); err != nil {
		t.Fatal(err)
	}
	unsigned := validReport()
	rawUnsigned, _ := json.Marshal(unsigned)
	if _, err := ParseAbuseReport(rawUnsigned); err == nil {
		t.Fatal("an unsigned report must be refused")
	}
	if _, err := ParseAbuseReport([]byte("{")); err == nil {
		t.Fatal("malformed json must be refused")
	}
}

func TestNewAbuseReportNormalises(t *testing.T) {
	report := NewAbuseReport("  ALICE.poweur.net ", "SPAMMER.poweur.net", "SPAM", nil, "")
	if report.Reporter != "alice.poweur.net" || report.Subject != "spammer.poweur.net" {
		t.Fatalf("identities not normalised: %+v", report)
	}
	if report.Reason != AbuseReasonSpam || report.Type != MsgTypeAbuseReport {
		t.Fatalf("report = %+v", report)
	}
	if err := report.Validate(); err != nil {
		t.Fatal(err)
	}
}
