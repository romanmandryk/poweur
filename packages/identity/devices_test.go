package identity

import (
	"strings"
	"testing"
	"time"
)

// Device registry document (EPIC-004 E04-T6). The validator here is the one
// enforced definition of devices.json — the JSON Schema under conventions/
// documents it, this rejects it — so the failure modes are what matter.

func TestDeviceIDFromFingerprint(t *testing.T) {
	cases := []struct {
		name        string
		fingerprint string
		wantEmpty   bool
	}{
		{"empty", "", true},
		{"blank", "   ", true},
		{"simple", "laptop-1", false},
		{"long", strings.Repeat("x", 4096), false},
		{"unicode", "café-☕", false},
	}
	for _, tc := range cases {
		got := DeviceIDFromFingerprint(tc.fingerprint)
		if tc.wantEmpty {
			if got != "" {
				t.Fatalf("%s: want empty id, got %q", tc.name, got)
			}
			continue
		}
		if !ValidDeviceID(got) {
			t.Fatalf("%s: derived id %q is not valid", tc.name, got)
		}
	}

	// Stable: the same laptop reconnecting lands on the same row, which is
	// the whole reason the id is derived rather than allocated.
	if a, b := DeviceIDFromFingerprint("laptop-1"), DeviceIDFromFingerprint("laptop-1"); a != b {
		t.Fatalf("derivation is not stable: %s vs %s", a, b)
	}
	// Case- and whitespace-insensitive, so a client that trims differently
	// on one run does not fork its own row.
	if a, b := DeviceIDFromFingerprint("Laptop-1"), DeviceIDFromFingerprint("  laptop-1 "); a != b {
		t.Fatalf("normalization failed: %s vs %s", a, b)
	}
	// Distinct fingerprints must not collide.
	if a, b := DeviceIDFromFingerprint("laptop-1"), DeviceIDFromFingerprint("laptop-2"); a == b {
		t.Fatalf("distinct fingerprints collided on %s", a)
	}
	// The fingerprint is not recoverable from the id.
	if id := DeviceIDFromFingerprint("my-hostname.local"); strings.Contains(id, "hostname") {
		t.Fatalf("id leaks the fingerprint: %s", id)
	}
}

func TestValidDeviceID(t *testing.T) {
	valid := DeviceIDFromFingerprint("laptop-1")
	cases := []struct {
		name string
		id   string
		want bool
	}{
		{"derived", valid, true},
		{"empty", "", false},
		{"no prefix", strings.TrimPrefix(valid, DeviceIDPrefix), false},
		{"wrong prefix", "sess_" + strings.TrimPrefix(valid, DeviceIDPrefix), false},
		{"too short", DeviceIDPrefix + "abcdef", false},
		{"too long", valid + "aa", false},
		{"uppercase", strings.ToUpper(valid), false},
		{"base32 excludes 0,1,8,9", DeviceIDPrefix + "0189abcdefghijkl", false},
		{"path traversal", DeviceIDPrefix + "../../etc/pass", false},
		{"slash", DeviceIDPrefix + "aaaa/aaaa/aaaaaa", false},
	}
	for _, tc := range cases {
		if got := ValidDeviceID(tc.id); got != tc.want {
			t.Fatalf("%s: ValidDeviceID(%q) = %v, want %v", tc.name, tc.id, got, tc.want)
		}
	}
}

