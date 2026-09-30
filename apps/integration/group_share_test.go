package integration_test

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestINT_GROUP_SHARE_01 (EPIC-024 E24-T3): folders shared with a group.
// Alice runs the group (its keys are on her device, relay A); Bob and Carol
// are members on relay B; Dave, outside the group on relay C, shares a folder
// with it. Members read and write with their own keys plus the group key;
// removing Carol locks her out after the key rotates; Eve, added later,
// reads old and new content.
func TestINT_GROUP_SHARE_01_FolderSharedWithAGroup(t *testing.T) {
	forEachScenario(t, func(t *testing.T, s *scenario) {
		alice := s.newActor("gsalice", "A")
		bob := s.newActor("gsbob", "B")
		carol := s.newActor("gscarol", "B")
		dave := s.newActor("gsdave", "C")
		eve := s.newActor("gseve", "C")
		crew := fmt.Sprintf("crew%d.poweur.net", time.Now().UnixNano()%1_000_000)
		s.zone.SetHost(crew, alice.Relay.cfg.RelayAddress)
		s.secret("Crew-Plans-2b8d", "group plan draft c9e1", "bob's group note 4f60", "dave's brief 71aa", "after carol left 0d3b")

		// The group: a Poweur ID with members, a group chat and group keys.
		alice.Run("group", "create", crew, "--member", alice.Identity, "--member", bob.Identity, "--member", carol.Identity, "--relay", alice.Relay.URL())
		asCrew := func(args ...string) string { return alice.Run(append(args, "--use-identity", crew)...) }

		// The group folder lives in the group's own drive, shared with the group.
		var folder map[string]string
		alice.JSON(&folder, "drive", "mkdir", "/Crew-Plans-2b8d", "--use-identity", crew)
		asCrew("drive", "put", alice.File("plan.md", "group plan draft c9e1\n"), "/Crew-Plans-2b8d/plan.md", "--json")
		asCrew("drive", "share", "add", "/Crew-Plans-2b8d", crew, "--role", "write", "--no-offer", "--json")
		groupPath := "/" + folder["node"]
		member := func(a *actor, args ...string) (int, string, string) {
			return a.Try(append(args, "--drive", crew, "--group", crew)...)
		}
		read := func(a *actor, path string, extra ...string) string {
			t.Helper()
			out := a.Out("read")
			a.Run(append([]string{"drive", "get", path, out}, extra...)...)
			return a.Read(out)
		}

		if got := read(bob, groupPath+"/plan.md", "--drive", crew, "--group", crew); got != "group plan draft c9e1\n" {
			t.Fatalf("bob read %q", got)
		}
		if code, _, stderr := member(bob, "drive", "put", bob.File("note.md", "bob's group note 4f60\n"), groupPath+"/note.md", "--json"); code != 0 {
			t.Fatalf("bob could not write to the group folder: %s", stderr)
		}
		if got := read(carol, groupPath+"/note.md", "--drive", crew, "--group", crew); got != "bob's group note 4f60\n" {
			t.Fatalf("carol read %q", got)
		}
		// Alice is an admin as well as a member: the relay shows her the whole
		// group drive, but her client opens only what the group key opens.
		if got := read(alice, groupPath+"/note.md", "--drive", crew, "--group", crew); got != "bob's group note 4f60\n" {
			t.Fatalf("alice as a member read %q", got)
		}
		// Without being in the group, the folder stays closed.
		if code, _, _ := dave.Try("drive", "get", groupPath+"/plan.md", dave.Out("x"), "--drive", crew, "--group", crew); code == 0 {
			t.Fatal("dave, outside the group, read the group folder")
		}

		// Dave, on another relay, shares his own folder with the group.
		var daveFolder map[string]string
		dave.JSON(&daveFolder, "drive", "mkdir", "/Brief")
		dave.Run("drive", "put", dave.File("brief.md", "dave's brief 71aa\n"), "/Brief/brief.md", "--json")
		dave.Run("drive", "share", "add", "/Brief", crew, "--role", "read", "--no-offer", "--json")
		davePath := "/" + daveFolder["node"] + "/brief.md"
		if got := read(bob, davePath, "--drive", dave.Identity, "--group", crew); got != "dave's brief 71aa\n" {
			t.Fatalf("bob read dave's folder as %q", got)
		}

		// Carol leaves the group. The group key moves to a new epoch without
		// her; the group folder rotates its keys before new content lands.
		alice.Run("group", "remove", crew, "--member", carol.Identity)
		asCrew("drive", "rotate", "/Crew-Plans-2b8d", "--json")
		asCrew("drive", "put", alice.File("later.md", "after carol left 0d3b\n"), "/Crew-Plans-2b8d/later.md", "--json")
		if code, _, _ := member(carol, "drive", "get", groupPath+"/later.md", carol.Out("x")); code == 0 {
			t.Fatal("carol read the group folder after leaving the group")
		}
		if got := read(bob, groupPath+"/later.md", "--drive", crew, "--group", crew); got != "after carol left 0d3b\n" {
			t.Fatalf("bob after the rotation read %q", got)
		}
		// Bob presents the new roster to Dave's relay: Carol's old one no
		// longer opens Dave's folder there.
		read(bob, davePath, "--drive", dave.Identity, "--group", crew)
		if code, _, _ := carol.Try("drive", "get", davePath, carol.Out("x"), "--drive", dave.Identity, "--group", crew); code == 0 {
			t.Fatal("carol read dave's group share after leaving the group")
		}

		// Eve joins later and reads what came before her, too.
		alice.Run("group", "add", crew, "--member", eve.Identity)
		for path, want := range map[string]string{
			groupPath + "/plan.md":  "group plan draft c9e1\n",
			groupPath + "/later.md": "after carol left 0d3b\n",
		} {
			if got := read(eve, path, "--drive", crew, "--group", crew); got != want {
				t.Fatalf("eve read %s as %q", path, got)
			}
		}
		if got := read(eve, davePath, "--drive", dave.Identity, "--group", crew); got != "dave's brief 71aa\n" {
			t.Fatalf("eve read dave's folder as %q", got)
		}
		if out := alice.Run("group", "show", crew); !strings.Contains(out, eve.Identity) || strings.Contains(out, carol.Identity) {
			t.Fatalf("group show:\n%s", out)
		}

		s.privacyScan()
	})
}
