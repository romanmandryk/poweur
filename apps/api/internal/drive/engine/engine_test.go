package engine

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/poweur/api/internal/drive/provider"
	"github.com/poweur/api/internal/drive/provider/fs"
	"github.com/poweur/api/internal/drive/provider/s3"
	identity "github.com/poweur/identity"
	"github.com/poweur/identity/drive"
)

const owner = "alice.poweur.net"

type fixture struct {
	t       *testing.T
	store   provider.Store
	eng     *Engine
	keys    map[string]ed25519.PrivateKey
	clock   time.Time
	changes []Change
	mu      sync.Mutex
	quota   int64
	// rosters stand in for the relay's group cache: group → members, admins.
	rosters map[string][2][]string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, store: testStore(t), keys: map[string]ed25519.PrivateKey{}, clock: time.Now().UTC()}
	for _, id := range []string{owner, "bob.poweur.net"} {
		_, priv, _ := ed25519.GenerateKey(nil)
		f.keys[id] = priv
	}
	f.eng = f.newEngine()
	return f
}

// testStore is a filesystem store, or a fresh prefix of an existing bucket
// when POWEUR_TEST_S3_ENDPOINT and POWEUR_TEST_S3_BUCKET are set (same
// variables as the provider's existing-bucket test).
func testStore(t *testing.T) provider.Store {
	t.Helper()
	endpoint, bucket := os.Getenv("POWEUR_TEST_S3_ENDPOINT"), os.Getenv("POWEUR_TEST_S3_BUCKET")
	if endpoint == "" || bucket == "" {
		store, err := fs.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		return store
	}
	region := os.Getenv("POWEUR_TEST_S3_REGION")
	if region == "" {
		region = "hel1"
	}
	store, err := s3.New(s3.Config{
		Endpoint: endpoint, Bucket: bucket, Region: region,
		AccessKey: os.Getenv("POWEUR_TEST_S3_ACCESS_KEY"), SecretKey: os.Getenv("POWEUR_TEST_S3_SECRET_KEY"),
		Secure: os.Getenv("POWEUR_TEST_S3_SECURE") != "0", Presign: os.Getenv("POWEUR_TEST_S3_PRESIGN") != "0",
		Prefix: "engine-test/" + hexID(8),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Probe(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		for cursor := ""; ; {
			page, err := store.List(ctx, "", cursor, 1000)
			if err != nil {
				return
			}
			for _, obj := range page.Objects {
				_ = store.Delete(ctx, obj.Key)
			}
			if page.Next == "" {
				return
			}
			cursor = page.Next
		}
	})
	return store
}

func (f *fixture) newEngine() *Engine {
	return New(Options{
		Store: f.store,
		Keys: func(_ context.Context, id string) (ed25519.PublicKey, error) {
			if k, ok := f.keys[id]; ok {
				return k.Public().(ed25519.PublicKey), nil
			}
			return nil, errors.New("unknown identity")
		},
		Quota: func(string) int64 { return f.quota },
		Now:   func() time.Time { return f.clock },
		Groups: func(group string) ([]string, []string, bool) {
			f.mu.Lock()
			defer f.mu.Unlock()
			r, ok := f.rosters[group]
			return r[0], r[1], ok
		},
		OnCommit: func(_ string, c Change, _ []PositionedRecord, _ Audience) {
			f.mu.Lock()
			f.changes = append(f.changes, c)
			f.mu.Unlock()
		},
	})
}

func hexID(n int) string {
	raw := make([]byte, n)
	_, _ = rand.Read(raw)
	return hex.EncodeToString(raw)
}

func payload(tag byte) *identity.SealedPayload {
	return &identity.SealedPayload{
		EphemeralPublicKey: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{tag}, 32)),
		Nonce:              base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{tag}, 12)),
		Ciphertext:         base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{tag}, 48)),
	}
}

