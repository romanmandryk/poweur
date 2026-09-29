package integration_test

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	clipkg "github.com/poweur/cli/pkg/cli"
)

func syncWrite(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	// Distinct mtimes: an edit within the same clock tick must still count.
	later := time.Now().Add(time.Duration(len(content)) * time.Millisecond)
	_ = os.Chtimes(full, later, later)
}

func syncRead(root, rel string) (string, bool) {
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	return string(raw), err == nil
}

// TestINT_SYNC_01_TwoDevicesConverge (E20-T9 acceptance, restored on v2): two
// machines of one identity sync a folder through the encrypted drive. Edits
// propagate; the same Markdown note edited offline on both merges with both
// edits; a binary conflict yields exactly one conflicted copy; a delete
// reaches the other machine's trash; the store never holds the plaintext.
func TestINT_SYNC_01_TwoDevicesConverge(t *testing.T) {
	forEachScenario(t, func(t *testing.T, s *scenario) {
		laptop := s.newActor("syncer", "A")
		desktop := &actor{s: s, Name: "desktop", Identity: laptop.Identity, Home: cloneHomeAsNewDevice(t, laptop.Home), Relay: laptop.Relay}
		dirA, dirB := t.TempDir(), t.TempDir()
		s.secret("Notes-Folder-19ab", "hello from the laptop 5c3e", "meeting line by laptop", "meeting line by desktop", "binary-by-laptop", "binary-by-desktop")

		syncWrite(t, dirA, "Notes-Folder-19ab/hello.md", "hello from the laptop 5c3e\n")
		laptop.Run("sync", "run", dirA)
		desktop.Run("sync", "run", dirB)
		if got, _ := syncRead(dirB, "Notes-Folder-19ab/hello.md"); got != "hello from the laptop 5c3e\n" {
			t.Fatalf("desktop received %q", got)
		}

		// Offline on both, different lines of the same note.
		note := "# Meeting\nagenda\nmeeting line\ndecisions\n"
		syncWrite(t, dirA, "Notes-Folder-19ab/meeting.md", note)
		laptop.Run("sync", "run", dirA)
		desktop.Run("sync", "run", dirB)
		syncWrite(t, dirA, "Notes-Folder-19ab/meeting.md", strings.Replace(note, "meeting line", "meeting line by laptop", 1))
		syncWrite(t, dirB, "Notes-Folder-19ab/meeting.md", strings.Replace(note, "decisions", "decisions: meeting line by desktop", 1))
		laptop.Run("sync", "run", dirA)
		out := desktop.Run("sync", "run", dirB)
		if !strings.Contains(out, "merged: Notes-Folder-19ab/meeting.md") {
			t.Fatalf("desktop did not merge:\n%s", out)
		}
		laptop.Run("sync", "run", dirA)
		want := "# Meeting\nagenda\nmeeting line by laptop\ndecisions: meeting line by desktop\n"
		for _, dir := range []string{dirA, dirB} {
			if got, _ := syncRead(dir, "Notes-Folder-19ab/meeting.md"); got != want {
				t.Fatalf("%s has %q", dir, got)
			}
		}

		// A binary file edited on both: one conflicted copy, nothing lost.
		syncWrite(t, dirA, "Notes-Folder-19ab/photo.bin", "v0")
		laptop.Run("sync", "run", dirA)
		desktop.Run("sync", "run", dirB)
		syncWrite(t, dirA, "Notes-Folder-19ab/photo.bin", "binary-by-laptop")
		syncWrite(t, dirB, "Notes-Folder-19ab/photo.bin", "binary-by-desktop")
		laptop.Run("sync", "run", dirA)
		out = desktop.Run("sync", "run", dirB)
		if strings.Count(out, "CONFLICT:") != 1 {
			t.Fatalf("expected one conflict:\n%s", out)
		}
		laptop.Run("sync", "run", dirA)
		entries, _ := os.ReadDir(filepath.Join(dirA, "Notes-Folder-19ab"))
		copies := 0
		for _, e := range entries {
			if strings.Contains(e.Name(), "conflicted copy") {
				copies++
				if got, _ := syncRead(dirA, "Notes-Folder-19ab/"+e.Name()); got != "binary-by-desktop" {
					t.Fatalf("conflicted copy holds %q", got)
				}
			}
		}
		if got, _ := syncRead(dirA, "Notes-Folder-19ab/photo.bin"); copies != 1 || got != "binary-by-laptop" {
			t.Fatalf("copies=%d winner=%q", copies, got)
		}

		// Status is read-only and says nothing is pending.
		if out := laptop.Run("sync", "status", dirA); !strings.Contains(out, "remote: no changes") || strings.Contains(out, "local ") {
			t.Fatalf("status after convergence:\n%s", out)
		}

		// A delete reaches the other machine's trash.
		if err := os.Remove(filepath.Join(dirA, "Notes-Folder-19ab", "hello.md")); err != nil {
			t.Fatal(err)
		}
		laptop.Run("sync", "run", dirA)
		desktop.Run("sync", "run", dirB)
		if _, ok := syncRead(dirB, "Notes-Folder-19ab/hello.md"); ok {
			t.Fatal("the delete did not reach the desktop")
		}
		trashed, _ := filepath.Glob(filepath.Join(dirB, ".poweur-trash", "*", "Notes-Folder-19ab", "hello.md"))
		if len(trashed) != 1 {
			t.Fatal("the deleted note is not in the desktop's trash")
		}

		s.privacyScan()
	})
}

