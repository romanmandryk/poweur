package sync

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	gopath "path"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// memRemote is a drive in memory with the properties the engine relies on:
// versions per replace file, ordered records per append file, a sequence
// that moves on every change and 409 on a stale base.
type memRemote struct {
	seq      int
	files    map[string]*memFile
	dirs     map[string]bool
	acked    string
	versions int
}

type memFile struct {
	versions map[string][]byte
	head     string
	append   bool
	records  [][]byte
}

func newMem() *memRemote { return &memRemote{files: map[string]*memFile{}, dirs: map[string]bool{}} }

func (m *memRemote) bump() string {
	m.seq++
	m.versions++
	return fmt.Sprintf("v%d", m.versions)
}

func (m *memRemote) Changes(_ context.Context, since string) (string, bool, error) {
	next := fmt.Sprint(m.seq)
	return next, since == "" || since != next, nil
}
func (m *memRemote) Tree(context.Context) ([]Entry, error) {
	var out []Entry
	for p := range m.dirs {
		out = append(out, Entry{Path: p, Dir: true})
	}
	for p, f := range m.files {
		out = append(out, m.entry(p, f))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}
func (m *memRemote) entry(p string, f *memFile) Entry {
	return Entry{Path: p, Version: f.head, Append: f.append, Records: uint64(len(f.records))}
}
func (m *memRemote) Read(_ context.Context, p string, w io.Writer) error {
	f := m.files[p]
	if f == nil {
		return os.ErrNotExist
	}
	if f.append {
		for _, r := range f.records {
			_, _ = w.Write(r)
		}
		return nil
	}
	_, err := w.Write(f.versions[f.head])
	return err
}
func (m *memRemote) ReadVersion(_ context.Context, p, v string, w io.Writer) error {
	f := m.files[p]
	if f == nil || f.versions[v] == nil {
		return os.ErrNotExist
	}
	_, err := w.Write(f.versions[v])
	return err
}
func (m *memRemote) Records(_ context.Context, p string, from uint64) ([][]byte, error) {
	return m.files[p].records[from-1:], nil
}
func (m *memRemote) Put(_ context.Context, p string, r io.Reader, base string) (Entry, error) {
	data, _ := io.ReadAll(r)
	f := m.files[p]
	if f == nil {
		if base != "" {
			return Entry{}, ErrConflict
		}
		f = &memFile{versions: map[string][]byte{}}
		m.files[p] = f
	} else if f.head != base {
		return Entry{}, ErrConflict
	}
	v := m.bump()
	f.versions[v], f.head = data, v
	return m.entry(p, f), nil
}
func (m *memRemote) CreateAppend(_ context.Context, p string, first []byte) (Entry, error) {
	if m.files[p] != nil {
		return Entry{}, ErrConflict
	}
	f := &memFile{append: true, head: m.bump(), records: [][]byte{first}}
	m.files[p] = f
	return m.entry(p, f), nil
}
func (m *memRemote) Append(_ context.Context, p string, data []byte) (Entry, error) {
	f := m.files[p]
	m.seq++
	f.records = append(f.records, data)
	return m.entry(p, f), nil
}
func (m *memRemote) Mkdir(_ context.Context, p string) error {
	m.seq++
	m.dirs[p] = true
	return nil
}
func (m *memRemote) Delete(_ context.Context, p string) error {
	m.seq++
	for q := range m.files {
		if under(q, p) {
			delete(m.files, q)
		}
	}
	for q := range m.dirs {
		if under(q, p) {
			delete(m.dirs, q)
		}
	}
	return nil
}
func (m *memRemote) Ack(_ context.Context, seq string) error { m.acked = seq; return nil }

// device is one machine: a root directory and an engine over the shared remote.
type device struct {
	t      *testing.T
	engine *Engine
}

func newDevice(t *testing.T, name string, remote Remote) *device {
	root := t.TempDir()
	return &device{t: t, engine: &Engine{Root: root, Remote: remote, State: &State{Files: map[string]FileState{}}, Ignore: LoadIgnore(root), Device: name}}
}

func (d *device) write(rel, content string) {
	d.t.Helper()
	full := filepath.Join(d.engine.Root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		d.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		d.t.Fatal(err)
	}
	// Distinct mtimes so the size+mtime cache never hides an edit.
	later := time.Now().Add(time.Duration(len(content)) * time.Millisecond)
	_ = os.Chtimes(full, later, later)
}

func (d *device) read(rel string) (string, bool) {
	raw, err := os.ReadFile(filepath.Join(d.engine.Root, filepath.FromSlash(rel)))
	return string(raw), err == nil
}

func (d *device) run() Report {
	d.t.Helper()
	rep, err := d.engine.Run(context.Background())
	if err != nil {
		d.t.Fatal(err)
	}
	return rep
}

func TestSyncTwoDevicesConverge(t *testing.T) {
	remote := newMem()
	a, b := newDevice(t, "laptop", remote), newDevice(t, "desktop", remote)
	a.write("notes/hello.md", "hello from a\n")
	a.run()
	b.run()
	if got, _ := b.read("notes/hello.md"); got != "hello from a\n" {
		t.Fatalf("b got %q", got)
	}
	b.write("notes/hello.md", "edited on b\n")
	b.run()
	a.run()
	if got, _ := a.read("notes/hello.md"); got != "edited on b\n" {
		t.Fatalf("a got %q", got)
	}
	if remote.acked == "" {
		t.Fatal("the sync position was never acknowledged")
	}
}

func TestSyncOfflineMarkdownEditsMerge(t *testing.T) {
	remote := newMem()
	a, b := newDevice(t, "laptop", remote), newDevice(t, "desktop", remote)
	a.write("note.md", "# Note\ntitle line\nmiddle line\nlast line\n")
	a.run()
	b.run()
	// Both edit offline, different lines; a syncs first.
	a.write("note.md", "# Note\ntitle line by a\nmiddle line\nlast line\n")
	b.write("note.md", "# Note\ntitle line\nmiddle line\nlast line by b\n")
	a.run()
	rep := b.run()
	if len(rep.Merged) != 1 {
		t.Fatalf("b's run: %+v", rep)
	}
	a.run()
	want := "# Note\ntitle line by a\nmiddle line\nlast line by b\n"
	for _, d := range []*device{a, b} {
		if got, _ := d.read("note.md"); got != want {
			t.Fatalf("%s has %q", d.engine.Device, got)
		}
	}
}

func TestSyncBinaryConflictKeepsOneCopy(t *testing.T) {
	remote := newMem()
	a, b := newDevice(t, "laptop", remote), newDevice(t, "desktop", remote)
	a.write("photo.png", "v0")
	a.run()
	b.run()
	a.write("photo.png", "from a")
	b.write("photo.png", "from b")
	a.run()
	rep := b.run()
	if len(rep.Conflicts) != 1 || !strings.Contains(rep.Conflicts[0], "photo (conflicted copy desktop") {
		t.Fatalf("conflicts: %+v", rep.Conflicts)
	}
	if got, _ := b.read("photo.png"); got != "from a" {
		t.Fatalf("winner %q", got)
	}
	a.run()
	if got, ok := a.read(rep.Conflicts[0]); !ok || got != "from b" {
		t.Fatalf("the conflicted copy reached a as %q %v", got, ok)
	}
	entries, _ := os.ReadDir(a.engine.Root)
	copies := 0
	for _, e := range entries {
		if strings.Contains(e.Name(), "conflicted copy") {
			copies++
		}
	}
	if copies != 1 {
		t.Fatalf("%d conflicted copies", copies)
	}
}

func TestSyncJSONMergesKeys(t *testing.T) {
	remote := newMem()
	a, b := newDevice(t, "laptop", remote), newDevice(t, "desktop", remote)
	a.write("settings.json", `{"theme":"dark","font":12}`)
	a.run()
	b.run()
	a.write("settings.json", `{"theme":"light","font":12}`)
	b.write("settings.json", `{"theme":"dark","font":14,"wrap":true}`)
	a.run()
	b.run()
	got, _ := b.read("settings.json")
	for _, want := range []string{`"theme": "light"`, `"font": 14`, `"wrap": true`} {
		if !strings.Contains(got, want) {
			t.Fatalf("merged json lacks %s: %s", want, got)
		}
	}
}

func TestSyncAppendFilesInterleave(t *testing.T) {
	remote := newMem()
	a, b := newDevice(t, "laptop", remote), newDevice(t, "desktop", remote)
	a.write("journal.log", "a1\n")
	a.run()
	if f := remote.files["journal.log"]; f == nil || !f.append {
		t.Fatal("a .log file must be created as an append file")
	}
	b.run()
	a.write("journal.log", "a1\na2\n")
	b.write("journal.log", "a1\nb1\n")
	a.run()
	b.run()
	a.run()
	want := "a1\na2\nb1\n"
	for _, d := range []*device{a, b} {
		if got, _ := d.read("journal.log"); got != want {
			t.Fatalf("%s has %q", d.engine.Device, got)
		}
	}
	// Rewriting history is not an append: it is kept aside.
	a.write("journal.log", "rewritten\n")
	rep := a.run()
	if len(rep.Conflicts) != 1 {
		t.Fatalf("rewrite: %+v", rep)
	}
	if got, _ := a.read("journal.log"); got != want {
		t.Fatalf("after a rewrite the log is %q", got)
	}
}

func TestSyncDeletesGoToTrash(t *testing.T) {
	remote := newMem()
	a, b := newDevice(t, "laptop", remote), newDevice(t, "desktop", remote)
	a.write("docs/old.txt", "keep me somewhere\n")
	a.run()
	b.run()
	if err := os.RemoveAll(filepath.Join(a.engine.Root, "docs")); err != nil {
		t.Fatal(err)
	}
	a.run()
	b.run()
	if _, ok := b.read("docs/old.txt"); ok {
		t.Fatal("the delete did not reach b")
	}
	var trashed bool
	_ = filepath.WalkDir(filepath.Join(b.engine.Root, TrashDir), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && gopath.Base(filepath.ToSlash(p)) == "old.txt" {
			raw, _ := os.ReadFile(p)
			trashed = bytes.Equal(raw, []byte("keep me somewhere\n"))
		}
		return nil
	})
	if !trashed {
		t.Fatal("the deleted file is not in b's trash")
	}
	// The trash itself never syncs.
	a.run()
	if _, err := os.Stat(filepath.Join(a.engine.Root, TrashDir)); err == nil {
		t.Fatal("the trash synced to a")
	}
}

