package integration_test

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	clipkg "github.com/poweur/cli/pkg/cli"
)

func TestINT_DRIVE_06_Phase8Client(t *testing.T) {
	zone := newZone(t)
	data := t.TempDir()
	ts, addr := newHostedRelay(t, zone, data)
	t.Cleanup(func() { ts.CloseClientConnections() })
	const alice, bob = "phasealice.poweur.net", "phasebob.poweur.net"
	zone.SetHost(alice, addr)
	zone.SetHost(bob, addr)
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })
	aliceHome, bobHome := t.TempDir(), t.TempDir()
	runCLI(t, aliceHome, "identity", "create", alice, "--hosted", "--relay", ts.URL, "--json")
	runCLI(t, bobHome, "identity", "create", bob, "--hosted", "--relay", ts.URL, "--json")

	line := filepath.Join(t.TempDir(), "line.txt")
	if err := os.WriteFile(line, []byte("secret-log-line"), 0600); err != nil {
		t.Fatal(err)
	}
	runCLI(t, aliceHome, "drive", "append", "/notes.log", line, "--json")
	if err := os.WriteFile(line, []byte("second-line"), 0600); err != nil {
		t.Fatal(err)
	}
	runCLI(t, aliceHome, "drive", "append", "/notes.log", line, "--json")
	raw, _ := runCLI(t, aliceHome, "drive", "tail", "/notes.log", "--from", "1", "--json")
	var records []map[string]any
	if err := json.Unmarshal([]byte(raw), &records); err != nil || len(records) != 2 || records[0]["text"] != "secret-log-line" || records[1]["text"] != "second-line" {
		t.Fatalf("tail: %s %v", raw, err)
	}

	doc := filepath.Join(t.TempDir(), "doc.txt")
	if err := os.WriteFile(doc, []byte("hello world"), 0600); err != nil {
		t.Fatal(err)
	}
	runCLI(t, aliceHome, "drive", "put", doc, "/doc.txt", "--json")
	ranged := filepath.Join(t.TempDir(), "ranged.txt")
	runCLI(t, aliceHome, "drive", "get", "/doc.txt", ranged, "--offset", "6", "--length", "5", "--json")
	got, err := os.ReadFile(ranged)
	if err != nil || string(got) != "world" {
		t.Fatalf("range read: %q %v", got, err)
	}
	raw, _ = runCLI(t, aliceHome, "drive", "history", "/doc.txt", "--json")
	var history struct {
		Versions []string `json:"versions"`
	}
	if err := json.Unmarshal([]byte(raw), &history); err != nil || len(history.Versions) == 0 {
		t.Fatalf("history: %s %v", raw, err)
	}

	raw, _ = runCLI(t, aliceHome, "drive", "link", "create", "/doc.txt", "--role", "read", "--json")
	var link map[string]string
	if err := json.Unmarshal([]byte(raw), &link); err != nil || len(link["fragment"]) < 40 || link["link"] == "" {
		t.Fatalf("link: %s %v", raw, err)
	}
	raw, _ = runCLI(t, aliceHome, "drive", "share", "add", "/doc.txt", bob, "--role", "read", "--json")
	var share map[string]string
	if err := json.Unmarshal([]byte(raw), &share); err != nil || share["member"] != bob || share["id"] == "" {
		t.Fatalf("share: %s %v", raw, err)
	}
	raw, _ = runCLI(t, aliceHome, "drive", "share", "ls", "--json")
	if !bytes.Contains([]byte(raw), []byte(share["id"])) {
		t.Fatalf("share list: %s", raw)
	}
	// Bob reads the shared file with his own keys, through his share.
	raw, _ = runCLI(t, bobHome, "drive", "shared", "--drive", alice, "--json")
	var shared []map[string]any
	if err := json.Unmarshal([]byte(raw), &shared); err != nil || len(shared) != 1 || shared[0]["node"] != share["node"] {
		t.Fatalf("bob's shared nodes: %s %v", raw, err)
	}
	bobCopy := filepath.Join(t.TempDir(), "bob-doc.txt")
	runCLI(t, bobHome, "drive", "get", "/"+share["node"], bobCopy, "--drive", alice, "--json")
	if got, err := os.ReadFile(bobCopy); err != nil || string(got) != "hello world" {
		t.Fatalf("bob reads the shared file: %q %v", got, err)
	}
	// Read does not write.
	if code, _, _ := runCLIFull(t, bobHome, "drive", "put", doc, "/"+share["node"], "--drive", alice, "--json"); code == 0 {
		t.Fatal("a reader replaced the shared file")
	}
	runCLI(t, aliceHome, "drive", "share", "rm", share["id"], "--json")
	if code, _, _ := runCLIFull(t, bobHome, "drive", "get", "/"+share["node"], bobCopy, "--drive", alice, "--json"); code == 0 {
		t.Fatal("bob still reads after revocation")
	}

	// A writer edits inside a shared folder; the owner's client verifies
	// that bob's version was allowed by the share before trusting it.
	runCLI(t, aliceHome, "drive", "mkdir", "/team", "--json")
	runCLI(t, aliceHome, "drive", "put", doc, "/team/plan.txt", "--json")
	raw, _ = runCLI(t, aliceHome, "drive", "share", "add", "/team", bob, "--role", "write", "--json")
	var team map[string]string
	if err := json.Unmarshal([]byte(raw), &team); err != nil {
		t.Fatal(err)
	}
	plan := filepath.Join(t.TempDir(), "plan.txt")
	if err := os.WriteFile(plan, []byte("bob's plan"), 0600); err != nil {
		t.Fatal(err)
	}
	runCLI(t, bobHome, "drive", "put", plan, "/"+team["node"]+"/plan.txt", "--drive", alice, "--json")
	runCLI(t, bobHome, "drive", "put", plan, "/"+team["node"]+"/notes.txt", "--drive", alice, "--json")
	for _, name := range []string{"plan.txt", "notes.txt"} {
		out := filepath.Join(t.TempDir(), name)
		runCLI(t, aliceHome, "drive", "get", "/team/"+name, out, "--json")
		if got, err := os.ReadFile(out); err != nil || string(got) != "bob's plan" {
			t.Fatalf("alice reads bob's %s: %q %v", name, got, err)
		}
	}

	raw, _ = runCLI(t, aliceHome, "drive", "trim", "/notes.log", "/notes.snap", "--json")
	var snap struct {
		Through float64 `json:"through"`
	}
	if err := json.Unmarshal([]byte(raw), &snap); err != nil || snap.Through != 2 {
		t.Fatalf("trim: %s %v", raw, err)
	}

	move := filepath.Join(t.TempDir(), "move.txt")
	if err := os.WriteFile(move, []byte("ship it"), 0600); err != nil {
		t.Fatal(err)
	}
	runCLI(t, aliceHome, "drive", "put", move, "/move.txt", "--json")
	// Bob lets alice write into his inbox; she moves her file there as a
	// member of his drive, with her own keys only.
	runCLI(t, bobHome, "drive", "mkdir", "/inbox", "--json")
	raw, _ = runCLI(t, bobHome, "drive", "share", "add", "/inbox", alice, "--role", "write", "--json")
	var inbox map[string]string
	if err := json.Unmarshal([]byte(raw), &inbox); err != nil {
		t.Fatal(err)
	}
	runCLI(t, aliceHome, "drive", "transfer", "/move.txt", "--to", bob, "--into", "/"+inbox["node"], "--json")
	for _, name := range []string{bob + ".key", bob + ".enc"} {
		if _, err := os.Stat(filepath.Join(aliceHome, ".poweur", "keys", name)); !os.IsNotExist(err) {
			t.Fatalf("alice's home holds bob's %s", name)
		}
	}
	copied := filepath.Join(t.TempDir(), "copied.txt")
	runCLI(t, bobHome, "drive", "get", "/inbox/move.txt", copied, "--json")
	got, err = os.ReadFile(copied)
	if err != nil || string(got) != "ship it" {
		t.Fatalf("transferred file: %q %v", got, err)
	}
	if code, _, stderr := runCLIFull(t, aliceHome, "drive", "get", "/move.txt", copied, "--json"); code == 0 || (!bytes.Contains([]byte(stderr), []byte("not found")) && !bytes.Contains([]byte(stderr), []byte("moved"))) {
		t.Fatalf("retired source still readable: %s", stderr)
	}

	var watch lockedBuffer
	t.Setenv("HOME", aliceHome)
	go clipkg.Run([]string{"drive", "watch", "--json"}, &watch, io.Discard)
	deadline := time.Now().Add(5 * time.Second)
	for !bytes.Contains(watch.snapshot(), []byte(`"ready"`)) {
		if time.Now().After(deadline) {
			t.Fatalf("drive stream did not open: %s", watch.snapshot())
		}
		time.Sleep(20 * time.Millisecond)
	}
	runCLI(t, aliceHome, "drive", "mkdir", "/watched", "--json")
	deadline = time.Now().Add(5 * time.Second)
	for !bytes.Contains(watch.snapshot(), []byte(`"drive.changed"`)) {
		if time.Now().After(deadline) {
			t.Fatalf("no drive.changed event: %s", watch.snapshot())
		}
		time.Sleep(20 * time.Millisecond)
	}

	err = filepath.Walk(data, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(raw, []byte("secret-log-line")) || bytes.Contains(raw, []byte("ship it")) {
			t.Errorf("plaintext in %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuffer) snapshot() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]byte(nil), l.b.Bytes()...)
}
