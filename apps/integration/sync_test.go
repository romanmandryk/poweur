// Integration tests for file sync (EPIC-004): the changes journal +
// manifest + chunked upload endpoints driven through the real relay, and
// the `poweur sync` client converging two "laptops" (two temp dirs, one
// identity) with conflicted-copy handling.
package integration_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// copyDir clones $HOME/.poweur so a second CLI home acts as a second
// device of the same identity.
func copyPoweurHome(t *testing.T, fromHome, toHome string) {
	t.Helper()
	src := filepath.Join(fromHome, ".poweur")
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		dst := filepath.Join(toHome, ".poweur", rel)
		if info.IsDir() {
			return os.MkdirAll(dst, 0o700)
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, raw, info.Mode().Perm())
	})
	if err != nil {
		t.Fatal(err)
	}
}

func writeFileP(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFileP(t *testing.T, root, rel string) (string, bool) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return "", false
	}
	return string(raw), true
}

// TestINT_SYNC_01: two devices of one identity converge through a relay —
// create, edit, delete propagate; a concurrent edit surfaces as a
// conflicted copy on the later syncer with no data loss (E04-T4
// acceptance).
func TestINT_SYNC_01_TwoDevicesConverge(t *testing.T) {
	zone := newZone(t)
	ts, _ := newHostedRelay(t, zone, t.TempDir())
	defer ts.Close()
	relayURL := ts.URL

	homeA := t.TempDir()
	runCLI(t, homeA, "identity", "create", "syncer.poweur.net", "--hosted", "--relay", relayURL, "--json")
	homeB := t.TempDir()
	copyPoweurHome(t, homeA, homeB)

	dirA := t.TempDir()
	dirB := t.TempDir()

	// Device A creates a file and syncs; device B receives it.
	writeFileP(t, dirA, "private/notes/hello.txt", "hello from A")
	runCLI(t, homeA, "sync", "run", dirA)
	runCLI(t, homeB, "sync", "run", dirB)
	if got, ok := readFileP(t, dirB, "private/notes/hello.txt"); !ok || got != "hello from A" {
		t.Fatalf("B must receive A's file: %q ok=%v", got, ok)
	}

	// B edits; A receives the edit.
	writeFileP(t, dirB, "private/notes/hello.txt", "edited on B")
	runCLI(t, homeB, "sync", "run", dirB)
	runCLI(t, homeA, "sync", "run", dirA)
	if got, _ := readFileP(t, dirA, "private/notes/hello.txt"); got != "edited on B" {
		t.Fatalf("A must receive B's edit: %q", got)
	}

	// Concurrent edits: A syncs first (wins the name), B keeps its version
	// as a conflicted copy and pushes it — nothing is lost anywhere.
	writeFileP(t, dirA, "private/notes/hello.txt", "concurrent A")
	writeFileP(t, dirB, "private/notes/hello.txt", "concurrent B")
	runCLI(t, homeA, "sync", "run", dirA)
	stdout, _ := runCLI(t, homeB, "sync", "run", dirB)
	if !strings.Contains(stdout, "CONFLICT") {
		t.Fatalf("B must report a conflict:\n%s", stdout)
	}
	if got, _ := readFileP(t, dirB, "private/notes/hello.txt"); got != "concurrent A" {
		t.Fatalf("winner on B must be A's version: %q", got)
	}
	entries, err := os.ReadDir(filepath.Join(dirB, "private", "notes"))
	if err != nil {
		t.Fatal(err)
	}
	var conflicted string
	for _, e := range entries {
		if strings.Contains(e.Name(), "conflicted copy") {
			conflicted = e.Name()
		}
	}
	if conflicted == "" {
		t.Fatalf("conflicted copy missing in %v", entries)
	}
	// A pulls and sees both the winner and the conflicted copy.
	runCLI(t, homeA, "sync", "run", dirA)
	if got, ok := readFileP(t, dirA, "private/notes/"+conflicted); !ok || got != "concurrent B" {
		t.Fatalf("conflicted copy must reach A: %q ok=%v", got, ok)
	}

	// Delete propagates.
	if err := os.RemoveAll(filepath.Join(dirA, "private", "notes")); err != nil {
		t.Fatal(err)
	}
	runCLI(t, homeA, "sync", "run", dirA)
	runCLI(t, homeB, "sync", "run", dirB)
	if _, err := os.Stat(filepath.Join(dirB, "private", "notes")); !os.IsNotExist(err) {
		t.Fatal("B must see A's directory delete")
	}
}

// TestINT_SYNC_02: the sync status command reports pending work without
// changing either side.
func TestINT_SYNC_02_Status(t *testing.T) {
	zone := newZone(t)
	ts, _ := newHostedRelay(t, zone, t.TempDir())
	defer ts.Close()

	home := t.TempDir()
	runCLI(t, home, "identity", "create", "statuser.poweur.net", "--hosted", "--relay", ts.URL, "--json")
	dir := t.TempDir()

	writeFileP(t, dir, "private/pending.txt", "unpushed")
	stdout, _ := runCLI(t, home, "sync", "status", dir)
	if !strings.Contains(stdout, "local new: private/pending.txt") {
		t.Fatalf("status must list the new local file:\n%s", stdout)
	}
	if !strings.Contains(stdout, "full resync required") {
		t.Fatalf("first-run status must flag the resync:\n%s", stdout)
	}
	if _, ok := readFileP(t, dir, ".poweur-sync.json"); ok {
		// Status must not have created state (it is read-only).
		t.Fatal("status must not write the state DB")
	}
}
