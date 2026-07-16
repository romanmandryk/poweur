package files

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testProvider(t *testing.T) (*FSProvider, string) {
	t.Helper()
	dir := t.TempDir()
	home := filepath.Join(dir, "identities", "alice__poweur__net")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	p := NewFSProvider(dir, func(identity string) (string, error) {
		return home, nil
	})
	if err := p.EnsureTree(context.Background(), "alice.poweur.net"); err != nil {
		t.Fatal(err)
	}
	return p, home
}

func TestFSProviderWriteReadRoundtrip(t *testing.T) {
	p, _ := testProvider(t)
	ctx := context.Background()
	f, err := p.OpenFile(ctx, "alice.poweur.net", "private/notes.txt", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("hello files")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	rf, err := p.OpenFile(ctx, "alice.poweur.net", "private/notes.txt", os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rf)
	_ = rf.Close()
	if string(got) != "hello files" {
		t.Fatalf("roundtrip got %q", got)
	}
	used, err := p.UsedBytes(ctx, "alice.poweur.net")
	if err != nil || used != int64(len("hello files")) {
		t.Fatalf("UsedBytes=%d err=%v", used, err)
	}
}

func TestFSProviderRootListingHidesOperationalDirs(t *testing.T) {
	p, home := testProvider(t)
	ctx := context.Background()
	// Operational dirs next to the roots must stay invisible.
	if err := os.MkdirAll(filepath.Join(home, "meta"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "spool"), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := p.OpenFile(ctx, "alice.poweur.net", "", os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	entries, err := f.Readdir(-1)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	want := "apps,poweur-sys,private,public,shared"
	if strings.Join(names, ",") != want {
		t.Fatalf("root listing %v want %s", names, want)
	}
}

func TestFSProviderRefusesSymlinks(t *testing.T) {
	p, home := testProvider(t)
	ctx := context.Background()
	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("top secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(home, "private", "link.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := p.OpenFile(ctx, "alice.poweur.net", "private/link.txt", os.O_RDONLY, 0); err == nil {
		t.Fatal("symlink must be invisible")
	}
	if _, err := p.Stat(ctx, "alice.poweur.net", "private/link.txt"); err == nil {
		t.Fatal("symlink stat must fail")
	}
}

func TestFSProviderRootsAreImmutable(t *testing.T) {
	p, _ := testProvider(t)
	ctx := context.Background()
	if err := p.RemoveAll(ctx, "alice.poweur.net", "private"); err == nil {
		t.Fatal("removing a root must fail")
	}
	if err := p.Rename(ctx, "alice.poweur.net", "private", "private2"); err == nil {
		t.Fatal("renaming a root must fail")
	}
	if _, err := p.OpenFile(ctx, "alice.poweur.net", "poweur-sys", os.O_WRONLY, 0o600); err == nil {
		t.Fatal("opening a root for write must fail")
	}
}

func TestIndexChangeCounterAndETags(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "identities", "alice__poweur__net")
	_ = os.MkdirAll(home, 0o700)
	homeFn := func(string) (string, error) { return home, nil }
	ix := NewIndex(homeFn)

	m1 := ix.RecordWrite("alice.poweur.net", "private/a.txt", "abc123", 5, time.Now())
	m2 := ix.RecordWrite("alice.poweur.net", "private/b.txt", "def456", 7, time.Now())
	if m2.ChangeID != m1.ChangeID+1 {
		t.Fatalf("change ids must be monotonic: %d then %d", m1.ChangeID, m2.ChangeID)
	}

	ix.RecordRename("alice.poweur.net", "private/a.txt", "public/a.txt")
	if _, ok := ix.Get("alice.poweur.net", "private/a.txt"); ok {
		t.Fatal("old path must be gone after rename")
	}
	moved, ok := ix.Get("alice.poweur.net", "public/a.txt")
	if !ok || moved.ETag != "abc123" {
		t.Fatalf("rename must carry etag: %+v ok=%v", moved, ok)
	}

	ix.RecordDelete("alice.poweur.net", "public/a.txt")
	if _, ok := ix.Get("alice.poweur.net", "public/a.txt"); ok {
		t.Fatal("deleted path must leave the index")
	}

	// Persistence: a fresh Index must see the same state from disk.
	ix2 := NewIndex(homeFn)
	if ix2.ChangeID("alice.poweur.net") != ix.ChangeID("alice.poweur.net") {
		t.Fatal("change counter must persist")
	}
	if _, ok := ix2.Get("alice.poweur.net", "private/b.txt"); !ok {
		t.Fatal("entries must persist")
	}
}