func TestSyncSelectiveAndIgnore(t *testing.T) {
	remote := newMem()
	a := newDevice(t, "laptop", remote)
	a.write("work/plan.md", "plan\n")
	a.write("personal/diary.md", "diary\n")
	a.write("work/build/out.bin", "artifact")
	a.write(".poweurignore", "build/\n")
	a.run()
	if remote.files["work/build/out.bin"] != nil {
		t.Fatal("ignored path uploaded")
	}
	b := newDevice(t, "desktop", remote)
	b.engine.Roots = []string{"work"}
	b.run()
	if _, ok := b.read("personal/diary.md"); ok {
		t.Fatal("selective sync fetched a path out of scope")
	}
	if got, _ := b.read("work/plan.md"); got != "plan\n" {
		t.Fatalf("in-scope file: %q", got)
	}
}

func TestSyncSettleDefersFilesBeingWritten(t *testing.T) {
	remote := newMem()
	a := newDevice(t, "laptop", remote)
	a.engine.Settle = time.Hour
	a.write("draft.md", "still typing\n")
	rep := a.run()
	if len(rep.Deferred) != 1 || remote.files["draft.md"] != nil {
		t.Fatalf("an unsettled file was uploaded: %+v", rep)
	}
	a.engine.Settle = 0
	a.run()
	if remote.files["draft.md"] == nil {
		t.Fatal("the settled file was not uploaded")
	}
}

func TestMergeTextConflictKeepsBoth(t *testing.T) {
	merged, n := MergeText("a\nb\nc\n", "a\nB1\nc\n", "a\nB2\nc\n", "me", "remote")
	if n != 1 || !strings.Contains(merged, "<<<<<<< me\nB1\n=======\nB2\n>>>>>>> remote\n") {
		t.Fatalf("%d %q", n, merged)
	}
	if merged, n := MergeText("a\nb", "a\nb\nc", "z\na\nb", "me", "r"); n != 0 || merged != "z\na\nb\nc\n" {
		t.Fatalf("insertions: %d %q", n, merged)
	}
}
