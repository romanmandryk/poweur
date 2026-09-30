package integration_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/poweur/integration/refapps/docs"
)

// TestE26_T2_CollaborativeMarkdown is EPIC-026's Markdown documents scenario,
// run through `poweur` commands only (the table in the epic lists each one).
// Alice owns the document on relay A; Bob edits and Carol comments from relay
// B (the same relay in the same-relay topology); an anonymous reader opens a
// password link.
func TestE26_T2_CollaborativeMarkdown(t *testing.T) {
	forEachScenario(t, func(t *testing.T, s *scenario) {
		alice := s.newActor("docalice", "A")
		bob := s.newActor("docbob", "B")
		carol := s.newActor("doccarol", "B")
		reader := s.anonymous("reader")

		title := "Quarterly-Roadmap-5e1f"
		folder, doc, comments, assets := docs.Paths(title)
		const (
			intro   = "Intro: we ship the drive in October 7c2a."
			middle  = "Middle: the sync daemon follows 91be."
			closing = "Closing: questions to alice 3d40."
			asset   = "PNG-diagram-bytes-e6a9"
		)
		v1Text := "# Roadmap\n\n" + intro + "\n\n" + middle + "\n\n" + closing + "\n"
		s.secret(title, intro, middle, closing, asset)

		// Create the document, an asset and the comment log.
		alice.Run("drive", "mkdir", "/"+docs.Root, "--json")
		var folderNode map[string]string
		alice.JSON(&folderNode, "drive", "mkdir", folder)
		alice.Run("drive", "mkdir", assets, "--json")
		v1 := putVersion(t, alice, alice.File("doc.md", v1Text), doc)
		alice.Run("drive", "put", alice.File("diagram.png", asset), assets+"/diagram.png", "--json")
		first, _ := docs.NewComment(intro, "Which week in October? a8f3")
		s.secret(first.Text)
		alice.Run("drive", "append", comments, alice.File("c1.json", recordJSON(t, first)), "--json")

		// Share: Bob edits, Carol reads the folder and may append comments.
		alice.Run("drive", "share", "add", folder, bob.Identity, "--role", "write", "--json")
		alice.Run("drive", "share", "add", folder, carol.Identity, "--role", "read", "--json")
		alice.Run("drive", "share", "add", comments, carol.Identity, "--role", "append", "--json")
		bob.acceptOffers(alice.Identity)
		if n := carol.acceptOffers(alice.Identity); n != 2 {
			t.Fatalf("carol accepted %d offers", n)
		}
		memberDoc := "/" + folderNode["node"] + "/" + docs.DocFile
		memberComments := "/" + folderNode["node"] + "/" + docs.CommentsFile

		// Bob sees Alice's edit through the change stream, without polling.
		events, done := bob.watch(alice.Identity, 1, 20*time.Second)
		intro2 := "Intro: we ship the drive in the second week of October 7c2a."
		v2Text := strings.Replace(v1Text, intro, intro2, 1)
		v2 := putVersion(t, alice, alice.File("doc.md", v2Text), doc)
		select {
		case <-events:
		case <-time.After(20 * time.Second):
			t.Fatal("bob got no change event for alice's edit")
		}
		if code := <-done; code != 0 {
			t.Fatalf("bob's watch exited %d", code)
		}
		if got := getText(t, bob, memberDoc, "--drive", alice.Identity); got != v2Text {
			t.Fatalf("bob read %q", got)
		}

		// Concurrent edits to different paragraphs: Alice saves first; Bob's
		// save conflicts, he merges three-way and saves on the new head.
		aliceText := strings.Replace(v2Text, closing, "Closing: questions to alice or bob 3d40.", 1)
		v3 := putVersion(t, alice, alice.File("doc.md", aliceText), doc, "--base", v2)
		bobText := strings.Replace(v2Text, middle, "Middle: the sync daemon follows in November 91be.", 1)
		v4 := saveWithMerge(t, bob, bobText, memberDoc, v2, alice.Identity)
		merged := getText(t, alice, doc)
		if !strings.Contains(merged, "questions to alice or bob") || !strings.Contains(merged, "follows in November") || strings.Contains(merged, "<<<<<<<") {
			t.Fatalf("different-paragraph merge lost an edit:\n%s", merged)
		}
		_ = v3

		// The same paragraph on both sides: a conflict block keeps both.
		aliceIntro := strings.Replace(merged, intro2, "Intro: alice says week two 7c2a.", 1)
		putVersion(t, alice, alice.File("doc.md", aliceIntro), doc, "--base", v4)
		bobIntro := strings.Replace(merged, intro2, "Intro: bob says week three 7c2a.", 1)
		saveWithMerge(t, bob, bobIntro, memberDoc, v4, alice.Identity)
		both := getText(t, alice, doc)
		if !strings.Contains(both, "alice says week two") || !strings.Contains(both, "bob says week three") || !strings.Contains(both, "<<<<<<< ") {
			t.Fatalf("same-paragraph edits were not both kept:\n%s", both)
		}

		// Statelessness: the host relay restarts with only its store.
		s.restart(alice.Relay.name)

		// Carol reads the document and the comments, comments, and cannot edit.
		if got := getText(t, carol, memberDoc, "--drive", alice.Identity); got != both {
			t.Fatalf("carol read %q", got)
		}
		carolComment, _ := docs.NewComment("Closing", "Add a date for questions f02d")
		s.secret(carolComment.Text)
		carol.Run("drive", "append", memberComments, carol.File("c2.json", recordJSON(t, carolComment)), "--drive", alice.Identity, "--json")
		if code, _, stderr := carol.Try("drive", "put", carol.File("doc.md", "carol rewrites"), memberDoc, "--drive", alice.Identity); code == 0 || !strings.Contains(stderr, "403") {
			t.Fatalf("carol wrote doc.md: exit %d %s", code, stderr)
		}
		if got := commentsOf(t, carol, alice.Identity, memberComments, "--drive", alice.Identity); len(got) != 2 || got[1].Author != carol.Identity {
			t.Fatalf("carol's view of comments: %+v", got)
		}
		// Alice resolves Carol's comment.
		alice.Run("drive", "append", comments, alice.File("r2.json", recordJSON(t, docs.ResolveComment(carolComment.ID))), "--json")
		resolved := commentsOf(t, alice, alice.Identity, comments)
		if len(resolved) != 2 || resolved[0].Resolved || !resolved[1].Resolved || resolved[1].ResolvedBy != alice.Identity {
			t.Fatalf("comments after resolve: %+v", resolved)
		}

		// Versions: the first draft is still there and can be restored.
		var history struct {
			Versions []string `json:"versions"`
		}
		alice.JSON(&history, "drive", "history", doc)
		if !contains(history.Versions, v1) || !contains(history.Versions, v4) {
			t.Fatalf("history %v lacks v1 %s / v4 %s", history.Versions, v1, v4)
		}
		old := alice.Out("v1.md")
		alice.Run("drive", "get", doc, old, "--version", v1, "--json")
		if alice.Read(old) != v1Text {
			t.Fatalf("v1 read back as %q", alice.Read(old))
		}
		putVersion(t, alice, old, doc)
		if got := getText(t, alice, doc); got != v1Text {
			t.Fatalf("restore: %q", got)
		}

		// A password link opens the document with no account.
		const password = "correct horse 6b1d"
		var link map[string]string
		alice.JSON(&link, "drive", "link", "create", folder, "--password", password)
		s.secret(password, link["fragment"])
		out := reader.Out("doc.md")
		reader.Run("drive", "link", "get", link["url"], out, "--path", docs.DocFile, "--password", password, "--json")
		if reader.Read(out) != v1Text {
			t.Fatalf("link reader got %q", reader.Read(out))
		}
		if code, _, _ := reader.Try("drive", "link", "get", link["url"], reader.Out("x"), "--path", docs.DocFile, "--password", "wrong"); code == 0 {
			t.Fatal("a wrong password opened the link")
		}

		// Revoke Bob: he can no longer read, including what Alice writes next.
		var listed struct {
			Shares []map[string]any `json:"shares"`
		}
		alice.JSON(&listed, "drive", "share", "ls")
		bobShare := ""
		for _, share := range listed.Shares {
			if share["member"] == bob.Identity && share["revoked"] == nil {
				bobShare, _ = share["id"].(string)
			}
		}
		if bobShare == "" {
			t.Fatalf("no share for bob in %v", listed.Shares)
		}
		alice.Run("drive", "share", "rm", bobShare, "--json")
		afterText := v1Text + "\nAfter revocation 2f7c.\n"
		s.secret("After revocation 2f7c.")
		putVersion(t, alice, alice.File("doc.md", afterText), doc)
		if code, _, _ := bob.Try("drive", "get", memberDoc, bob.Out("doc.md"), "--drive", alice.Identity); code == 0 {
			t.Fatal("bob still reads after revocation")
		}
		if got := getText(t, carol, memberDoc, "--drive", alice.Identity); got != afterText {
			t.Fatalf("carol after bob's revocation read %q", got)
		}

		s.privacyScan()
	})
}

