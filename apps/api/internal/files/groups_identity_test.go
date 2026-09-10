package files

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	idpkg "github.com/poweur/identity"
)

// Group identities in the grant engine (E05-T5). Unlike an owner-local
// group, the membership document lives in the *group's own* tree and is
// signed by the *group's own* key, so these tests need more than one
// identity — which is the point: naming a group in a grant must confer no
// power at all over that group.

const groupID = "team.acme.poweur.net"

type groupIdentityFixture struct {
	store *GrantStore
	keys  map[string]ed25519.PrivateKey
	pubs  map[string]ed25519.PublicKey
	homes map[string]string
	logs  []string
}

func newGroupIdentityFixture(t *testing.T, identities ...string) *groupIdentityFixture {
	t.Helper()
	dir := t.TempDir()
	fx := &groupIdentityFixture{
		keys:  map[string]ed25519.PrivateKey{},
		pubs:  map[string]ed25519.PublicKey{},
		homes: map[string]string{},
	}
	for _, id := range identities {
		home := filepath.Join(dir, "identities", strings.NewReplacer(".", "__").Replace(id))
		if err := os.MkdirAll(home, 0o700); err != nil {
			t.Fatal(err)
		}
		pub, priv, _ := ed25519.GenerateKey(nil)
		fx.homes[strings.ToLower(id)] = home
		fx.keys[strings.ToLower(id)] = priv
		fx.pubs[strings.ToLower(id)] = pub
	}
	homeFn := func(identity string) (string, error) {
		home, ok := fx.homes[strings.ToLower(identity)]
		if !ok {
			return "", os.ErrNotExist
		}
		return home, nil
	}
	provider := NewFSProvider(dir, homeFn)
	for _, id := range identities {
		if err := provider.EnsureTree(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}
	fx.store = &GrantStore{
		Provider: provider,
		OwnerKey: func(owner string) (ed25519.PublicKey, bool) {
			pub, ok := fx.pubs[strings.ToLower(owner)]
			return pub, ok
		},
		Logf: func(format string, args ...any) { fx.logs = append(fx.logs, format) },
	}
	return fx
}

func (fx *groupIdentityFixture) write(t *testing.T, identity, path string, doc any) {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	full := filepath.Join(fx.homes[strings.ToLower(identity)], filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// setMembership writes the group's own self.json, signed by the group key.
func (fx *groupIdentityFixture) setMembership(t *testing.T, epoch int, admins, members []string) {
	t.Helper()
	gr := idpkg.ShareGroup{
		Group: groupID, Owner: groupID,
		Members: members, Admins: admins, Epoch: epoch,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := gr.Sign(fx.keys[groupID]); err != nil {
		t.Fatal(err)
	}
	fx.write(t, groupID, idpkg.GroupSelfDoc, gr)
}

// grantToGroup writes alice's grant naming the group identity.
func (fx *groupIdentityFixture) grantToGroup(t *testing.T, shareID, path, group string, perms []string) {
	t.Helper()
	g := idpkg.ShareGrant{
		ShareID: shareID, Owner: grantOwner, Path: path,
		Audience: []idpkg.ShareAudience{{Group: group}}, Permissions: perms,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := g.Sign(fx.keys[grantOwner]); err != nil {
		t.Fatal(err)
	}
	fx.write(t, grantOwner, "poweur-sys/relay/shares/"+shareID+".json", g)
}

func (fx *groupIdentityFixture) snap() *GrantSet {
	return fx.store.Snapshot(context.Background(), grantOwner)
}

// The acceptance case: a grant to a group identity gives every member
// access, and adding one is a single signed update to a document that is
// not in the granting owner's tree at all.
func TestGroupIdentityGrantsAccessToMembers(t *testing.T) {
	fx := newGroupIdentityFixture(t, grantOwner, groupID)
	fx.setMembership(t, 1, []string{bob}, []string{bob})
	fx.grantToGroup(t, "shr_gid", "shared/project", groupID, []string{idpkg.PermRead, idpkg.PermWrite})

	set := fx.snap()
	if !set.Allowed(grantOwner, bob, "shared/project/readme.txt", AccessRead) {
		t.Fatal("a member must read through the group grant")
	}
	if !set.Allowed(grantOwner, bob, "shared/project/readme.txt", AccessWrite) {
		t.Fatal("a member must write through a read-write group grant")
	}
	if set.Allowed(grantOwner, carol, "shared/project/readme.txt", AccessRead) {
		t.Fatal("a non-member must not read")
	}

	// One signed membership update: carol in, bob out. The grant is not
	// touched — and could not be, by carol or bob.
	fx.setMembership(t, 2, []string{bob}, []string{carol})
	set = fx.snap()
	if !set.Allowed(grantOwner, carol, "shared/project/readme.txt", AccessRead) {
		t.Fatal("an added member must gain access with no new grant")
	}
	if set.Allowed(grantOwner, bob, "shared/project/readme.txt", AccessRead) {
		t.Fatal("a removed member must lose access on the next request")
	}
}

// Naming a group in a grant must confer no power over that group. Only the
// group's own key can say who is in it.
func TestGroupIdentityMembershipIsNotForgeableByTheGrantOwner(t *testing.T) {
	fx := newGroupIdentityFixture(t, grantOwner, groupID)
	fx.setMembership(t, 1, []string{bob}, []string{bob})
	fx.grantToGroup(t, "shr_gid", "shared/project", groupID, []string{idpkg.PermRead})

	// Alice signs a membership document for a group she does not own and
	// drops it into the group's tree (the relay stores both, so this is the
	// realistic compromise).
	forged := idpkg.ShareGroup{
		Group: groupID, Owner: groupID,
		Members: []string{bob, carol, dave}, Admins: []string{grantOwner},
		Epoch:     99,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := forged.Sign(fx.keys[grantOwner]); err != nil {
		t.Fatal(err)
	}
	fx.write(t, groupID, idpkg.GroupSelfDoc, forged)

	set := fx.snap()
	for _, who := range []string{bob, carol, dave} {
		if set.Allowed(grantOwner, who, "shared/project/readme.txt", AccessRead) {
			t.Fatalf("%s got access through a forged membership document", who)
		}
	}
	// It also fails loudly rather than silently.
	if len(fx.logs) == 0 {
		t.Fatal("a rejected group identity must be logged")
	}
}

// A membership document that names a different group must not be served
// for this one.
func TestGroupIdentityDocumentMustNameItself(t *testing.T) {
	fx := newGroupIdentityFixture(t, grantOwner, groupID)
	fx.grantToGroup(t, "shr_gid", "shared/project", groupID, []string{idpkg.PermRead})

	// Correctly signed by the group key, but claiming to be another group.
	impostor := idpkg.ShareGroup{
		Group: "other.acme.poweur.net", Owner: "other.acme.poweur.net",
		Members: []string{bob}, Admins: []string{bob}, Epoch: 1,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := impostor.Sign(fx.keys[groupID]); err != nil {
		t.Fatal(err)
	}
	fx.write(t, groupID, idpkg.GroupSelfDoc, impostor)

	if fx.snap().Allowed(grantOwner, bob, "shared/project/readme.txt", AccessRead) {
		t.Fatal("a document naming another group must not grant access here")
	}
}

// An owner-local group document sitting at self.json is not a group
// identity, and must not be treated as one.
func TestGroupIdentityRejectsOwnerLocalDocumentAtSelf(t *testing.T) {
	fx := newGroupIdentityFixture(t, grantOwner, groupID)
	fx.grantToGroup(t, "shr_gid", "shared/project", groupID, []string{idpkg.PermRead})

	local := idpkg.ShareGroup{
		Group: "team", Owner: groupID, Members: []string{bob},
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := local.Sign(fx.keys[groupID]); err != nil {
		t.Fatal(err)
	}
	fx.write(t, groupID, idpkg.GroupSelfDoc, local)

	if fx.snap().Allowed(grantOwner, bob, "shared/project/readme.txt", AccessRead) {
		t.Fatal("an owner-local document at self.json must not act as a group identity")
	}
}

// A group with no membership document, and one hosted somewhere this relay
// cannot resolve, both deny rather than opening up.
func TestGroupIdentityMissingOrUnresolvableDenies(t *testing.T) {
	fx := newGroupIdentityFixture(t, grantOwner, groupID)

	// Group exists here but has never published a membership document.
	fx.grantToGroup(t, "shr_empty", "shared/project", groupID, []string{idpkg.PermRead})
	if fx.snap().Allowed(grantOwner, bob, "shared/project/readme.txt", AccessRead) {
		t.Fatal("a group with no membership document must grant nothing")
	}

	// Group hosted on another relay: v1 cannot resolve it, so it denies.
	fx.grantToGroup(t, "shr_remote", "shared/project", "team.elsewhere.example.org", []string{idpkg.PermRead})
	if fx.snap().Allowed(grantOwner, bob, "shared/project/readme.txt", AccessRead) {
		t.Fatal("an unresolvable group identity must fail closed")
	}
	var sawDeferral bool
	for _, l := range fx.logs {
		if strings.Contains(l, "cross-relay groups are deferred") {
			sawDeferral = true
		}
	}
	if !sawDeferral {
		t.Fatalf("an unresolvable group should say why: %v", fx.logs)
	}
}

// The two group namespaces coexist: a dotted name is a group identity, a
// bare one is the owner's own group, and neither shadows the other.
func TestGroupIdentityAndOwnerLocalGroupsCoexist(t *testing.T) {
	fx := newGroupIdentityFixture(t, grantOwner, groupID)
	fx.setMembership(t, 1, []string{bob}, []string{bob})

	// alice's own "team" contains carol only.
	local := idpkg.ShareGroup{
		Group: "team", Owner: grantOwner, Members: []string{carol},
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := local.Sign(fx.keys[grantOwner]); err != nil {
		t.Fatal(err)
	}
	fx.write(t, grantOwner, "poweur-sys/relay/groups/team.json", local)

	fx.grantToGroup(t, "shr_local", "shared/local", "team", []string{idpkg.PermRead})
	fx.grantToGroup(t, "shr_remote", "shared/remote", groupID, []string{idpkg.PermRead})

	set := fx.snap()
	// The owner-local group governs its own grant, and only that one.
	if !set.Allowed(grantOwner, carol, "shared/local/x.txt", AccessRead) {
		t.Fatal("owner-local group member must read the owner-local grant")
	}
	if set.Allowed(grantOwner, carol, "shared/remote/x.txt", AccessRead) {
		t.Fatal("owner-local membership must not reach the group-identity grant")
	}
	// …and the group identity governs its own.
	if !set.Allowed(grantOwner, bob, "shared/remote/x.txt", AccessRead) {
		t.Fatal("group-identity member must read the group-identity grant")
	}
	if set.Allowed(grantOwner, bob, "shared/local/x.txt", AccessRead) {
		t.Fatal("group-identity membership must not reach the owner-local grant")
	}
}

// A member of a group identity sees the grant in their own share listing,
// so the received-shares view works for group grants too.
func TestGroupIdentityVisibleShares(t *testing.T) {
	fx := newGroupIdentityFixture(t, grantOwner, groupID)
	fx.setMembership(t, 1, []string{bob}, []string{bob})
	fx.grantToGroup(t, "shr_vis", "shared/project", groupID, []string{idpkg.PermRead})

	set := fx.snap()
	if got := set.VisibleShares(bob); len(got) != 1 || got[0].ShareID != "shr_vis" {
		t.Fatalf("member's visible shares: %+v", got)
	}
	if got := set.VisibleShares(carol); len(got) != 0 {
		t.Fatalf("non-member's visible shares: %+v", got)
	}
}

// Resolving the same group named by several grants must not re-read it per
// grant — a snapshot is one request's worth of work.
func TestGroupIdentityResolvedOncePerSnapshot(t *testing.T) {
	fx := newGroupIdentityFixture(t, grantOwner, groupID)
	fx.setMembership(t, 1, []string{bob}, []string{bob})
	for _, id := range []string{"shr_a", "shr_b", "shr_c"} {
		fx.grantToGroup(t, id, "shared/"+id, groupID, []string{idpkg.PermRead})
	}
	set := fx.snap()
	if len(set.groups) != 1 {
		t.Fatalf("want the group resolved once, got %d entries: %+v", len(set.groups), set.groups)
	}
	for _, id := range []string{"shr_a", "shr_b", "shr_c"} {
		if !set.Allowed(grantOwner, bob, "shared/"+id+"/x.txt", AccessRead) {
			t.Fatalf("grant %s must still work", id)
		}
	}
}
