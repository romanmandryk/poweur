package cli

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"

	idpkg "github.com/poweur/identity"
)

// `poweur group` (EPIC-005 E05-T5). The end-to-end path — register the
// group identity, sign its membership document with its own key, and have
// a member reach a grant addressed to it — runs against real relays in
// apps/integration/sharing_test.go. This file covers the parts that must
// hold before anything is signed or written: argument handling, the shape
// of the document the CLI builds, and the membership arithmetic.

func TestGroupDispatcherUsage(t *testing.T) {
	cases := [][]string{{}, {"nonsense"}, {"created"}}
	for _, args := range cases {
		var stdout, stderr bytes.Buffer
		if code := runGroup(args, &stdout, &stderr); code != 1 {
			t.Fatalf("args %v: exit %d want 1", args, code)
		}
		if stderr.Len() == 0 {
			t.Fatalf("args %v: expected a usage message", args)
		}
		if stdout.Len() != 0 {
			t.Fatalf("args %v: nothing should reach stdout: %q", args, stdout.String())
		}
	}
}

// Everything here must be refused before the CLI loads a key, mints a
// token or contacts a relay.
func TestGroupCommandsRejectBadArguments(t *testing.T) {
	cases := []struct {
		name string
		run  func(args []string, stdout, stderr *bytes.Buffer) int
		args []string
		want string
	}{
		{
			"create without a group id",
			func(a []string, o, e *bytes.Buffer) int { return runGroupCreate(a, o, e) },
			[]string{}, "usage:",
		},
		{
			"create with two group ids",
			func(a []string, o, e *bytes.Buffer) int { return runGroupCreate(a, o, e) },
			[]string{"team.acme.poweur.net", "other.acme.poweur.net"}, "usage:",
		},
		{
			// A bare name is an owner-local group, which has its own verb.
			"create with a bare owner-local name",
			func(a []string, o, e *bytes.Buffer) int { return runGroupCreate(a, o, e) },
			[]string{"team"}, "poweur share group set",
		},
		{
			"show without a group id",
			func(a []string, o, e *bytes.Buffer) int { return runGroupShow(a, o, e) },
			[]string{}, "usage:",
		},
		{
			"add without a group id",
			func(a []string, o, e *bytes.Buffer) int { return runGroupUpdate(a, o, e, true) },
			[]string{}, "usage:",
		},
		{
			"add with no member or admin",
			func(a []string, o, e *bytes.Buffer) int { return runGroupUpdate(a, o, e, true) },
			[]string{"team.acme.poweur.net"}, "at least one --member or --admin",
		},
		{
			"remove with no member or admin",
			func(a []string, o, e *bytes.Buffer) int { return runGroupUpdate(a, o, e, false) },
			[]string{"team.acme.poweur.net"}, "at least one --member or --admin",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := tc.run(tc.args, &stdout, &stderr); code == 0 {
				t.Fatalf("want a non-zero exit, got 0 (stdout %q)", stdout.String())
			}
			if !strings.Contains(stderr.String(), tc.want) {
				t.Fatalf("stderr %q missing %q", stderr.String(), tc.want)
			}
			if stdout.Len() != 0 {
				t.Fatalf("a rejected command must print nothing to stdout: %q", stdout.String())
			}
		})
	}
}