// chunk encrypts plaintext into a valid stored chunk and uploads it.
func (f *fixture) chunk(plaintext string) drive.ChunkRef {
	f.t.Helper()
	ctxBytes, _ := drive.Context(owner, strings.Repeat("0", 32), drive.PurposeContent, 1)
	data, err := drive.EncryptChunk(bytes.Repeat([]byte{1}, 32), []byte(plaintext), ctxBytes)
	if err != nil {
		f.t.Fatal(err)
	}
	ref, err := f.eng.PutChunk(context.Background(), owner, data)
	if err != nil {
		f.t.Fatal(err)
	}
	return ref
}

func (f *fixture) sign(m *drive.Manifest) {
	f.t.Helper()
	if err := m.Sign(f.keys[m.Author]); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) commit(m drive.Manifest, pages []drive.ChunkPage) (Result, error) {
	f.sign(&m)
	return f.eng.Commit(context.Background(), owner, Request{ID: hexID(16), Manifest: &m, Pages: pages})
}

func (f *fixture) mustCommit(m drive.Manifest, pages []drive.ChunkPage) Result {
	f.t.Helper()
	r, err := f.commit(m, pages)
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}

func (f *fixture) root() string {
	f.t.Helper()
	id := hexID(16)
	f.mustCommit(drive.Manifest{Format: 1, Drive: owner, Node: id, Version: hexID(16), Operation: drive.OpCreate, Author: owner,
		Generation: 1, Kind: drive.KindFolder, NodeKey: payload(1), Pages: []string{}}, nil)
	return id
}

func (f *fixture) folder(parent string, nameHash string) string {
	f.t.Helper()
	id := hexID(16)
	f.mustCommit(drive.Manifest{Format: 1, Drive: owner, Node: id, Version: hexID(16), Operation: drive.OpCreate, Author: owner,
		Generation: 1, Kind: drive.KindFolder, Folder: parent, Name: payload(2), NameHash: nameHash, NodeKey: payload(3), Pages: []string{}}, nil)
	return id
}

func (f *fixture) file(parent, nameHash, mode string, refs ...drive.ChunkRef) (string, string) {
	f.t.Helper()
	id := hexID(16)
	pages, hashes, err := drive.SplitPages(owner, id, refs)
	if err != nil {
		f.t.Fatal(err)
	}
	if hashes == nil {
		hashes = []string{}
	}
	v := hexID(16)
	f.mustCommit(drive.Manifest{Format: 1, Drive: owner, Node: id, Version: v, Operation: drive.OpCreate, Author: owner,
		Generation: 1, Kind: drive.KindFile, Mode: mode, Folder: parent, Name: payload(4), NameHash: nameHash,
		NodeKey: payload(5), ContentKey: payload(6), Count: uint64(len(refs)), Pages: hashes}, pages)
	return id, v
}

func (f *fixture) replace(node, base string, refs ...drive.ChunkRef) (Result, error) {
	pages, hashes, _ := drive.SplitPages(owner, node, refs)
	if hashes == nil {
		hashes = []string{}
	}
	return f.commit(drive.Manifest{Format: 1, Drive: owner, Node: node, Version: hexID(16), Parent: base, Operation: drive.OpReplace,
		Author: owner, Generation: 1, Kind: drive.KindFile, Mode: drive.ModeReplace, Count: uint64(len(refs)), Pages: hashes}, pages)
}

// grant commits a share of node to member with role, signed by the owner.
func (f *fixture) grant(node, member, role string) drive.Share {
	f.t.Helper()
	n, err := f.eng.Node(context.Background(), owner, node)
	if err != nil {
		f.t.Fatal(err)
	}
	id, _ := drive.NewShareID()
	s := drive.Share{Format: 1, Drive: owner, ID: id, Node: node, Member: member, Role: role, Generation: n.Generation,
		NodePublic: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{8}, 32)),
		Issuer:     owner, Issued: f.clock.UTC().Truncate(time.Second).Format(time.RFC3339)}
	if drive.KeyBearing(role) {
		s.NodeKey = payload(9)
	}
	if err := s.Sign(f.keys[owner]); err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.eng.Commit(context.Background(), owner, Request{ID: hexID(16), Share: &s, Author: owner}); err != nil {
		f.t.Fatal(err)
	}
	return s
}

func nameHash(tag byte) string { return strings.Repeat(fmt.Sprintf("%02x", tag), 32) }

