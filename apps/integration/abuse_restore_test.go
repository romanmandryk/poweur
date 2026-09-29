package integration_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	clipkg "github.com/poweur/cli/pkg/cli"
)

// INT_ABUSE_03 (Phase 9 restore, on v2): alice exports her signed blocklist to
// her encrypted drive, shares it read-only with bob, and bob adopts it; the
// accepted contact never leaks into it, and a tampered list is refused.
func TestINT_ABUSE_03_BlocklistExportShareImport(t *testing.T) {
	zone := newZone(t)
	ts, addr := newHostedRelay(t, zone, t.TempDir())
	const alice, bob = "blkalice.poweur.net", "blkbob.poweur.net"
	zone.SetHost(alice, addr)
	zone.SetHost(bob, addr)
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })
	aliceHome, bobHome := t.TempDir(), t.TempDir()
	runCLI(t, aliceHome, "identity", "create", alice, "--hosted", "--relay", ts.URL, "--json")
	runCLI(t, bobHome, "identity", "create", bob, "--hosted", "--relay", ts.URL, "--json")

	runCLI(t, aliceHome, "contacts", "block", "spamco.example.test")
	runCLI(t, aliceHome, "contacts", "block", "phisher.example.test")
	runCLI(t, aliceHome, "contacts", "add", bob)

	exportOut, _ := runCLI(t, aliceHome, "blocks", "export", "--name", "alice's list", "--json")
	if !strings.Contains(exportOut, "spamco.example.test") || !strings.Contains(exportOut, "phisher.example.test") || strings.Contains(exportOut, bob) {
		t.Fatalf("export: %s", exportOut)
	}
	// Published is not shared: adoption is alice's decision to grant.
	if code, _, stderr := runCLIFull(t, bobHome, "blocks", "import", alice); code == 0 || !strings.Contains(stderr, "has not shared a blocklist") {
		t.Fatalf("import before sharing: %d %s", code, stderr)
	}
	runCLI(t, aliceHome, "drive", "share", "add", "/shared/blocks.json", bob, "--no-offer", "--json")

	code, stdout, stderr := runCLIFull(t, bobHome, "blocks", "import", alice, "--json")
	if code != 0 {
		t.Fatalf("import: %s / %s", stdout, stderr)
	}
	var merge struct {
		Publisher string   `json:"publisher"`
		Blocked   []string `json:"blocked"`
	}
	if err := json.Unmarshal([]byte(stdout), &merge); err != nil || merge.Publisher != alice || len(merge.Blocked) != 2 {
		t.Fatalf("merge: %s %v", stdout, err)
	}
	if ls, _ := runCLI(t, bobHome, "contacts", "ls"); !strings.Contains(ls, "spamco.example.test") || !strings.Contains(ls, "blocked") {
		t.Fatalf("contacts after import: %s", ls)
	}

	// A list altered after signing is refused, loudly.
	tampered := aliceHome + "/tampered.json"
	runCLI(t, aliceHome, "blocks", "export", "--no-publish", "--out", tampered, "--json")
	raw, _ := os.ReadFile(tampered)
	var doc map[string]any
	_ = json.Unmarshal(raw, &doc)
	entries, _ := doc["entries"].([]any)
	doc["entries"] = append(entries, map[string]any{"identity": "innocent.example.test"})
	patched, _ := json.MarshalIndent(doc, "", "  ")
	_ = os.WriteFile(tampered, patched, 0o600)
	if code, _, stderr := runCLIFull(t, bobHome, "blocks", "import", "--file", tampered); code == 0 || !strings.Contains(stderr, "REFUSING TO IMPORT") {
		t.Fatalf("tampered list: %d %s", code, stderr)
	}
	if ls, _ := runCLI(t, bobHome, "contacts", "ls"); strings.Contains(ls, "innocent.example.test") {
		t.Fatalf("a name added after signing reached contacts: %s", ls)
	}
}
