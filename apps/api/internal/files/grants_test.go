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

const (
	grantOwner = "alice.poweur.net"
	bob        = "bob.example.org"
	carol      = "carol.poweur.net"
	dave       = "dave.example.org"
)

type grantFixture struct {
	store *GrantStore
	priv  ed25519.PrivateKey
	pub   ed25519.PublicKey
	home  string
	logs  []string
}

func newGrantFixture(t *testing.T) *grantFixture {
	t.Helper()
	dir := t.TempDir()
	home := filepath.Join(dir, "identities", "alice__poweur__net")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	homeFn := func(string) (string, error) { return home, nil }
	provider := NewFSProvider(dir, homeFn)
	if err := provider.EnsureTree(context.Background(), grantOwner); err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := ed25519.GenerateKey(nil)
	fx := &grantFixture{priv: priv, pub: pub, home: home}
	fx.store = &GrantStore{
		Provider: provider,
		OwnerKey: func(owner string) (ed25519.PublicKey, bool) {
			return pub, strings.EqualFold(owner, grantOwner)
		},
		Logf: func(format string, args ...any) {
			fx.logs = append(fx.logs, format)
		},
	}
	return fx
}

func (fx *grantFixture) writeDoc(t *testing.T, dir, name string, doc any) {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	full := filepath.Join(fx.home, filepath.FromSlash(dir))
	if err := os.MkdirAll(full, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(full, name), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (fx *grantFixture) addGrant(t *testing.T, id, path string, audience []idpkg.ShareAudience, perms []string, expires string) {
	t.Helper()
	g := idpkg.ShareGrant{
		ShareID: id, Owner: grantOwner, Path: path,
		Audience: audience, Permissions: perms,
		CreatedAt: time.Now().UTC().Format(time.RFC3339), ExpiresAt: expires,
	}
	if err := g.Sign(fx.priv); err != nil {
		t.Fatal(err)
	}
	fx.writeDoc(t, "poweur-sys/relay/shares", id+".json", g)
}

func (fx *grantFixture) addGroup(t *testing.T, name string, members ...string) {
	t.Helper()
	gr := idpkg.ShareGroup{
		Group: name, Owner: grantOwner, Members: members,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := gr.Sign(fx.priv); err != nil {
		t.Fatal(err)
	}
	fx.writeDoc(t, "poweur-sys/relay/groups", name+".json", gr)
}

func (fx *grantFixture) snap() *GrantSet {
	return fx.store.Snapshot(context.Background(), grantOwner)
}

func ids(names ...string) []idpkg.ShareAudience {
	var out []idpkg.ShareAudience
	for _, n := range names {
		out = append(out, idpkg.ShareAudience{ID: n})
	}
	return out
}

func TestGrantSubtreePropagation(t *testing.T) {
	fx := newGrantFixture(t)
	fx.addGrant(t, "shr_1", "shared/project-x", ids(bob), []string{"read", "write"}, "")
	gs := fx.snap()

	// The grant covers the path itself and everything below — including
	// paths that do not exist yet (mkdir/put of new children).
	for _, p := range []string{
		"shared/project-x",
		"shared/project-x/file.txt",
		"shared/project-x/deep/nested/dir/file.bin",
	} {
		if !gs.Allowed(grantOwner, bob, p, AccessRead) || !gs.Allowed(grantOwner, bob, p, AccessWrite) {
			t.Fatalf("grant must propagate to %s", p)
		}
	}
	// Siblings and other roots stay closed.
	for _, p := range []string{
		"shared/other-project",
		"shared/project-x2", // prefix similarity must not leak
		"private/secret.txt",
		"apps/net.poweur.tasks/db.json",
	} {
		if gs.Allowed(grantOwner, bob, p, AccessRead) {
			t.Fatalf("grant must not leak to %s", p)
		}
	}
}

func TestGrantAncestorNavigationReadOnly(t *testing.T) {
	fx := newGrantFixture(t)
	fx.addGrant(t, "shr_1", "shared/team/docs/report.txt", ids(bob), []string{"read"}, "")
	gs := fx.snap()

	// Ancestor directories are readable (traversal)…
	for _, p := range []string{"", "shared", "shared/team", "shared/team/docs"} {
		if !gs.Allowed(grantOwner, bob, p, AccessRead) {
			t.Fatalf("ancestor %q must be readable for traversal", p)
		}
	}
	// …but never writable, and never readable for non-audience visitors.
	for _, p := range []string{"shared", "shared/team", "shared/team/docs"} {
		if gs.Allowed(grantOwner, bob, p, AccessWrite) {
			t.Fatalf("ancestor %q must not be writable", p)
		}
		if gs.Allowed(grantOwner, dave, p, AccessRead) {
			t.Fatalf("ancestor %q must not open for non-audience", p)
		}
	}
	// Sibling files inside a readable ancestor stay hidden.
	if gs.Allowed(grantOwner, bob, "shared/team/docs/other.txt", AccessRead) {
		t.Fatal("sibling of granted file must stay hidden")
	}
}

func TestGrantSingleFileVsDirectory(t *testing.T) {
	fx := newGrantFixture(t)
	fx.addGrant(t, "shr_file", "shared/one-file.txt", ids(bob), []string{"read", "write"}, "")
	fx.addGrant(t, "shr_dir", "shared/whole-dir", ids(carol), []string{"read", "write"}, "")
	gs := fx.snap()

	if !gs.Allowed(grantOwner, bob, "shared/one-file.txt", AccessWrite) {
		t.Fatal("file grant must allow writing the file")
	}
	if gs.Allowed(grantOwner, bob, "shared/whole-dir/x.txt", AccessRead) {
		t.Fatal("file grant must not open other paths")
	}
	if !gs.Allowed(grantOwner, carol, "shared/whole-dir/new/sub/file.txt", AccessWrite) {
		t.Fatal("dir grant must cover new children")
	}
	if gs.Allowed(grantOwner, carol, "shared/one-file.txt", AccessRead) {
		t.Fatal("dir grant must not open bob's file")
	}
}

func TestGrantPermissionCombinations(t *testing.T) {
	fx := newGrantFixture(t)
	fx.addGrant(t, "shr_ro", "shared/read-only", ids(bob), []string{"read"}, "")
	fx.addGrant(t, "shr_rw", "shared/read-write", ids(bob), []string{"read", "write"}, "")
	fx.addGrant(t, "shr_w", "shared/write-implies-read", ids(bob), []string{"write"}, "")
	gs := fx.snap()

	cases := []struct {
		path       string
		read, write bool
	}{
		{"shared/read-only/f", true, false},
		{"shared/read-write/f", true, true},
		{"shared/write-implies-read/f", true, true}, // write implies read
	}
	for _, tc := range cases {
		if got := gs.Allowed(grantOwner, bob, tc.path, AccessRead); got != tc.read {
			t.Fatalf("%s read = %v want %v", tc.path, got, tc.read)
		}
		if got := gs.Allowed(grantOwner, bob, tc.path, AccessWrite); got != tc.write {
			t.Fatalf("%s write = %v want %v", tc.path, got, tc.write)
		}
	}
}

func TestGrantMultipleAudiencesAndUnion(t *testing.T) {
	fx := newGrantFixture(t)
	// One grant, two direct audience members.
	fx.addGrant(t, "shr_multi", "shared/multi", ids(bob, carol), []string{"read"}, "")
	// Bob additionally has write via a second overlapping grant: union wins.
	fx.addGrant(t, "shr_extra", "shared/multi/hot", ids(bob), []string{"read", "write"}, "")
	gs := fx.snap()

	for _, v := range []string{bob, carol} {
		if !gs.Allowed(grantOwner, v, "shared/multi/doc.txt", AccessRead) {
			t.Fatalf("%s must read the multi share", v)
		}
	}
	if gs.Allowed(grantOwner, dave, "shared/multi/doc.txt", AccessRead) {
		t.Fatal("non-audience visitor must be denied")
	}
	if gs.Allowed(grantOwner, carol, "shared/multi/hot/f", AccessWrite) {
		t.Fatal("carol has no write grant")
	}
	if !gs.Allowed(grantOwner, bob, "shared/multi/hot/f", AccessWrite) {
		t.Fatal("bob's overlapping write grant must apply (union)")
	}
}

func TestGrantGroups(t *testing.T) {
	fx := newGrantFixture(t)
	fx.addGroup(t, "team", bob, carol)
	fx.addGrant(t, "shr_g", "shared/team-folder", []idpkg.ShareAudience{{Group: "team"}}, []string{"read", "write"}, "")
	gs := fx.snap()

	for _, v := range []string{bob, carol} {
		if !gs.Allowed(grantOwner, v, "shared/team-folder/f", AccessWrite) {
			t.Fatalf("group member %s must have access", v)
		}
	}
	if gs.Allowed(grantOwner, dave, "shared/team-folder/f", AccessRead) {
		t.Fatal("non-member must be denied")
	}

	// Membership update: adding dave grants access without a new grant;
	// removing bob revokes his.
	fx.addGroup(t, "team", carol, dave)
	gs = fx.snap()
	if !gs.Allowed(grantOwner, dave, "shared/team-folder/f", AccessRead) {
		t.Fatal("newly added member must gain access")
	}
	if gs.Allowed(grantOwner, bob, "shared/team-folder/f", AccessRead) {
		t.Fatal("removed member must lose access")
	}
}

func TestGrantExpiryAndRevocation(t *testing.T) {
	fx := newGrantFixture(t)
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	fx.addGrant(t, "shr_expired", "shared/expired", ids(bob), []string{"read"}, past)
	fx.addGrant(t, "shr_live", "shared/live", ids(bob), []string{"read"}, future)
	gs := fx.snap()

	if gs.Allowed(grantOwner, bob, "shared/expired/f", AccessRead) {
		t.Fatal("expired grant must deny")
	}
	if !gs.Allowed(grantOwner, bob, "shared/live/f", AccessRead) {
		t.Fatal("unexpired grant must allow")
	}

	// Revocation = deleting the grant file; the next snapshot denies.
	if err := os.Remove(filepath.Join(fx.home, "poweur-sys", "relay", "shares", "shr_live.json")); err != nil {
		t.Fatal(err)
	}
	if fx.snap().Allowed(grantOwner, bob, "shared/live/f", AccessRead) {
		t.Fatal("deleted grant must deny on next snapshot")
	}
}

func TestGrantRejectsForgedAndMalformed(t *testing.T) {
	fx := newGrantFixture(t)

	// Signed by the wrong key (forged).
	_, wrongPriv, _ := ed25519.GenerateKey(nil)
	forged := idpkg.ShareGrant{
		ShareID: "shr_forged", Owner: grantOwner, Path: "shared/forged",
		Audience: ids(bob), Permissions: []string{"read", "write"},
	}
	if err := forged.Sign(wrongPriv); err != nil {
		t.Fatal(err)
	}
	fx.writeDoc(t, "poweur-sys/relay/shares", "shr_forged.json", forged)

	// Signed correctly but owner field points elsewhere (grant replay).
	replay := idpkg.ShareGrant{
		ShareID: "shr_replay", Owner: "mallory.poweur.net", Path: "shared/replay",
		Audience: ids(bob), Permissions: []string{"read"},
	}
	if err := replay.Sign(fx.priv); err != nil {
		t.Fatal(err)
	}
	fx.writeDoc(t, "poweur-sys/relay/shares", "shr_replay.json", replay)

	// Valid signature but path outside the shareable roots (crafted file
	// bypassing client validation).
	type rawGrant struct {
		ShareID     string                `json:"share_id"`
		Owner       string                `json:"owner"`
		Path        string                `json:"path"`
		Audience    []idpkg.ShareAudience `json:"audience"`
		Permissions []string              `json:"permissions"`
		Signature   string                `json:"signature"`
	}
	fx.writeDoc(t, "poweur-sys/relay/shares", "shr_private.json", rawGrant{
		ShareID: "shr_private", Owner: grantOwner, Path: "private/secrets",
		Audience: ids(bob), Permissions: []string{"read"}, Signature: "AAAA",
	})

	// Plain garbage.
	fullDir := filepath.Join(fx.home, "poweur-sys", "relay", "shares")
	_ = os.MkdirAll(fullDir, 0o700)
	if err := os.WriteFile(filepath.Join(fullDir, "junk.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	gs := fx.snap()
	for _, p := range []string{"shared/forged/f", "shared/replay/f", "private/secrets/f"} {
		if gs.Allowed(grantOwner, bob, p, AccessRead) {
			t.Fatalf("rejected grant must not grant access to %s", p)
		}
	}
	if len(fx.logs) < 4 {
		t.Fatalf("malformed grants must be rejected loudly, got %d log lines", len(fx.logs))
	}
}

func TestGrantAnonymousNeverMatches(t *testing.T) {
	fx := newGrantFixture(t)
	fx.addGrant(t, "shr_1", "shared/x", ids(bob), []string{"read"}, "")
	if fx.snap().Allowed(grantOwner, "", "shared/x/f", AccessRead) {
		t.Fatal("anonymous must never match a grant")
	}
}
