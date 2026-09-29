package engine

import (
	"context"
	"testing"

	"github.com/poweur/identity/drive"
)

// A listing hands a reader every child with the versions carrying its
// envelopes, in one call; Path does the same for a node's ancestry.
func TestListingAndPath(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	root := f.root()
	docs := f.folder(root, nameHash(1))
	file, v1 := f.file(docs, nameHash(2), drive.ModeReplace, f.chunk("one"))
	res, err := f.replace(file, v1, f.chunk("two"))
	if err != nil {
		t.Fatal(err)
	}
	other, _ := f.file(docs, nameHash(3), drive.ModeReplace, f.chunk("three"))
	s := f.grant(file, bob, drive.RoleRead)

	l, err := f.eng.Listing(ctx, owner, docs, "", 0)
	if err != nil || l.Folder.ID != docs || len(l.Children) != 2 {
		t.Fatalf("listing: %+v %v", l, err)
	}
	for _, c := range l.Children {
		if c.ID != file {
			continue
		}
		// Head first, then the create version holding every envelope.
		if len(c.Versions) != 2 || c.Versions[0].Version != res.Head || c.Versions[1].Version != v1 || c.KeyVersion != v1 || c.NameVersion != v1 || c.ContentVersion != v1 {
			t.Fatalf("file entry: %+v", c)
		}
	}
	if len(l.Shares) != 1 || l.Shares[0].ID != s.ID {
		t.Fatalf("evidence: %+v", l.Shares)
	}
	// Pages of one child follow the cursor.
	first, _ := f.eng.Listing(ctx, owner, docs, "", 1)
	second, _ := f.eng.Listing(ctx, owner, docs, first.Cursor, 1)
	if len(first.Children) != 1 || first.Cursor == "" || len(second.Children) != 1 || second.Children[0].ID == first.Children[0].ID || second.Cursor != "" {
		t.Fatalf("pages: %+v / %+v", first, second)
	}
	path, err := f.eng.Path(ctx, owner, file)
	if err != nil || len(path) != 3 || path[0].ID != file || path[1].ID != docs || path[2].ID != root {
		t.Fatalf("path: %+v %v", path, err)
	}
	_ = other

	// Nodes from a snapshot that predates envelope tracking are filled in by
	// walking back from the head.
	h, _ := f.eng.open(ctx, owner)
	n := h.st.Nodes[file]
	n.KeyVersion, n.NameVersion, n.ContentVersion = "", "", ""
	h.mu.Unlock()
	l, _ = f.eng.Listing(ctx, owner, docs, "", 0)
	for _, c := range l.Children {
		if c.ID == file && (c.KeyVersion != v1 || c.NameVersion != v1 || c.ContentVersion != v1 || len(c.Versions) != 2) {
			t.Fatalf("legacy fill: %+v", c)
		}
	}
}
