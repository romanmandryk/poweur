package integration_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	clipkg "github.com/poweur/cli/pkg/cli"
)

// TestINT_ATTACHMENT_01 proves the envelope remains tiny while a 20 MB file
// crosses relays through DAV under the message-created read grant.
func TestINT_ATTACHMENT_01_TwentyMegabytesAcrossRelays(t *testing.T) {
	zone := newZone(t)
	_, addrA := newHostedRelay(t, zone, t.TempDir())
	_, addrB := newRelay(t, zone)
	relayA, relayB := "http://"+addrA, "http://"+addrB
	clipkg.ConfigureIdentityResolver("http", true, addrA)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	aliceHome, bobHome := t.TempDir(), t.TempDir()
	runCLI(t, aliceHome, "identity", "create", "attachalice", "--parent-domain", "poweur.net",
		"--relay", relayA, "--dns-provider", "mock", "--dns-token", "integration")
	runCLI(t, bobHome, "identity", "create", "attachbob", "--parent-domain", "example.org",
		"--relay", relayB, "--dns-provider", "mock", "--dns-token", "integration")

	want := bytes.Repeat([]byte("poweur-attachment\n"), (20*1024*1024)/18)
	want = append(want, bytes.Repeat([]byte{'x'}, 20*1024*1024-len(want))...)
	source := filepath.Join(t.TempDir(), "photo.jpg")
	if err := os.WriteFile(source, want, 0o600); err != nil {
		t.Fatal(err)
	}
	send, _ := runCLI(t, aliceHome, "send", "attachbob.example.org", "a large photo",
		"--attach", source, "--sign-with", "identity", "--json")
	if len(send) >= 512*1024 {
		t.Fatalf("attachment leaked into spool envelope: %d bytes", len(send))
	}

	inboxRaw, _ := runCLI(t, bobHome, "inbox", "--json")
	var inbox struct {
		Messages []struct {
			Type     string            `json:"type"`
			Metadata map[string]string `json:"metadata"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(inboxRaw)), &inbox); err != nil || len(inbox.Messages) != 1 {
		t.Fatalf("inbox: %v %s", err, inboxRaw)
	}
	message := inbox.Messages[0]
	if message.Type != "chat.attachment" {
		t.Fatalf("type=%q metadata=%v", message.Type, message.Metadata)
	}
	destination := filepath.Join(t.TempDir(), "download.jpg")
	runCLI(t, bobHome, "attachment", "get", message.Metadata["attachment_owner"],
		message.Metadata["attachment_path"], "--sha256", message.Metadata["attachment_sha256"], "--output", destination)
	got, err := os.ReadFile(destination)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("download mismatch: bytes=%d err=%v", len(got), err)
	}
	sum := sha256.Sum256(got)
	if hex.EncodeToString(sum[:]) != message.Metadata["attachment_sha256"] {
		t.Fatal("download hash does not match signed metadata")
	}
}