// The document `poweur group create` builds must be one the relay's own
// parser accepts as a group identity — including the group being its own
// owner, which is what lets the relay verify it with the group's key.
func TestNewGroupDocumentIsAValidGroupIdentity(t *testing.T) {
	group := newGroupDocument(
		"  Team.Acme.Poweur.NET ",
		[]string{"Alice.poweur.net", "alice.poweur.net"},
		[]string{"bob.example.org", "alice.poweur.net", " "},
	)
	if group.Group != "team.acme.poweur.net" || group.Owner != "team.acme.poweur.net" {
		t.Fatalf("a group identity is its own owner, got %q/%q", group.Group, group.Owner)
	}
	if !group.IsGroupIdentity() {
		t.Fatal("an admins list is what marks the document as a group identity")
	}
	if group.Epoch != 1 {
		t.Fatalf("epoch %d want 1", group.Epoch)
	}
	if got := strings.Join(group.Admins, ","); got != "alice.poweur.net" {
		t.Fatalf("admins %q: duplicates and case should collapse", got)
	}
	if got := strings.Join(group.Members, ","); got != "alice.poweur.net,bob.example.org" {
		t.Fatalf("members %q: want sorted, deduped, no blanks", got)
	}
	if err := group.Validate(); err != nil {
		t.Fatalf("the CLI's document shape must validate: %v", err)
	}

	// Signed with the group's own key, it round-trips through the parser
	// the relay uses.
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	group.UpdatedAt = "2026-07-17T10:00:00Z"
	if err := group.Sign(priv); err != nil {
		t.Fatal(err)
	}
	raw, err := json.MarshalIndent(group, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := idpkg.ParseShareGroup(raw)
	if err != nil {
		t.Fatalf("relay-side parse failed: %v", err)
	}
	if err := parsed.VerifySignature(pub); err != nil {
		t.Fatalf("relay-side verify failed: %v", err)
	}
	if !parsed.HasMember("BOB.example.org") {
		t.Fatal("membership must be case-insensitive")
	}
	if !parsed.HasAdmin("alice.poweur.net") || parsed.HasAdmin("bob.example.org") {
		t.Fatal("admin list did not survive the round trip")
	}
}

// An owner-local group name may not be created through this verb, and the
// document format refuses one that looks like a Poweur ID — that dot is
// the whole separation between the two namespaces.
func TestGroupIdentityNamespaceIsSeparate(t *testing.T) {
	cases := []struct {
		name     string
		identity bool
	}{
		{"team", false},
		{"team-2", false},
		{"team.acme.poweur.net", true},
		{"acme.net", true},
	}
	for _, tc := range cases {
		if got := idpkg.IsGroupIdentityName(tc.name); got != tc.identity {
			t.Fatalf("%q: IsGroupIdentityName = %v want %v", tc.name, got, tc.identity)
		}
	}
	// An owner-local document may not squat on the identity namespace.
	local := idpkg.ShareGroup{Group: "team.acme.poweur.net", Owner: "alice.poweur.net", Members: []string{"bob.example.org"}}
	if err := local.Validate(); err == nil {
		t.Fatal("an owner-local group must not be named like a Poweur ID")
	}
}

func TestApplyGroupUpdate(t *testing.T) {
	base := func() idpkg.ShareGroup {
		return idpkg.ShareGroup{
			Group: "team.acme.poweur.net", Owner: "team.acme.poweur.net",
			Members: []string{"alice.poweur.net", "bob.example.org"},
			Admins:  []string{"alice.poweur.net"},
			Epoch:   3,
		}
	}
	cases := []struct {
		name        string
		setup       func(*idpkg.ShareGroup)
		add         bool
		members     []string
		admins      []string
		wantMembers string
		wantAdmins  string
		wantEpoch   int
		wantChanged bool
		wantErr     string
	}{
		{
			name: "add a member bumps the epoch", add: true,
			members:     []string{"Carol.poweur.net"},
			wantMembers: "alice.poweur.net,bob.example.org,carol.poweur.net",
			wantAdmins:  "alice.poweur.net", wantEpoch: 4, wantChanged: true,
		},
		{
			name: "promote an existing member to admin", add: true,
			admins:      []string{"bob.example.org"},
			wantMembers: "alice.poweur.net,bob.example.org",
			wantAdmins:  "alice.poweur.net,bob.example.org", wantEpoch: 4, wantChanged: true,
		},
		{
			name: "adding an existing member changes nothing", add: true,
			members:     []string{"BOB.example.org"},
			wantMembers: "alice.poweur.net,bob.example.org",
			wantAdmins:  "alice.poweur.net", wantEpoch: 3, wantChanged: false,
		},
		{
			name: "remove a member", add: false,
			members:     []string{"bob.example.org"},
			wantMembers: "alice.poweur.net",
			wantAdmins:  "alice.poweur.net", wantEpoch: 4, wantChanged: true,
		},
		{
			name: "removing a non-member changes nothing", add: false,
			members:     []string{"mallory.example.org"},
			wantMembers: "alice.poweur.net,bob.example.org",
			wantAdmins:  "alice.poweur.net", wantEpoch: 3, wantChanged: false,
		},
		{
			name: "the last admin cannot be removed", add: false,
			admins:  []string{"alice.poweur.net"},
			wantErr: "last admin",
		},
		{
			name:  "removing an admin who is not the last one is fine",
			setup: func(g *idpkg.ShareGroup) { g.Admins = []string{"alice.poweur.net", "bob.example.org"} },
			add:   false, admins: []string{"alice.poweur.net"},
			wantMembers: "alice.poweur.net,bob.example.org",
			wantAdmins:  "bob.example.org", wantEpoch: 4, wantChanged: true,
		},
		{
			// Demoting the last admin and promoting a new one in the same
			// breath is not something one command can do, so the refusal
			// stands even when a replacement is named.
			name: "the last admin cannot be swapped out by a removal", add: false,
			admins: []string{"alice.poweur.net"}, members: []string{"bob.example.org"},
			wantErr: "last admin",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			group := base()
			if tc.setup != nil {
				tc.setup(&group)
			}
			got, changed, err := applyGroupUpdate(group, tc.add, tc.members, tc.admins)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
				}
				if changed {
					t.Fatal("a refused update must not report a change")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if changed != tc.wantChanged {
				t.Fatalf("changed = %v want %v", changed, tc.wantChanged)
			}
			if s := strings.Join(got.Members, ","); s != tc.wantMembers {
				t.Fatalf("members = %q want %q", s, tc.wantMembers)
			}
			if s := strings.Join(got.Admins, ","); s != tc.wantAdmins {
				t.Fatalf("admins = %q want %q", s, tc.wantAdmins)
			}
			if got.Epoch != tc.wantEpoch {
				t.Fatalf("epoch = %d want %d", got.Epoch, tc.wantEpoch)
			}
			if changed {
				if err := got.Validate(); err != nil {
					t.Fatalf("an updated document must stay valid: %v", err)
				}
			}
		})
	}
}

