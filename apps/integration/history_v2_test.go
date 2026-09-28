package integration_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	clipkg "github.com/poweur/cli/pkg/cli"
)

func TestINT_HISTORY_01_AppendLogSurvivesInboxDrain(t *testing.T) {
	zone := newZone(t)
	data := t.TempDir()
	ts, addr := newHostedRelay(t, zone, data)
	const alice, bob = "histalice.poweur.net", "histbob.poweur.net"
	zone.SetHost(alice, addr)
	zone.SetHost(bob, addr)
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })
	aliceHome, bobHome := t.TempDir(), t.TempDir()
	runCLI(t, aliceHome, "identity", "create", alice, "--hosted", "--relay", ts.URL, "--json")
	runCLI(t, bobHome, "identity", "create", bob, "--hosted", "--relay", ts.URL, "--json")

	const first, second = "history-secret-phrase", "history-second-line"
	runCLI(t, aliceHome, "send", bob, first)
	runCLI(t, aliceHome, "send", bob, second)
	runCLI(t, bobHome, "inbox")

	raw, _ := runCLI(t, bobHome, "history", "--json")
	var page struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal([]byte(raw), &page); err != nil || len(page.Messages) != 2 {
		t.Fatalf("bob history: %s %v", raw, err)
	}
	if page.Messages[0]["body"] != first || page.Messages[1]["body"] != second {
		t.Fatalf("order: %s", raw)
	}
	raw, _ = runCLI(t, aliceHome, "history", bob, "--limit", "1", "--json")
	if err := json.Unmarshal([]byte(raw), &page); err != nil || len(page.Messages) != 1 || page.Messages[0]["body"] != second {
		t.Fatalf("alice tail: %s %v", raw, err)
	}
	cursor, _ := page.Messages[0]["position"].(float64)
	// The sender's log cursor is on the message objects only when a peer was named.
	raw, _ = runCLI(t, aliceHome, "history", bob, "--before", "2", "--json")
	if err := json.Unmarshal([]byte(raw), &page); err != nil || len(page.Messages) != 1 || page.Messages[0]["body"] != first {
		t.Fatalf("before cursor %v: %s %v", cursor, raw, err)
	}
	// Picking the inbox up again must not duplicate the archive.
	runCLI(t, bobHome, "inbox")
	raw, _ = runCLI(t, bobHome, "history", alice, "--json")
	if err := json.Unmarshal([]byte(raw), &page); err != nil || len(page.Messages) != 2 {
		t.Fatalf("deduped history: %s %v", raw, err)
	}

	err := filepath.Walk(data, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(raw, []byte(first)) || bytes.Contains(raw, []byte(second)) {
			t.Errorf("plaintext history in %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
