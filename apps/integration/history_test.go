// Client-side message history (EPIC-009 E09-T1, client half): the archive
// under `poweur-sys/private/messages/`.
//
// The relay's inbox is a spool that drains on pickup, so "did the message
// survive?" is not a question about the relay at all — it is a question about
// whether the client wrote it down. These tests answer it the only way that
// counts: read the inbox, then throw the local state away, and look again.
package integration_test

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// historyJSON reads `poweur history --json` for an identity.
func historyJSON(t *testing.T, home, identity string, extra ...string) struct {
	Identity string `json:"identity"`
	Messages []struct {
		ID        string `json:"id"`
		Sender    string `json:"sender"`
		Recipient string `json:"recipient"`
		Timestamp string `json:"timestamp"`
		Type      string `json:"type"`
		Queue     string `json:"queue"`
		Body      string `json:"body"`
	} `json:"messages"`
	Unread map[string]int `json:"unread"`
} {
	t.Helper()
	args := append([]string{"history", "--json", "--use-identity", identity}, extra...)
	stdout, _ := runCLI(t, home, args...)
	var out struct {
		Identity string `json:"identity"`
		Messages []struct {
			ID        string `json:"id"`
			Sender    string `json:"sender"`
			Recipient string `json:"recipient"`
			Timestamp string `json:"timestamp"`
			Type      string `json:"type"`
			Queue     string `json:"queue"`
			Body      string `json:"body"`
		} `json:"messages"`
		Unread map[string]int `json:"unread"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("history --json: %v\n%s", err, stdout)
	}
	return out
}

func bodies(records []struct {
	ID        string `json:"id"`
	Sender    string `json:"sender"`
	Recipient string `json:"recipient"`
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Queue     string `json:"queue"`
	Body      string `json:"body"`
}) []string {
	out := make([]string, 0, len(records))
	for _, r := range records {
		out = append(out, r.Body)
	}
	return out
}

// TestINT_HISTORY_01_SurvivesDrainAndFreshDevice: the message is read once,
// the relay forgets it, the local home is thrown away — and it is still there.
//
// This is the exact shape of the bug it exists to prevent: a client that
// renders straight off the last pickup shows the message once and loses it on
// the next reload, with nothing left on the relay to re-fetch.
func TestINT_HISTORY_01_SurvivesDrainAndFreshDevice(t *testing.T) {
	const alice = "histalice.poweur.net"
	const bob = "histbob.poweur.net"
	relayURL, _ := journeyRelay(t, alice, bob)

	aliceHome := t.TempDir()
	bobHome := t.TempDir()
	createIdentity(t, aliceHome, alice, relayURL)
	createIdentity(t, bobHome, bob, relayURL)

	runCLI(t, bobHome, "send", alice, "the first thing bob said")
	runCLI(t, aliceHome, "inbox") // the one and only delivery

	// The relay has genuinely forgotten it: a second pickup is empty.
	stdout, _ := runCLI(t, aliceHome, "inbox")
	if strings.Contains(stdout, "the first thing bob said") {
		t.Fatalf("precondition: the relay was supposed to drain, got:\n%s", stdout)
	}

	// The archive still has it, with the conversation attributed correctly.
	got := historyJSON(t, aliceHome, alice, "--keep-unread")
	if len(got.Messages) != 1 {
		t.Fatalf("history after one message: %v", bodies(got.Messages))
	}
	record := got.Messages[0]
	if record.Body != "the first thing bob said" || record.Sender != bob || record.Queue != "inbox" {
		t.Fatalf("archived record: %+v", record)
	}

	// A brand new device, holding only the identity, reads the same history:
	// this is the property that makes the tree the right home for it.
	freshHome := t.TempDir()
	copyIdentity(t, aliceHome, freshHome, alice)
	rewriteRelayURL(t, freshHome, relayURL)
	fresh := historyJSON(t, freshHome, alice, "--keep-unread")
	if len(fresh.Messages) != 1 || fresh.Messages[0].Body != "the first thing bob said" {
		t.Fatalf("a second device saw %v", bodies(fresh.Messages))
	}
}

// TestINT_HISTORY_02_BothSidesOfTheConversation: an archive that holds only
// what arrived is half a conversation. The sender's own copy is the half the
// relay can never supply — it hands nobody their own message back.
func TestINT_HISTORY_02_BothSidesOfTheConversation(t *testing.T) {
	const alice = "convalice.poweur.net"
	const bob = "convbob.poweur.net"
	relayURL, _ := journeyRelay(t, alice, bob)

	aliceHome := t.TempDir()
	bobHome := t.TempDir()
	createIdentity(t, aliceHome, alice, relayURL)
	createIdentity(t, bobHome, bob, relayURL)

	runCLI(t, bobHome, "send", alice, "morning")
	runCLI(t, aliceHome, "inbox")
	runCLI(t, aliceHome, "send", bob, "morning yourself")
	runCLI(t, bobHome, "inbox")
	runCLI(t, bobHome, "send", alice, "coffee?")
	runCLI(t, aliceHome, "inbox")

	got := historyJSON(t, aliceHome, alice, "--keep-unread")
	want := []string{"morning", "morning yourself", "coffee?"}
	if len(got.Messages) != len(want) {
		t.Fatalf("alice's history = %v, want %v", bodies(got.Messages), want)
	}
	for i, body := range want {
		if got.Messages[i].Body != body {
			t.Fatalf("history out of order: %v, want %v", bodies(got.Messages), want)
		}
	}
	// Direction is recorded, not inferred: the middle one is hers.
	if got.Messages[1].Queue != "sent" || got.Messages[1].Recipient != bob {
		t.Fatalf("outbound record: %+v", got.Messages[1])
	}

	// Filtering to the conversation gives the same three — both directions
	// thread under one peer rather than two.
	filtered := historyJSON(t, aliceHome, alice, "--keep-unread", bob)
	if len(filtered.Messages) != 3 {
		t.Fatalf("conversation with %s = %v", bob, bodies(filtered.Messages))
	}
}

// TestINT_HISTORY_03_UnreadCountsReachZero: the badge bug, at the layer that
// decides it. A count of messages *held* can never reach zero; a count of
// messages newer than a read mark can, and must stay there.
func TestINT_HISTORY_03_UnreadCountsReachZero(t *testing.T) {
	const alice = "unreadalice.poweur.net"
	const bob = "unreadbob.poweur.net"
	relayURL, _ := journeyRelay(t, alice, bob)

	aliceHome := t.TempDir()
	bobHome := t.TempDir()
	createIdentity(t, aliceHome, alice, relayURL)
	createIdentity(t, bobHome, bob, relayURL)

	runCLI(t, bobHome, "send", alice, "one")
	runCLI(t, bobHome, "send", alice, "two")
	runCLI(t, aliceHome, "inbox")

	unread := historyJSON(t, aliceHome, alice, "--keep-unread").Unread
	if unread[bob] != 2 {
		t.Fatalf("two unread expected, got %v", unread)
	}

	// Reading the history is reading the messages.
	runCLI(t, aliceHome, "history", "--use-identity", alice)
	if unread := historyJSON(t, aliceHome, alice, "--keep-unread").Unread; unread[bob] != 0 {
		t.Fatalf("unread must clear once read, got %v", unread)
	}

	// It stays cleared across a reload — the mark lives in the tree, not in
	// whatever the process happened to remember.
	freshHome := t.TempDir()
	copyIdentity(t, aliceHome, freshHome, alice)
	rewriteRelayURL(t, freshHome, relayURL)
	if unread := historyJSON(t, freshHome, alice, "--keep-unread").Unread; unread[bob] != 0 {
		t.Fatalf("read marks did not survive to another device: %v", unread)
	}

	// A new message makes it non-zero again, which is the other half of a
	// badge being worth anything.
	runCLI(t, bobHome, "send", alice, "three")
	runCLI(t, aliceHome, "inbox")
	if unread := historyJSON(t, aliceHome, alice, "--keep-unread").Unread; unread[bob] != 1 {
		t.Fatalf("a new message must be unread again, got %v", unread)
	}
}

// TestINT_HISTORY_04_QueuesAreArchivedSeparately: an anonymous message is
// archived, and archived as anonymous. Collapsing it into the signed
// conversation would let an unsigned stranger appear as a known contact in
// the one place a person goes to check what was actually said.
func TestINT_HISTORY_04_AnonymousStaysAnonymous(t *testing.T) {
	const rcpt = "hanonrcpt.poweur.net"
	const sender = "hanonsend.poweur.net"
	relayURL, _ := journeyRelay(t, rcpt, sender)

	rcptHome := t.TempDir()
	senderHome := t.TempDir()
	createIdentity(t, rcptHome, rcpt, relayURL)
	createIdentity(t, senderHome, sender, relayURL)

	runCLI(t, rcptHome, "policy", "set", "open", "--anon-allow", "--anon-challenge", "none")
	runCLI(t, senderHome, "send", rcpt, "signed and attributable")
	runCLI(t, senderHome, "send", rcpt, "unsigned and not", "--anon")
	runCLI(t, rcptHome, "inbox")
	runCLI(t, rcptHome, "anon")

	got := historyJSON(t, rcptHome, rcpt, "--keep-unread")
	var signed, anon int
	for _, r := range got.Messages {
		switch r.Queue {
		case "inbox":
			signed++
			if r.Sender != sender {
				t.Fatalf("signed record lost its sender: %+v", r)
			}
		case "anonymous":
			anon++
			if r.Sender != "" {
				t.Fatalf("anonymous record must not claim a sender: %+v", r)
			}
		}
	}
	if signed != 1 || anon != 1 {
		t.Fatalf("want one of each queue, got %v", got.Messages)
	}
	// They are separate conversations, so the unread counts are separate too.
	if got.Unread[sender] != 1 || got.Unread["anonymous"] != 1 {
		t.Fatalf("unread by conversation: %v", got.Unread)
	}
}

// TestINT_HISTORY_05_SealedFromTheRelay: the archive lives in the owner-only
// zone, and the bytes there are sealed. "The relay must not read it" is a
// contract; this is the part that makes it a fact.
func TestINT_HISTORY_05_SealedFromTheRelay(t *testing.T) {
	const alice = "sealalice.poweur.net"
	const bob = "sealbob.poweur.net"
	relayURL, _ := journeyRelay(t, alice, bob)

	aliceHome := t.TempDir()
	bobHome := t.TempDir()
	createIdentity(t, aliceHome, alice, relayURL)
	createIdentity(t, bobHome, bob, relayURL)

	const secret = "the passphrase is hunter2"
	runCLI(t, bobHome, "send", alice, secret)
	runCLI(t, aliceHome, "inbox")

	// Find the archived file through the owner's own listing…
	tok := mintTokenViaCLI(t, aliceHome, "--use-identity", alice)
	resp := davDo(t, "PROPFIND", relayURL+"/dav/"+alice+"/poweur-sys/private/messages/", tok, nil,
		map[string]string{"Depth": "infinity"})
	listing := readAll(t, resp)
	if !strings.Contains(listing, "poweur-sys/private/messages") {
		t.Fatalf("the archive directory is not in the tree:\n%s", listing)
	}

	// …and confirm the stored bytes are an envelope, not the message.
	path := extractHistoryPath(t, listing)
	resp = davDo(t, http.MethodGet, relayURL+"/dav/"+alice+"/"+path, tok, nil, nil)
	stored := readAll(t, resp)
	if strings.Contains(stored, secret) {
		t.Fatalf("history is stored in the clear:\n%s", stored)
	}
	var sealed struct {
		Alg                string `json:"alg"`
		EphemeralPublicKey string `json:"ephemeral_public_key"`
		Nonce              string `json:"nonce"`
		Ciphertext         string `json:"ciphertext"`
	}
	if err := json.Unmarshal([]byte(stored), &sealed); err != nil {
		t.Fatalf("stored record is not a sealed document: %v\n%s", err, stored)
	}
	if sealed.Alg == "" || sealed.Ciphertext == "" || sealed.Nonce == "" {
		t.Fatalf("incomplete envelope: %+v", sealed)
	}

	// Bob, who is not the owner, cannot reach it at all.
	bobTok := mintTokenViaCLI(t, bobHome, "--use-identity", bob)
	resp = davDo(t, http.MethodGet, relayURL+"/dav/"+alice+"/"+path, bobTok, nil, nil)
	resp.Body.Close()
	if resp.StatusCode < 400 {
		t.Fatalf("another identity read the owner-only archive: HTTP %d", resp.StatusCode)
	}

	// And the owner still reads it perfectly.
	got := historyJSON(t, aliceHome, alice, "--keep-unread")
	if len(got.Messages) != 1 || got.Messages[0].Body != secret {
		t.Fatalf("owner cannot read their own archive: %v", bodies(got.Messages))
	}
}

// readAll drains and closes a response body.
func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// extractHistoryPath pulls one archived record's tree path out of a PROPFIND
// multistatus body.
func extractHistoryPath(t *testing.T, listing string) string {
	t.Helper()
	const marker = "poweur-sys/private/messages/"
	for _, chunk := range strings.Split(listing, "<D:href>") {
		href := chunk
		if i := strings.Index(href, "</D:href>"); i >= 0 {
			href = href[:i]
		}
		i := strings.Index(href, marker)
		if i < 0 || !strings.HasSuffix(href, ".json") {
			continue
		}
		return href[i:]
	}
	t.Fatalf("no archived record in the listing:\n%s", listing)
	return ""
}

// copyIdentity clones just the identity material — keys and config — into a
// new HOME, the way enrolling a second device would. Nothing about the
// messages comes with it, which is the point: what the new device can see is
// exactly what lives on the relay.
func copyIdentity(t *testing.T, from, to, identity string) {
	t.Helper()
	src := filepath.Join(from, ".poweur")
	dst := filepath.Join(to, ".poweur")
	if err := os.MkdirAll(filepath.Join(dst, "keys"), 0o700); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(src, "keys"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), identity) {
			continue
		}
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
}
