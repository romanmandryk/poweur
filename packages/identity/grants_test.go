package identity

import (
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func testGrant() ShareGrant {
	return ShareGrant{
		ShareID:     "shr_test1",
		Owner:       "alice.poweur.net",
		Path:        "shared/project-x",
		Audience:    []ShareAudience{{ID: "bob.example.org"}},
		Permissions: []string{PermRead, PermWrite},
		CreatedAt:   "2026-07-17T10:00:00Z",
	}
}

func TestShareGrantSignVerifyRoundtrip(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	g := testGrant()
	if err := g.Sign(priv); err != nil {
		t.Fatal(err)
	}
	if err := g.VerifySignature(pub); err != nil {
		t.Fatal(err)
	}

	// JSON roundtrip must keep the signature valid.
	raw, _ := json.Marshal(g)
	parsed, err := ParseShareGrant(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := parsed.VerifySignature(pub); err != nil {
		t.Fatal(err)
	}

	// A different key must fail.
	otherPub, _, _ := ed25519.GenerateKey(nil)
	if err := g.VerifySignature(otherPub); err == nil {
		t.Fatal("wrong key must fail verification")
	}
}

func TestShareGrantTamperDetection(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	tamper := []func(*ShareGrant){
		func(g *ShareGrant) { g.Path = "shared/other" },
		func(g *ShareGrant) { g.Audience = append(g.Audience, ShareAudience{ID: "eve.example.org"}) },
		func(g *ShareGrant) { g.Audience[0].ID = "eve.example.org" },
		func(g *ShareGrant) { g.Permissions = []string{PermWrite} },
		func(g *ShareGrant) { g.ExpiresAt = "2030-01-01T00:00:00Z" },
		func(g *ShareGrant) { g.Owner = "mallory.poweur.net" },
		func(g *ShareGrant) { g.ShareID = "shr_other" },
		func(g *ShareGrant) { g.SourceShareID = "shr_public" },
	}
	for i, mutate := range tamper {
		g := testGrant()
		if err := g.Sign(priv); err != nil {
			t.Fatal(err)
		}
		mutate(&g)
		if err := g.VerifySignature(pub); err == nil {
			t.Fatalf("tamper case %d must fail verification", i)
		}
	}
}

func TestShareGrantCanonicalOrderIndependence(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	a := testGrant()
	a.Audience = []ShareAudience{{ID: "bob.example.org"}, {Group: "team"}}
	a.Permissions = []string{PermWrite, PermRead}
	b := testGrant()
	b.Audience = []ShareAudience{{Group: "team"}, {ID: "Bob.Example.org"}}
	b.Permissions = []string{PermRead, PermWrite}
	if a.Canonical() != b.Canonical() {
		t.Fatalf("canonical form must be order- and case-independent:\n%q\n%q", a.Canonical(), b.Canonical())
	}
	_ = priv
}

func TestShareGrantValidation(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*ShareGrant)
		errHas string
	}{
		{"private path", func(g *ShareGrant) { g.Path = "private/secret" }, "only cover paths under"},
		{"poweur-sys path", func(g *ShareGrant) { g.Path = "poweur-sys/relay/shares" }, "only cover paths under"},
		{"public path", func(g *ShareGrant) { g.Path = "public/x" }, "only cover paths under"},
		{"bare shared root", func(g *ShareGrant) { g.Path = "shared" }, "share a subfolder"},
		{"traversal", func(g *ShareGrant) { g.Path = "shared/../private/x" }, "invalid grant path segment"},
		{"empty audience", func(g *ShareGrant) { g.Audience = nil }, "audience is empty"},
		{"both id and group", func(g *ShareGrant) { g.Audience = []ShareAudience{{ID: "a", Group: "b"}} }, "exactly one"},
		{"neither id nor group", func(g *ShareGrant) { g.Audience = []ShareAudience{{}} }, "exactly one"},
		{"unknown permission", func(g *ShareGrant) { g.Permissions = []string{"admin"} }, "unknown permission"},
		{"empty permissions", func(g *ShareGrant) { g.Permissions = nil }, "permissions is empty"},
		{"bad expiry", func(g *ShareGrant) { g.ExpiresAt = "tomorrow" }, "invalid expires_at"},
		{"missing owner", func(g *ShareGrant) { g.Owner = "" }, "owner is required"},
		{"missing share id", func(g *ShareGrant) { g.ShareID = "" }, "share_id is required"},
		{"source same as direct", func(g *ShareGrant) { g.SourceShareID = g.ShareID }, "must differ"},
		{"source traversal", func(g *ShareGrant) { g.SourceShareID = "../public" }, "invalid source_share_id"},
		{"source needs one recipient", func(g *ShareGrant) {
			g.SourceShareID = "shr_public"
			g.Audience = append(g.Audience, ShareAudience{ID: "carol.example.org"})
		}, "exactly one direct"},
	}
	for _, tc := range cases {
		g := testGrant()
		tc.mutate(&g)
		err := g.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.errHas) {
			t.Fatalf("%s: want error containing %q, got %v", tc.name, tc.errHas, err)
		}
	}
	// apps paths are shareable.
	g := testGrant()
	g.Path = "apps/net.poweur.tasks/project-1"
	if err := g.Validate(); err != nil {
		t.Fatalf("apps subtree must be shareable: %v", err)
	}
}

func TestShareGrantConversionSourceIsSigned(t *testing.T) {
	g := testGrant()
	g.SourceShareID = "shr_public"
	if err := g.Validate(); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(g.Canonical(), "\npoweur-share-source\nshr_public") {
		t.Fatalf("canonical source marker missing: %q", g.Canonical())
	}
}

func TestShareGrantExpiry(t *testing.T) {
	g := testGrant()
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	if g.Expired(now) {
		t.Fatal("no expiry must not expire")
	}
	g.ExpiresAt = "2026-07-17T11:00:00Z"
	if !g.Expired(now) {
		t.Fatal("past expiry must expire")
	}
	g.ExpiresAt = "2026-07-17T13:00:00Z"
	if g.Expired(now) {
		t.Fatal("future expiry must not expire")
	}
	g.ExpiresAt = "garbage"
	if !g.Expired(now) {
		t.Fatal("unparseable expiry must fail closed")
	}
}

func TestShareGroupSignVerifyAndMembership(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	gr := ShareGroup{
		Group:     "team",
		Owner:     "alice.poweur.net",
		Members:   []string{"bob.example.org", "Carol.Poweur.Net"},
		UpdatedAt: "2026-07-17T10:00:00Z",
	}
	if err := gr.Sign(priv); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(gr)
	parsed, err := ParseShareGroup(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := parsed.VerifySignature(pub); err != nil {
		t.Fatal(err)
	}
	if !parsed.HasMember("carol.poweur.net") || !parsed.HasMember("BOB.example.org") {
		t.Fatal("membership must be case-insensitive")
	}
	if parsed.HasMember("eve.example.org") {
		t.Fatal("non-member must not match")
	}
	// Tampered member list fails.
	parsed.Members = append(parsed.Members, "eve.example.org")
	if err := parsed.VerifySignature(pub); err == nil {
		t.Fatal("member tampering must fail verification")
	}
	// Unsigned parse fails.
	gr2 := gr
	gr2.Signature = ""
	raw2, _ := json.Marshal(gr2)
	if _, err := ParseShareGroup(raw2); err == nil {
		t.Fatal("unsigned group must be rejected")
	}
}
