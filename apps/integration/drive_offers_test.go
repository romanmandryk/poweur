package integration_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	clipkg "github.com/poweur/cli/pkg/cli"
)

// offerBody picks up home's inbox and returns the decrypted body of the
// newest message of msgType from peer, as the history records it.
func offerBody(t *testing.T, home, peer, msgType string) string {
	t.Helper()
	runCLI(t, home, "inbox")
	raw, _ := runCLI(t, home, "history", peer, "--json")
	var out struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("history: %v\n%s", err, raw)
	}
	for i := len(out.Messages) - 1; i >= 0; i-- {
		m := out.Messages[i]
		if body, ok := m["body"].(string); ok && m["type"] == msgType {
			return body
		}
	}
	t.Fatalf("no %s from %s in history:\n%s", msgType, peer, raw)
	return ""
}

// INT_DRIVE_07 (PCP-0008): sharing sends bob an encrypted offer; accepting
// it opens the node through the share, mounts it in bob's own encrypted
// drive and tells alice; revoking tells bob, whose share then stops working.
func TestINT_DRIVE_07_ShareOffersAndMounts(t *testing.T) {
	zone := newZone(t)
	ts, addr := newHostedRelay(t, zone, t.TempDir())
	const alice, bob = "offeralice.poweur.net", "offerbob.poweur.net"
	zone.SetHost(alice, addr)
	zone.SetHost(bob, addr)
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })
	aliceHome, bobHome := t.TempDir(), t.TempDir()
	runCLI(t, aliceHome, "identity", "create", alice, "--hosted", "--relay", ts.URL, "--json")
	runCLI(t, bobHome, "identity", "create", bob, "--hosted", "--relay", ts.URL, "--json")
	runCLI(t, aliceHome, "contacts", "add", bob)
	runCLI(t, bobHome, "contacts", "add", alice)

	doc := filepath.Join(t.TempDir(), "plan.txt")
	if err := os.WriteFile(doc, []byte("the plan"), 0600); err != nil {
		t.Fatal(err)
	}
	runCLI(t, aliceHome, "drive", "mkdir", "/Plans", "--json")
	runCLI(t, aliceHome, "drive", "put", doc, "/Plans/plan.txt", "--json")
	raw, _ := runCLI(t, aliceHome, "drive", "share", "add", "/Plans", bob, "--role", "read", "--json")
	var share map[string]string
	if err := json.Unmarshal([]byte(raw), &share); err != nil {
		t.Fatal(err)
	}

	offer := offerBody(t, bobHome, alice, "sys.share.offer")
	offerFile := filepath.Join(t.TempDir(), "offer.json")
	if err := os.WriteFile(offerFile, []byte(offer), 0600); err != nil {
		t.Fatal(err)
	}
	raw, _ = runCLI(t, bobHome, "drive", "accept", offerFile, "--json")
	var mount map[string]string
	if err := json.Unmarshal([]byte(raw), &mount); err != nil || mount["name"] != "Plans" || mount["node"] != share["node"] || mount["drive"] != alice {
		t.Fatalf("accept: %s %v", raw, err)
	}
	raw, _ = runCLI(t, bobHome, "drive", "mounts", "--json")
	if !strings.Contains(raw, share["node"]) {
		t.Fatalf("mounts: %s", raw)
	}
	// The mount lives in bob's own drive, encrypted: his listing shows it.
	raw, _ = runCLI(t, bobHome, "drive", "list", "/.poweur/private", "--json")
	if !strings.Contains(raw, "mounts.json") {
		t.Fatalf("bob's private folder: %s", raw)
	}
	// Through the mount bob reads the shared folder.
	out := filepath.Join(t.TempDir(), "read.txt")
	runCLI(t, bobHome, "drive", "get", "/"+mount["node"]+"/plan.txt", out, "--drive", alice, "--json")
	if got, _ := os.ReadFile(out); string(got) != "the plan" {
		t.Fatalf("read through the mount: %q", got)
	}
	if !strings.Contains(offerBody(t, aliceHome, bob, "sys.share.accept"), share["id"]) {
		t.Fatal("alice was not told")
	}

	// A forged offer (a share bob made up, signed by bob) is refused.
	var forged map[string]any
	_ = json.Unmarshal([]byte(offer), &forged)
	forged["share"].(map[string]any)["role"] = "write"
	forgedRaw, _ := json.Marshal(forged)
	if err := os.WriteFile(offerFile, forgedRaw, 0600); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := runCLIFull(t, bobHome, "drive", "accept", offerFile, "--json"); code == 0 {
		t.Fatal("a tampered offer was accepted")
	}

	runCLI(t, aliceHome, "drive", "share", "rm", share["id"], "--json")
	if !strings.Contains(offerBody(t, bobHome, alice, "sys.share.revoked"), share["id"]) {
		t.Fatal("bob was not told about the revocation")
	}
	if code, _, _ := runCLIFull(t, bobHome, "drive", "get", "/"+mount["node"]+"/plan.txt", out, "--drive", alice, "--json"); code == 0 {
		t.Fatal("read after revocation")
	}
}