// putVersion puts local as remote and returns the new version.
func putVersion(t *testing.T, a *actor, local, remote string, extra ...string) string {
	t.Helper()
	var out map[string]string
	a.JSON(&out, append([]string{"drive", "put", local, remote}, extra...)...)
	if out["version"] == "" {
		t.Fatalf("put returned no version: %v", out)
	}
	return out["version"]
}

func getText(t *testing.T, a *actor, remote string, extra ...string) string {
	t.Helper()
	out := a.Out("get")
	a.Run(append([]string{"drive", "get", remote, out}, extra...)...)
	return a.Read(out)
}

// saveWithMerge is the editor's save: put on the version the edit started
// from; on a conflict (exit 3) merge base, ours and the new head, and put on
// the head. It returns the saved version.
func saveWithMerge(t *testing.T, a *actor, ours, remote, base, drive string) string {
	t.Helper()
	local := a.File("edit.md", ours)
	code, stdout, stderr := a.Try("drive", "put", local, remote, "--drive", drive, "--base", base, "--json")
	if code == 0 {
		t.Fatalf("%s: expected a conflict saving on %s", a.Name, base)
	}
	var conflict map[string]string
	if code != 3 || json.Unmarshal([]byte(stdout), &conflict) != nil || conflict["error"] != "conflict" || conflict["head"] == "" {
		t.Fatalf("%s: put --base: exit %d %s %s", a.Name, code, stdout, stderr)
	}
	baseFile := a.Out("base.md")
	a.Run("drive", "get", remote, baseFile, "--drive", drive, "--version", base)
	theirs := getText(t, a, remote, "--drive", drive)
	merged, _ := docs.Merge(a.Read(baseFile), ours, theirs, a.Identity, "latest")
	return putVersion(t, a, a.File("merged.md", merged), remote, "--drive", drive, "--base", conflict["head"])
}

func recordJSON(t *testing.T, r docs.Record) string {
	t.Helper()
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func commentsOf(t *testing.T, a *actor, owner, remote string, extra ...string) []docs.Comment {
	t.Helper()
	var records []struct {
		Author string `json:"author"`
		Text   string `json:"text"`
	}
	a.JSON(&records, append([]string{"drive", "tail", remote}, extra...)...)
	entries := make([]docs.Entry, 0, len(records))
	for _, r := range records {
		entries = append(entries, docs.Entry{Author: r.Author, Text: r.Text})
	}
	return docs.Reduce(owner, entries)
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}
