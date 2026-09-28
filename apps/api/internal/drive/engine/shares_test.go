package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/poweur/identity/drive"
)

const bob = "bob.poweur.net"

// commitAs signs m as its author and commits it.
func (f *fixture) commitAs(m drive.Manifest, pages []drive.ChunkPage) (Result, error) {
	f.sign(&m)
	return f.eng.Commit(context.Background(), owner, Request{ID: hexID(16), Manifest: &m, Pages: pages, Author: m.Author})
}

func (f *fixture) replaceAs(author, node string, refs ...drive.ChunkRef) (Result, error) {
	n, _ := f.eng.Node(context.Background(), owner, node)
	pages, hashes, _ := drive.SplitPages(owner, node, refs)
	if hashes == nil {
		hashes = []string{}
	}
	return f.commitAs(drive.Manifest{Format: 1, Drive: owner, Node: node, Version: hexID(16), Parent: n.Head, Operation: drive.OpReplace,
		Author: author, Generation: n.Generation, Kind: drive.KindFile, Mode: drive.ModeReplace, Count: uint64(len(refs)), Pages: hashes}, pages)
}

func (f *fixture) createAs(author, folder, kind, mode string, tag byte) (string, error) {
	id := hexID(16)
	m := drive.Manifest{Format: 1, Drive: owner, Node: id, Version: hexID(16), Operation: drive.OpCreate, Author: author,
		Generation: 1, Kind: kind, Mode: mode, Folder: folder, Name: payload(2), NameHash: nameHash(tag), NodeKey: payload(3), Pages: []string{}}
	if kind == drive.KindFile {
		m.ContentKey = payload(4)
	}
	_, err := f.commitAs(m, nil)
	return id, err
}

func (f *fixture) rotate(node string) error {
	n, _ := f.eng.Node(context.Background(), owner, node)
	head, err := f.eng.Version(context.Background(), owner, node, n.Head)
	if err != nil {
		return err
	}
	m := drive.Manifest{Format: 1, Drive: owner, Node: node, Version: hexID(16), Parent: n.Head, Operation: drive.OpRotate, Author: owner,
		Generation: n.Generation + 1, Kind: n.Kind, Mode: n.Mode, NodeKey: payload(5), Count: n.Count, Pages: head.Pages}
	if n.Kind == drive.KindFile {
		m.ContentKey = payload(6)
	}
	_, err = f.commitAs(m, nil)
	return err
}

func (f *fixture) unshare(actor, id string) error {
	_, err := f.eng.Commit(context.Background(), owner, Request{ID: hexID(16), Unshare: &Unshare{ID: id}, Author: actor})
	return err
}

func TestShareRolesAndInheritance(t *testing.T) {
	f := newFixture(t)
	f.keys["carol.poweur.net"] = f.keys[bob] // any valid key; carol signs nothing here
	ctx := context.Background()
	root := f.root()
	docs := f.folder(root, nameHash(1))
	notes, _ := f.file(docs, nameHash(2), drive.ModeReplace, f.chunk("notes"))
	comments, _ := f.file(docs, nameHash(3), drive.ModeAppend)
	private := f.folder(root, nameHash(4))
	f.grant(docs, bob, drive.RoleRead)
	f.grant(comments, bob, drive.RoleAppend)

	// Read on the folder reaches everything below it, nothing beside it.
	for _, node := range []string{docs, notes, comments} {
		if err := f.eng.Authorize(ctx, owner, bob, node, drive.RoleRead); err != nil {
			t.Fatalf("inherited read on %s: %v", node, err)
		}
	}
	if err := f.eng.Authorize(ctx, owner, bob, private, drive.RoleRead); !errors.Is(err, ErrForbidden) {
		t.Fatalf("read outside the share: %v", err)
	}
	// Read does not write; the append grant on one file adds appends only there.
	if _, err := f.replaceAs(bob, notes, f.chunk("edit")); !errors.Is(err, ErrForbidden) {
		t.Fatalf("reader replaced: %v", err)
	}
	r := record(f, comments, bob, 1, "", f.chunk("a comment"))
	if _, err := f.eng.Commit(ctx, owner, Request{ID: hexID(16), Records: []drive.AppendRecord{r}}); err != nil {
		t.Fatalf("member append: %v", err)
	}
	// Strangers get nothing, including whether a node exists.
	if err := f.eng.Authorize(ctx, owner, "carol.poweur.net", notes, drive.RoleRead); !errors.Is(err, ErrForbidden) {
		t.Fatalf("stranger: %v", err)
	}
	if err := f.eng.Authorize(ctx, owner, "carol.poweur.net", strings.Repeat("e", 32), drive.RoleRead); !errors.Is(err, ErrForbidden) {
		t.Fatalf("stranger probing an unknown node: %v", err)
	}
	// Survives a cold start.
	f.eng.Forget()
	if err := f.eng.Authorize(ctx, owner, bob, notes, drive.RoleRead); err != nil {
		t.Fatalf("after restart: %v", err)
	}
}