func TestNormalizeAndRemoveIDs(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		drop []string
		want string
	}{
		{"sorts and dedupes", []string{"c.net", "a.net", "C.net", "a.net"}, nil, "a.net,c.net"},
		{"drops blanks", []string{" ", "", "a.net"}, nil, "a.net"},
		{"trims and lowercases", []string{"  B.NET "}, nil, "b.net"},
		{"remove is case-insensitive", []string{"a.net", "b.net"}, []string{"A.NET"}, "b.net"},
		{"removing everything is empty", []string{"a.net"}, []string{"a.net"}, ""},
		{"removing nothing keeps all", []string{"b.net", "a.net"}, []string{"z.net"}, "a.net,b.net"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			if tc.drop == nil {
				got = normalizeIDs(tc.in)
			} else {
				got = removeIDs(tc.in, tc.drop)
			}
			if s := strings.Join(got, ","); s != tc.want {
				t.Fatalf("got %q want %q", s, tc.want)
			}
		})
	}
}

// The membership document is written and read at exactly one path, and
// both implementations have to agree on it.
func TestGroupSelfDocPath(t *testing.T) {
	if idpkg.GroupSelfDoc != "poweur-sys/relay/groups/self.json" {
		t.Fatalf("GroupSelfDoc = %q", idpkg.GroupSelfDoc)
	}
	if !strings.HasPrefix(idpkg.GroupSelfDoc, groupsTreeDir+"/") {
		t.Fatalf("%q should live under the groups dir %q", idpkg.GroupSelfDoc, groupsTreeDir)
	}
}
