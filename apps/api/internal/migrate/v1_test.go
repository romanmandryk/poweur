package migrate

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/poweur/api/internal/drive/engine"
	"github.com/poweur/api/internal/drive/provider/fs"
	"github.com/poweur/api/internal/storage"
	idpkg "github.com/poweur/identity"
)

func write(t *testing.T, root, rel string, data []byte) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// v1Tree lays out a storage-v1 POWEUR_DATA with two identities.
func v1Tree(t *testing.T) (string, []byte) {
	t.Helper()
	root := t.TempDir()
	pub, priv, _ := ed25519.GenerateKey(nil)
	doc := idpkg.NewDocument("alice.poweur.net", idpkg.FormatEd25519PublicKey(pub), "", "relay.example", nil)
	doc.UpdatedAt = "2026-09-01T00:00:00Z"
	if err := doc.Sign(priv); err != nil {
		t.Fatal(err)
	}
	id, _ := doc.MarshalJSONDocument()
	home := "identities/alice__poweur__net/"
	avatar := append([]byte("\xff\xd8\xff\xe0"), bytes.Repeat([]byte{1}, 64)...)
	write(t, root, home+"poweur-sys/public/id.json", id)
	write(t, root, home+"poweur-sys/public/profile.json", []byte(`{"version":1,"display_name":"Alice","avatar":"public/avatars/avatar-e6eb5dfa.JPG"}`))
	write(t, root, home+"public/avatars/avatar-e6eb5dfa.JPG", avatar)
	write(t, root, home+"poweur-sys/relay/contacts.json", []byte(`{"version":1,"contacts":[]}`))
	write(t, root, home+"poweur-sys/relay/inbox-policy.json", []byte(`{"version":1,"mode":"contacts_only"}`))
	write(t, root, home+"poweur-sys/relay/analytics.json", []byte(`not json`))
	write(t, root, home+"poweur-sys/private/messages/2026-09/x.json", []byte(`{"plaintext":"old history"}`))
	write(t, root, home+"private/photo.jpg", []byte("a v1 file"))
	write(t, root, "identities/ghost__poweur__net/poweur-sys/public/profile.json", []byte(`{}`))
	write(t, root, "keystore/alice__poweur__net.json", []byte(`[{"enrollment_id":"e1","kind":"passkey"}]`))
	write(t, root, "spool/messages/alice__poweur__net/00000000000000000001.json", []byte(`{"seq":1,"at":"2026-09-01T00:00:00Z","item":{"id":"m1"}}`))
	write(t, root, "storage-quotas.json", []byte(`{}`))
	return root, avatar
}

func TestMigrateV1(t *testing.T) {
	ctx := context.Background()
	root, avatar := v1Tree(t)

	// A dry run reads everything and writes nothing.
	dry, err := Run(ctx, Options{DataDir: root, DryRun: true})
	if err != nil || len(dry.Identities) != 1 || dry.Keystores != 1 || dry.Messages != 1 {
		t.Fatalf("dry run: %+v %v", dry, err)
	}
	for _, name := range []string{"drives", "relay"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("dry run wrote %s", name)
		}
	}

	report, err := Run(ctx, Options{DataDir: root})
	if err != nil {
		t.Fatal(err)
	}
	got := report.Identities[0]
	if got.Identity != "alice.poweur.net" || strings.Join(got.Files, ",") != "id.json,avatar.jpg,profile.json,contacts.json,inbox-policy.json" || got.Dropped != 2 {
		t.Fatalf("identity report: %+v", got)
	}
	if len(report.Skipped) != 2 || !strings.Contains(strings.Join(report.Skipped, "|"), "analytics.json") {
		t.Fatalf("skipped: %v", report.Skipped)
	}

	// What a v2 relay reads back from the same directory.
	store, _ := fs.Open(root)
	ids, err := storage.OpenIdentityStore(store)
	if err != nil || !ids.Exists("alice.poweur.net") {
		t.Fatalf("identity index: %v", err)
	}
	eng := engine.New(engine.Options{Store: store})
	profile, _, err := eng.SystemRead(ctx, "alice.poweur.net", ".poweur/public/profile.json")
	var p map[string]any
	if err != nil || json.Unmarshal(profile, &p) != nil || p["avatar"] != "avatar.jpg" {
		t.Fatalf("profile: %s %v", profile, err)
	}
	if img, _, err := eng.SystemRead(ctx, "alice.poweur.net", ".poweur/public/avatar.jpg"); err != nil || !bytes.Equal(img, avatar) {
		t.Fatalf("avatar: %v", err)
	}
	if _, _, err := eng.SystemRead(ctx, "alice.poweur.net", ".poweur/relay/analytics.json"); err == nil {
		t.Fatal("invalid analytics migrated")
	}
	inbox, _ := storage.OpenInboxStore(store)
	if pending, _ := inbox.Since("alice.poweur.net", ""); len(pending) != 1 {
		t.Fatalf("spool: %+v", pending)
	}
	keys, _ := storage.OpenKeystoreStore(store)
	if len(keys.List("alice.poweur.net")) != 1 {
		t.Fatal("keystore")
	}
	// Nothing private moved into the provider in plaintext.
	_ = filepath.WalkDir(filepath.Join(root, "drives"), func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			raw, _ := os.ReadFile(path)
			if bytes.Contains(raw, []byte("old history")) || bytes.Contains(raw, []byte("a v1 file")) {
				t.Errorf("v1 private data in %s", path)
			}
		}
		return nil
	})
	// The v1 trees are kept aside; the quota file stays where the relay reads it.
	for _, name := range []string{"identities.v1-backup", "spool.v1-backup", "keystore.v1-backup", "storage-quotas.json"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// A second run finds nothing left to do and changes nothing.
	changes, _, _ := eng.Changes(ctx, "alice.poweur.net", 0, 0)
	again, err := Run(ctx, Options{DataDir: root})
	if err != nil || len(again.Identities) != 0 {
		t.Fatalf("rerun: %+v %v", again, err)
	}
	eng.Forget()
	if after, _, _ := eng.Changes(ctx, "alice.poweur.net", 0, 0); len(after) != len(changes) {
		t.Fatal("rerun changed the drive")
	}
}

// Interrupted after writing, before the rename: running again rewrites
// nothing and completes.
func TestMigrateV1Resumes(t *testing.T) {
	ctx := context.Background()
	root, _ := v1Tree(t)
	if _, err := Run(ctx, Options{DataDir: root, KeepSource: true}); err != nil {
		t.Fatal(err)
	}
	store, _ := fs.Open(root)
	eng := engine.New(engine.Options{Store: store})
	before, _, _ := eng.Changes(ctx, "alice.poweur.net", 0, 0)
	if _, err := Run(ctx, Options{DataDir: root}); err != nil {
		t.Fatal(err)
	}
	eng.Forget()
	if after, _, _ := eng.Changes(ctx, "alice.poweur.net", 0, 0); len(after) != len(before) {
		t.Fatalf("resumed run duplicated writes: %d -> %d", len(before), len(after))
	}
	if _, err := os.Stat(filepath.Join(root, "identities.v1-backup")); err != nil {
		t.Fatal("resumed run did not finish")
	}
}
