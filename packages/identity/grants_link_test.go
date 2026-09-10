package identity

import (
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"
)

// Link shares (E05-T4): the capability-URL grant variant.

func testLinkGrant(t *testing.T) ShareGrant {
	t.Helper()
	token, err := GenerateLinkToken()
	if err != nil {
		t.Fatal(err)
	}
	return ShareGrant{
		ShareID:     "shr_link1",
		Owner:       "alice.poweur.net",
		Path:        "shared/project-x",
		Audience:    []ShareAudience{{Link: token}},
		Permissions: []string{PermRead},
		CreatedAt:   "2026-07-17T10:00:00Z",
	}
}

func TestGenerateLinkTokenShape(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 64; i++ {
		tok, err := GenerateLinkToken()
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidateLinkToken(tok); err != nil {
			t.Fatalf("generated token %q rejected: %v", tok, err)
		}
		if seen[tok] {
			t.Fatalf("duplicate token %q", tok)
		}
		seen[tok] = true
	}
}

func TestValidateLinkToken(t *testing.T) {
	valid := strings.Repeat("a", LinkTokenLen)
	cases := []struct {
		name  string
		token string
		ok    bool
	}{
		{"valid", valid, true},
		{"valid digits", strings.Repeat("2", LinkTokenLen), true},
		{"empty", "", false},
		{"too short", valid[1:], false},
		{"too long", valid + "a", false},
		{"uppercase", strings.ToUpper(valid), false},
		{"base32 hole 0", strings.Repeat("0", LinkTokenLen), false},
		{"base32 hole 1", strings.Repeat("1", LinkTokenLen), false},
		{"base32 hole 8", strings.Repeat("8", LinkTokenLen), false},
		{"path separator", valid[2:] + "/a", false},
		{"dot", valid[1:] + ".", false},
		{"non-ascii", valid[1:] + "é", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateLinkToken(tc.token)
			if tc.ok && err != nil {
				t.Fatalf("want valid, got %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("want invalid, got nil")
			}
		})
	}
}

