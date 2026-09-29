package relay

import (
	"encoding/json"
	"github.com/poweur/api/internal/drive/provider/fs"
	"os"
	"path/filepath"
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

// With a store, overrides live in it (so an S3 relay needs no disk and every
// process agrees); edits are validated, conditional and picked up within a
// minute; the local file applies only while the store holds none.
func TestQuotaOverridesFromStore(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	store, err := fs.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "storage-quotas.json")
	writeQuotaFile(t, path, `{"alice.poweur.net": "1GB"}`, time.Now())
	var bad []error
	q := newQuotaOverrides(path, func(err error) { bad = append(bad, err) })
	q.store = store
	if got, ok := q.lookup("alice.poweur.net"); !ok || got != 1e9 {
		t.Fatalf("file fallback: %d %v", got, ok)
	}
	values, err := EditQuotas(ctx, store, func(doc map[string]json.RawMessage) error {
		doc["alice.poweur.net"] = json.RawMessage(`"3GiB"`)
		doc["bob.poweur.net"] = json.RawMessage(`0`)
		return nil
	})
	if err != nil || values["alice.poweur.net"] != 3<<30 {
		t.Fatalf("edit: %v %v", values, err)
	}
	q.checked = time.Time{} // the next minute has passed
	if got, _ := q.lookup("alice.poweur.net"); got != 3<<30 {
		t.Fatalf("store wins over the file: %d", got)
	}
	if got, ok := q.lookup("bob.poweur.net"); !ok || got != 0 {
		t.Fatalf("unlimited override: %d %v", got, ok)
	}
	// An invalid size is refused before anything is written.
	if _, err := EditQuotas(ctx, store, func(doc map[string]json.RawMessage) error {
		doc["carol.poweur.net"] = json.RawMessage(`"lots"`)
		return nil
	}); err == nil {
		t.Fatal("invalid size written")
	}
	// A broken document (written behind the tool's back) keeps the last good one.
	if _, err := store.Put(ctx, QuotaObjectKey, []byte(`{"alice.poweur.net": `)); err != nil {
		t.Fatal(err)
	}
	q.checked = time.Time{}
	if got, _ := q.lookup("alice.poweur.net"); got != 3<<30 || len(bad) != 1 {
		t.Fatalf("broken store document: %d %v", got, bad)
	}
	// Removing it from the store falls back to the file again.
	if err := store.Delete(ctx, QuotaObjectKey); err != nil {
		t.Fatal(err)
	}
	q.checked = time.Time{}
	if got, _ := q.lookup("alice.poweur.net"); got != 1e9 {
		t.Fatalf("back to the file: %d", got)
	}
}
