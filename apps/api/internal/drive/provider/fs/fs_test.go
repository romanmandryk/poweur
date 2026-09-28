package fs_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/poweur/api/internal/drive/provider"
	storefs "github.com/poweur/api/internal/drive/provider/fs"
	"github.com/poweur/api/internal/drive/provider/providertest"
)

func TestConformance(t *testing.T) {
	providertest.Run(t, func(t *testing.T) provider.Store {
		s, err := storefs.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		return s
	})
}

func TestRestartAndSymlinkEscape(t *testing.T) {
	dir := t.TempDir()
	s, err := storefs.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	tag, err := s.Put(t.Context(), "drives/owner/chunks/object", []byte("durable"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = storefs.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.Get(t.Context(), "drives/owner/chunks/object", nil)
	if err != nil || string(got.Data) != "durable" || got.ETag != tag {
		t.Fatalf("restart: %+v %v", got, err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(t.Context(), "escape/secret", nil); err == nil {
		t.Fatal("read escaped root")
	}
	if _, err := s.Put(t.Context(), "escape/secret", []byte("changed")); err == nil {
		t.Fatal("write escaped root")
	}
	if err := s.Delete(t.Context(), "escape/secret"); err == nil {
		t.Fatal("delete escaped root")
	}
	raw, err := os.ReadFile(filepath.Join(outside, "secret"))
	if err != nil || string(raw) != "outside" {
		t.Fatalf("outside changed: %q %v", raw, err)
	}
}

func TestListSkipsSiblingTree(t *testing.T) {
	dir := t.TempDir()
	s, err := storefs.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	sibling := filepath.Join(dir, "identities")
	if err := os.MkdirAll(sibling, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sibling, "bad name"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sibling, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(t.Context(), "drives/a", []byte("a")); err != nil {
		t.Fatal(err)
	}
	page, err := s.List(t.Context(), "drives/", "", 10)
	if err != nil || len(page.Objects) != 1 || page.Objects[0].Key != "drives/a" {
		t.Fatalf("sibling leaked into listing: %+v %v", page, err)
	}
}

func TestListHidesInterruptedWrites(t *testing.T) {
	dir := t.TempDir()
	s, err := storefs.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := os.WriteFile(filepath.Join(dir, ".tmp-interrupted"), []byte("not committed"), 0600); err != nil {
		t.Fatal(err)
	}
	page, err := s.List(t.Context(), "", "", 10)
	if err != nil || len(page.Objects) != 0 {
		t.Fatalf("temporary object listed: %+v %v", page, err)
	}
	if _, err := s.PresignGet(t.Context(), "key", time.Minute); !errors.Is(err, provider.ErrUnsupported) {
		t.Fatal(err)
	}
	if _, err := s.PresignPut(t.Context(), "key", "hash", 1, time.Minute); !errors.Is(err, provider.ErrUnsupported) {
		t.Fatal(err)
	}
}
