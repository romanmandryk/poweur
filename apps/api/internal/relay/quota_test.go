package relay

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseQuotaSize(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int64
		ok   bool
	}{
		{"0", 0, true},
		{"1024", 1024, true},
		{"200MB", 200_000_000, true},
		{"200 MiB", 200 << 20, true},
		{"2GB", 2_000_000_000, true},
		{"1.5GiB", 3 << 29, true},
		{"5gb", 5_000_000_000, true},
		{"10KB", 10_000, true},
		{"", 0, false},
		{"lots", 0, false},
		{"-1GB", 0, false},
	} {
		got, err := parseQuotaSize(tc.in)
		if (err == nil) != tc.ok || (tc.ok && got != tc.want) {
			t.Errorf("parseQuotaSize(%q) = %d, %v; want %d ok=%v", tc.in, got, err, tc.want, tc.ok)
		}
	}
}

func TestParseQuotaOverrides(t *testing.T) {
	values, err := parseQuotaOverrides([]byte(`{"Alice.Poweur.net": "1GB", "bob.poweur.net": 2048, "carl.poweur.net": 0}`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{"alice.poweur.net": 1e9, "bob.poweur.net": 2048, "carl.poweur.net": 0}
	for id, size := range want {
		if values[id] != size {
			t.Errorf("%s = %d, want %d", id, values[id], size)
		}
	}
	for _, bad := range []string{`[]`, `{"a.poweur.net": true}`, `{"a.poweur.net": -5}`, `{"a.poweur.net": "big"}`, `not json`} {
		if _, err := parseQuotaOverrides([]byte(bad)); err == nil {
			t.Errorf("%s: want an error", bad)
		}
	}
}

func writeQuotaFile(t *testing.T, path, body string, at time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	// Distinct mtimes: some filesystems only keep whole seconds.
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

func TestQuotaOverridesReloadAndKeepLastGood(t *testing.T) {
	path := filepath.Join(t.TempDir(), "storage-quotas.json")
	var bad []error
	q := newQuotaOverrides(path, func(err error) { bad = append(bad, err) })

	// No file: nobody has an override, and that is not an error.
	if _, ok := q.lookup("alice.poweur.net"); ok || len(bad) != 0 {
		t.Fatalf("missing file: ok=%v bad=%v", ok, bad)
	}

	start := time.Now().Add(-time.Hour)
	writeQuotaFile(t, path, `{"alice.poweur.net": "1GB"}`, start)
	if got, ok := q.lookup("ALICE.poweur.net"); !ok || got != 1e9 {
		t.Fatalf("after write: %d %v", got, ok)
	}

	// Support raises it; no restart.
	writeQuotaFile(t, path, `{"alice.poweur.net": "5GB"}`, start.Add(time.Minute))
	if got, _ := q.lookup("alice.poweur.net"); got != 5e9 {
		t.Fatalf("after edit: %d", got)
	}

	// A broken edit keeps the last good version and is reported once.
	writeQuotaFile(t, path, `{"alice.poweur.net": "5GB",`, start.Add(2*time.Minute))
	for range 3 {
		if got, _ := q.lookup("alice.poweur.net"); got != 5e9 {
			t.Fatalf("broken edit dropped the override: %d", got)
		}
	}
	if len(bad) != 1 {
		t.Fatalf("broken file reported %d times, want once", len(bad))
	}

	// Removing the file removes the overrides.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.lookup("alice.poweur.net"); ok {
		t.Fatal("override survived removing the file")
	}
}

func TestStorageQuotaOverrideAndContact(t *testing.T) {
	server, ts := newDAVTestServer(t, 64, 0)
	server.cfg.QuotaContact = "helpdesk.poweur.net"
	path := filepath.Join(t.TempDir(), "storage-quotas.json")
	server.quotas = newQuotaOverrides(path, nil)
	alice := registerDAVIdentity(t, server, ts, "quotaover.poweur.net")
	token := mintDAVToken(t, ts, alice, "", "")
	base := "/dav/" + alice.name

	// Over the relay default: 507, naming who to ask.
	resp := davReq(t, ts, http.MethodPut, base+"/private/big.txt", token, bytes.Repeat([]byte("x"), 100), nil)
	var refused ErrorResponse
	_ = json.NewDecoder(resp.Body).Decode(&refused)
	resp.Body.Close()
	if resp.StatusCode != http.StatusInsufficientStorage || refused.Error != "quota_exceeded" {
		t.Fatalf("over default quota: %d %+v", resp.StatusCode, refused)
	}
	if refused.Contact != "helpdesk.poweur.net" || !strings.Contains(refused.Detail, "message helpdesk.poweur.net") {
		t.Fatalf("507 does not name the contact: %+v", refused)
	}

	// Support raises this one identity's limit; the same upload now fits.
	writeQuotaFile(t, path, `{"quotaover.poweur.net": "1MB"}`, time.Now())
	resp = davReq(t, ts, http.MethodPut, base+"/private/big.txt", token, bytes.Repeat([]byte("x"), 100), nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		t.Fatalf("under override: %d", resp.StatusCode)
	}

	// The quota report shows the identity's own limit and the contact.
	resp = davReq(t, ts, http.MethodGet, "/files/"+alice.name+"/quota", token, nil, nil)
	var report map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&report)
	resp.Body.Close()
	if report["quota_bytes"] != float64(1e6) || report["contact"] != "helpdesk.poweur.net" {
		t.Fatalf("quota report: %v", report)
	}

	// Without a contact the 507 still says what happened.
	server.cfg.QuotaContact = ""
	writeQuotaFile(t, path, `{}`, time.Now().Add(time.Minute))
	resp = davReq(t, ts, http.MethodPut, base+"/private/more.txt", token, bytes.Repeat([]byte("y"), 100), nil)
	refused = ErrorResponse{}
	_ = json.NewDecoder(resp.Body).Decode(&refused)
	resp.Body.Close()
	if resp.StatusCode != http.StatusInsufficientStorage || refused.Detail != "identity storage quota exceeded" || refused.Contact != "" {
		t.Fatalf("no contact: %d %+v", resp.StatusCode, refused)
	}
}
