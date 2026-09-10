// Integration tests for abuse pressure beyond the individual inbox
// (EPIC-007 E07-T5): per-sender-relay request throttling, `sys.abuse.report`
// routed to the operator accountable for the subject, and blocklists pooled
// through an EPIC-005 share.
//
// All three are relay-HTTP behaviours driven from the CLI, which is what this
// suite exists for: real relays, real DAV, real signatures, only DNS faked.
package integration_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	relaypkg "github.com/poweur/api/pkg/relay"
	clipkg "github.com/poweur/cli/pkg/cli"

	"github.com/poweur/integration/fakedns"
)

// pathCounter records how many times a path was POSTed to a relay, so a test
// can assert not just that something worked but that it arrived at the right
// operator.
type pathCounter struct {
	mu sync.Mutex
	n  map[string]int
}

func (c *pathCounter) count(path string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n[path]
}

// newCountingRelay is newRelay plus a POST counter, optional hosted domains,
// and optional per-sender-relay request metering.
func newCountingRelay(t *testing.T, zone *fakedns.Zone, hosted []string, requestRelay relaypkg.RateLimits) (string, *pathCounter) {
	t.Helper()
	ts := httptest.NewUnstartedServer(nil)
	addr := ts.Listener.Addr().String()
	cfg := relaypkg.Config{
		ListenAddr:           addr,
		RelayAddress:         addr,
		RelayScheme:          "http",
		DNSTTL:               time.Minute,
		ChallengeTTL:         time.Minute,
		Version:              "integration-test",
		DataDir:              t.TempDir(),
		HostedDomains:        hosted,
		ResolverAllowPrivate: true,
		// Wide open, so any rejection below comes from the feature under test.
		RateLimits:         relaypkg.RateLimits{PerMinute: 1000, PerHour: 10000, PerDay: 100000},
		RequestRelayLimits: requestRelay,
	}
	providers := relaypkg.NewProviderFactory(cfg)
	relaypkg.RegisterProvider(providers, "mock", zone.Provider())
	server := relaypkg.NewServer(cfg, zone, providers)

	counter := &pathCounter{n: map[string]int{}}
	inner := relaypkg.Router(server)
	ts.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			counter.mu.Lock()
			counter.n[r.URL.Path]++
			counter.mu.Unlock()
		}
		inner.ServeHTTP(w, r)
	})
	ts.Start()
	t.Cleanup(ts.Close)
	return addr, counter
}

// runCLIFull runs the CLI and hands back everything, including the exit code —
// these tests care about the refusals as much as the successes.
func runCLIFull(t *testing.T, home string, args ...string) (int, string, string) {
	t.Helper()
	t.Setenv("HOME", home)
	var stdout, stderr bytes.Buffer
	code := clipkg.Run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// newDNSIdentity creates a DNS-backed identity homed on the given relay and
// returns its HOME dir and full name.
func newDNSIdentity(t *testing.T, name, domain, relayAddr string) (string, string) {
	t.Helper()
	home := t.TempDir()
	runCLI(t, home, "identity", "create", name,
		"--parent-domain", domain, "--relay", "http://"+relayAddr,
		"--dns-provider", "mock", "--dns-token", "integration")
	return home, name + "." + domain
}

// TestINT_ABUSE_01: a request flood is metered on the relay that carried it.
//
// The shape is the one that defeats a per-identity cap: several identities,
// each registered moments ago on the same relay, each sending exactly one
// contact request. None of them is individually noisy. Together they are the
// flood, and the only field they cannot vary is where they are hosted.
func TestINT_ABUSE_01_RequestFloodMeteredPerSenderRelay(t *testing.T) {
	zone := newZone(t)
	// The meter lives on the *recipient's* relay: it is the enforcement point
	// for its own users' inboxes, and the only party with a reason to care.
	// Two contact-request admissions per sending relay per minute.
	addrA, _ := newCountingRelay(t, zone, []string{"poweur.net"}, relaypkg.RateLimits{PerMinute: 2})
	addrCheap, _ := newCountingRelay(t, zone, nil, relaypkg.RateLimits{})
	addrOther, _ := newCountingRelay(t, zone, nil, relaypkg.RateLimits{})

	zone.SetHost("floodalice.poweur.net", addrA)
	clipkg.ConfigureIdentityResolver("http", true, addrA)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	aliceHome := t.TempDir()
	runCLI(t, aliceHome, "identity", "create", "floodalice.poweur.net", "--hosted", "--relay", "http://"+addrA, "--json")
	runCLI(t, aliceHome, "policy", "set", "contacts_and_requests")

	var cheapHomes, cheapNames []string
	for _, n := range []string{"cheap1", "cheap2", "cheap3"} {
		home, name := newDNSIdentity(t, n, "cheapco.test", addrCheap)
		cheapHomes = append(cheapHomes, home)
		cheapNames = append(cheapNames, name)
	}

	for i := 0; i < 2; i++ {
		runCLI(t, cheapHomes[i], "contacts", "request", "floodalice.poweur.net", "hi there")
	}

	// The third identity is brand new and has sent nothing. Its relay has.
	code, _, stderr := runCLIFull(t, cheapHomes[2], "contacts", "request", "floodalice.poweur.net", "hi there")
	if code == 0 {
		t.Fatal("a third request from the same relay must be throttled")
	}
	if !strings.Contains(stderr, "rate_limit_exceeded") && !strings.Contains(stderr, "sender_relay") {
		t.Fatalf("the refusal should name the relay-scoped limit, got: %s", stderr)
	}

	// The bucket is the relay, not the name. Buying a second domain and
	// hosting it in the same place is the cheapest evasion there is, so a
	// fresh domain on the exhausted relay must still be throttled…
	sameRelayHome, sameRelayName := newDNSIdentity(t, "fresh", "anotherco.test", addrCheap)
	if code, _, _ := runCLIFull(t, sameRelayHome, "contacts", "request", "floodalice.poweur.net", "hi"); code == 0 {
		t.Fatalf("%s shares a relay with the flood and must share its budget", sameRelayName)
	}

	// …and, the other way round, a name in the flood's own domain hosted
	// somewhere else must not be. Somebody on a well-run relay is untouched
	// by a neighbour they have never heard of; without that the meter is
	// collective punishment across the network rather than one relay's
	// problem to fix.
	politeHome, politeName := newDNSIdentity(t, "polite", "cheapco.test", addrOther)
	runCLI(t, politeHome, "contacts", "request", "floodalice.poweur.net", "hello")

	stdout, _ := runCLI(t, aliceHome, "requests")
	for _, want := range []string{cheapNames[0], cheapNames[1], politeName} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("requests queue missing %s: %s", want, stdout)
		}
	}
	if strings.Contains(stdout, cheapNames[2]) {
		t.Fatalf("a throttled request reached the queue anyway: %s", stdout)
	}
}

