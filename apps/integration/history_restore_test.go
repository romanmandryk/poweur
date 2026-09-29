package integration_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Phase 9 restore list (EPIC-020): the v1 history scenarios, against the
// encrypted drive archive (`.poweur/private/messages/<peer>.jsonl`).

type historyRecordJSON struct {
	ID        string `json:"id"`
	Sender    string `json:"sender"`
	Recipient string `json:"recipient"`
	Queue     string `json:"queue"`
	Body      string `json:"body"`
	ThreadID  string `json:"thread_id"`
}

type historyPageJSON struct {
	Messages []historyRecordJSON `json:"messages"`
	Unread   map[string]int      `json:"unread"`
}

func historyPage(t *testing.T, home, identity string, extra ...string) historyPageJSON {
	t.Helper()
	stdout, _ := runCLI(t, home, append([]string{"history", "--json", "--use-identity", identity}, extra...)...)
	var out historyPageJSON
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("history --json: %v\n%s", err, stdout)
	}
	return out
}

func historyBodies(page historyPageJSON) []string {
	out := make([]string, 0, len(page.Messages))
	for _, r := range page.Messages {
		out = append(out, r.Body)
	}
	return out
}

// freshDevice copies only an identity's keys and the CLI config into a new
// home: no local cache, so everything else must come from the drive.
func freshDevice(t *testing.T, from string) string {
	t.Helper()
	to := t.TempDir()
	src, dst := filepath.Join(from, ".poweur"), filepath.Join(to, ".poweur")
	if err := os.MkdirAll(filepath.Join(dst, "keys"), 0o700); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(src, "keys"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		raw, err := os.ReadFile(filepath.Join(src, "keys", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, "keys", entry.Name()), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(src, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "config.toml"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return to
}

// INT_HISTORY_02: the archive holds both sides of a conversation, in order,
// with direction recorded, and a fresh device reads the same thing.
func TestINT_HISTORY_02_BothSidesOfTheConversation(t *testing.T) {
	const alice, bob = "convalice.poweur.net", "convbob.poweur.net"
	relayURL, _ := journeyRelay(t, alice, bob)
	aliceHome, bobHome := t.TempDir(), t.TempDir()
	createIdentity(t, aliceHome, alice, relayURL)
	createIdentity(t, bobHome, bob, relayURL)

	runCLI(t, bobHome, "send", alice, "morning")
	runCLI(t, aliceHome, "inbox")
	runCLI(t, aliceHome, "send", bob, "morning yourself")
	runCLI(t, bobHome, "inbox")
	runCLI(t, bobHome, "send", alice, "coffee?")
	runCLI(t, aliceHome, "inbox")

	want := []string{"morning", "morning yourself", "coffee?"}
	got := historyPage(t, aliceHome, alice, "--keep-unread")
	if strings.Join(historyBodies(got), "|") != strings.Join(want, "|") {
		t.Fatalf("alice's history = %v, want %v", historyBodies(got), want)
	}
	if got.Messages[1].Queue != "sent" || got.Messages[1].Recipient != bob {
		t.Fatalf("outbound record: %+v", got.Messages[1])
	}
	if filtered := historyPage(t, aliceHome, alice, "--keep-unread", bob); len(filtered.Messages) != 3 {
		t.Fatalf("conversation with %s = %v", bob, historyBodies(filtered))
	}
	if fresh := historyPage(t, freshDevice(t, aliceHome), alice, "--keep-unread"); strings.Join(historyBodies(fresh), "|") != strings.Join(want, "|") {
		t.Fatalf("a fresh device saw %v", historyBodies(fresh))
	}
}

// INT_HISTORY_03: unread counts come from read marks, reach zero once read,
// stay zero on another device, and rise again with a new message.
func TestINT_HISTORY_03_UnreadCountsReachZero(t *testing.T) {
	const alice, bob = "unreadalice.poweur.net", "unreadbob.poweur.net"
	relayURL, _ := journeyRelay(t, alice, bob)
	aliceHome, bobHome := t.TempDir(), t.TempDir()
	createIdentity(t, aliceHome, alice, relayURL)
	createIdentity(t, bobHome, bob, relayURL)

	runCLI(t, bobHome, "send", alice, "one")
	runCLI(t, bobHome, "send", alice, "two")
	runCLI(t, aliceHome, "inbox")
	if unread := historyPage(t, aliceHome, alice, "--keep-unread").Unread; unread[bob] != 2 {
		t.Fatalf("two unread expected, got %v", unread)
	}
	runCLI(t, aliceHome, "history", "--use-identity", alice) // reading is reading
	if unread := historyPage(t, aliceHome, alice, "--keep-unread").Unread; unread[bob] != 0 {
		t.Fatalf("unread must clear once read, got %v", unread)
	}
	if unread := historyPage(t, freshDevice(t, aliceHome), alice, "--keep-unread").Unread; unread[bob] != 0 {
		t.Fatalf("read marks did not reach another device: %v", unread)
	}
	runCLI(t, bobHome, "send", alice, "three")
	runCLI(t, aliceHome, "inbox")
	if unread := historyPage(t, aliceHome, alice, "--keep-unread").Unread; unread[bob] != 1 {
		t.Fatalf("a new message must be unread again, got %v", unread)
	}
}

// INT_HISTORY_04: an anonymous message is archived as anonymous, never folded
// into a signed conversation, with its own unread count.
func TestINT_HISTORY_04_AnonymousStaysAnonymous(t *testing.T) {
	const rcpt, sender = "hanonrcpt.poweur.net", "hanonsend.poweur.net"
	relayURL, _ := journeyRelay(t, rcpt, sender)
	rcptHome, senderHome := t.TempDir(), t.TempDir()
	createIdentity(t, rcptHome, rcpt, relayURL)
	createIdentity(t, senderHome, sender, relayURL)

	runCLI(t, rcptHome, "policy", "set", "open", "--anon-allow", "--anon-challenge", "none")
	runCLI(t, senderHome, "send", rcpt, "signed and attributable")
	runCLI(t, senderHome, "send", rcpt, "unsigned and not", "--anon")
	runCLI(t, rcptHome, "inbox")
	runCLI(t, rcptHome, "anon")

	check := func(page historyPageJSON) {
		t.Helper()
		var signed, anon int
		for _, r := range page.Messages {
			switch r.Queue {
			case "inbox":
				signed++
				if r.Sender != sender {
					t.Fatalf("signed record lost its sender: %+v", r)
				}
			case "anonymous":
				anon++
				if r.Sender != "" {
					t.Fatalf("anonymous record claims a sender: %+v", r)
				}
			}
		}
		if signed != 1 || anon != 1 {
			t.Fatalf("want one of each queue, got %+v", page.Messages)
		}
		if page.Unread[sender] != 1 || page.Unread["anonymous"] != 1 {
			t.Fatalf("unread by conversation: %v", page.Unread)
		}
	}
	check(historyPage(t, rcptHome, rcpt, "--keep-unread"))
	// Anonymous history persists: another device reads it from the drive.
	check(historyPage(t, freshDevice(t, rcptHome), rcpt, "--keep-unread"))
}

// INT_TYPED_07: the archive keeps thread_id on both sides, so a reload
// regroups a conversation the way it was shown.
func TestINT_TYPED_07_ThreadSurvivesHistory(t *testing.T) {
	const alice, bob = "thralice.poweur.net", "thrbob.poweur.net"
	relayURL, _ := journeyRelay(t, alice, bob)
	aliceHome, bobHome := t.TempDir(), t.TempDir()
	createIdentity(t, aliceHome, alice, relayURL)
	createIdentity(t, bobHome, bob, relayURL)

	runCLI(t, aliceHome, "send", bob, "in the rebrand thread", "--thread", "thr_rebrand")
	if inbox, _ := runCLI(t, bobHome, "inbox"); !strings.Contains(inbox, "[thread thr_rebrand]") {
		t.Fatalf("inbox should mark the thread:\n%s", inbox)
	}
	for who, home := range map[string]string{bob: bobHome, alice: aliceHome} {
		page := historyPage(t, freshDevice(t, home), who, "--keep-unread", "--thread", "thr_rebrand")
		if len(page.Messages) != 1 || page.Messages[0].ThreadID != "thr_rebrand" {
			t.Fatalf("%s's archive lost the thread: %+v", who, page.Messages)
		}
	}
}
