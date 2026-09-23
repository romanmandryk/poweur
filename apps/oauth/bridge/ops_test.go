package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHealthReportsTheDatabase(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	p := b.get("/health")
	if p.status != http.StatusOK || !strings.Contains(p.body, `"status":"ok"`) || strings.Contains(p.body, "versionHash") {
		t.Fatalf("health = %d %s", p.status, p.body)
	}
	VersionHash, BuildTime = "abc123", "2026-09-23 10:00"
	t.Cleanup(func() { VersionHash, BuildTime = "", "" })
	p = b.get("/health")
	if !strings.Contains(p.body, `"versionHash":"abc123"`) || !strings.Contains(p.body, `"buildTime":"2026-09-23 10:00"`) {
		t.Fatalf("health without build stamps: %s", p.body)
	}
	h.store.Close()
	p = b.get("/health")
	var out map[string]string
	_ = json.Unmarshal([]byte(p.body), &out)
	if p.status != http.StatusServiceUnavailable || out["status"] != "unavailable" || out["error"] != "database" {
		t.Fatalf("health with a closed database = %d %s", p.status, p.body)
	}
}

func TestPolicyPagesStateTheRunningConfig(t *testing.T) {
	h := newHarness(t, func(c *Config) {
		c.AbuseContact = "abuse@bridge.example"
		c.SecurityContact = "security@bridge.example"
		c.Retention = Retention{SignIns: 30 * 24 * time.Hour}
	})
	b := h.browser()
	want := policyView{
		Contact: "abuse@bridge.example", SecurityContact: "security@bridge.example", Pairwise: true,
		Audit: "90 days", SignIns: "30 days", Consents: "365 days", Session: "12 hours",
		Txn: "10 minutes", Code: "1 minute", AccessToken: "10 minutes", Registration: "open",
	}
	for _, path := range []string{"/privacy", "/security"} {
		var got policyView
		p := b.get(path)
		if p.data(t, &got); p.status != http.StatusOK || got != want {
			t.Errorf("%s = %d %+v, want %+v", path, p.status, got, want)
		}
	}
	var abuse contactPage
	if b.get("/abuse").data(t, &abuse); abuse.Contact != "abuse@bridge.example" {
		t.Errorf("/abuse = %+v", abuse)
	}
}

func TestRegistrationIsOpenUnlessChosen(t *testing.T) {
	store, _ := OpenStore(context.Background(), ":memory:")
	defer store.Close()
	keys, _ := NewMemoryKeyRing(nil)
	srv, err := New(context.Background(), Config{Issuer: "https://oauth.example", Store: store, Keys: keys, AbuseContact: "a@x.example"})
	if err != nil {
		t.Fatal(err)
	}
	if srv.cfg.ClientRegistration != RegistrationOpen {
		t.Fatalf("default registration = %q", srv.cfg.ClientRegistration)
	}
	if srv.cfg.SecurityContact != "a@x.example" {
		t.Fatalf("security contact default = %q", srv.cfg.SecurityContact)
	}
}

// A restored backup is the same bridge to its clients: same subjects, same
// key ids, and tokens signed before the backup still verify.
func TestBackupRestoreKeepsSubjectsAndKeys(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	kek := make([]byte, 32)
	for i := range kek {
		kek[i] = byte(i)
	}
	open := func(path string) (*Server, *Store) {
		t.Helper()
		store, err := OpenStore(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { store.Close() })
		srv, err := New(ctx, Config{Issuer: "https://oauth.example", Store: store, KeyEncryptionKey: kek})
		if err != nil {
			t.Fatal(err)
		}
		return srv, store
	}
	client := &Client{ID: "rp", RedirectURIs: []string{"https://rp.example/cb"}}

	orig, origStore := open(filepath.Join(dir, "oauth.db"))
	client.Sector = "rp.example"
	sub := orig.subject(client, alice)
	token, err := orig.keys.Sign(map[string]any{"sub": sub})
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(dir, "backup.db")
	if err := origStore.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	if err := origStore.Backup(ctx, backup); err == nil {
		t.Fatal("backup overwrote an existing file")
	}

	restored := filepath.Join(dir, "restored.db")
	raw, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(restored, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	again, _ := open(restored)
	if got := again.subject(client, alice); got != sub {
		t.Fatalf("pairwise subject changed across restore: %s != %s", got, sub)
	}
	if _, err := verifyJWS(token, again.keys.JWKS()); err != nil {
		t.Fatalf("token from before the backup does not verify after restore: %v", err)
	}
	if a, b := orig.keys.List(), again.keys.List(); len(a) != len(b) || a[0].KID != b[0].KID {
		t.Fatalf("keys differ: %v vs %v", a, b)
	}

	// The wrong key-encryption key cannot use the backup.
	kek[0] ^= 1
	store, err := OpenStore(ctx, restored)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := New(ctx, Config{Issuer: "https://oauth.example", Store: store, KeyEncryptionKey: kek}); err == nil {
		t.Fatal("restored with the wrong key-encryption key")
	}
}

func TestMetricsCountRoutesLimitsAndEvents(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.RateLimits.Token = 1 })
	h.fullFlow(h.browser(), h.users[alice], "openid")
	h.token(codeForm("nope", rpRedirect), "rp", rpSecret) // over the limit of 1/min

	rec := httptest.NewRecorder()
	h.srv.MetricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()
	for _, want := range []string{
		`poweur_oauth_http_requests_total{route="GET /authorize",code="3xx"} 1`,
		`poweur_oauth_http_requests_total{route="POST /token",code="2xx"} 1`,
		`poweur_oauth_http_request_duration_seconds_count{route="POST /token"} 1`,
		`poweur_oauth_rate_limited_total{route="/token"} 1`,
		`poweur_oauth_events_total{event="token.issued"} 1`,
		`poweur_oauth_info{version="` + Version + `"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics lack %s", want)
		}
	}
	if strings.Contains(body, alice) || strings.Contains(body, "/t/") && !strings.Contains(body, "{id}") {
		t.Error("metrics leak identities or transaction ids")
	}
}