// TestINT_SYNC_02_SharedFolderAcrossRelays: a member syncs a folder shared
// with them from another relay; their edits and the owner's meet.
func TestINT_SYNC_02_SharedFolderAcrossRelays(t *testing.T) {
	forEachScenario(t, func(t *testing.T, s *scenario) {
		alice := s.newActor("syncowner", "A")
		bob := s.newActor("syncmember", "B")
		var folder map[string]string
		alice.JSON(&folder, "drive", "mkdir", "/Team-Shared-6d0f")
		alice.Run("drive", "share", "add", "/Team-Shared-6d0f", bob.Identity, "--role", "write", "--json")
		bob.acceptOffers(alice.Identity)

		dirA, dirB := t.TempDir(), t.TempDir()
		syncWrite(t, dirA, "Team-Shared-6d0f/todo.md", "- ship sync\n")
		alice.Run("sync", "run", dirA)
		bob.Run("sync", "run", dirB, "--drive", alice.Identity, "--folder", "/"+folder["node"])
		if got, _ := syncRead(dirB, "todo.md"); got != "- ship sync\n" {
			t.Fatalf("bob received %q", got)
		}
		syncWrite(t, dirB, "todo.md", "- ship sync\n- write docs (bob)\n")
		syncWrite(t, dirB, "notes/bob.txt", "from bob\n")
		bob.Run("sync", "run", dirB, "--drive", alice.Identity, "--folder", "/"+folder["node"])
		alice.Run("sync", "run", dirA)
		if got, _ := syncRead(dirA, "Team-Shared-6d0f/todo.md"); got != "- ship sync\n- write docs (bob)\n" {
			t.Fatalf("alice has %q", got)
		}
		if got, _ := syncRead(dirA, "Team-Shared-6d0f/notes/bob.txt"); got != "from bob\n" {
			t.Fatalf("alice lacks bob's new file: %q", got)
		}
		// The same root cannot be pointed at another drive by mistake.
		if code, _, stderr := bob.Try("sync", "run", dirB); code == 0 || !strings.Contains(stderr, "already syncs") {
			t.Fatalf("re-targeting a synced root: %d %s", code, stderr)
		}
	})
}

