package integration_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	clipkg "github.com/poweur/cli/pkg/cli"
)

// INT_DRIVE_08 (E20-T7 revocation + rotation): alice shares a folder with
// bob (write) and dave (read). Bob writes there, then alice revokes him.
// `share rm` rotates the folder's keys: alice still verifies and reads
// bob's past version (his revoked share is evidence), writes resume, dave
// reads new content through his re-issued share, and bob's old keys open
// nothing.
func TestINT_DRIVE_08_RevokeRotates(t *testing.T) {
	zone := newZone(t)
	ts, addr := newHostedRelay(t, zone, t.TempDir())
	t.Cleanup(func() { ts.CloseClientConnections() })
	const alice, bob, dave = "rotalice.poweur.net", "rotbob.poweur.net", "rotdave.poweur.net"
	for _, id := range []string{alice, bob, dave} {
		zone.SetHost(id, addr)
	}
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })
	aliceHome, bobHome, daveHome := t.TempDir(), t.TempDir(), t.TempDir()
	runCLI(t, aliceHome, "identity", "create", alice, "--hosted", "--relay", ts.URL, "--json")
	runCLI(t, bobHome, "identity", "create", bob, "--hosted", "--relay", ts.URL, "--json")
	runCLI(t, daveHome, "identity", "create", dave, "--hosted", "--relay", ts.URL, "--json")

	write := func(name, text string) string {
		path := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	read := func(home string, args ...string) string {
		out := filepath.Join(t.TempDir(), "out")
		runCLI(t, home, append([]string{"drive", "get", args[0], out, "--json"}, args[1:]...)...)
		got, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		return string(got)
	}
	runCLI(t, aliceHome, "drive", "mkdir", "/lab", "--json")
	runCLI(t, aliceHome, "drive", "mkdir", "/lab/inner", "--json")
	runCLI(t, aliceHome, "drive", "put", write("deep.txt", "deep"), "/lab/inner/deep.txt", "--json")
	runCLI(t, aliceHome, "drive", "append", "/lab/journal.log", write("line", "before"), "--json")
	var bobShare, daveShare map[string]string
	raw, _ := runCLI(t, aliceHome, "drive", "share", "add", "/lab", bob, "--role", "write", "--no-offer", "--json")
	if err := json.Unmarshal([]byte(raw), &bobShare); err != nil {
		t.Fatal(err)
	}
	raw, _ = runCLI(t, aliceHome, "drive", "share", "add", "/lab", dave, "--role", "read", "--no-offer", "--json")
	if err := json.Unmarshal([]byte(raw), &daveShare); err != nil {
		t.Fatal(err)
	}
	lab := "/" + bobShare["node"]
	runCLI(t, bobHome, "drive", "put", write("bob.txt", "by bob"), lab+"/bob.txt", "--drive", alice, "--json")

	runCLI(t, aliceHome, "drive", "share", "rm", bobShare["id"], "--no-offer", "--json")
	if got := read(aliceHome, "/lab/bob.txt"); got != "by bob" {
		t.Fatalf("alice reads bob's version after revoking him: %q", got)
	}
	runCLI(t, aliceHome, "drive", "put", write("after.txt", "after"), "/lab/after.txt", "--json")
	runCLI(t, aliceHome, "drive", "append", "/lab/journal.log", write("line", "after"), "--json")
	raw, _ = runCLI(t, aliceHome, "drive", "tail", "/lab/journal.log", "--from", "1", "--json")
	var records []map[string]any
	if err := json.Unmarshal([]byte(raw), &records); err != nil || len(records) != 2 || records[0]["text"] != "before" || records[1]["text"] != "after" {
		t.Fatalf("journal across the rotation: %s %v", raw, err)
	}
	if got := read(aliceHome, "/lab/inner/deep.txt"); got != "deep" {
		t.Fatalf("re-keyed subtree: %q", got)
	}
	if got := read(daveHome, lab+"/after.txt", "--drive", alice); got != "after" {
		t.Fatalf("dave reads through his re-issued share: %q", got)
	}
	if code, _, _ := runCLIFull(t, bobHome, "drive", "get", lab+"/after.txt", filepath.Join(t.TempDir(), "x"), "--drive", alice, "--json"); code == 0 {
		t.Fatal("bob still reads after revocation")
	}
	// An explicit rotation works too, and dave's share follows it again.
	runCLI(t, aliceHome, "drive", "rotate", "/lab", "--json")
	if got := read(daveHome, lab+"/inner/deep.txt", "--drive", alice); got != "deep" {
		t.Fatalf("dave after a second rotation: %q", got)
	}
}
