package files

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"testing"
	"time"

	idpkg "github.com/poweur/identity"
)

// Link-share resolution in the grant engine (E05-T4). The token *is* the
// credential, so these tests care most about what a wrong token gets and
// about link grants staying off the identity-authenticated code path.

// addLinkGrant signs and writes a link-share grant into the fixture's tree.
func (fx *grantFixture) addLinkGrant(t *testing.T, id, path, token, expires string, link *idpkg.ShareLink) {
	t.Helper()
	g := idpkg.ShareGrant{
		ShareID: id, Owner: grantOwner, Path: path,
		Audience:    []idpkg.ShareAudience{{Link: token}},
		Permissions: []string{idpkg.PermRead},
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
		ExpiresAt:   expires,
		Link:        link,
	}
	if err := g.Sign(fx.priv); err != nil {
		t.Fatal(err)
	}
	fx.writeDoc(t, "poweur-sys/relay/shares", id+".json", g)
}

// removeDoc deletes a document from the tree — what revocation is.
func (fx *grantFixture) removeDoc(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.Remove(filepath.Join(fx.home, filepath.FromSlash(dir), name)); err != nil {
		t.Fatal(err)
	}
}

func mustLinkToken(t *testing.T) string {
	t.Helper()
	tok, err := idpkg.GenerateLinkToken()
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestLinkGrantLookup(t *testing.T) {
	fx := newGrantFixture(t)
	live := mustLinkToken(t)
	expired := mustLinkToken(t)
	fx.addLinkGrant(t, "shr_live", "shared/project", live, "", nil)
	fx.addLinkGrant(t, "shr_expired", "shared/project", expired,
		time.Now().Add(-time.Minute).UTC().Format(time.RFC3339), nil)

	cases := []struct {
		name        string
		token       string
		wantFound   bool
		wantExpired bool
		wantShare   string
	}{
		{"live token resolves", live, true, false, "shr_live"},
		{"expired token is found but flagged", expired, true, true, "shr_expired"},
		{"unknown token", mustLinkToken(t), false, false, ""},
		{"empty token", "", false, false, ""},
		{"malformed token", "not-a-token", false, false, ""},
		{"near miss", live[:len(live)-1] + "z", false, false, ""},
		{"truncated token", live[:10], false, false, ""},
		{"token with a path traversal", "../" + live, false, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			grant, found, expired := fx.snap().LinkGrant(tc.token)
			if found != tc.wantFound || expired != tc.wantExpired {
				t.Fatalf("found=%v expired=%v, want %v/%v", found, expired, tc.wantFound, tc.wantExpired)
			}
			if found && grant.ShareID != tc.wantShare {
				t.Fatalf("share %q want %q", grant.ShareID, tc.wantShare)
			}
		})
	}
}

// The token is case-insensitive by construction, so a mail client that
// lowercases (or a user who shouts) still lands on the file.
func TestLinkGrantTokenIsCaseInsensitive(t *testing.T) {
	fx := newGrantFixture(t)
	token := mustLinkToken(t)
	fx.addLinkGrant(t, "shr_case", "shared/project", token, "", nil)
	for _, presented := range []string{token, upper(token), " " + token + " "} {
		if _, found, _ := fx.snap().LinkGrant(presented); !found {
			t.Fatalf("token %q must resolve", presented)
		}
	}
}

func upper(s string) string {
	out := []rune(s)
	for i, r := range out {
		if r >= 'a' && r <= 'z' {
			out[i] = r - 32
		}
	}
	return string(out)
}

// A link grant must never widen what an *authenticated* visitor can do: the
// capability lives entirely on the /s/<token> path.
func TestLinkGrantNeverMatchesAnIdentity(t *testing.T) {
	fx := newGrantFixture(t)
	token := mustLinkToken(t)
	fx.addLinkGrant(t, "shr_link", "shared/project", token, "", nil)
	set := fx.snap()

	for _, visitor := range []string{bob, carol, token, upper(token), grantOwner, ""} {
		if set.Allowed(grantOwner, visitor, "shared/project/readme.txt", AccessRead) {
			t.Fatalf("visitor %q must not read through a link grant", visitor)
		}
		if set.Allowed(grantOwner, visitor, "shared/project/readme.txt", AccessWrite) {
			t.Fatalf("visitor %q must not write through a link grant", visitor)
		}
	}
	// It is also invisible to share listings — those answer "what may this
	// identity see", and a link grant's answer is always "nothing".
	if got := set.VisibleShares(token); len(got) != 0 {
		t.Fatalf("link grant leaked into VisibleShares: %+v", got)
	}
	if got := set.VisibleShares(bob); len(got) != 0 {
		t.Fatalf("link grant leaked into VisibleShares: %+v", got)
	}
}