func TestLinkGrantValidation(t *testing.T) {
	token, _ := GenerateLinkToken()
	other, _ := GenerateLinkToken()
	hash, err := HashLinkPassword("hunter2")
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		mutate  func(*ShareGrant)
		wantErr string
	}{
		{"plain link", func(*ShareGrant) {}, ""},
		{"with password", func(g *ShareGrant) { g.Link = &ShareLink{Password: hash} }, ""},
		{"with cap", func(g *ShareGrant) { g.Link = &ShareLink{MaxDownloads: 5} }, ""},
		{"write permission", func(g *ShareGrant) { g.Permissions = []string{PermRead, PermWrite} }, "read-only"},
		{"mixed audience", func(g *ShareGrant) {
			g.Audience = append(g.Audience, ShareAudience{ID: "bob.example.org"})
		}, "link alone"},
		{"two links", func(g *ShareGrant) {
			g.Audience = []ShareAudience{{Link: token}, {Link: other}}
		}, "at most one link"},
		{"id and link in one entry", func(g *ShareGrant) {
			g.Audience = []ShareAudience{{ID: "bob.example.org", Link: token}}
		}, "exactly one of id, group or link"},
		{"malformed token", func(g *ShareGrant) { g.Audience = []ShareAudience{{Link: "nope"}} }, "26 characters"},
		{"plaintext password", func(g *ShareGrant) { g.Link = &ShareLink{Password: "hunter2"} }, "argon2id"},
		{"negative cap", func(g *ShareGrant) { g.Link = &ShareLink{MaxDownloads: -1} }, "max_downloads"},
		{"absurd cap", func(g *ShareGrant) { g.Link = &ShareLink{MaxDownloads: MaxLinkDownloads + 1} }, "max_downloads"},
		{"link options without link audience", func(g *ShareGrant) {
			g.Audience = []ShareAudience{{ID: "bob.example.org"}}
			g.Link = &ShareLink{MaxDownloads: 3}
		}, "require a link audience"},
		{"private path", func(g *ShareGrant) { g.Path = "private/diary.txt" }, "grants may only cover"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := testLinkGrant(t)
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

func TestLinkGrantAccessors(t *testing.T) {
	g := testLinkGrant(t)
	token, ok := g.LinkToken()
	if !ok || token == "" {
		t.Fatal("link grant must expose its token")
	}
	if !g.IsLink() {
		t.Fatal("IsLink must be true")
	}
	if !g.MatchesLinkToken(token) || !g.MatchesLinkToken(strings.ToUpper(token)) {
		t.Fatal("token match must accept the token (case-insensitively)")
	}
	if g.MatchesLinkToken(token[:len(token)-1] + "z") {
		t.Fatal("a near-miss token must not match")
	}
	if g.MatchesLinkToken("") {
		t.Fatal("an empty token must not match")
	}
	if g.RequiresPassword() || g.MaxDownloads() != 0 {
		t.Fatal("a plain link has no password and no cap")
	}

	plain := testGrant()
	if _, ok := plain.LinkToken(); ok {
		t.Fatal("an identity grant is not a link grant")
	}
	if plain.MatchesLinkToken("anything") {
		t.Fatal("an identity grant must never match a link token")
	}
}

func TestLinkGrantPassword(t *testing.T) {
	g := testLinkGrant(t)
	if !g.CheckLinkPassword("") {
		t.Fatal("a link with no password accepts anything")
	}
	hash, err := HashLinkPassword("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	g.Link = &ShareLink{Password: hash}
	if !g.RequiresPassword() {
		t.Fatal("password must be required")
	}
	if !g.CheckLinkPassword("hunter2") {
		t.Fatal("correct password must verify")
	}
	for _, wrong := range []string{"", "hunter3", "HUNTER2", hash} {
		if g.CheckLinkPassword(wrong) {
			t.Fatalf("wrong password %q must not verify", wrong)
		}
	}
	// A corrupted hash fails closed rather than accepting everything.
	g.Link = &ShareLink{Password: "$argon2id$broken"}
	if g.CheckLinkPassword("hunter2") || g.CheckLinkPassword("") {
		t.Fatal("a corrupt hash must fail closed")
	}
}

func TestLinkGrantCanonicalAndSignature(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)

	// A grant without link options signs the same eight lines as before.
	plain := testGrant()
	if got := strings.Count(plain.Canonical(), "\n"); got != 7 {
		t.Fatalf("non-link canonical must stay 8 lines, got %d", got+1)
	}

	hash, _ := HashLinkPassword("hunter2")
	g := testLinkGrant(t)
	g.Link = &ShareLink{Password: hash, MaxDownloads: 3}
	if err := g.Sign(priv); err != nil {
		t.Fatal(err)
	}
	token, _ := g.LinkToken()
	canon := g.Canonical()
	for _, want := range []string{"link:" + token, "poweur-share-link", hash, "\n3"} {
		if !strings.Contains(canon, want) {
			t.Fatalf("canonical %q missing %q", canon, want)
		}
	}
	raw, _ := json.Marshal(g)
	parsed, err := ParseShareGrant(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := parsed.VerifySignature(pub); err != nil {
		t.Fatal(err)
	}

	// Stripping or weakening the link options breaks the signature — the
	// relay stores these files, so it must not be able to edit them.
	tamper := []func(*ShareGrant){
		func(g *ShareGrant) { g.Link = nil },
		func(g *ShareGrant) { g.Link = &ShareLink{MaxDownloads: 3} },
		func(g *ShareGrant) { g.Link = &ShareLink{Password: hash} },
		func(g *ShareGrant) { g.Link = &ShareLink{Password: hash, MaxDownloads: 9999} },
		func(g *ShareGrant) {
			other, _ := GenerateLinkToken()
			g.Audience = []ShareAudience{{Link: other}}
		},
	}
	for i, mutate := range tamper {
		bad := parsed
		bad.Link = &ShareLink{Password: hash, MaxDownloads: 3}
		mutate(&bad)
		if err := bad.VerifySignature(pub); err == nil {
			t.Fatalf("tamper %d must fail verification", i)
		}
	}
}
