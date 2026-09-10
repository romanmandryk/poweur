// Integration coverage for the device registry (EPIC-004 E04-T6) against a
// real in-process relay and the real CLI: two devices sharing one identity,
// and a revocation that has to actually stop one of them.
package integration_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	clipkg "github.com/poweur/cli/pkg/cli"
	idpkg "github.com/poweur/identity"
)

// cloneHomeAsNewDevice copies an identity's CLI state into a second HOME and
// drops its device record, so the copy comes up as a different device on the
// same identity — what happens after key enrollment onto a second machine.
func cloneHomeAsNewDevice(t *testing.T, src string) string {
	t.Helper()
	dst := t.TempDir()
	srcRoot := filepath.Join(src, ".poweur")
	err := filepath.Walk(srcRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcRoot, path)
		if err != nil {
			return err
		}
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
	// A fresh fingerprint is what makes this a second device rather than the
	// same one on a new disk.
	_ = os.Remove(filepath.Join(dst, ".poweur", "device.json"))
	// Sessions are per-device too; keeping the copy would give both machines
	// the same session id.
	_ = os.RemoveAll(filepath.Join(dst, ".poweur", "sessions"))
	return dst
}

func devicesListViaCLI(t *testing.T, home, identity string) []idpkg.Device {
	t.Helper()
	stdout, _ := runCLI(t, home, "devices", "list", "--use-identity", identity, "--json")
	var out struct {
		Identity string         `json:"identity"`
		Devices  []idpkg.Device `json:"devices"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("devices list output: %s (%v)", stdout, err)
	}
	return out.Devices
}

func deviceIDViaCLI(t *testing.T, home string) string {
	t.Helper()
	stdout, _ := runCLI(t, home, "devices", "show", "--json")
	var out struct {
		DeviceID string `json:"device_id"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("devices show output: %s (%v)", stdout, err)
	}
	if !idpkg.ValidDeviceID(out.DeviceID) {
		t.Fatalf("devices show returned %q", out.DeviceID)
	}
	return out.DeviceID
}

