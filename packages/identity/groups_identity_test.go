package identity

import (
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"
)

// Group identities (E05-T5): the same signed member-list document as an
// owner-local group, plus an admin list and a membership epoch, signed by
// the group's *own* identity key and living at GroupSelfDoc in the group's
// own tree.

func testGroupIdentity() ShareGroup {
	return ShareGroup{
		Group:     "team.acme.poweur.net",
		Owner:     "team.acme.poweur.net",
		Members:   []string{"alice.poweur.net", "bob.example.org"},
		Admins:    []string{"alice.poweur.net"},
		Epoch:     1,
		UpdatedAt: "2026-07-17T10:00:00Z",
	}
}

func TestIsGroupIdentityName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"team.acme.poweur.net", true},
		{"a.b", true},
		{" team.acme.poweur.net ", true},
		{"team", false},
		{"design-team", false},
		{"", false},
		{"  ", false},
	}
	for _, tc := range cases {
		if got := IsGroupIdentityName(tc.name); got != tc.want {
			t.Fatalf("IsGroupIdentityName(%q) = %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestGroupIdentityValidation(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*ShareGroup)
		wantErr string
	}{
		{"valid", func(*ShareGroup) {}, ""},
		{"case-insensitive self-ownership", func(g *ShareGroup) {
			g.Owner = "TEAM.acme.POWEUR.net"
		}, ""},
		{"many admins", func(g *ShareGroup) {
			g.Admins = append(g.Admins, "carol.poweur.net", "dave.example.org")
		}, ""},
		{"epoch zero is allowed", func(g *ShareGroup) { g.Epoch = 0 }, ""},
		{"owned by someone else", func(g *ShareGroup) {
			g.Owner = "alice.poweur.net"
		}, "group and owner must both be its own Poweur ID"},
		{"name is not an id", func(g *ShareGroup) {
			g.Group, g.Owner = "team", "team"
		}, "must be a Poweur ID"},
		{"negative epoch", func(g *ShareGroup) { g.Epoch = -1 }, "epoch must not be negative"},
		{"empty admin entry", func(g *ShareGroup) {
			g.Admins = []string{"alice.poweur.net", "  "}
		}, "empty entry"},
		{"too many members", func(g *ShareGroup) {
			g.Members = make([]string, MaxGroupMembers+1)
		}, "exceeds"},
		{"slash in name", func(g *ShareGroup) {
			g.Group, g.Owner = "a/b.poweur.net", "a/b.poweur.net"
		}, "must not contain slashes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := testGroupIdentity()
			tc.mutate(&g)
			err := g.Validate()
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("want valid, got %v", err)
			case tc.wantErr != "" && err == nil:
				t.Fatalf("want error containing %q, got nil", tc.wantErr)
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

// The two namespaces must not be able to collide, or a grant naming a
// group would be ambiguous about which one it meant.
func TestOwnerLocalGroupsStayOutOfTheIdentityNamespace(t *testing.T) {
	local := ShareGroup{
		Group: "team.acme.poweur.net", Owner: "alice.poweur.net",
		Members: []string{"bob.example.org"}, UpdatedAt: "2026-07-17T10:00:00Z",
	}
	err := local.Validate()
	if err == nil || !strings.Contains(err.Error(), "must not look like a Poweur ID") {
		t.Fatalf("a dotted owner-local group name must be refused, got %v", err)
	}
	// An epoch without an admin list is the same mistake in another shape.
	stray := ShareGroup{
		Group: "team", Owner: "alice.poweur.net", Epoch: 4,
		UpdatedAt: "2026-07-17T10:00:00Z",
	}
	if err := stray.Validate(); err == nil || !strings.Contains(err.Error(), "epoch belongs to group identities") {
		t.Fatalf("epoch without admins must be refused, got %v", err)
	}
	// The ordinary owner-local group is untouched.
	ok := ShareGroup{
		Group: "team", Owner: "alice.poweur.net",
		Members: []string{"bob.example.org"}, UpdatedAt: "2026-07-17T10:00:00Z",
	}
	if err := ok.Validate(); err != nil {
		t.Fatalf("an ordinary owner-local group must still validate: %v", err)
	}
	if ok.IsGroupIdentity() {
		t.Fatal("a group with no admins is not a group identity")
	}
}

// Adding group identities must not have invalidated a single existing
// owner-local group signature.
func TestOwnerLocalGroupCanonicalUnchanged(t *testing.T) {
	local := ShareGroup{
		Group: "Team", Owner: "Alice.poweur.net",
		Members:   []string{"Zoe.example.org", "bob.example.org", " carol.poweur.net "},
		UpdatedAt: "2026-01-15T09:30:00Z",
	}
	want := "poweur-share-group\nteam\nalice.poweur.net\n" +
		"bob.example.org,carol.poweur.net,zoe.example.org\n2026-01-15T09:30:00Z"
	if got := local.Canonical(); got != want {
		t.Fatalf("owner-local canonical changed:\n got %q\nwant %q", got, want)
	}
	if got := strings.Count(local.Canonical(), "\n"); got != 4 {
		t.Fatalf("owner-local canonical must stay 5 lines, got %d", got+1)
	}
}

func TestGroupIdentityCanonicalAndSignature(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	g := testGroupIdentity()
	g.Admins = []string{"Zoe.example.org", "alice.poweur.net"}
	g.Epoch = 7
	if err := g.Sign(priv); err != nil {
		t.Fatal(err)
	}
	canon := g.Canonical()
	// Admins are sorted and lowercased like members, so JSON ordering never
	// matters.
	for _, want := range []string{
		"poweur-group-identity",
		"alice.poweur.net,zoe.example.org",
		"\n7",
	} {
		if !strings.Contains(canon, want) {
			t.Fatalf("canonical %q missing %q", canon, want)
		}
	}
	if got := strings.Count(canon, "\n"); got != 7 {
		t.Fatalf("group-identity canonical must be 8 lines, got %d", got+1)
	}

	raw, _ := json.Marshal(g)
	parsed, err := ParseShareGroup(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := parsed.VerifySignature(pub); err != nil {
		t.Fatal(err)
	}
	if !parsed.IsGroupIdentity() || parsed.Epoch != 7 {
		t.Fatalf("round trip lost the identity fields: %+v", parsed)
	}

	// Whoever stores the document must not be able to edit authority into
	// or out of it.
	tamper := []struct {
		name   string
		mutate func(*ShareGroup)
	}{
		{"admin added", func(g *ShareGroup) {
			g.Admins = append(g.Admins, "mallory.example.org")
		}},
		{"admin removed", func(g *ShareGroup) { g.Admins = g.Admins[:1] }},
		{"admins stripped", func(g *ShareGroup) { g.Admins = nil }},
		{"member added", func(g *ShareGroup) {
			g.Members = append(g.Members, "mallory.example.org")
		}},
		{"epoch rolled back", func(g *ShareGroup) { g.Epoch = 1 }},
		{"epoch advanced", func(g *ShareGroup) { g.Epoch = 8 }},
	}
	for _, tc := range tamper {
		t.Run(tc.name, func(t *testing.T) {
			bad := parsed
			bad.Admins = append([]string(nil), parsed.Admins...)
			bad.Members = append([]string(nil), parsed.Members...)
			tc.mutate(&bad)
			if err := bad.VerifySignature(pub); err == nil {
				t.Fatal("tampering must break the signature")
			}
		})
	}
}

func TestGroupIdentityAdminAndMemberChecks(t *testing.T) {
	g := testGroupIdentity()
	for _, admin := range []string{"alice.poweur.net", "ALICE.poweur.net", " alice.poweur.net "} {
		if !g.HasAdmin(admin) {
			t.Fatalf("HasAdmin(%q) must be true", admin)
		}
	}
	for _, notAdmin := range []string{"bob.example.org", "", "alice.poweur.net.evil.org"} {
		if g.HasAdmin(notAdmin) {
			t.Fatalf("HasAdmin(%q) must be false", notAdmin)
		}
	}
	// Being an admin does not by itself imply membership, and vice versa —
	// the two lists are independent on purpose.
	if !g.HasMember("bob.example.org") || g.HasMember("carol.poweur.net") {
		t.Fatal("membership check is wrong")
	}
	lopsided := testGroupIdentity()
	lopsided.Members = []string{"bob.example.org"}
	if !lopsided.HasAdmin("alice.poweur.net") || lopsided.HasMember("alice.poweur.net") {
		t.Fatal("an admin who is not a member must be exactly that")
	}
}

// A group identity is unsigned-by-anyone-else by construction: the parser
// still refuses a document with no signature at all.
func TestParseGroupIdentityRequiresSignature(t *testing.T) {
	g := testGroupIdentity()
	raw, _ := json.Marshal(g)
	if _, err := ParseShareGroup(raw); err == nil || !strings.Contains(err.Error(), "unsigned") {
		t.Fatalf("want an unsigned error, got %v", err)
	}
}
