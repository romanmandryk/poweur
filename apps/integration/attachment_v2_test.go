package integration_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	clipkg "github.com/poweur/cli/pkg/cli"
)

func TestINT_HISTORY_02_AttachmentIsCiphertext(t *testing.T) {
	zone := newZone(t)
	data := t.TempDir()
	ts, addr := newHostedRelay(t, zone, data)
	const alice, bob = "attachalice.poweur.net", "attachbob.poweur.net"
	zone.SetHost(alice, addr)
	zone.SetHost(bob, addr)
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })
	aliceHome, bobHome := t.TempDir(), t.TempDir()
	runCLI(t, aliceHome, "identity", "create", alice, "--hosted", "--relay", ts.URL, "--json")
	runCLI(t, bobHome, "identity", "create", bob, "--hosted", "--relay", ts.URL, "--json")

	dir := t.TempDir()
	secret := filepath.Join(dir, "secret-name.bin")
	const body = "attachment-secret-bytes"
	if err := os.WriteFile(secret, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	runCLI(t, aliceHome, "send", bob, "see-attached-caption", "--attach", secret)
	inbox, _ := runCLI(t, bobHome, "inbox")
	if !bytes.Contains([]byte(inbox), []byte("secret-name.bin")) || !bytes.Contains([]byte(inbox), []byte("see-attached-caption")) {
		t.Fatalf("inbox did not open the attachment label:\n%s", inbox)
	}
	raw, _ := runCLI(t, bobHome, "history", alice, "--json")
	var page struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(raw), &page); err != nil || len(page.Messages) != 1 || page.Messages[0].ID == "" {
		t.Fatalf("history: %s %v", raw, err)
	}
	out := filepath.Join(t.TempDir(), "saved.bin")
	runCLI(t, bobHome, "attach", "save", alice, "--id", page.Messages[0].ID, "--out", out, "--json")
	got, err := os.ReadFile(out)
	if err != nil || string(got) != body {
		t.Fatalf("saved attachment: %q %v", got, err)
	}
	err = filepath.Walk(data, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, hidden := range []string{body, "secret-name.bin", "see-attached-caption"} {
			if bytes.Contains(raw, []byte(hidden)) {
				t.Errorf("plaintext %q in %s", hidden, path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