// A grant whose signature does not cover the token it carries grants
// nothing — the relay stores these files, so it must not be able to swap in
// a token of its own.
func TestLinkGrantForgedAndTamperedIgnored(t *testing.T) {
	fx := newGrantFixture(t)
	honest := mustLinkToken(t)
	fx.addLinkGrant(t, "shr_ok", "shared/project", honest, "", nil)

	// Signed by somebody else entirely.
	forgedToken := mustLinkToken(t)
	forged := idpkg.ShareGrant{
		ShareID: "shr_forged", Owner: grantOwner, Path: "shared/private-project",
		Audience:    []idpkg.ShareAudience{{Link: forgedToken}},
		Permissions: []string{idpkg.PermRead},
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
	}
	_, otherPriv, _ := ed25519.GenerateKey(nil)
	if err := forged.Sign(otherPriv); err != nil {
		t.Fatal(err)
	}
	fx.writeDoc(t, "poweur-sys/relay/shares", "shr_forged.json", forged)

	// Honestly signed, then edited in place: the token swapped for another.
	swapped := idpkg.ShareGrant{
		ShareID: "shr_swapped", Owner: grantOwner, Path: "shared/project",
		Audience:    []idpkg.ShareAudience{{Link: honest}},
		Permissions: []string{idpkg.PermRead},
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
	}
	if err := swapped.Sign(fx.priv); err != nil {
		t.Fatal(err)
	}
	swappedToken := mustLinkToken(t)
	swapped.Audience = []idpkg.ShareAudience{{Link: swappedToken}}
	fx.writeDoc(t, "poweur-sys/relay/shares", "shr_swapped.json", swapped)

	// And one where the download cap was quietly raised after signing.
	capped := idpkg.ShareGrant{
		ShareID: "shr_capped", Owner: grantOwner, Path: "shared/project",
		Audience:    []idpkg.ShareAudience{{Link: mustLinkToken(t)}},
		Permissions: []string{idpkg.PermRead},
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
		Link:        &idpkg.ShareLink{MaxDownloads: 1},
	}
	if err := capped.Sign(fx.priv); err != nil {
		t.Fatal(err)
	}
	cappedToken, _ := capped.LinkToken()
	capped.Link = &idpkg.ShareLink{MaxDownloads: 100000}
	fx.writeDoc(t, "poweur-sys/relay/shares", "shr_capped.json", capped)

	set := fx.snap()
	for name, token := range map[string]string{
		"forged":     forgedToken,
		"swapped":    swappedToken,
		"raised cap": cappedToken,
	} {
		if _, found, _ := set.LinkGrant(token); found {
			t.Fatalf("%s grant must be rejected on load", name)
		}
	}
	// The honest one still works, so the rejections above are about the
	// tampering and not about link grants being broken.
	if _, found, _ := set.LinkGrant(honest); !found {
		t.Fatal("the honestly signed link grant must still resolve")
	}
}

// A live grant wins over an expired one carrying the same token, so
// re-issuing a link without deleting the old document does the obvious
// thing rather than serving an expiry page.
func TestLinkGrantLiveBeatsExpired(t *testing.T) {
	fx := newGrantFixture(t)
	token := mustLinkToken(t)
	fx.addLinkGrant(t, "shr_old", "shared/project", token,
		time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), nil)
	fx.addLinkGrant(t, "shr_new", "shared/project", token, "", nil)

	grant, found, expired := fx.snap().LinkGrant(token)
	if !found || expired || grant.ShareID != "shr_new" {
		t.Fatalf("found=%v expired=%v share=%s", found, expired, grant.ShareID)
	}
}

// Revocation is deleting the document, and the engine re-reads per request,
// so the very next lookup is a miss — no cache window.
func TestLinkGrantRevocationIsImmediate(t *testing.T) {
	fx := newGrantFixture(t)
	token := mustLinkToken(t)
	fx.addLinkGrant(t, "shr_rev", "shared/project", token, "", nil)
	if _, found, _ := fx.snap().LinkGrant(token); !found {
		t.Fatal("grant must resolve before revocation")
	}
	fx.removeDoc(t, "poweur-sys/relay/shares", "shr_rev.json")
	if _, found, expired := fx.snap().LinkGrant(token); found || expired {
		t.Fatal("a revoked link must be indistinguishable from one that never existed")
	}
}

// Link options ride along on the resolved grant so the endpoint can enforce
// them without re-reading the file.
func TestLinkGrantCarriesItsOptions(t *testing.T) {
	fx := newGrantFixture(t)
	hash, err := idpkg.HashLinkPassword("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	token := mustLinkToken(t)
	fx.addLinkGrant(t, "shr_opt", "shared/project", token, "",
		&idpkg.ShareLink{Password: hash, MaxDownloads: 7})

	grant, found, _ := fx.snap().LinkGrant(token)
	if !found {
		t.Fatal("grant must resolve")
	}
	if !grant.RequiresPassword() || !grant.CheckLinkPassword("hunter2") {
		t.Fatal("password must survive the round trip through the store")
	}
	if grant.CheckLinkPassword("wrong") {
		t.Fatal("the wrong password must not verify")
	}
	if grant.MaxDownloads() != 7 {
		t.Fatalf("max downloads %d want 7", grant.MaxDownloads())
	}
}