// TestINT_SYNC_03_WatchFollowsBothSides: `sync watch` uploads a local edit
// once it settles and applies a remote edit pushed by the change stream.
func TestINT_SYNC_03_WatchFollowsBothSides(t *testing.T) {
	forEachScenario(t, func(t *testing.T, s *scenario) {
		if s.storage.name != "fs" || s.topo.name != "same-relay" {
			t.Skip("one topology is enough for the watcher")
		}
		laptop := s.newActor("watcher", "A")
		desktop := &actor{s: s, Name: "desktop", Identity: laptop.Identity, Home: cloneHomeAsNewDevice(t, laptop.Home), Relay: laptop.Relay}
		dirA, dirB := t.TempDir(), t.TempDir()

		lines := startSyncWatch(t, s, laptop, dirA)
		waitLine(t, lines, "watching ")

		// A local edit on the watched side goes up by itself.
		syncWrite(t, dirA, "live.md", "typed on the laptop\n")
		waitLine(t, lines, "uploaded: live.md")
		desktop.Run("sync", "run", dirB)
		if got, _ := syncRead(dirB, "live.md"); got != "typed on the laptop\n" {
			t.Fatalf("desktop got %q", got)
		}
		// A remote edit comes down through the change stream.
		syncWrite(t, dirB, "live.md", "edited on the desktop\n")
		desktop.Run("sync", "run", dirB)
		waitLine(t, lines, "downloaded: live.md")
		if got, _ := syncRead(dirA, "live.md"); got != "edited on the desktop\n" {
			t.Fatalf("laptop got %q", got)
		}
	})
}

// startSyncWatch runs `poweur sync watch` as a in the background and returns
// its output lines. The harness lock is held until the watcher has read its
// configuration (HOME is process-wide).
func startSyncWatch(t *testing.T, s *scenario, a *actor, dir string) <-chan string {
	t.Helper()
	reader, writer := io.Pipe()
	lines := make(chan string, 64)
	s.mu.Lock()
	t.Setenv("HOME", a.Home)
	started := make(chan struct{})
	go func() {
		var stderr bytes.Buffer
		clipkg.Run([]string{"sync", "watch", dir, "--settle", "300ms", "--interval", "200ms", "--timeout", "6s"}, writer, &stderr)
		writer.Close()
	}()
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(reader)
		first := true
		for scanner.Scan() {
			if first {
				first = false
				close(started)
			}
			lines <- scanner.Text()
		}
	}()
	select {
	case <-started:
	case <-time.After(15 * time.Second):
		s.mu.Unlock()
		t.Fatal("sync watch never started")
	}
	s.mu.Unlock()
	// Let the watcher reach its timeout before the relays close, so no
	// stream is left open when they shut down.
	t.Cleanup(func() {
		for range lines {
		}
	})
	return lines
}

func waitLine(t *testing.T, lines <-chan string, want string) {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatalf("sync watch ended before %q", want)
			}
			if strings.Contains(line, want) {
				return
			}
		case <-deadline:
			t.Fatalf("no %q from sync watch", want)
		}
	}
}

// TestINT_DEVICES_02_SyncCursorVisibleToOwner (restored on v2): a sync run
// leaves the owner able to see how far that device got, in devices.json.
func TestINT_DEVICES_02_SyncCursorVisibleToOwner(t *testing.T) {
	forEachScenario(t, func(t *testing.T, s *scenario) {
		if s.storage.name != "fs" || s.topo.name != "same-relay" {
			t.Skip("the registry does not depend on topology")
		}
		t.Setenv("POWEUR_DEVICE_NAME", "sync box")
		t.Setenv("POWEUR_DEVICE_KIND", "agent")
		owner := s.newActor("devsync", "A")
		deviceID := deviceIDOf(t, owner.Home)
		dir := t.TempDir()
		syncWrite(t, dir, "note.txt", "synced")
		owner.Run("sync", "run", dir)
		var found bool
		for _, d := range devicesViaCLI(t, owner.Home, owner.Identity) {
			if d.ID != deviceID {
				continue
			}
			found = true
			if d.SyncCursor == "" || d.SyncedAt == "" {
				t.Fatalf("no sync position after a sync: %+v", d)
			}
			if d.Name != "sync box" {
				t.Fatalf("device name %q", d.Name)
			}
		}
		if !found {
			t.Fatal("the syncing device is not in the registry")
		}
	})
}
