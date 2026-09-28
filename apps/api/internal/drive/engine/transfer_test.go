package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/poweur/identity/drive"
)

func TestTransferRetiresSubtreeAndForwardsLinks(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	root := f.root()
	board := f.folder(root, nameHash(1))
	card, _ := f.file(board, nameHash(2), drive.ModeReplace, f.chunk("a card"))
	log, _ := f.file(board, nameHash(3), drive.ModeAppend)
	r := record(f, log, owner, 1, "", f.chunk("an event"))
	if _, err := f.eng.Commit(ctx, owner, Request{ID: hexID(16), Records: []drive.AppendRecord{r}}); err != nil {
		t.Fatal(err)
	}
	f.grant(board, bob, drive.RoleWrite)
	linkID, _ := drive.NewShareID()
	f.grantWith(board, "", drive.RoleRead, func(s *drive.Share) { s.Member, s.Link = "", linkID })
	keep := f.folder(root, nameHash(4))
	used, _ := f.eng.Usage(ctx, owner)

	transfer := func(actor string, x Transfer) error {
		_, err := f.eng.Commit(ctx, owner, Request{ID: hexID(16), Transfer: &x, Author: actor})
		return err
	}
	to := Transfer{Node: board, To: "space.poweur.net", ToNode: hexID(16)}
	if err := transfer(bob, to); !errors.Is(err, ErrForbidden) {
		t.Fatalf("writer transfers: %v", err)
	}
	for _, bad := range []Transfer{{Node: root, To: to.To, ToNode: to.ToNode}, {Node: board, To: owner, ToNode: to.ToNode}, {Node: board, To: "Space.poweur.net", ToNode: to.ToNode}, {Node: board, To: to.To, ToNode: "x"}} {
		if err := transfer(owner, bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%+v: %v", bad, err)
		}
	}
	if err := transfer(owner, to); err != nil {
		t.Fatal(err)
	}
	for _, node := range []string{board, card, log} {
		if n, _ := f.eng.Node(ctx, owner, node); !n.Removed {
			t.Fatalf("%s still live", node)
		}
	}
	if n, _ := f.eng.Node(ctx, owner, keep); n.Removed {
		t.Fatal("sibling retired")
	}
	if err := f.eng.Authorize(ctx, owner, bob, card, drive.RoleRead); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member keeps access to the old copy: %v", err)
	}
	if _, err := f.eng.Link(ctx, owner, linkID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old link still live: %v", err)
	}
	fwd, ok, _ := f.eng.Forwarded(ctx, owner, linkID)
	if !ok || fwd.Drive != to.To || fwd.Node != to.ToNode {
		t.Fatalf("link forward: %+v %v", fwd, ok)
	}
	if moved, ok, _ := f.eng.Moved(ctx, owner, board); !ok || moved.Drive != to.To {
		t.Fatalf("node forward: %+v", moved)
	}
	// The log's record chunk is released now; the card's content after retention.
	after, _ := f.eng.Usage(ctx, owner)
	if after >= used {
		t.Fatalf("usage did not drop: %d -> %d", used, after)
	}
	f.eng.Forget()
	if fwd2, ok, _ := f.eng.Forwarded(ctx, owner, linkID); !ok || fwd2 != fwd {
		t.Fatal("forward lost on restart")
	}
	if kids, _, _ := f.eng.Children(ctx, owner, root, "", 0); len(kids) != 1 || kids[0].ID != keep {
		t.Fatalf("root children after transfer: %+v", kids)
	}
}

func TestRemovingAnAppendFileReleasesRecords(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	root := f.root()
	log, _ := f.file(root, nameHash(1), drive.ModeAppend)
	ref := f.chunk("entry")
	r := record(f, log, owner, 1, "", ref)
	if _, err := f.eng.Commit(ctx, owner, Request{ID: hexID(16), Records: []drive.AppendRecord{r}}); err != nil {
		t.Fatal(err)
	}
	before, _ := f.eng.Usage(ctx, owner)
	n, _ := f.eng.Node(ctx, owner, log)
	if _, err := f.commit(drive.Manifest{Format: 1, Drive: owner, Node: log, Version: hexID(16), Parent: n.Head, Operation: drive.OpRemove,
		Author: owner, Generation: 1, Kind: drive.KindFile, Mode: drive.ModeAppend, Pages: []string{}}, nil); err != nil {
		t.Fatal(err)
	}
	if after, _ := f.eng.Usage(ctx, owner); after != before-int64(ref.Size) {
		t.Fatalf("usage %d -> %d, want -%d", before, after, ref.Size)
	}
}
