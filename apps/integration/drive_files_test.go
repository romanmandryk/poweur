package integration_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	clipkg "github.com/poweur/cli/pkg/cli"
)

func TestINT_DRIVE_05_EncryptedFileCLI(t *testing.T) {
	zone := newZone(t)
	data := t.TempDir()
	ts, addr := newHostedRelay(t, zone, data)
	defer ts.Close()
	const name = "filecli.poweur.net"
	zone.SetHost(name, addr)
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })
	home := t.TempDir()
	runCLI(t, home, "identity", "create", name, "--hosted", "--relay", ts.URL, "--json")
	runCLI(t, home, "drive", "mkdir", "/work", "--json")
	input := filepath.Join(t.TempDir(), "input.bin")
	output := filepath.Join(t.TempDir(), "output.bin")
	plain := bytes.Repeat([]byte("private-file-content-"), 220000)
	if err := os.WriteFile(input, plain, 0600); err != nil {
		t.Fatal(err)
	}
	runCLI(t, home, "drive", "put", input, "/work/note.txt", "--json")
	runCLI(t, home, "drive", "get", "/work/note.txt", output, "--json")
	got, err := os.ReadFile(output)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("multi-chunk download: %d %v", len(got), err)
	}
	// Replacement preserves the key tree; a fresh process must find the
	// inherited keys through the signed manifest chain.
	if err := os.WriteFile(input, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	runCLI(t, home, "drive", "put", input, "/work/note.txt", "--json")
	runCLI(t, home, "drive", "mv", "/work/note.txt", "/renamed.txt", "--json")
	runCLI(t, home, "drive", "get", "/renamed.txt", output, "--json")
	got, err = os.ReadFile(output)
	if err != nil || string(got) != "replacement" {
		t.Fatalf("reopened moved file: %s %v", got, err)
	}
	raw, _ := runCLI(t, home, "drive", "ls", "/", "--json")
	var rows []map[string]any
	if err := json.Unmarshal([]byte(raw), &rows); err != nil || len(rows) != 2 {
		t.Fatalf("decrypted listing: %s %v", raw, err)
	}
	runCLI(t, home, "drive", "rm", "/renamed.txt", "--json")
	raw, _ = runCLI(t, home, "drive", "ls", "/work", "--json")
	if raw != "[]\n" {
		t.Fatalf("empty folder: %q", raw)
	}
	// Drive plaintext and names must not be persisted in the provider.
	err = filepath.Walk(data, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, secret := range []string{"private-file-content-", "note.txt", "renamed.txt", "replacement"} {
			if bytes.Contains(raw, []byte(secret)) {
				t.Errorf("plaintext %q in %s", secret, path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
