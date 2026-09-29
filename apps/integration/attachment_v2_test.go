package integration_test

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	relaypkg "github.com/poweur/api/pkg/relay"
	clipkg "github.com/poweur/cli/pkg/cli"
	idpkg "github.com/poweur/identity"
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

// INT_HISTORY_03 (E20-T11 acceptance): a 20 MB attachment crosses two relays.
// Alice's relay holds only ciphertext; Bob, homed on relay B, saves the exact
// bytes through his read share on Alice's drive.
func TestINT_HISTORY_03_TwentyMegabytesAcrossRelays(t *testing.T) {
	zone := newZone(t)
	dataA := t.TempDir()
	_, addrA := newHostedRelay(t, zone, dataA)
	addrB, _ := newCountingRelay(t, zone, nil, relaypkg.RateLimits{})
	clipkg.ConfigureIdentityResolver("http", true, addrA)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })
	// Both on their own domains, so each relay resolves the other's user.
	aliceHome, alice := newDNSIdentity(t, "bigalice", "senders.test", addrA)
	bobHome, bob := newDNSIdentity(t, "bigbob", "members.test", addrB)
	runCLI(t, bobHome, "policy", "set", "open")

	payload := make([]byte, idpkg.MaxAttachmentBytes)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "big-report.bin")
	if err := os.WriteFile(path, payload, 0600); err != nil {
		t.Fatal(err)
	}
	runCLI(t, aliceHome, "send", bob, "the big one", "--attach", path)
	runCLI(t, bobHome, "inbox")
	raw, _ := runCLI(t, bobHome, "history", alice, "--json")
	var page struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(raw), &page); err != nil || len(page.Messages) != 1 {
		t.Fatalf("history: %s %v", raw, err)
	}
	out := filepath.Join(t.TempDir(), "saved.bin")
	runCLI(t, bobHome, "attach", "save", alice, "--id", page.Messages[0].ID, "--out", out, "--json")
	got, err := os.ReadFile(out)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("saved attachment differs: %d bytes, %v", len(got), err)
	}
	// Relay A stored the file, its name and caption only as ciphertext.
	sample := payload[1<<20 : 1<<20+64]
	err = filepath.Walk(dataA, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, hidden := range [][]byte{sample, []byte("big-report.bin"), []byte("the big one")} {
			if bytes.Contains(raw, hidden) {
				t.Errorf("plaintext %q in %s", hidden[:min(len(hidden), 16)], path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
