package integration_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestE31_T1_TrivialScenario is the harness acceptance: share a file, the
// member edits it, the owner reads it back — in every topology and provider,
// across a restart of the host relay with its caches gone, with the privacy
// scan clean, and the scanner catching a deliberately planted plaintext.
func TestE31_T1_TrivialScenario(t *testing.T) {
	forEachScenario(t, func(t *testing.T, s *scenario) {
		alice := s.newActor("t1alice", "A")
		bob := s.newActor("t1bob", "B")
		const folderName, first, edited = "harness-folder-4c1e", "harness first draft 91a7", "harness edit by bob 5d02"
		s.secret(folderName, first, edited)

		var folder map[string]string
		alice.JSON(&folder, "drive", "mkdir", "/"+folderName)
		alice.Run("drive", "put", alice.File("note.txt", first), "/"+folderName+"/note.txt", "--json")
		alice.Run("drive", "share", "add", "/"+folderName, bob.Identity, "--role", "write", "--json")
		bob.acceptOffers(alice.Identity)

		// Bob works in the shared folder through its node id.
		shared := "/" + folder["node"] + "/note.txt"
		out := bob.Out("note.txt")
		bob.Run("drive", "get", shared, out, "--drive", alice.Identity, "--json")
		if got := bob.Read(out); got != first {
			t.Fatalf("bob read %q", got)
		}

		// A watcher on the host drive sees Bob's commit, relay to relay.
		events, done := alice.watch("", 1, 20*time.Second)
		bob.Run("drive", "put", bob.File("note.txt", edited), shared, "--drive", alice.Identity, "--json")
		select {
		case event := <-events:
			if event["type"] == nil {
				t.Fatalf("event without type: %v", event)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("no change event for bob's edit")
		}
		if code := <-done; code != 0 {
			t.Fatalf("watch exited %d", code)
		}

		// Statelessness: the host relay restarts with nothing but its store.
		// A stream open across the restart is dropped; a new one works.
		_, dropped := bob.watch(alice.Identity, 1, 20*time.Second)
		s.restart(alice.Relay.name)
		select {
		case code := <-dropped:
			if code == 0 {
				t.Fatal("a stream cut by the restart reported success")
			}
		case <-time.After(20 * time.Second):
			t.Fatal("the stream survived a relay restart")
		}
		events, done = bob.watch(alice.Identity, 1, 20*time.Second)
		alice.Run("drive", "put", alice.File("ping.txt", "ping"), "/"+folderName+"/ping.txt", "--json")
		select {
		case <-events:
		case <-time.After(20 * time.Second):
			t.Fatal("no event on the stream reopened after the restart")
		}
		if code := <-done; code != 0 {
			t.Fatalf("reopened watch exited %d", code)
		}
		out = alice.Out("note.txt")
		alice.Run("drive", "get", "/"+folderName+"/note.txt", out, "--json")
		if got := alice.Read(out); got != edited {
			t.Fatalf("alice read %q after bob's edit and a restart", got)
		}

		s.privacyScan()

		// The scanner itself: a planted plaintext object must be reported.
		if s.storage.name == "fs" {
			planted := filepath.Join(alice.Relay.cfg.DataDir, "drives", "planted", "leak")
			if err := os.MkdirAll(filepath.Dir(planted), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(planted, []byte("oops "+first), 0o600); err != nil {
				t.Fatal(err)
			}
			findings := s.scanFindings()
			if len(findings) != 1 || !strings.Contains(findings[0], "drives/planted/leak") {
				t.Fatalf("scanner missed the planted plaintext: %v", findings)
			}
		}
	})
}