func TestNormalizeDeviceKind(t *testing.T) {
	cases := []struct{ in, want string }{
		{"laptop", DeviceKindLaptop},
		{"LAPTOP", DeviceKindLaptop},
		{"  phone  ", DeviceKindPhone},
		{"agent", DeviceKindAgent},
		{"", DeviceKindUnknown},
		// A device that calls itself something new is still a device;
		// refusing the write would cost the owner the whole row.
		{"toaster", DeviceKindUnknown},
	}
	for _, tc := range cases {
		if got := NormalizeDeviceKind(tc.in); got != tc.want {
			t.Fatalf("NormalizeDeviceKind(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSanitizeDeviceName(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"plain", "Roman's laptop", "Roman's laptop"},
		{"trimmed", "  laptop  ", "laptop"},
		{"strips control chars", "lap\x00top\x1b", "laptop"},
		{"strips newline injection", "laptop\nX-Injected: yes", "laptopX-Injected: yes"},
		{"strips del", "lap\x7ftop", "laptop"},
		{"empty", "", ""},
		{"only control", "\x00\x01", ""},
	}
	for _, tc := range cases {
		if got := SanitizeDeviceName(tc.in); got != tc.want {
			t.Fatalf("%s: SanitizeDeviceName(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
	long := SanitizeDeviceName(strings.Repeat("a", MaxDeviceNameLen+50))
	if len(long) != MaxDeviceNameLen {
		t.Fatalf("long name capped to %d, want %d", len(long), MaxDeviceNameLen)
	}
	// Sanitizing is idempotent — Device.Validate relies on that to reject a
	// name a client hand-crafted rather than one this function produced.
	for _, tc := range cases {
		once := SanitizeDeviceName(tc.in)
		if twice := SanitizeDeviceName(once); twice != once {
			t.Fatalf("%s: not idempotent: %q then %q", tc.name, once, twice)
		}
	}
}

func TestDeviceValidate(t *testing.T) {
	id := DeviceIDFromFingerprint("laptop-1")
	stamp := time.Now().UTC().Format(time.RFC3339)

	good := Device{ID: id, Name: "laptop", Kind: DeviceKindLaptop, AddedAt: stamp, LastSeen: stamp}
	if err := good.Validate(); err != nil {
		t.Fatalf("valid device rejected: %v", err)
	}

	cases := []struct {
		name   string
		device Device
		want   string
	}{
		{"bad id", Device{ID: "nope"}, "invalid device id"},
		{"empty id", Device{}, "invalid device id"},
		{"unsanitized name", Device{ID: id, Name: "lap\ntop"}, "printable"},
		{"overlong name", Device{ID: id, Name: strings.Repeat("a", MaxDeviceNameLen+1)}, "printable"},
		{"bad kind", Device{ID: id, Kind: "toaster"}, "invalid kind"},
		{"bad added_at", Device{ID: id, AddedAt: "yesterday"}, "added_at"},
		{"bad last_seen", Device{ID: id, LastSeen: "soon"}, "last_seen"},
		{"bad synced_at", Device{ID: id, SyncedAt: "1999"}, "synced_at"},
		{"bad revoked_at", Device{ID: id, Revoked: true, RevokedAt: "nope"}, "revoked_at"},
		{"revoked without stamp", Device{ID: id, Revoked: true}, "revoked_at"},
		{"empty scope", Device{ID: id, SyncScopes: []string{""}}, "empty"},
		{"traversal scope", Device{ID: id, SyncScopes: []string{"public/../../etc"}}, "invalid sync_scopes segment"},
		{"dot scope", Device{ID: id, SyncScopes: []string{"."}}, "invalid sync_scopes segment"},
		{"dotdot scope", Device{ID: id, SyncScopes: []string{".."}}, "invalid sync_scopes segment"},
		{"double slash scope", Device{ID: id, SyncScopes: []string{"public//deep"}}, "invalid sync_scopes segment"},
		{"too many scopes", Device{ID: id, SyncScopes: make([]string, MaxDeviceSyncScopes+1)}, "at most"},
		{"empty app password", Device{ID: id, AppPasswords: []string{" "}}, "app_passwords entry is empty"},
	}
	for _, tc := range cases {
		err := tc.device.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: want error containing %q, got %v", tc.name, tc.want, err)
		}
	}

	// Scopes that should pass.
	for _, scope := range []string{"public", "shared/photos", "/public/", "apps/net.poweur.tasks"} {
		d := Device{ID: id, SyncScopes: []string{scope}}
		if err := d.Validate(); err != nil {
			t.Fatalf("scope %q rejected: %v", scope, err)
		}
	}
}

func TestDevicesFileValidate(t *testing.T) {
	a := DeviceIDFromFingerprint("a")
	b := DeviceIDFromFingerprint("b")

	if err := (DevicesFile{Version: 1, Devices: []Device{{ID: a}, {ID: b}}}).Validate(); err != nil {
		t.Fatalf("valid file rejected: %v", err)
	}
	// Version 0 is the pre-versioning shape and stays acceptable on read.
	if err := (DevicesFile{Devices: []Device{{ID: a}}}).Validate(); err != nil {
		t.Fatalf("version 0 rejected: %v", err)
	}

	cases := []struct {
		name string
		file DevicesFile
		want string
	}{
		{"bad version", DevicesFile{Version: 7}, "unsupported devices.json version"},
		{"duplicate", DevicesFile{Devices: []Device{{ID: a}, {ID: strings.ToUpper(a)}}}, "invalid device id"},
		{"bad row", DevicesFile{Devices: []Device{{ID: "junk"}}}, "invalid device id"},
		{"too many", DevicesFile{Devices: make([]Device, MaxDevices+1)}, "at most"},
	}
	for _, tc := range cases {
		err := tc.file.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: want error containing %q, got %v", tc.name, tc.want, err)
		}
	}

	// A genuine duplicate (same casing, since uppercase ids are invalid on
	// their own) is caught by the uniqueness check.
	dup := DevicesFile{Devices: []Device{{ID: a}, {ID: a}}}
	if err := dup.Validate(); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate ids: %v", err)
	}
}

func TestParseDevicesFile(t *testing.T) {
	id := DeviceIDFromFingerprint("laptop-1")
	good := `{"version":1,"devices":[{"id":"` + id + `","name":"laptop","kind":"laptop"}]}`
	f, err := ParseDevicesFile([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Devices) != 1 || f.Devices[0].Name != "laptop" {
		t.Fatalf("round-trip lost the row: %+v", f)
	}

	bad := []struct{ name, raw string }{
		{"not json", `nope`},
		{"truncated", `{"version":1,"devices":[`},
		{"bad id", `{"version":1,"devices":[{"id":"dev_../../x"}]}`},
		{"bad kind", `{"version":1,"devices":[{"id":"` + id + `","kind":"toaster"}]}`},
		{"bad version", `{"version":99,"devices":[]}`},
		{"traversal scope", `{"version":1,"devices":[{"id":"` + id + `","sync_scopes":["../etc"]}]}`},
	}
	for _, tc := range bad {
		if _, err := ParseDevicesFile([]byte(tc.raw)); err == nil {
			t.Fatalf("%s: want a parse error, got none", tc.name)
		}
	}
}

func TestDevicesFileUpsertFindRemove(t *testing.T) {
	a := DeviceIDFromFingerprint("a")
	b := DeviceIDFromFingerprint("b")

	var f DevicesFile
	f = f.Upsert(Device{ID: a, Name: "first"})
	f = f.Upsert(Device{ID: b, Name: "second"})
	f = f.Upsert(Device{ID: a, Name: "renamed"})

	if len(f.Devices) != 2 {
		t.Fatalf("upsert appended instead of replacing: %+v", f.Devices)
	}
	// First-seen order survives, so a listing does not shuffle on every
	// check-in.
	if f.Devices[0].ID != a || f.Devices[1].ID != b {
		t.Fatalf("upsert reordered rows: %+v", f.Devices)
	}
	if d, ok := f.Find(a); !ok || d.Name != "renamed" {
		t.Fatalf("find after upsert: %+v %v", d, ok)
	}
	if _, ok := f.Find(DeviceIDFromFingerprint("missing")); ok {
		t.Fatal("found a device that was never added")
	}
	if !f.Remove(a) {
		t.Fatal("remove reported no such device")
	}
	if f.Remove(a) {
		t.Fatal("remove reported success twice")
	}
	if len(f.Devices) != 1 || f.Devices[0].ID != b {
		t.Fatalf("remove took the wrong row: %+v", f.Devices)
	}
}

func TestDevicesFileRevoke(t *testing.T) {
	id := DeviceIDFromFingerprint("phone")
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)

	f := DevicesFile{Version: 1, Devices: []Device{
		{ID: id, Name: "phone", AppPasswords: []string{"phone-dav"}},
	}}
	if !f.Revoke(id, now) {
		t.Fatal("revoke reported no such device")
	}
	d, _ := f.Find(id)
	if !d.Revoked || d.RevokedAt != now.Format(time.RFC3339) {
		t.Fatalf("revoke did not stamp the row: %+v", d)
	}
	// The row stays as an audit trail, but must stop naming credentials
	// that were just deleted.
	if d.AppPasswords != nil {
		t.Fatalf("revoke left app passwords named: %+v", d.AppPasswords)
	}
	if err := f.Validate(); err != nil {
		t.Fatalf("revoked file no longer validates: %v", err)
	}
	// Revoking twice is not an error the caller should act on.
	if f.Revoke(id, now) {
		t.Fatal("second revoke reported a change")
	}
	if f.Revoke(DeviceIDFromFingerprint("nope"), now) {
		t.Fatal("revoked a device that does not exist")
	}
}

func TestDeviceStale(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		syncedAt string
		wantOK   bool
		wantAge  time.Duration
	}{
		{"never synced", "", false, 0},
		{"unparseable", "three days ago", false, 0},
		{"three days", now.Add(-72 * time.Hour).Format(time.RFC3339), true, 72 * time.Hour},
		{"just now", now.Format(time.RFC3339), true, 0},
	}
	for _, tc := range cases {
		age, ok := Device{ID: DeviceIDFromFingerprint("x"), SyncedAt: tc.syncedAt}.Stale(now)
		if ok != tc.wantOK {
			t.Fatalf("%s: ok = %v, want %v", tc.name, ok, tc.wantOK)
		}
		if ok && age != tc.wantAge {
			t.Fatalf("%s: age = %v, want %v", tc.name, age, tc.wantAge)
		}
	}
	// Offsets are honoured, not assumed to be UTC.
	offset := now.Add(-24 * time.Hour).In(time.FixedZone("x", 3600)).Format(time.RFC3339)
	if age, ok := (Device{SyncedAt: offset}).Stale(now); !ok || age != 24*time.Hour {
		t.Fatalf("offset timestamp: age=%v ok=%v", age, ok)
	}
}