func TestAppendAndCreateWithoutRead(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	root := f.root()
	log, _ := f.file(root, nameHash(1), drive.ModeAppend)
	inbox := f.folder(root, nameHash(2))
	earlier, _ := f.file(inbox, nameHash(3), drive.ModeReplace, f.chunk("an earlier submission"))
	f.grant(log, bob, drive.RoleAppend)
	f.grant(inbox, bob, drive.RoleCreate)

	if err := f.eng.Authorize(ctx, owner, bob, log, drive.RoleRead); !errors.Is(err, ErrForbidden) {
		t.Fatalf("append-only member can read: %v", err)
	}
	r := record(f, log, bob, 1, "", f.chunk("mine"))
	if _, err := f.eng.Commit(ctx, owner, Request{ID: hexID(16), Records: []drive.AppendRecord{r}}); err != nil {
		t.Fatalf("append: %v", err)
	}
	// Create-only: a new file lands; prior submissions stay closed.
	if _, err := f.createAs(bob, inbox, drive.KindFile, drive.ModeReplace, 9); err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, node := range []string{inbox, earlier} {
		if err := f.eng.Authorize(ctx, owner, bob, node, drive.RoleRead); !errors.Is(err, ErrForbidden) {
			t.Fatalf("create-only member reads %s: %v", node, err)
		}
	}
	if _, err := f.replaceAs(bob, earlier, f.chunk("overwrite")); !errors.Is(err, ErrForbidden) {
		t.Fatalf("create-only member replaced a submission: %v", err)
	}
	if _, err := f.createAs(bob, root, drive.KindFile, drive.ModeReplace, 10); !errors.Is(err, ErrForbidden) {
		t.Fatalf("create outside the shared folder: %v", err)
	}
}

func TestAdminDelegationAndShareChecks(t *testing.T) {
	f := newFixture(t)
	const carol = "carol.poweur.net"
	f.keys[carol] = f.keys[bob]
	ctx := context.Background()
	root := f.root()
	team := f.folder(root, nameHash(1))
	f.grant(team, bob, drive.RoleAdmin)
	n, _ := f.eng.Node(ctx, owner, team)
	share := func(issuer, member, role string, generation uint64) error {
		id, _ := drive.NewShareID()
		s := drive.Share{Format: 1, Drive: owner, ID: id, Node: team, Member: member, Role: role, Generation: generation,
			NodePublic: "CAgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAg", Issuer: issuer, Issued: "2026-09-28T00:00:00Z"}
		if drive.KeyBearing(role) {
			s.NodeKey = payload(9)
		}
		if err := s.Sign(f.keys[issuer]); err != nil {
			t.Fatal(err)
		}
		_, err := f.eng.Commit(ctx, owner, Request{ID: hexID(16), Share: &s, Author: issuer})
		return err
	}
	if err := share(bob, carol, drive.RoleWrite, n.Generation); err != nil {
		t.Fatalf("admin delegates: %v", err)
	}
	if err := share(carol, "dave.poweur.net", drive.RoleRead, n.Generation); !errors.Is(err, ErrForbidden) {
		t.Fatalf("writer delegates: %v", err)
	}
	if err := share(bob, carol, drive.RoleRead, n.Generation+1); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale generation: %v", err)
	}
	if err := share(owner, owner, drive.RoleRead, n.Generation); !errors.Is(err, ErrInvalid) {
		t.Fatalf("sharing with the owner: %v", err)
	}
	// Bob may administer team, not the root above it.
	if err := f.eng.Authorize(ctx, owner, bob, root, drive.RoleRead); !errors.Is(err, ErrForbidden) {
		t.Fatalf("admin above the share: %v", err)
	}
	listed, _ := f.eng.Shares(ctx, owner, carol)
	if len(listed) != 1 || listed[0].Member != carol {
		t.Fatalf("carol's shares: %+v", listed)
	}
	if all, _ := f.eng.Shares(ctx, owner, bob); len(all) != 2 {
		t.Fatalf("admin sees shares on its node: %+v", all)
	}
}