// TestINT_DEVICES_01: the acceptance criterion. Two devices on one identity;
// revoking one kills its DAV token, its app password and its session, and it
// cannot mint a replacement — while the other device is untouched.
func TestINT_DEVICES_01_RevokeStopsTheDevice(t *testing.T) {
	zone := newZone(t)
	ts, addr := newHostedRelay(t, zone, t.TempDir())
	defer ts.Close()
	relayURL := "http://" + addr
	const identity = "devowner.poweur.net"
	zone.SetHost(identity, addr)
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	laptop := t.TempDir()
	t.Setenv("POWEUR_DEVICE_NAME", "the laptop")
	t.Setenv("POWEUR_DEVICE_KIND", "laptop")
	runCLI(t, laptop, "identity", "create", identity, "--hosted", "--relay", relayURL, "--json")
	laptopID := deviceIDViaCLI(t, laptop)

	// A second machine holding the same identity key.
	phone := cloneHomeAsNewDevice(t, laptop)
	t.Setenv("POWEUR_DEVICE_NAME", "the phone")
	t.Setenv("POWEUR_DEVICE_KIND", "phone")
	phoneID := deviceIDViaCLI(t, phone)
	if phoneID == laptopID {
		t.Fatal("the clone kept the laptop's fingerprint — it is not a second device")
	}

	// Each device mints a DAV token, which is what puts it in the registry.
	laptopToken := mintTokenViaCLI(t, laptop, "--use-identity", identity)
	phoneToken := mintTokenViaCLI(t, phone, "--use-identity", identity)

	// The laptop takes an app password. It is bound to the laptop by
	// default, which is what makes it die with the device.
	stdout, _ := runCLI(t, laptop, "dav", "password", "add", "--name", "laptop-finder", "--json")
	var pw struct {
		Password string `json:"password"`
		DeviceID string `json:"device_id"`
	}
	if err := json.Unmarshal([]byte(stdout), &pw); err != nil {
		t.Fatalf("dav password add output: %s (%v)", stdout, err)
	}
	if pw.DeviceID != laptopID {
		t.Fatalf("app password bound to %q, want the laptop %q", pw.DeviceID, laptopID)
	}

	// The phone takes a session, so revocation has one to sweep. `inbox`
	// registers a session and reports the device in the same breath.
	runCLI(t, laptop, "inbox", "--use-identity", identity)

	// Both devices are visible to the owner, with the labels they chose.
	devices := devicesListViaCLI(t, phone, identity)
	if len(devices) != 2 {
		t.Fatalf("want two devices, got %d: %+v", len(devices), devices)
	}
	byID := map[string]idpkg.Device{}
	for _, d := range devices {
		byID[d.ID] = d
	}
	if byID[laptopID].Name != "the laptop" || byID[laptopID].Kind != idpkg.DeviceKindLaptop {
		t.Fatalf("laptop row: %+v", byID[laptopID])
	}
	if byID[phoneID].Name != "the phone" || byID[phoneID].Kind != idpkg.DeviceKindPhone {
		t.Fatalf("phone row: %+v", byID[phoneID])
	}
	if len(byID[laptopID].AppPasswords) != 1 || byID[laptopID].AppPasswords[0] != "laptop-finder" {
		t.Fatalf("app password not linked to the laptop: %+v", byID[laptopID].AppPasswords)
	}

	base := relayURL + "/dav/" + identity
	// Everything works before the revocation.
	resp := davDo(t, http.MethodPut, base+"/private/before.txt", laptopToken, []byte("hi"), nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("laptop PUT before revoke: %d", resp.StatusCode)
	}
	basic := base64.StdEncoding.EncodeToString([]byte(identity + ":" + pw.Password))
	if code := basicPropfind(t, base, basic); code != 207 {
		t.Fatalf("app password before revoke: %d want 207", code)
	}

	// The phone revokes the laptop.
	stdout, _ = runCLI(t, phone, "devices", "revoke", laptopID, "--use-identity", identity, "--json")
	var result struct {
		DeviceID     string `json:"device_id"`
		Sessions     int    `json:"sessions_revoked"`
		DAVTokens    int    `json:"dav_tokens_revoked"`
		AppPasswords int    `json:"app_passwords_revoked"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("devices revoke output: %s (%v)", stdout, err)
	}
	if result.DeviceID != laptopID {
		t.Fatalf("revoked %q, want %q", result.DeviceID, laptopID)
	}
	if result.DAVTokens < 1 || result.AppPasswords != 1 || result.Sessions < 1 {
		t.Fatalf("revocation reported %+v — it did not take the laptop's credentials", result)
	}

	// The laptop's DAV token is dead.
	resp = davDo(t, http.MethodPut, base+"/private/after.txt", laptopToken, []byte("nope"), nil)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked token still writes: %d %s", resp.StatusCode, body)
	}
	// Its app password is dead.
	if code := basicPropfind(t, base, basic); code != http.StatusUnauthorized {
		t.Fatalf("revoked app password: %d want 401", code)
	}
	// The phone is untouched.
	resp = davDo(t, http.MethodGet, base+"/private/before.txt", phoneToken, nil, nil)
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(got) != "hi" {
		t.Fatalf("phone lost access: %d %q", resp.StatusCode, got)
	}

	// And the laptop cannot re-arm itself: minting a fresh token from the
	// same machine is refused, even though it still holds the identity key.
	var out, errBuf bytes.Buffer
	t.Setenv("HOME", laptop)
	if code := clipkg.Run([]string{"dav", "token", "--use-identity", identity, "--json"}, &out, &errBuf); code == 0 {
		t.Fatalf("revoked device minted a fresh token: %s", out.String())
	}
	if !strings.Contains(errBuf.String(), "device_revoked") {
		t.Fatalf("expected a device_revoked refusal, got: %s", errBuf.String())
	}

	// The row survives as an audit trail.
	after := devicesListViaCLI(t, phone, identity)
	for _, d := range after {
		if d.ID != laptopID {
			continue
		}
		if !d.Revoked || d.RevokedAt == "" {
			t.Fatalf("laptop row not marked revoked: %+v", d)
		}
		if len(d.AppPasswords) != 0 {
			t.Fatalf("revoked row still names credentials: %+v", d.AppPasswords)
		}
	}
}

// TestINT_DEVICES_02: the per-device sync cursor. A `poweur sync` run leaves
// the owner able to see how far that device got, without asking it.
func TestINT_DEVICES_02_SyncCursorVisibleToOwner(t *testing.T) {
	zone := newZone(t)
	ts, addr := newHostedRelay(t, zone, t.TempDir())
	defer ts.Close()
	relayURL := "http://" + addr
	const identity = "devsync.poweur.net"
	zone.SetHost(identity, addr)
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	home := t.TempDir()
	t.Setenv("POWEUR_DEVICE_NAME", "sync box")
	t.Setenv("POWEUR_DEVICE_KIND", "agent")
	runCLI(t, home, "identity", "create", identity, "--hosted", "--relay", relayURL, "--json")
	deviceID := deviceIDViaCLI(t, home)

	// Before any sync, the device has no cursor to report.
	runCLI(t, home, "dav", "token", "--use-identity", identity, "--json")
	for _, d := range devicesListViaCLI(t, home, identity) {
		if d.ID == deviceID && d.SyncCursor != "" {
			t.Fatalf("cursor recorded before any sync: %+v", d)
		}
	}

	dir := filepath.Join(t.TempDir(), "tree")
	if err := os.MkdirAll(filepath.Join(dir, "private"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "private", "note.txt"), []byte("synced"), 0o600); err != nil {
		t.Fatal(err)
	}
	runCLI(t, home, "sync", "run", "--use-identity", identity, "--path", "private", dir)

	var found bool
	for _, d := range devicesListViaCLI(t, home, identity) {
		if d.ID != deviceID {
			continue
		}
		found = true
		if d.SyncCursor == "" || d.SyncedAt == "" {
			t.Fatalf("no server-side cursor after a sync: %+v", d)
		}
		// sync_scopes stays empty here on purpose: `poweur sync --path`
		// filters client-side and asks the changes feed for the whole tree,
		// so the relay is told no scopes to record. A client that does send
		// ?paths= gets them recorded — TestDeviceSyncCursorRecorded in the
		// relay package covers that half.
		if len(d.SyncScopes) != 0 {
			t.Fatalf("sync scopes appeared without the client asking: %+v", d.SyncScopes)
		}
		// Staleness is answerable from the owner's own registry, which is
		// the point of storing the cursor server-side.
		if age, ok := d.Stale(timeNowUTC()); !ok || age > time.Minute {
			t.Fatalf("staleness unreadable: age=%v ok=%v", age, ok)
		}
		if d.Kind != idpkg.DeviceKindAgent {
			t.Fatalf("kind %q, want agent", d.Kind)
		}
	}
	if !found {
		t.Fatal("the syncing device never appeared in the registry")
	}
}

// TestINT_DEVICES_03: devices.json is the relay's document. The owner may
// read it over DAV but not write it, which is what makes "sync clients pull
// it, never push it" enforcement rather than etiquette.
func TestINT_DEVICES_03_RegistryIsRelayManaged(t *testing.T) {
	zone := newZone(t)
	ts, addr := newHostedRelay(t, zone, t.TempDir())
	defer ts.Close()
	relayURL := "http://" + addr
	const identity = "devmanaged.poweur.net"
	zone.SetHost(identity, addr)
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	home := t.TempDir()
	runCLI(t, home, "identity", "create", identity, "--hosted", "--relay", relayURL, "--json")
	token := mintTokenViaCLI(t, home, "--use-identity", identity)
	path := relayURL + "/dav/" + identity + "/poweur-sys/relay/devices.json"

	resp := davDo(t, http.MethodPut, path, token, []byte(`{"version":1,"devices":[]}`), nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("owner PUT of devices.json: %d want 403", resp.StatusCode)
	}

	resp = davDo(t, http.MethodGet, path, token, nil, nil)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("owner GET of devices.json: %d want 200", resp.StatusCode)
	}
	doc, err := idpkg.ParseDevicesFile(raw)
	if err != nil {
		t.Fatalf("relay wrote a document its own validator rejects: %v\n%s", err, raw)
	}
	if len(doc.Devices) == 0 {
		t.Fatalf("registry is empty after the CLI introduced itself: %s", raw)
	}
}

func basicPropfind(t *testing.T, base, basicAuth string) int {
	t.Helper()
	req, err := http.NewRequest("PROPFIND", base+"/private/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Basic "+basicAuth)
	req.Header.Set("Depth", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func timeNowUTC() time.Time { return time.Now().UTC() }
