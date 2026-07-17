package sync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// fakeRemote is an in-memory relay: a file map plus a journal, mirroring
// the server's changes/manifest semantics.
type fakeRemote struct {
	files    map[string]string // path -> content ("" + dirs[path] for dirs)
	dirs     map[string]bool
	journal  []Change
	changeID int64
	// compactedThrough simulates journal compaction for gap tests.
	compactedThrough int64
}

func newFakeRemote() *fakeRemote {
	return &fakeRemote{files: map[string]string{}, dirs: map[string]bool{}}
}

func sha(content string) string {
	h := sha256.Sum256([]byte(content))
	return hex.EncodeToString(h[:])
}

func (f *fakeRemote) record(op, path, content string) {
	f.changeID++
	rec := Change{ChangeID: f.changeID, Op: op, Path: path}
	if op == "put" {
		rec.ETag = sha(content)
		rec.Size = int64(len(content))
	}
	f.journal = append(f.journal, rec)
}

func (f *fakeRemote) put(path, content string) {
	f.files[path] = content
	f.record("put", path, content)
}

func (f *fakeRemote) mkdir(path string) {
	f.dirs[path] = true
	f.record("mkdir", path, "")
}

func (f *fakeRemote) del(path string) {
	for p := range f.files {
		if under(p, path) {
			delete(f.files, p)
		}
	}
	for p := range f.dirs {
		if under(p, path) {
			delete(f.dirs, p)
		}
	}
	f.record("delete", path, "")
}

func (f *fakeRemote) Manifest(ctx context.Context) ([]Entry, string, error) {
	var out []Entry
	for p := range f.dirs {
		out = append(out, Entry{Path: p, Dir: true, MTime: time.Now()})
	}
	for p, c := range f.files {
		out = append(out, Entry{Path: p, ETag: sha(c), Size: int64(len(c)), MTime: time.Now()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, fmt.Sprint(f.changeID), nil
}

func (f *fakeRemote) Changes(ctx context.Context, since string) ([]Change, string, bool, error) {
	var s int64
	if since != "" {
		fmt.Sscan(since, &s)
	}
	if s < f.compactedThrough {
		return nil, since, true, nil
	}
	var out []Change
	for _, rec := range f.journal {
		if rec.ChangeID > s {
			out = append(out, rec)
		}
	}
	return out, fmt.Sprint(f.changeID), false, nil
}

func (f *fakeRemote) Get(ctx context.Context, path string) (io.ReadCloser, error) {
	c, ok := f.files[path]
	if !ok {
		return nil, fmt.Errorf("not found: %s", path)
	}
	return io.NopCloser(strings.NewReader(c)), nil
}

func (f *fakeRemote) Put(ctx context.Context, path string, r io.Reader, size int64) error {
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		return err
	}
	f.put(path, buf.String())
	return nil
}

func (f *fakeRemote) Mkdir(ctx context.Context, path string) error {
	f.mkdir(path)
	return nil
}

func (f *fakeRemote) Delete(ctx context.Context, path string) error {
	f.del(path)
	return nil
}

// --- test helpers ---

func newTestEngine(t *testing.T) (*Engine, *fakeRemote) {
	t.Helper()
	root := t.TempDir()
	st, err := LoadState(root)
	if err != nil {
		t.Fatal(err)
	}
	remote := newFakeRemote()
	return &Engine{Root: root, Remote: remote, State: st, Ignore: LoadIgnore(root), Device: "testbox"}, remote
}

func writeLocal(t *testing.T, e *Engine, tree, content string) {
	t.Helper()
	full := filepath.Join(e.Root, filepath.FromSlash(tree))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	// Nudge mtime so the dirty-check cache never masks an edit.
	past := time.Now().Add(-time.Second)
	_ = os.Chtimes(full, past, past)
}

func readLocal(t *testing.T, e *Engine, tree string) (string, bool) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(e.Root, filepath.FromSlash(tree)))
	if err != nil {
		return "", false
	}
	return string(raw), true
}

