package integration_test

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	relaypkg "github.com/poweur/api/pkg/relay"
	clipkg "github.com/poweur/cli/pkg/cli"

	"github.com/poweur/integration/fakedns"
)

// startHostedRelayAt is newHostedRelay on a fixed address, so a "restarted"
// relay answers where clients and DNS already point.
func startHostedRelayAt(t *testing.T, zone *fakedns.Zone, dataDir, addr string) *httptest.Server {
	t.Helper()
	var listener net.Listener
	var err error
	for i := 0; i < 50; i++ {
		if listener, err = net.Listen("tcp", addr); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("listen %s: %v", addr, err)
	}
	cfg := relaypkg.Config{
		ListenAddr: addr, RelayAddress: addr, RelayScheme: "http",
		DNSTTL: time.Minute, ChallengeTTL: time.Minute, Version: "integration-test",
		DataDir: dataDir, HostedDomains: []string{"poweur.net"}, ResolverAllowPrivate: true,
		RateLimits: relaypkg.RateLimits{PerMinute: 1000, PerHour: 10000, PerDay: 100000},
	}
	providers := relaypkg.NewProviderFactory(cfg)
	relaypkg.RegisterProvider(providers, "mock", zone.Provider())
	ts := httptest.NewUnstartedServer(relaypkg.Router(relaypkg.NewServer(cfg, zone, providers)))
	ts.Listener.Close()
	ts.Listener = listener
	ts.Start()
	t.Cleanup(ts.Close)
	return ts
}

// INT_STATELESS_01 (E20-T6 acceptance): stop the relay, keep only the
// provider, start a new process — policy, contacts, devices, the pending
// inbox, the identity index and quotas behave identically. With the
// filesystem provider "only the provider" is the whole data directory, so
// the test also proves nothing is written beside drives/ and relay/.
func TestINT_STATELESS_01_RestartFromProviderOnly(t *testing.T) {
	zone := newZone(t)
	dataDir := t.TempDir()
	first, addr := newHostedRelay(t, zone, dataDir)
	relayURL := first.URL
	for _, id := range []string{"slalice.poweur.net", "slbob.poweur.net", "slcarol.poweur.net"} {
		zone.SetHost(id, addr)
	}
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	aliceHome, bobHome, carolHome := t.TempDir(), t.TempDir(), t.TempDir()
	runCLI(t, aliceHome, "identity", "create", "slalice.poweur.net", "--hosted", "--relay", relayURL, "--json")
	runCLI(t, bobHome, "identity", "create", "slbob.poweur.net", "--hosted", "--relay", relayURL, "--json")
	runCLI(t, carolHome, "identity", "create", "slcarol.poweur.net", "--hosted", "--relay", relayURL, "--json")

	// Alice admits only contacts, adds bob, and a device of hers is seen.
	runCLI(t, aliceHome, "policy", "set", "contacts_only")
	runCLI(t, aliceHome, "contacts", "add", "slbob.poweur.net")
	openDeviceStream(t, relayURL, aliceHome, "slalice.poweur.net")

	// Bob's message waits in the spool; carol is refused.
	runCLI(t, bobHome, "send", "slalice.poweur.net", "waiting across the restart")
	if code := runCLICode(t, carolHome, "send", "slalice.poweur.net", "not a contact"); code == 0 {
		t.Fatal("contacts_only must refuse a stranger")
	}
	devicesBefore := devicesOf(t, relayURL, aliceHome, "slalice.poweur.net")
	usageBefore := driveUsage(t, relayURL, aliceHome, "slalice.poweur.net")

	// Nothing durable lives outside the provider's two prefixes.
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if name := entry.Name(); name != "drives" && name != "relay" {
			t.Errorf("relay wrote %q outside the provider layout", name)
		}
	}

	// Stop it and start a new process over the same provider.
	first.Close()
	startHostedRelayAt(t, zone, dataDir, addr)
	if after := driveUsage(t, relayURL, aliceHome, "slalice.poweur.net"); after != usageBefore {
		t.Fatalf("usage changed: %v -> %v", usageBefore, after)
	}

	stdout, _ := runCLI(t, aliceHome, "inbox")
	if !strings.Contains(stdout, "waiting across the restart") {
		t.Fatalf("pending inbox lost: %s", stdout)
	}
	if code := runCLICode(t, carolHome, "send", "slalice.poweur.net", "still not a contact"); code == 0 {
		t.Fatal("policy lost across the restart")
	}
	runCLI(t, bobHome, "send", "slalice.poweur.net", "contacts survive too")
	if stdout, _ := runCLI(t, aliceHome, "contacts", "ls"); !strings.Contains(stdout, "slbob.poweur.net") {
		t.Fatalf("contacts lost: %s", stdout)
	}
	// The device seen before the restart is still registered, unchanged (the
	// CLI's own calls since then may add its device beside it).
	before := devicesBefore[strings.Index(devicesBefore, `{"id"`) : strings.Index(devicesBefore, "}")+1]
	if after := devicesOf(t, relayURL, aliceHome, "slalice.poweur.net"); !strings.Contains(after, before) {
		t.Fatalf("device lost across the restart:\n%s\n%s", devicesBefore, after)
	}
	// The identity document is served from the index and mirrored in the drive.
	if status, raw := sysFileRequest(t, relayURL, aliceHome, "slalice.poweur.net", http.MethodGet, ".poweur/public/id.json", nil); status != http.StatusOK || !strings.Contains(string(raw), "slalice.poweur.net") {
		t.Fatalf("id.json in drive: %d %s", status, raw)
	}
}

// openDeviceStream opens and closes an event stream naming a device, which is
// how the relay learns about it.
func openDeviceStream(t *testing.T, relayURL, home, identity string) {
	t.Helper()
	c := driveClient{t: t, relay: relayURL, identity: identity, key: loadIdentityKey(t, home, identity)}
	resp, _ := http.Get(relayURL + "/auth/challenge?identity=" + identity)
	var challenge struct {
		Challenge string `json:"challenge"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&challenge)
	resp.Body.Close()
	req, _ := http.NewRequest(http.MethodGet, relayURL+"/events/"+identity, nil)
	req.Header.Set("X-Poweur-Identity", identity)
	req.Header.Set("X-Poweur-Challenge", challenge.Challenge)
	req.Header.Set("X-Poweur-Signature", c.sign(challenge.Challenge))
	req.Header.Set("X-Poweur-Device", "laptop-fingerprint-0001")
	req.Header.Set("X-Poweur-Device-Name", "Alice's laptop")
	req.Header.Set("X-Poweur-Device-Kind", "cli")
	stream, err := http.DefaultClient.Do(req)
	if err != nil || stream.StatusCode != http.StatusOK {
		t.Fatalf("event stream: %v", err)
	}
	stream.Body.Close()
}

func devicesOf(t *testing.T, relayURL, home, identity string) string {
	t.Helper()
	c := driveClient{t: t, relay: relayURL, identity: identity, key: loadIdentityKey(t, home, identity)}
	resp, raw := c.do(http.MethodGet, "/devices/"+identity, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), "laptop") {
		t.Fatalf("devices: %d %s", resp.StatusCode, raw)
	}
	return string(raw)
}

func driveUsage(t *testing.T, relayURL, home, identity string) any {
	t.Helper()
	c := driveClient{t: t, relay: relayURL, identity: identity, key: loadIdentityKey(t, home, identity)}
	status, out := c.json(http.MethodGet, "/drive/"+identity, nil)
	if status != http.StatusOK {
		t.Fatalf("drive: %d %v", status, out)
	}
	return out["used"]
}
