package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/poweur/identity/drive"
)

func (f *fixture) setRoster(group string, members, admins []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rosters == nil {
		f.rosters = map[string][2][]string{}
	}
	f.rosters[group] = [2][]string{members, admins}
}

func TestGroupMembers(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	const team, carol, dave = "team.poweur.net", "carol.poweur.net", "dave.poweur.net"
	f.setRoster(team, []string{bob}, []string{carol})
	root := f.root()
	docs := f.folder(root, nameHash(1))
	notes, _ := f.file(docs, nameHash(2), drive.ModeReplace, f.chunk("team notes"))
	other := f.folder(root, nameHash(3))
	f.grant(docs, team, drive.RoleRead)
	f.grant(other, team, drive.RoleRead)
	for _, actor := range []string{bob, carol} {
		if err := f.eng.Authorize(ctx, owner, actor, notes, drive.RoleRead); err != nil {
			t.Fatalf("%s through the group: %v", actor, err)
		}
	}
	if err := f.eng.Authorize(ctx, owner, dave, notes, drive.RoleRead); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-member: %v", err)
	}
	// Bob leaves the roster: access ends at once, and the keys he held must rotate.
	f.setRoster(team, nil, []string{carol})
	if err := f.eng.RevokeGroupMembers(ctx, owner, team, []string{bob}); err != nil {
		t.Fatal(err)
	}
	if err := f.eng.Authorize(ctx, owner, bob, notes, drive.RoleRead); !errors.Is(err, ErrForbidden) {
		t.Fatalf("removed member: %v", err)
	}
	for _, node := range []string{docs, notes, other} {
		if n, _ := f.eng.Node(ctx, owner, node); !n.RotateRequired {
			t.Fatalf("%s not marked for rotation", node)
		}
	}
	if n, _ := f.eng.Node(ctx, owner, root); n.RotateRequired {
		t.Fatal("root marked, but nothing shared it")
	}
	// Carol sees the revocation in her feed.
	changes, _, err := f.eng.ChangesFor(ctx, owner, carol, 0, 0)
	if err != nil || changes[len(changes)-1].Operation != "group.revoke" {
		t.Fatalf("carol's feed: %+v %v", changes, err)
	}
	// A drive with no share to the group journals nothing.
	before, _, _ := f.eng.Changes(ctx, owner, 0, 0)
	if err := f.eng.RevokeGroupMembers(ctx, owner, "unrelated.poweur.net", []string{bob}); err != nil {
		t.Fatal(err)
	}
	if after, _, _ := f.eng.Changes(ctx, owner, 0, 0); len(after) != len(before) {
		t.Fatal("unrelated revocation was journalled")
	}
}

// A Space: the drive belongs to a group identity. Its admins administer the
// drive; a share naming the group itself reaches every member.
func TestSpaceDrive(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	const carol = "carol.poweur.net"
	f.keys[carol] = f.keys[bob]
	f.setRoster(owner, []string{carol}, []string{bob})
	root := f.root()
	board := f.folder(root, nameHash(1))
	if err := f.eng.Authorize(ctx, owner, bob, board, drive.RoleAdmin); err != nil {
		t.Fatalf("space admin: %v", err)
	}
	if _, err := f.createAs(bob, board, drive.KindFile, drive.ModeReplace, 2); err != nil {
		t.Fatalf("space admin creates: %v", err)
	}
	if err := f.eng.Authorize(ctx, owner, carol, board, drive.RoleRead); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member before any share: %v", err)
	}
	f.grant(board, owner, drive.RoleWrite)
	if err := f.eng.Authorize(ctx, owner, carol, board, drive.RoleWrite); err != nil {
		t.Fatalf("member through the space-wide share: %v", err)
	}
	// On a personal drive the owner still cannot be a member.
	f.mu.Lock()
	delete(f.rosters, owner)
	f.mu.Unlock()
	n, _ := f.eng.Node(ctx, owner, root)
	id, _ := drive.NewShareID()
	s := drive.Share{Format: 1, Drive: owner, ID: id, Node: root, Member: owner, Role: drive.RoleRead, Generation: n.Generation, NodeKey: payload(1),
		NodePublic: "CAgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAg", Issuer: owner, Issued: "2026-09-28T00:00:00Z"}
	_ = s.Sign(f.keys[owner])
	if _, err := f.eng.Commit(ctx, owner, Request{ID: hexID(16), Share: &s, Author: owner}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("share with a personal owner: %v", err)
	}
}