func TestCreateReplaceAndConflict(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	root := f.root()
	a, b := f.chunk("one"), f.chunk("two")
	file, v1 := f.file(root, nameHash(1), drive.ModeReplace, a, b)

	info, err := f.eng.Node(ctx, owner, file)
	if err != nil || info.Head != v1 || info.Count != 2 {
		t.Fatalf("node: %+v %v", info, err)
	}
	children, _, err := f.eng.Children(ctx, owner, root, "", 0)
	if err != nil || len(children) != 1 || children[0].ID != file {
		t.Fatalf("children: %+v %v", children, err)
	}
	// Editing one chunk: the other is reused, so only one new chunk counts.
	used, _ := f.eng.Usage(ctx, owner)
	c := f.chunk("three")
	r, err := f.replace(file, v1, a, c)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := f.eng.Usage(ctx, owner)
	if after != used+int64(c.Size) {
		t.Fatalf("usage %d -> %d, want +%d", used, after, c.Size)
	}
	// Two writers on one base: the second is a conflict.
	if _, err := f.replace(file, v1, b); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale base: %v", err)
	}
	m, err := f.eng.Version(ctx, owner, file, r.Head)
	if err != nil || m.Parent != v1 {
		t.Fatalf("version: %+v %v", m, err)
	}
	// A chunk is readable through a version that references it, never by hash.
	if _, err := f.eng.ChunkKey(ctx, owner, file, r.Head, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.eng.ChunkKey(ctx, owner, file, r.Head, b.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("chunk outside the version: %v", err)
	}
	// A chunk that was never uploaded cannot be committed.
	ghost := drive.ChunkRef{ID: strings.Repeat("9", 64), Size: a.Size}
	if _, err := f.replace(file, r.Head, ghost); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing chunk: %v", err)
	}
	if missing, err := f.eng.Missing(ctx, owner, []drive.ChunkRef{a, ghost}); err != nil || len(missing) != 1 || missing[0] != ghost {
		t.Fatalf("missing: %+v %v", missing, err)
	}
}

func TestTreeRules(t *testing.T) {
	f := newFixture(t)
	root := f.root()
	docs := f.folder(root, nameHash(1))
	inner := f.folder(docs, nameHash(2))
	// Sibling names are unique within a folder.
	if _, err := f.commit(drive.Manifest{Format: 1, Drive: owner, Node: hexID(16), Version: hexID(16), Operation: drive.OpCreate,
		Author: owner, Generation: 1, Kind: drive.KindFolder, Folder: root, Name: payload(2), NameHash: nameHash(1), NodeKey: payload(3), Pages: []string{}}, nil); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate name: %v", err)
	}
	// A second root is refused.
	if _, err := f.commit(drive.Manifest{Format: 1, Drive: owner, Node: hexID(16), Version: hexID(16), Operation: drive.OpCreate,
		Author: owner, Generation: 1, Kind: drive.KindFolder, NodeKey: payload(1), Pages: []string{}}, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("second root: %v", err)
	}
	info, _ := f.eng.Node(context.Background(), owner, docs)
	move := func(node, base, into string, hash string) error {
		n, _ := f.eng.Node(context.Background(), owner, node)
		_, err := f.commit(drive.Manifest{Format: 1, Drive: owner, Node: node, Version: hexID(16), Parent: base, Operation: drive.OpMove,
			Author: owner, Generation: n.Generation, Kind: n.Kind, Mode: n.Mode, Folder: into, Name: payload(7), NameHash: hash, NodeKey: payload(8), Pages: []string{}}, nil)
		return err
	}
	if err := move(docs, info.Head, inner, nameHash(3)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("move into own descendant: %v", err)
	}
	innerInfo, _ := f.eng.Node(context.Background(), owner, inner)
	if err := move(inner, innerInfo.Head, root, nameHash(4)); err != nil {
		t.Fatalf("move: %v", err)
	}
	moved, _ := f.eng.Node(context.Background(), owner, inner)
	if moved.Folder != root || moved.NameHash != nameHash(4) {
		t.Fatalf("moved node: %+v", moved)
	}
	// The old name is free again.
	f.folder(docs, nameHash(2))
	// A folder with children cannot be removed; the root never can.
	docsInfo, _ := f.eng.Node(context.Background(), owner, docs)
	remove := func(node, base, kind string) error {
		_, err := f.commit(drive.Manifest{Format: 1, Drive: owner, Node: node, Version: hexID(16), Parent: base, Operation: drive.OpRemove,
			Author: owner, Generation: 1, Kind: kind, Pages: []string{}}, nil)
		return err
	}
	if err := remove(docs, docsInfo.Head, drive.KindFolder); !errors.Is(err, ErrConflict) {
		t.Fatalf("remove non-empty folder: %v", err)
	}
	rootInfo, _ := f.eng.Node(context.Background(), owner, root)
	if err := remove(root, rootInfo.Head, drive.KindFolder); err == nil {
		t.Fatal("root removed")
	}
	if err := remove(inner, moved.Head, drive.KindFolder); err != nil {
		t.Fatalf("remove empty folder: %v", err)
	}
	if kids, _, _ := f.eng.Children(context.Background(), owner, root, "", 0); len(kids) != 1 {
		t.Fatalf("removed folder still listed: %+v", kids)
	}
}

