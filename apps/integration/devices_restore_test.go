package integration_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	clipkg "github.com/poweur/cli/pkg/cli"
	idpkg "github.com/poweur/identity"
)

// cloneHomeAsNewDevice copies an identity's CLI state into a second home and
// drops its device record and sessions, so the copy is a second device on the
// same identity (what key enrolment onto another machine produces).
func cloneHomeAsNewDevice(t *testing.T, src string) string {
	t.Helper()
	dst := t.TempDir()
	srcRoot := filepath.Join(src, ".poweur")
	err := filepath.Walk(srcRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(srcRoot, path)
		target := filepath.Join(dst, ".poweur", rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, raw, info.Mode().Perm())
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(dst, ".poweur", "device.json"))
	_ = os.RemoveAll(filepath.Join(dst, ".poweur", "sessions"))
	_ = os.RemoveAll(filepath.Join(dst, ".poweur", "cache"))
	return dst
}

func devicesViaCLI(t *testing.T, home, identity string) []idpkg.Device {
	t.Helper()
	stdout, _ := runCLI(t, home, "devices", "list", "--use-identity", identity, "--json")
	var out struct {
		Devices []idpkg.Device `json:"devices"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("devices list: %s (%v)", stdout, err)
	}
	return out.Devices
}

func deviceIDOf(t *testing.T, home string) string {
	t.Helper()
	stdout, _ := runCLI(t, home, "devices", "show", "--json")
	var out struct {
		DeviceID string `json:"device_id"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil || !idpkg.ValidDeviceID(out.DeviceID) {
		t.Fatalf("devices show: %s (%v)", stdout, err)
	}
	return out.DeviceID
}

// INT_DEVICES_01 (Phase 9 restore, on v2 sessions): two devices on one
// identity; revoking one ends its sessions and it cannot open a new one under
// the same device, while the other is untouched. The row stays as a record.
func TestINT_DEVICES_01_RevokeStopsTheDevice(t *testing.T) {
	zone := newZone(t)
	ts, addr := newHostedRelay(t, zone, t.TempDir())
	const identity = "devowner.poweur.net"
	zone.SetHost(identity, addr)
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	laptop := t.TempDir()
	t.Setenv("POWEUR_DEVICE_NAME", "the laptop")
	t.Setenv("POWEUR_DEVICE_KIND", "laptop")
	runCLI(t, laptop, "identity", "create", identity, "--hosted", "--relay", ts.URL, "--json")
	laptopID := deviceIDOf(t, laptop)
	runCLI(t, laptop, "inbox", "--use-identity", identity) // opens the laptop's session

	phone := cloneHomeAsNewDevice(t, laptop)
	t.Setenv("POWEUR_DEVICE_NAME", "the phone")
	t.Setenv("POWEUR_DEVICE_KIND", "phone")
	phoneID := deviceIDOf(t, phone)
	if phoneID == laptopID {
		t.Fatal("the clone kept the laptop's fingerprint")
	}
	runCLI(t, phone, "inbox", "--use-identity", identity)

	byID := map[string]idpkg.Device{}
	for _, d := range devicesViaCLI(t, phone, identity) {
		byID[d.ID] = d
	}
	if byID[laptopID].Name != "the laptop" || byID[phoneID].Name != "the phone" || byID[phoneID].Kind != idpkg.DeviceKindPhone {
		t.Fatalf("registry rows: %+v", byID)
	}

	stdout, _ := runCLI(t, phone, "devices", "revoke", laptopID, "--use-identity", identity, "--json")
	var result struct {
		DeviceID string `json:"device_id"`
		Sessions int    `json:"sessions_revoked"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil || result.DeviceID != laptopID || result.Sessions < 1 {
		t.Fatalf("devices revoke: %s (%v)", stdout, err)
	}

	// The laptop cannot open a session again under its device.
	t.Setenv("POWEUR_DEVICE_NAME", "the laptop")
	if code, _, errOut := runCLIFull(t, laptop, "inbox", "--use-identity", identity); code == 0 || !strings.Contains(errOut, "device_revoked") {
		t.Fatalf("revoked laptop re-armed itself: code=%d %s", code, errOut)
	}
	// The phone is untouched.
	runCLI(t, phone, "inbox", "--use-identity", identity)

	for _, d := range devicesViaCLI(t, phone, identity) {
		if d.ID == laptopID && (!d.Revoked || d.RevokedAt == "") {
			t.Fatalf("laptop row not kept as revoked: %+v", d)
		}
	}
}
