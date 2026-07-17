package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestUploads(t *testing.T) (*Uploads, *Index, *FSProvider) {
	t.Helper()
	dir := t.TempDir()
	home := filepath.Join(dir, "identities", "alice__poweur__net")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	homeFn := func(string) (string, error) { return home, nil }
	provider := NewFSProvider(dir, homeFn)
	if err := provider.EnsureTree(context.Background(), owner); err != nil {
		t.Fatal(err)
	}
	return NewUploads(homeFn), NewIndex(homeFn), provider
}

func TestUploadChunkedAssembly(t *testing.T) {
	u, ix, p := newTestUploads(t)
	body := strings.Repeat("chunky", 1000)
	info, err := u.Create(owner, "private/big.bin", int64(len(body)), owner)
	if err != nil {
		t.Fatal(err)
	}

	half := len(body) / 2
	off, err := u.Append(owner, info.ID, 0, strings.NewReader(body[:half]))
	if err != nil || off != int64(half) {
		t.Fatalf("first chunk: off=%d err=%v", off, err)
	}
	off, err = u.Append(owner, info.ID, off, strings.NewReader(body[half:]))
	if err != nil || off != int64(len(body)) {
		t.Fatalf("second chunk: off=%d err=%v", off, err)
	}

	meta, err := u.Assemble(context.Background(), p, ix, owner, info.ID)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(body))
	if meta.ETag != hex.EncodeToString(sum[:]) || meta.Size != int64(len(body)) {
		t.Fatalf("assembled meta mismatch: %+v", meta)
	}
	f, err := p.OpenFile(context.Background(), owner, "private/big.bin", os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(f)
	f.Close()
	if string(got) != body {
		t.Fatal("assembled body mismatch")
	}
	if _, err := u.Get(owner, info.ID); err == nil {
		t.Fatal("spool must be gone after assembly")
	}
	// The write must be journaled with the actor.
	recs, _, _ := ix.Changes(owner, 0, 0, nil)
	if len(recs) != 1 || recs[0].Op != OpPut || recs[0].Actor != owner {
		t.Fatalf("assembly must journal a put: %+v", recs)
	}
}

func TestUploadResumeAfterInterrupt(t *testing.T) {
	u, ix, p := newTestUploads(t)
	body := strings.Repeat("x", 4096)
	info, err := u.Create(owner, "private/r.bin", int64(len(body)), owner)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a dropped connection: partial chunk then a stale-offset retry.
	if _, err := u.Append(owner, info.ID, 0, strings.NewReader(body[:1000])); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Append(owner, info.ID, 0, strings.NewReader(body)); !errors.Is(err, ErrUploadOffset) {
		t.Fatalf("stale offset must be rejected, got %v", err)
	}
	// Resume from the reported offset (what HEAD returns).
	got, err := u.Get(owner, info.ID)
	if err != nil || got.Offset != 1000 {
		t.Fatalf("offset after interrupt: %+v err=%v", got, err)
	}
	if _, err := u.Append(owner, info.ID, got.Offset, strings.NewReader(body[1000:])); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Assemble(context.Background(), p, ix, owner, info.ID); err != nil {
		t.Fatal(err)
	}
}

func TestUploadRejectsOverflowAndIncompleteAssembly(t *testing.T) {
	u, ix, p := newTestUploads(t)
	info, err := u.Create(owner, "private/o.bin", 10, owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.Assemble(context.Background(), p, ix, owner, info.ID); err == nil {
		t.Fatal("assembling an incomplete upload must fail")
	}
	if _, err := u.Append(owner, info.ID, 0, strings.NewReader(strings.Repeat("y", 11))); !errors.Is(err, ErrUploadOverflow) {
		t.Fatalf("overflow must be rejected, got %v", err)
	}
}

func TestUploadSweepExpires(t *testing.T) {
	u, _, _ := newTestUploads(t)
	old, err := u.Create(owner, "private/old.bin", 5, owner)
	if err != nil {
		t.Fatal(err)
	}
	// Backdate the spool files past the TTL.
	dir, _ := u.dir(owner)
	stale := time.Now().Add(-UploadTTL - time.Hour)
	for _, name := range []string{old.ID + ".part", old.ID + ".json"} {
		if err := os.Chtimes(filepath.Join(dir, name), stale, stale); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := u.Create(owner, "private/new.bin", 5, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Get(owner, old.ID); err == nil {
		t.Fatal("expired upload must be swept on the next create")
	}
}

func TestUploadCancel(t *testing.T) {
	u, _, _ := newTestUploads(t)
	info, err := u.Create(owner, "private/c.bin", 5, owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := u.Cancel(owner, info.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Get(owner, info.ID); err == nil {
		t.Fatal("cancelled upload must be gone")
	}
	if err := u.Cancel(owner, info.ID); err == nil {
		t.Fatal("double cancel must fail")
	}
}
