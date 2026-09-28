package drive

import (
	"os"
	"path/filepath"
	"testing"

	protocol "github.com/poweur/identity/drive"
)

func TestFileCache(t *testing.T) {
	dir := t.TempDir()
	c := FileCache{Dir: dir}
	data := []byte("ciphertext")
	id := protocol.ChunkID(data)
	if got, err := c.Get(id); err != nil || got != nil {
		t.Fatalf("miss %v %v", got, err)
	}
	if err := c.Put(id, data); err != nil {
		t.Fatal(err)
	}
	restarted := FileCache{Dir: dir}
	got, err := restarted.Get(id)
	if err != nil || string(got) != string(data) {
		t.Fatalf("read %q %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(dir, id), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := c.Get(id); err != nil || got != nil {
		t.Fatalf("corrupt hit %q %v", got, err)
	}
	if err := c.Put(id, []byte("wrong")); err == nil {
		t.Fatal("accepted mismatched hash")
	}
	for _, bad := range []string{"../secret", "", "ABC"} {
		if _, err := c.Get(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
		if err := c.Put(bad, data); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	if err := os.Remove(filepath.Join(dir, id)); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, id)); err != nil {
		t.Fatal(err)
	}
	if got, err := c.Get(id); err != nil || got != nil {
		t.Fatalf("symlink hit %q %v", got, err)
	}
}