func TestAuthorityAndSignatures(t *testing.T) {
	f := newFixture(t)
	root := f.root()
	m := drive.Manifest{Format: 1, Drive: owner, Node: hexID(16), Version: hexID(16), Operation: drive.OpCreate, Author: "bob.poweur.net",
		Generation: 1, Kind: drive.KindFolder, Folder: root, Name: payload(2), NameHash: nameHash(1), NodeKey: payload(3), Pages: []string{}}
	if _, err := f.commit(m, nil); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-owner write: %v", err)
	}
	// A manifest signed by someone else than its author is refused.
	m.Author = owner
	if err := m.Sign(f.keys["bob.poweur.net"]); err != nil {
		t.Fatal(err)
	}
	if _, err := f.eng.Commit(context.Background(), owner, Request{ID: hexID(16), Manifest: &m}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("forged signature: %v", err)
	}
}

func TestIdempotentRequests(t *testing.T) {
	f := newFixture(t)
	root := f.root()
	m := drive.Manifest{Format: 1, Drive: owner, Node: hexID(16), Version: hexID(16), Operation: drive.OpCreate, Author: owner,
		Generation: 1, Kind: drive.KindFolder, Folder: root, Name: payload(2), NameHash: nameHash(1), NodeKey: payload(3), Pages: []string{}}
	f.sign(&m)
	req := Request{ID: hexID(16), Manifest: &m}
	first, err := f.eng.Commit(context.Background(), owner, req)
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.eng.Commit(context.Background(), owner, req)
	if err != nil || again.Head != first.Head || again.Seq != first.Seq {
		t.Fatalf("retry: %+v %v", again, err)
	}
	other := m
	other.Version = hexID(16)
	f.sign(&other)
	if _, err := f.eng.Commit(context.Background(), owner, Request{ID: req.ID, Manifest: &other}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("reused ID: %v", err)
	}
}

func record(f *fixture, node, author string, seq uint64, prev string, refs ...drive.ChunkRef) drive.AppendRecord {
	f.t.Helper()
	r := drive.AppendRecord{Format: 1, Drive: owner, Node: node, Author: author, Generation: 1, Sequence: seq, Previous: prev, Chunks: refs}
	if err := r.Sign(f.keys[author]); err != nil {
		f.t.Fatal(err)
	}
	return r
}