func syncOnce(t *testing.T, e *Engine) Report {
	t.Helper()
	pullRep, err := e.Pull(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pushRep, err := e.Push(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pullRep.Uploaded = append(pullRep.Uploaded, pushRep.Uploaded...)
	pullRep.DeletedR = append(pullRep.DeletedR, pushRep.DeletedR...)
	pullRep.MkdirR = append(pullRep.MkdirR, pushRep.MkdirR...)
	return pullRep
}

// --- tests ---

func TestSyncInitialPullAndPush(t *testing.T) {
	e, remote := newTestEngine(t)
	remote.mkdir("private/docs")
	remote.put("private/docs/remote.txt", "from remote")
	writeLocal(t, e, "private/local.txt", "from local")

	rep := syncOnce(t, e)
	if got, ok := readLocal(t, e, "private/docs/remote.txt"); !ok || got != "from remote" {
		t.Fatalf("remote file must arrive locally: %q ok=%v", got, ok)
	}
	if remote.files["private/local.txt"] != "from local" {
		t.Fatal("local file must arrive remotely")
	}
	if len(rep.Conflicts) != 0 {
		t.Fatalf("no conflicts expected: %+v", rep.Conflicts)
	}
}

func TestSyncTwoClientsConverge(t *testing.T) {
	a, remote := newTestEngine(t)
	broot := t.TempDir()
	bst, _ := LoadState(broot)
	b := &Engine{Root: broot, Remote: remote, State: bst, Ignore: LoadIgnore(broot), Device: "laptop-b"}

	writeLocal(t, a, "private/a.txt", "written on A")
	syncOnce(t, a)
	syncOnce(t, b)
	if got, ok := readLocal(t, b, "private/a.txt"); !ok || got != "written on A" {
		t.Fatalf("B must see A's file: %q ok=%v", got, ok)
	}

	writeLocal(t, b, "private/a.txt", "edited on B")
	syncOnce(t, b)
	syncOnce(t, a)
	if got, _ := readLocal(t, a, "private/a.txt"); got != "edited on B" {
		t.Fatalf("A must see B's edit: %q", got)
	}

	// B deletes; A converges.
	if err := os.Remove(filepath.Join(b.Root, "private", "a.txt")); err != nil {
		t.Fatal(err)
	}
	syncOnce(t, b)
	syncOnce(t, a)
	if _, ok := readLocal(t, a, "private/a.txt"); ok {
		t.Fatal("A must see B's delete")
	}
}

func TestConflictLocalEditRemoteEdit(t *testing.T) {
	e, remote := newTestEngine(t)
	remote.put("private/f.txt", "base")
	syncOnce(t, e)

	writeLocal(t, e, "private/f.txt", "local edit")
	remote.put("private/f.txt", "remote edit")

	rep := syncOnce(t, e)
	if len(rep.Conflicts) != 1 {
		t.Fatalf("want 1 conflict, got %+v", rep.Conflicts)
	}
	if got, _ := readLocal(t, e, "private/f.txt"); got != "remote edit" {
		t.Fatalf("winner must be the remote version: %q", got)
	}
	loser := rep.Conflicts[0]
	if !strings.Contains(loser, "conflicted copy from testbox") {
		t.Fatalf("conflicted-copy name convention: %q", loser)
	}
	if got, ok := readLocal(t, e, loser); !ok || got != "local edit" {
		t.Fatalf("losing version must be preserved: %q ok=%v", got, ok)
	}
	// The push half re-uploads the conflicted copy — no data loss remotely.
	if remote.files[loser] != "local edit" {
		t.Fatal("conflicted copy must be pushed")
	}
}

func TestConflictLocalDeleteRemoteEdit(t *testing.T) {
	e, remote := newTestEngine(t)
	remote.put("private/f.txt", "base")
	syncOnce(t, e)

	if err := os.Remove(filepath.Join(e.Root, "private", "f.txt")); err != nil {
		t.Fatal(err)
	}
	remote.put("private/f.txt", "remote edit")

	syncOnce(t, e)
	// The edit wins over the delete: the file reappears locally.
	if got, ok := readLocal(t, e, "private/f.txt"); !ok || got != "remote edit" {
		t.Fatalf("remote edit must win over local delete: %q ok=%v", got, ok)
	}
}

func TestConflictLocalEditRemoteDelete(t *testing.T) {
	e, remote := newTestEngine(t)
	remote.put("private/f.txt", "base")
	syncOnce(t, e)

	writeLocal(t, e, "private/f.txt", "local edit")
	remote.del("private/f.txt")

	syncOnce(t, e)
	// The edit wins: file survives locally and is re-uploaded.
	if got, ok := readLocal(t, e, "private/f.txt"); !ok || got != "local edit" {
		t.Fatalf("local edit must win over remote delete: %q ok=%v", got, ok)
	}
	if remote.files["private/f.txt"] != "local edit" {
		t.Fatal("surviving edit must be pushed back")
	}
}

func TestConflictBothCreate(t *testing.T) {
	e, remote := newTestEngine(t)
	syncOnce(t, e) // establish cursor

	writeLocal(t, e, "private/new.txt", "created locally")
	remote.put("private/new.txt", "created remotely")

	rep := syncOnce(t, e)
	if len(rep.Conflicts) != 1 {
		t.Fatalf("both-create must conflict: %+v", rep)
	}
	if got, _ := readLocal(t, e, "private/new.txt"); got != "created remotely" {
		t.Fatalf("remote version wins the name: %q", got)
	}
	if got, ok := readLocal(t, e, rep.Conflicts[0]); !ok || got != "created locally" {
		t.Fatalf("local version preserved as conflicted copy: %q ok=%v", got, ok)
	}
}

func TestNoDataLossOnUnchangedDelete(t *testing.T) {
	e, remote := newTestEngine(t)
	remote.mkdir("private/dir")
	remote.put("private/dir/f.txt", "base")
	syncOnce(t, e)

	remote.del("private/dir")
	syncOnce(t, e)
	if _, ok := readLocal(t, e, "private/dir/f.txt"); ok {
		t.Fatal("clean local copy must follow the remote delete")
	}
	if _, err := os.Stat(filepath.Join(e.Root, "private", "dir")); !os.IsNotExist(err) {
		t.Fatal("emptied directory must be removed")
	}
}

func TestJournalGapFallsBackToManifest(t *testing.T) {
	e, remote := newTestEngine(t)
	remote.put("private/a.txt", "v1")
	syncOnce(t, e)

	remote.put("private/b.txt", "v2")
	remote.compactedThrough = remote.changeID // cursor now predates journal

	syncOnce(t, e)
	if got, ok := readLocal(t, e, "private/b.txt"); !ok || got != "v2" {
		t.Fatalf("manifest resync must recover the gap: %q ok=%v", got, ok)
	}
}

func TestLocalDeletePropagates(t *testing.T) {
	e, remote := newTestEngine(t)
	writeLocal(t, e, "private/dir/f.txt", "data")
	syncOnce(t, e)
	if _, ok := remote.files["private/dir/f.txt"]; !ok {
		t.Fatal("setup: file must be remote")
	}
	if err := os.RemoveAll(filepath.Join(e.Root, "private", "dir")); err != nil {
		t.Fatal(err)
	}
	syncOnce(t, e)
	if _, ok := remote.files["private/dir/f.txt"]; ok {
		t.Fatal("local delete must propagate")
	}
	if remote.dirs["private/dir"] {
		t.Fatal("directory delete must propagate")
	}
}

func TestIgnoreAndScopeFiltering(t *testing.T) {
	e, remote := newTestEngine(t)
	if err := os.WriteFile(filepath.Join(e.Root, IgnoreFileName), []byte("*.tmp\nbuild/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.Ignore = LoadIgnore(e.Root)
	writeLocal(t, e, "private/keep.txt", "keep")
	writeLocal(t, e, "private/skip.tmp", "skip")
	writeLocal(t, e, "private/build/out.bin", "skip")
	writeLocal(t, e, "unknown-root/x.txt", "skip") // outside DefaultRoots

	syncOnce(t, e)
	if _, ok := remote.files["private/keep.txt"]; !ok {
		t.Fatal("kept file must sync")
	}
	for _, p := range []string{"private/skip.tmp", "private/build/out.bin", "unknown-root/x.txt"} {
		if _, ok := remote.files[p]; ok {
			t.Fatalf("%s must not sync", p)
		}
	}
	// The state DB itself must never sync.
	if _, ok := remote.files[StateFileName]; ok {
		t.Fatal("state DB must never sync")
	}
}

func TestStatusReportsPendingBothSides(t *testing.T) {
	e, remote := newTestEngine(t)
	remote.put("private/base.txt", "base")
	syncOnce(t, e)

	writeLocal(t, e, "private/new.txt", "new")
	writeLocal(t, e, "private/base.txt", "changed")
	remote.put("private/incoming.txt", "incoming")

	st, err := e.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(st.LocalNew) != 1 || st.LocalNew[0] != "private/new.txt" {
		t.Fatalf("LocalNew: %+v", st.LocalNew)
	}
	if len(st.LocalModified) != 1 || st.LocalModified[0] != "private/base.txt" {
		t.Fatalf("LocalModified: %+v", st.LocalModified)
	}
	if st.RemotePending != 1 {
		t.Fatalf("RemotePending = %d", st.RemotePending)
	}
}
