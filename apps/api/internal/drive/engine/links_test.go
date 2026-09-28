package engine

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/poweur/identity/drive"
)

// grantWith is grant with caps and link fields applied before signing.
func (f *fixture) grantWith(node, member, role string, edit func(*drive.Share)) drive.Share {
	f.t.Helper()
	n, _ := f.eng.Node(context.Background(), owner, node)
	id, _ := drive.NewShareID()
	s := drive.Share{Format: 1, Drive: owner, ID: id, Node: node, Member: member, Role: role, Generation: n.Generation,
		NodePublic: base64.RawURLEncoding.EncodeToString(make([]byte, 32)), Issuer: owner, Issued: "2026-09-28T00:00:00Z"}
	if drive.KeyBearing(role) {
		s.NodeKey = payload(9)
	}
	edit(&s)
	if err := s.Sign(f.keys[owner]); err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.eng.Commit(context.Background(), owner, Request{ID: hexID(16), Share: &s, Author: owner}); err != nil {
		f.t.Fatal(err)
	}
	return s
}

func TestShareCaps(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	root := f.root()
	inbox := f.folder(root, nameHash(1))
	log, _ := f.file(root, nameHash(2), drive.ModeAppend)
	f.grantWith(inbox, bob, drive.RoleCreate, func(s *drive.Share) { s.Caps.Files = 1 })
	f.grantWith(log, bob, drive.RoleAppend, func(s *drive.Share) { s.Caps.Records = 2 })

	// One form response per identity.
	if _, err := f.createAs(bob, inbox, drive.KindFile, drive.ModeReplace, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := f.createAs(bob, inbox, drive.KindFile, drive.ModeReplace, 4); !errors.Is(err, ErrCap) {
		t.Fatalf("second response: %v", err)
	}
	prev := ""
	for seq := uint64(1); seq <= 3; seq++ {
		r := record(f, log, bob, seq, prev, f.chunk("line"))
		_, err := f.eng.Commit(ctx, owner, Request{ID: hexID(16), Records: []drive.AppendRecord{r}})
		if seq <= 2 && err != nil || seq == 3 && !errors.Is(err, ErrCap) {
			t.Fatalf("record %d: %v", seq, err)
		}
		prev, _ = r.Hash()
	}
	// Spending is journalled: a cold start still refuses.
	f.eng.Forget()
	if _, err := f.createAs(bob, inbox, drive.KindFile, drive.ModeReplace, 5); !errors.Is(err, ErrCap) {
		t.Fatalf("after restart: %v", err)
	}
	// The owner is never capped.
	if _, err := f.createAs(owner, inbox, drive.KindFile, drive.ModeReplace, 6); err != nil {
		t.Fatalf("owner: %v", err)
	}
	// Bytes caps are quota errors.
	photos := f.folder(root, nameHash(7))
	f.grantWith(photos, bob, drive.RoleWrite, func(s *drive.Share) { s.Caps.Bytes = 1 })
	file, err := f.createAs(bob, photos, drive.KindFile, drive.ModeReplace, 8)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.replaceAs(bob, file, f.chunk("too big")); !errors.Is(err, ErrQuota) {
		t.Fatalf("bytes cap: %v", err)
	}
}

func TestLinks(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	root := f.root()
	docs := f.folder(root, nameHash(1))
	notes, _ := f.file(docs, nameHash(2), drive.ModeReplace, f.chunk("public-ish"))
	linkID, _ := drive.NewShareID()
	verifier := []byte("verifier half of argon2id")
	f.grantWith(docs, "", drive.RoleRead, func(s *drive.Share) {
		s.Member, s.Link = "", linkID
		s.KDF, s.Salt, s.VerifierHash = drive.ShareKDF, base64.RawURLEncoding.EncodeToString(make([]byte, 16)), drive.VerifierHash(verifier)
		s.Caps.Downloads = 2
	})
	info, err := f.eng.Link(ctx, owner, linkID)
	if err != nil || !info.Password || info.Salt == "" || info.Remaining != 2 {
		t.Fatalf("link info: %+v %v", info, err)
	}
	if err := f.eng.OpenLink(ctx, owner, linkID, []byte("wrong"), true); !errors.Is(err, ErrForbidden) {
		t.Fatalf("wrong password: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := f.eng.OpenLink(ctx, owner, linkID, verifier, true); err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
	}
	// Reads with the link actor follow the share's role.
	actor := LinkActor(linkID)
	if err := f.eng.Authorize(ctx, owner, actor, notes, drive.RoleRead); err != nil {
		t.Fatalf("link read: %v", err)
	}
	if err := f.eng.Authorize(ctx, owner, actor, root, drive.RoleRead); !errors.Is(err, ErrForbidden) {
		t.Fatalf("link read outside: %v", err)
	}
	// Opens are counted durably; the exhausted link disappears.
	f.eng.Forget()
	if err := f.eng.OpenLink(ctx, owner, linkID, verifier, true); !errors.Is(err, ErrCap) {
		t.Fatalf("third open: %v", err)
	}
	if _, err := f.eng.Link(ctx, owner, linkID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("exhausted link info: %v", err)
	}
	if _, err := f.eng.Link(ctx, owner, "0123456789abcdef0123456789abcdef"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown link: %v", err)
	}
}

func TestGuestWritesThroughLinks(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	root := f.root()
	inbox := f.folder(root, nameHash(1))
	linkID, _ := drive.NewShareID()
	f.grantWith(inbox, "", drive.RoleCreate, func(s *drive.Share) { s.Member, s.Link, s.Caps.Files = "", linkID, 1 })
	_, guestPriv, _ := ed25519.GenerateKey(nil)
	guest := drive.GuestAuthor(guestPriv.Public().(ed25519.PublicKey))
	submission := func(committer string) error {
		m := drive.Manifest{Format: 1, Drive: owner, Node: hexID(16), Version: hexID(16), Operation: drive.OpCreate, Author: guest,
			Generation: 1, Kind: drive.KindFile, Mode: drive.ModeReplace, Folder: inbox, Name: payload(2), NameHash: hexID(32),
			NodeKey: payload(3), ContentKey: payload(4), Pages: []string{}}
		if err := m.Sign(guestPriv); err != nil {
			t.Fatal(err)
		}
		_, err := f.eng.Commit(ctx, owner, Request{ID: hexID(16), Manifest: &m, Author: committer})
		return err
	}
	if err := submission(""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("guest without a link: %v", err)
	}
	if err := submission(bob); !errors.Is(err, ErrForbidden) {
		t.Fatalf("guest through an identity: %v", err)
	}
	if err := submission(LinkActor(hexID(16))); !errors.Is(err, ErrForbidden) {
		t.Fatalf("guest through an unknown link: %v", err)
	}
	if err := submission(LinkActor(linkID)); err != nil {
		t.Fatalf("guest through the link: %v", err)
	}
	if err := submission(LinkActor(linkID)); !errors.Is(err, ErrCap) {
		t.Fatalf("link's file cap: %v", err)
	}
	// An identity cannot pass off another identity's signed manifest.
	m := drive.Manifest{Format: 1, Drive: owner, Node: hexID(16), Version: hexID(16), Operation: drive.OpCreate, Author: owner,
		Generation: 1, Kind: drive.KindFolder, Folder: root, Name: payload(2), NameHash: nameHash(9), NodeKey: payload(3), Pages: []string{}}
	f.sign(&m)
	if _, err := f.eng.Commit(ctx, owner, Request{ID: hexID(16), Manifest: &m, Author: bob}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("replayed manifest: %v", err)
	}
}