func TestAppendPositionsAndAuthorChains(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	root := f.root()
	log, _ := f.file(root, nameHash(1), drive.ModeAppend)
	r1 := record(f, log, owner, 1, "", f.chunk("a"))
	h1, _ := r1.Hash()
	r2 := record(f, log, owner, 2, h1, f.chunk("b"))
	res, err := f.eng.Commit(ctx, owner, Request{ID: hexID(16), Records: []drive.AppendRecord{r1, r2}})
	if err != nil || len(res.Positions) != 2 || res.Positions[1] != 2 {
		t.Fatalf("append: %+v %v", res, err)
	}
	// A duplicate, a gap and a forked chain are all refused.
	for name, bad := range map[string]drive.AppendRecord{
		"duplicate": r2,
		"gap":       record(f, log, owner, 4, h1, f.chunk("c")),
		"fork":      record(f, log, owner, 3, h1, f.chunk("d")),
	} {
		if _, err := f.eng.Commit(ctx, owner, Request{ID: hexID(16), Records: []drive.AppendRecord{bad}}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	got, next, err := f.eng.Records(ctx, owner, log, 2, 10)
	if err != nil || len(got) != 1 || got[0].Position != 2 || got[0].Record.Sequence != 2 || next != 3 {
		t.Fatalf("records: %+v %d %v", got, next, err)
	}
	// Replace is not how an append file changes.
	info, _ := f.eng.Node(ctx, owner, log)
	if _, err := f.commit(drive.Manifest{Format: 1, Drive: owner, Node: log, Version: hexID(16), Parent: info.Head, Operation: drive.OpReplace,
		Author: owner, Generation: 1, Kind: drive.KindFile, Mode: drive.ModeAppend, Pages: []string{}}, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("replace on append file: %v", err)
	}
}

func TestTrimAndResync(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	root := f.root()
	log, _ := f.file(root, nameHash(1), drive.ModeAppend)
	prev := ""
	var refs []drive.ChunkRef
	for i := uint64(1); i <= 3; i++ {
		ref := f.chunk(fmt.Sprintf("entry %d", i))
		refs = append(refs, ref)
		r := record(f, log, owner, i, prev, ref)
		prev, _ = r.Hash()
		if _, err := f.eng.Commit(ctx, owner, Request{ID: hexID(16), Records: []drive.AppendRecord{r}}); err != nil {
			t.Fatal(err)
		}
	}
	snapFile, snapVersion := f.file(root, nameHash(2), drive.ModeReplace, f.chunk("folded state"))
	before, _ := f.eng.Usage(ctx, owner)
	if _, err := f.eng.Commit(ctx, owner, Request{ID: hexID(16), Author: owner, Trim: &Trim{Node: log, Before: 3, Snapshot: SnapshotRef{Node: snapFile, Version: snapVersion}}}); err != nil {
		t.Fatal(err)
	}
	after, _ := f.eng.Usage(ctx, owner)
	if after != before-int64(refs[0].Size+refs[1].Size) {
		t.Fatalf("trim did not release chunks: %d -> %d", before, after)
	}
	if _, _, err := f.eng.Records(ctx, owner, log, 1, 10); !errors.Is(err, ErrResync) {
		t.Fatalf("read before trim: %v", err)
	}
	info, _ := f.eng.Node(ctx, owner, log)
	if info.TrimSnapshot == nil || info.TrimSnapshot.Version != snapVersion {
		t.Fatalf("trim snapshot: %+v", info)
	}
	got, _, err := f.eng.Records(ctx, owner, log, 3, 10)
	if err != nil || len(got) != 1 || got[0].Position != 3 {
		t.Fatalf("retained records: %+v %v", got, err)
	}
	// Trimming needs owner authority.
	if _, err := f.eng.Commit(ctx, owner, Request{ID: hexID(16), Author: "bob.poweur.net", Trim: &Trim{Node: log, Before: 4, Snapshot: SnapshotRef{Node: snapFile, Version: snapVersion}}}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-owner trim: %v", err)
	}
}

// Deleting every cache and reloading from the store yields the same drive —
// from the journal alone, from a snapshot plus its suffix, and ignoring a
// corrupt snapshot.
func TestStatelessRestart(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	root := f.root()
	file, v1 := f.file(root, nameHash(1), drive.ModeReplace, f.chunk("x"))
	r, _ := f.replace(file, v1, f.chunk("y"))
	log, _ := f.file(root, nameHash(2), drive.ModeAppend)
	rec := record(f, log, owner, 1, "", f.chunk("z"))
	if _, err := f.eng.Commit(ctx, owner, Request{ID: hexID(16), Records: []drive.AppendRecord{rec}}); err != nil {
		t.Fatal(err)
	}
	snapshot := func(e *Engine) (NodeInfo, int64, []Change) {
		info, err := e.Node(ctx, owner, file)
		if err != nil {
			t.Fatal(err)
		}
		used, _ := e.Usage(ctx, owner)
		changes, _, _ := e.Changes(ctx, owner, 0, 0)
		return info, used, changes
	}
	info, used, changes := snapshot(f.eng)
	check := func(label string, e *Engine) {
		t.Helper()
		gotInfo, gotUsed, gotChanges := snapshot(e)
		if gotInfo.Head != info.Head || gotInfo.Head != r.Head || gotUsed != used || len(gotChanges) != len(changes) {
			t.Fatalf("%s: %+v used %d changes %d", label, gotInfo, gotUsed, len(gotChanges))
		}
		if got, _, err := e.Records(ctx, owner, log, 1, 10); err != nil || len(got) != 1 {
			t.Fatalf("%s records: %v", label, err)
		}
	}
	f.eng.Forget()
	check("journal replay", f.eng)
	// With a snapshot, cold start reads it and replays the suffix.
	h, _ := f.eng.open(ctx, owner)
	if err := f.eng.writeSnapshot(ctx, h); err != nil {
		t.Fatal(err)
	}
	h.mu.Unlock()
	f.eng.Forget()
	check("snapshot", f.eng)
	// A snapshot that disagrees with the journal is ignored.
	prefix, _ := drivePrefix(owner)
	seqs, _ := listSeqs(ctx, f.store, prefix+"snapshots/")
	if _, err := f.store.Put(ctx, prefix+"snapshots/"+seqName(seqs[len(seqs)-1])+".json", []byte(`{"format":1,"drive":"alice.poweur.net","seq":1,"segment_hash":"bogus"}`)); err != nil {
		t.Fatal(err)
	}
	f.eng.Forget()
	check("corrupt snapshot", f.eng)
	// A missing committed segment fails closed.
	if err := f.store.Delete(ctx, prefix+"journal/"+seqName(2)+".json"); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Delete(ctx, prefix+"snapshots/"+seqName(seqs[len(seqs)-1])+".json"); err != nil {
		t.Fatal(err)
	}
	f.eng.Forget()
	if _, err := f.eng.Node(ctx, owner, file); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing segment: %v", err)
	}
}

func TestCollect(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	root := f.root()
	old := f.chunk("old content")
	file, v1 := f.file(root, nameHash(1), drive.ModeReplace, old)
	if _, err := f.replace(file, v1, f.chunk("new content")); err != nil {
		t.Fatal(err)
	}
	orphan := f.chunk("uploaded, never committed")
	used, _ := f.eng.Usage(ctx, owner)
	// Nothing is old enough yet.
	if res, err := f.eng.Collect(ctx, owner); err != nil || res.Versions != 0 || res.Orphans != 0 {
		t.Fatalf("early collect: %+v %v", res, err)
	}
	f.clock = f.clock.Add(31 * 24 * time.Hour)
	res, err := f.eng.Collect(ctx, owner)
	if err != nil || res.Versions != 1 || res.Chunks != 1 || res.Orphans != 1 || res.Freed != int64(old.Size) {
		t.Fatalf("collect: %+v %v", res, err)
	}
	after, _ := f.eng.Usage(ctx, owner)
	if after != used-int64(old.Size) {
		t.Fatalf("usage %d -> %d", used, after)
	}
	prefix, _ := drivePrefix(owner)
	for _, id := range []string{old.ID, orphan.ID} {
		if _, err := f.store.Get(ctx, prefix+"chunks/"+id, nil); !errors.Is(err, provider.ErrNotFound) {
			t.Fatalf("chunk %s survived: %v", id, err)
		}
	}
	if _, err := f.eng.Version(ctx, owner, file, v1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("collected version still readable: %v", err)
	}
	// Collection is journalled: a restart does not bring the version back.
	f.eng.Forget()
	if restarted, _ := f.eng.Usage(ctx, owner); restarted != after {
		t.Fatalf("restart usage %d, want %d", restarted, after)
	}
}

func TestQuota(t *testing.T) {
	f := newFixture(t)
	root := f.root()
	a := f.chunk("fits")
	f.quota = int64(a.Size)
	f.file(root, nameHash(1), drive.ModeReplace, a)
	ctxBytes, _ := drive.Context(owner, strings.Repeat("0", 32), drive.PurposeContent, 1)
	data, _ := drive.EncryptChunk(bytes.Repeat([]byte{1}, 32), []byte("too much"), ctxBytes)
	if _, err := f.eng.PutChunk(context.Background(), owner, data); !errors.Is(err, ErrQuota) {
		t.Fatalf("upload over quota: %v", err)
	}
}

func TestConcurrentReplacesOneWins(t *testing.T) {
	f := newFixture(t)
	root := f.root()
	file, v1 := f.file(root, nameHash(1), drive.ModeReplace, f.chunk("base"))
	refs := make([]drive.ChunkRef, 8)
	for i := range refs {
		refs[i] = f.chunk(fmt.Sprintf("writer %d", i))
	}
	var wg sync.WaitGroup
	results := make([]error, len(refs))
	for i := range refs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, results[i] = f.replace(file, v1, refs[i])
		}(i)
	}
	wg.Wait()
	wins := 0
	for _, err := range results {
		switch {
		case err == nil:
			wins++
		case !errors.Is(err, ErrConflict):
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if wins != 1 {
		t.Fatalf("%d writers won", wins)
	}
}

func TestCommitNotifiesAfterDurability(t *testing.T) {
	f := newFixture(t)
	root := f.root()
	f.folder(root, nameHash(1))
	if len(f.changes) != 2 || f.changes[1].Operation != drive.OpCreate || f.changes[0].Node != root {
		t.Fatalf("changes: %+v", f.changes)
	}
	changes, next, err := f.eng.Changes(context.Background(), owner, 1, 10)
	if err != nil || len(changes) != 1 || next != 2 {
		t.Fatalf("changes after 1: %+v %d %v", changes, next, err)
	}
}

// Concurrent appends from several authors, through two engines sharing one
// store (two relay processes): every acknowledged record keeps the position
// it was given, and a cold reader sees the same order.
func TestConcurrentAppendsKeepPositions(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	engines := []*Engine{f.newEngine(), f.newEngine()}
	root := f.root()
	log, _ := f.file(root, nameHash(1), drive.ModeAppend)
	f.grant(log, "bob.poweur.net", drive.RoleAppend)
	const perAuthor = 12
	authors := []string{owner, "bob.poweur.net"}
	type ack struct {
		author   string
		sequence uint64
		position uint64
	}
	var mu sync.Mutex
	var acks []ack
	var wg sync.WaitGroup
	for i, author := range authors {
		wg.Add(1)
		go func(i int, author string) {
			defer wg.Done()
			prev := ""
			for seq := uint64(1); seq <= perAuthor; seq++ {
				r := record(f, log, author, seq, prev, f.chunk(fmt.Sprintf("%s %d", author, seq)))
				e := engines[(i+int(seq))%len(engines)]
				for {
					res, err := e.Commit(ctx, owner, Request{ID: hexID(16), Records: []drive.AppendRecord{r}})
					if errors.Is(err, ErrStale) {
						continue
					}
					if err != nil {
						t.Errorf("%s %d: %v", author, seq, err)
						return
					}
					mu.Lock()
					acks = append(acks, ack{author, seq, res.Positions[0]})
					mu.Unlock()
					break
				}
				prev, _ = r.Hash()
			}
		}(i, author)
	}
	wg.Wait()
	if t.Failed() {
		return
	}
	cold := f.newEngine()
	got, _, err := cold.Records(ctx, owner, log, 1, 0)
	if err != nil || len(got) != perAuthor*len(authors) {
		t.Fatalf("records: %d %v", len(got), err)
	}
	for _, a := range acks {
		r := got[a.position-1].Record
		if r.Author != a.author || r.Sequence != a.sequence {
			t.Fatalf("position %d holds %s/%d, acknowledged %s/%d", a.position, r.Author, r.Sequence, a.author, a.sequence)
		}
	}
}

func TestSystemZone(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	const policy = ".poweur/relay/inbox-policy.json"
	for _, bad := range []string{"poweur/relay/x.json", ".poweur/private/x.json", ".poweur/relay/../x", ".poweur/relay/.x", ".poweur/relay/a/b"} {
		if _, err := f.eng.SystemWrite(ctx, owner, bad, []byte("{}"), WriterOwner, nil); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v", bad, err)
		}
	}
	absent := ""
	h1, err := f.eng.SystemWrite(ctx, owner, policy, []byte(`{"mode":"open"}`), WriterOwner, &absent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.eng.SystemWrite(ctx, owner, policy, []byte(`{"mode":"x"}`), WriterOwner, &absent); !errors.Is(err, ErrExists) {
		t.Fatalf("create over existing: %v", err)
	}
	stale := strings.Repeat("0", 64)
	if _, err := f.eng.SystemWrite(ctx, owner, policy, []byte(`{"mode":"x"}`), WriterOwner, &stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale base: %v", err)
	}
	h2, err := f.eng.SystemWrite(ctx, owner, policy, []byte(`{"mode":"contacts_only"}`), WriterOwner, &h1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.eng.SystemWrite(ctx, owner, ".poweur/state/devices.json", []byte(`{"devices":[]}`), WriterRelay, nil); err != nil {
		t.Fatal(err)
	}
	// The replaced document's bytes are gone; the current ones read back.
	prefix, _ := drivePrefix(owner)
	if _, err := f.store.Get(ctx, prefix+"system/"+h1, nil); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("replaced blob kept: %v", err)
	}
	used, _ := f.eng.Usage(ctx, owner)
	if used != int64(len(`{"mode":"contacts_only"}`)+len(`{"devices":[]}`)) {
		t.Fatalf("usage %d", used)
	}
	check := func(label string) {
		t.Helper()
		raw, hash, err := f.eng.SystemRead(ctx, owner, policy)
		if err != nil || string(raw) != `{"mode":"contacts_only"}` || hash != h2 {
			t.Fatalf("%s read: %q %v", label, raw, err)
		}
		list, _ := f.eng.SystemList(ctx, owner, ".poweur/")
		if len(list) != 2 || list[0].Path != policy || list[1].Writer != WriterRelay {
			t.Fatalf("%s list: %+v", label, list)
		}
	}
	check("warm")
	f.eng.Forget()
	check("cold")
	changes, _, _ := f.eng.Changes(ctx, owner, 0, 0)
	if len(changes) != 3 || changes[2].Path != ".poweur/state/devices.json" || changes[0].Operation != "system.put" {
		t.Fatalf("changes: %+v", changes)
	}
	// A tampered blob is refused rather than served.
	if _, err := f.store.Put(ctx, prefix+"system/"+h2, []byte(`{"mode":"open"}`)); err != nil {
		t.Fatal(err)
	}
	f.eng.Forget()
	if _, _, err := f.eng.SystemRead(ctx, owner, policy); err == nil {
		t.Fatal("tampered system file served")
	}
	if err := f.eng.SystemDelete(ctx, owner, policy, WriterOwner, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.eng.SystemRead(ctx, owner, policy); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted: %v", err)
	}
	if err := f.eng.SystemDelete(ctx, owner, policy, WriterOwner, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete absent: %v", err)
	}
	// Owner writes count against quota; the relay's own records do not.
	f.quota = 1
	if _, err := f.eng.SystemWrite(ctx, owner, ".poweur/public/profile.json", []byte(`{"name":"x"}`), WriterOwner, nil); !errors.Is(err, ErrQuota) {
		t.Fatalf("owner over quota: %v", err)
	}
	if _, err := f.eng.SystemWrite(ctx, owner, ".poweur/state/devices.json", []byte(`{"devices":[1]}`), WriterRelay, nil); err != nil {
		t.Fatalf("relay record over quota: %v", err)
	}
}
