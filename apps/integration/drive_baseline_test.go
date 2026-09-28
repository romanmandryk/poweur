package integration_test

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"testing"

	clipkg "github.com/poweur/cli/pkg/cli"
	idpkg "github.com/poweur/identity"
)

// The public avatar must survive loss of all relay memory and remain readable
// through the identity host, not a deprecated file-storage URL.
func TestINT_PROFILE_02_AvatarSurvivesRestart(t *testing.T) {
	zone := newZone(t)
	data := t.TempDir()
	ts, addr := newHostedRelay(t, zone, data)
	const name = "avatarv2.poweur.net"
	zone.SetHost(name, addr)
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })
	home := t.TempDir()
	runCLI(t, home, "identity", "create", name, "--hosted", "--relay", ts.URL, "--json")
	var encoded bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatal(err)
	}
	writeRelaySysFile(t, ts.URL, home, name, ".poweur/public/avatar.png", encoded.Bytes())
	ts.Close()
	restarted, newAddr := newHostedRelay(t, zone, data)
	defer restarted.Close()
	zone.SetHost(name, newAddr)
	req, _ := http.NewRequest("GET", restarted.URL+"/.well-known/poweur/avatar.png", nil)
	req.Host = name
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/png" || !bytes.Equal(body, encoded.Bytes()) {
		t.Fatalf("avatar after restart: status=%d type=%s err=%v", resp.StatusCode, resp.Header.Get("Content-Type"), err)
	}
}

// Device state is relay-managed even when the requesting client holds the
// identity key; it is readable through the v2 system-file adapter.
func TestINT_DEVICES_03_RegistryIsRelayManaged(t *testing.T) {
	zone := newZone(t)
	ts, addr := newHostedRelay(t, zone, t.TempDir())
	defer ts.Close()
	const name = "devicesv2.poweur.net"
	zone.SetHost(name, addr)
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })
	home := t.TempDir()
	runCLI(t, home, "identity", "create", name, "--hosted", "--relay", ts.URL, "--json")
	runCLI(t, home, "inbox", "--use-identity", name)
	c := driveClient{t: t, relay: ts.URL, identity: name, key: loadIdentityKey(t, home, name)}
	path := "/identities/" + name + "/system/.poweur/state/devices.json"
	resp, raw := c.do("GET", path, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("registry read: %d %s", resp.StatusCode, raw)
	}
	doc, err := idpkg.ParseDevicesFile(raw)
	if err != nil || len(doc.Devices) == 0 {
		t.Fatalf("registry: %s %v", raw, err)
	}
	for _, method := range []string{"PUT", "DELETE"} {
		resp, raw = c.do(method, path, []byte(`{"version":1,"devices":[]}`))
		if resp.StatusCode != 403 {
			t.Fatalf("owner %s: %d %s", method, resp.StatusCode, raw)
		}
	}
}