func TestRevocationForcesRotation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	root := f.root()
	docs := f.folder(root, nameHash(1))
	notes, _ := f.file(docs, nameHash(2), drive.ModeReplace, f.chunk("before"))
	outside, _ := f.file(root, nameHash(3), drive.ModeReplace, f.chunk("unrelated"))
	s := f.grant(docs, bob, drive.RoleRead)
	if err := f.unshare(bob, strings.Repeat("0", 32)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown share: %v", err)
	}
	if err := f.unshare(owner, s.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.eng.Authorize(ctx, owner, bob, notes, drive.RoleRead); !errors.Is(err, ErrForbidden) {
		t.Fatalf("revoked member still reads: %v", err)
	}
	// Bob holds the old folder key: nothing new is written under it.
	if _, err := f.replaceAs(owner, notes, f.chunk("after")); !errors.Is(err, ErrConflict) {
		t.Fatalf("write under a revoked key: %v", err)
	}
	if _, err := f.createAs(owner, docs, drive.KindFile, drive.ModeReplace, 7); !errors.Is(err, ErrConflict) {
		t.Fatalf("create under a revoked folder key: %v", err)
	}
	if _, err := f.replaceAs(owner, outside, f.chunk("fine")); err != nil {
		t.Fatalf("unrelated node blocked: %v", err)
	}
	info, _ := f.eng.Node(ctx, owner, docs)
	if _, err := f.eng.Commit(ctx, owner, Request{ID: hexID(16), Author: owner, Share: func() *drive.Share {
		again := s
		again.ID = hexID(16)
		again.Generation = info.Generation
		_ = again.Sign(f.keys[owner])
		return &again
	}()}); !errors.Is(err, ErrConflict) {
		t.Fatalf("re-sharing an unrotated key: %v", err)
	}
	// Rotating clears the node it rotates, and only that node.
	if err := f.rotate(notes); err != nil {
		t.Fatal(err)
	}
	if _, err := f.replaceAs(owner, notes, f.chunk("after")); err != nil {
		t.Fatalf("write after rotation: %v", err)
	}
	if _, err := f.createAs(owner, docs, drive.KindFile, drive.ModeReplace, 7); !errors.Is(err, ErrConflict) {
		t.Fatalf("folder still needs its own rotation: %v", err)
	}
	if err := f.rotate(docs); err != nil {
		t.Fatal(err)
	}
	if _, err := f.createAs(owner, docs, drive.KindFile, drive.ModeReplace, 7); err != nil {
		t.Fatalf("create after folder rotation: %v", err)
	}
	// Revoking an append-only share hands out no key, so nothing rotates.
	log, _ := f.file(root, nameHash(8), drive.ModeAppend)
	a := f.grant(log, bob, drive.RoleAppend)
	if err := f.unshare(bob, a.ID); err != nil {
		t.Fatalf("member leaves: %v", err)
	}
	if n, _ := f.eng.Node(ctx, owner, log); n.RotateRequired {
		t.Fatal("append-only revocation forced a rotation")
	}
}

func TestExpiryAndFilteredChanges(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	root := f.root()
	docs := f.folder(root, nameHash(1))
	private := f.folder(root, nameHash(2))
	s := f.grant(docs, bob, drive.RoleRead)
	notes, _ := f.file(docs, nameHash(3), drive.ModeReplace, f.chunk("shared"))
	f.file(private, nameHash(4), drive.ModeReplace, f.chunk("mine"))
	if _, err := f.eng.SystemWrite(ctx, owner, ".poweur/relay/contacts.json", []byte(`{}`), WriterOwner, nil); err != nil {
		t.Fatal(err)
	}
	changes, next, err := f.eng.ChangesFor(ctx, owner, bob, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	for _, c := range changes {
		seen = append(seen, c.Operation+":"+c.Node+c.Path)
	}
	want := []string{"create:" + docs, "share:" + docs, "create:" + notes}
	if strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Fatalf("bob sees %v, want %v", seen, want)
	}
	all, _, _ := f.eng.Changes(ctx, owner, 0, 0)
	if next != all[len(all)-1].Seq {
		t.Fatalf("cursor %d does not reach the end %d", next, all[len(all)-1].Seq)
	}
	if _, _, err := f.eng.ChangesFor(ctx, owner, "carol.poweur.net", 0, 0); !errors.Is(err, ErrForbidden) {
		t.Fatalf("stranger reads the feed: %v", err)
	}
	// An expiring share stops working at its expiry, without a commit.
	f.eng.Forget()
	expiring := s
	expiring.ID, expiring.Node, expiring.Expires = hexID(16), private, f.clock.Add(time.Hour).UTC().Truncate(time.Second).Format(time.RFC3339)
	n, _ := f.eng.Node(ctx, owner, private)
	expiring.Generation = n.Generation
	_ = expiring.Sign(f.keys[owner])
	if _, err := f.eng.Commit(ctx, owner, Request{ID: hexID(16), Share: &expiring, Author: owner}); err != nil {
		t.Fatal(err)
	}
	if err := f.eng.Authorize(ctx, owner, bob, private, drive.RoleRead); err != nil {
		t.Fatalf("before expiry: %v", err)
	}
	f.clock = f.clock.Add(2 * time.Hour)
	if err := f.eng.Authorize(ctx, owner, bob, private, drive.RoleRead); !errors.Is(err, ErrForbidden) {
		t.Fatalf("after expiry: %v", err)
	}
}