// TestINT_ABUSE_02: `poweur report` reaches the operator who hosts the
// subject — and nobody else.
func TestINT_ABUSE_02_ReportReachesTheSubjectsRelay(t *testing.T) {
	zone := newZone(t)
	addrA, counterA := newCountingRelay(t, zone, nil, relaypkg.RateLimits{})
	addrB, counterB := newCountingRelay(t, zone, nil, relaypkg.RateLimits{})

	aliceHome, alice := newDNSIdentity(t, "repalice", "reporters.test", addrA)
	_, spammer := newDNSIdentity(t, "loud", "cheapco.test", addrB)

	code, stdout, stderr := runCLIFull(t, aliceHome, "report", spammer,
		"--reason=spam", "--note=twelve identical messages overnight", "--message-ids=m-1,m-2", "--json")
	if code != 0 {
		t.Fatalf("report failed: %s / %s", stdout, stderr)
	}
	var out struct {
		Subject string `json:"subject"`
		Reason  string `json:"reason"`
		Status  string `json:"status"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("report output %q: %v", stdout, err)
	}
	if out.Status != "recorded" || out.Subject != spammer || out.Reason != "spam" {
		t.Fatalf("report result: %+v", out)
	}

	// Routing is the whole point: the complaint has to land with the operator
	// who can act on it, not with the reporter's own relay, which cannot.
	if counterB.count("/abuse") != 1 {
		t.Fatalf("subject's relay saw %d reports, want 1", counterB.count("/abuse"))
	}
	if counterA.count("/abuse") != 0 {
		t.Fatalf("reporter's own relay must not receive the report (saw %d)", counterA.count("/abuse"))
	}

	// One reporter, one subject, one data point per day: otherwise the count
	// measures clicks and can be manufactured by whoever is most persistent.
	_, stdout, _ = runCLIFull(t, aliceHome, "report", spammer, "--reason=harassment", "--json")
	if !strings.Contains(stdout, "duplicate") {
		t.Fatalf("second report from the same reporter should be a duplicate: %s", stdout)
	}

	// Self-reporting is refused before anything leaves the machine.
	code, _, _ = runCLIFull(t, aliceHome, "report", alice, "--reason=spam")
	if code == 0 {
		t.Fatal("an identity must not be able to report itself")
	}
	if counterB.count("/abuse") != 2 {
		t.Fatalf("subject's relay saw %d reports total, want 2", counterB.count("/abuse"))
	}
}

// TestINT_ABUSE_03: a blocklist is a block decision made portable — exported
// signed, handed to a chosen audience by an EPIC-005 share, adopted into the
// importer's own contacts where they can see and undo it.
func TestINT_ABUSE_03_BlocklistExportShareImport(t *testing.T) {
	zone := newZone(t)
	ts, addr := newHostedRelay(t, zone, t.TempDir())
	defer ts.Close()
	relayURL := ts.URL
	for _, id := range []string{"blkalice.poweur.net", "blkbob.poweur.net"} {
		zone.SetHost(id, addr)
	}
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	aliceHome, bobHome := t.TempDir(), t.TempDir()
	runCLI(t, aliceHome, "identity", "create", "blkalice.poweur.net", "--hosted", "--relay", relayURL, "--json")
	runCLI(t, bobHome, "identity", "create", "blkbob.poweur.net", "--hosted", "--relay", relayURL, "--json")

	// Alice has already done the work of finding out who these two are.
	runCLI(t, aliceHome, "contacts", "block", "spamco.example.test")
	runCLI(t, aliceHome, "contacts", "block", "phisher.example.test")
	// Bob is a contact, not a menace, and must survive every round trip.
	runCLI(t, aliceHome, "contacts", "add", "blkbob.poweur.net")

	exportOut, _ := runCLI(t, aliceHome, "blocks", "export", "--name", "alice's list",
		"--out", aliceHome+"/blocks.json", "--json")
	if !strings.Contains(exportOut, "spamco.example.test") || !strings.Contains(exportOut, "phisher.example.test") {
		t.Fatalf("export: %s", exportOut)
	}
	if strings.Contains(exportOut, "blkbob.poweur.net") {
		t.Fatalf("an accepted contact must never be exported as a block: %s", exportOut)
	}

	// Published but not yet shared: adoption is somebody's decision to grant,
	// not a side effect of publishing.
	if code, _, _ := runCLIFull(t, bobHome, "blocks", "import", "blkalice.poweur.net"); code == 0 {
		t.Fatal("import must fail before the list is shared")
	}

	runCLI(t, aliceHome, "share", "add", "/shared/blocks.json", "--with", "blkbob.poweur.net", "--perm", "read")

	code, stdout, stderr := runCLIFull(t, bobHome, "blocks", "import", "blkalice.poweur.net", "--json")
	if code != 0 {
		t.Fatalf("import failed: %s / %s", stdout, stderr)
	}
	var merge struct {
		Publisher string   `json:"publisher"`
		Blocked   []string `json:"blocked"`
	}
	if err := json.Unmarshal([]byte(stdout), &merge); err != nil {
		t.Fatalf("import output %q: %v", stdout, err)
	}
	if merge.Publisher != "blkalice.poweur.net" || len(merge.Blocked) != 2 {
		t.Fatalf("merge: %+v", merge)
	}

	// The adopted blocks are bob's own contacts now — visible, attributed to
	// the list they came from, and undoable by him.
	lsOut, _ := runCLI(t, bobHome, "contacts", "ls")
	if !strings.Contains(lsOut, "spamco.example.test") || !strings.Contains(lsOut, "blocked") {
		t.Fatalf("contacts ls after import: %s", lsOut)
	}

	// A list altered in transit is refused. Without this check, "adopt this
	// list" would mean "let whoever served the file edit your blocklist".
	tampered := aliceHome + "/tampered.json"
	raw, _ := runCLI(t, aliceHome, "blocks", "export", "--no-publish", "--out", tampered, "--json")
	_ = raw
	patchBlocklistFile(t, tampered, "innocent.example.test")
	code, _, stderr = runCLIFull(t, bobHome, "blocks", "import", "--file", tampered)
	if code == 0 {
		t.Fatal("a tampered blocklist must not import")
	}
	if !strings.Contains(stderr, "REFUSING TO IMPORT") {
		t.Fatalf("refusal should be loud: %s", stderr)
	}
	lsOut, _ = runCLI(t, bobHome, "contacts", "ls")
	if strings.Contains(lsOut, "innocent.example.test") {
		t.Fatalf("a name added after signing must never reach contacts: %s", lsOut)
	}
}

// patchBlocklistFile appends an entry to a signed blocklist without touching
// the signature — an attacker with the file in hand and no key.
func patchBlocklistFile(t *testing.T, path, extra string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("blocklist %s: %v", path, err)
	}
	entries, _ := doc["entries"].([]any)
	doc["entries"] = append(entries, map[string]any{"identity": extra})
	patched, _ := json.MarshalIndent(doc, "", "  ")
	if err := os.WriteFile(path, patched, 0o600); err != nil {
		t.Fatal(err)
	}
}
