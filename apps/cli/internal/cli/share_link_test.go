package cli

import (
	"bytes"
	"strings"
	"testing"

	idpkg "github.com/poweur/identity"
)

// `poweur share link` argument handling (E05-T4). The end-to-end path
// against a real relay lives in apps/integration; this covers the parts
// that must fail *before* anything is signed or written, plus the two
// places a token could accidentally reach a terminal.

func TestShareLinkURL(t *testing.T) {
	got := shareLinkURL("alice.poweur.net", "k7m4qz2rt6vwx3ab5cdefghijn")
	if want := "https://alice.poweur.net/s/k7m4qz2rt6vwx3ab5cdefghijn"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// `share ls` must not print a capability token: the whole audience column
// for a link share is the word "link".
func TestDescribeAudienceHidesLinkTokens(t *testing.T) {
	token, err := idpkg.GenerateLinkToken()
	if err != nil {
		t.Fatal(err)
	}
	got := describeAudience([]idpkg.ShareAudience{{Link: token}})
	if got != "link" {
		t.Fatalf("got %q want %q", got, "link")
	}
	if strings.Contains(got, token) {
		t.Fatal("share ls must never print the token")
	}
	// The other audience shapes are unchanged.
	mixed := describeAudience([]idpkg.ShareAudience{
		{ID: "bob.example.org"}, {Group: "team"},
	})
	if mixed != "bob.example.org, group:team" {
		t.Fatalf("got %q", mixed)
	}
}

// Every one of these must be rejected without loading an identity, minting
// a token or writing a grant.
func TestShareLinkAddRejectsBadArguments(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no path", []string{}, "usage:"},
		{"two paths", []string{"shared/a", "shared/b"}, "usage:"},
		{"private path", []string{"private/diary.txt"}, "may only cover paths under"},
		{"poweur-sys path", []string{"poweur-sys/relay/shares"}, "may only cover paths under"},
		{"shared root itself", []string{"shared"}, "cannot grant the shared root itself"},
		{"empty path", []string{""}, "grant path is empty"},
		{"bad expiry", []string{"shared/x", "--expires", "tomorrow"}, "invalid --expires"},
		{"both password flags", []string{"shared/x", "--password", "a", "--password-stdin"}, "not both"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := runShareLinkAdd(tc.args, &stdout, &stderr); code == 0 {
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

func TestShareLinkUsage(t *testing.T) {
	for _, args := range [][]string{{}, {"nonsense"}} {
		var stdout, stderr bytes.Buffer
		if code := runShareLink(args, &stdout, &stderr); code != 1 {
			t.Fatalf("args %v: exit %d want 1", args, code)
		}
		if stderr.Len() == 0 {
			t.Fatalf("args %v: expected a usage message", args)
		}
	}
	// `share link` is reachable from the `share` dispatcher, and revocation
	// is deliberately *not* a second verb here.
	var stdout, stderr bytes.Buffer
	runShare([]string{"link"}, &stdout, &stderr)
	if !strings.Contains(stderr.String(), "poweur share revoke") {
		t.Fatalf("usage should point at the existing revoke verb: %q", stderr.String())
	}
}

// A grant the CLI builds must be one the relay's own validator accepts —
// read-only, single-token audience, hashed password.
func TestShareLinkGrantShapeIsValid(t *testing.T) {
	token, err := idpkg.GenerateLinkToken()
	if err != nil {
		t.Fatal(err)
	}
	hash, err := idpkg.HashLinkPassword("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	grant := idpkg.ShareGrant{
		ShareID: "shr_0011223344556677", Owner: "alice.poweur.net",
		Path:        "shared/project-x",
		Audience:    []idpkg.ShareAudience{{Link: token}},
		Permissions: []string{idpkg.PermRead},
		CreatedAt:   "2026-07-17T10:00:00Z",
		Link:        &idpkg.ShareLink{Password: hash, MaxDownloads: 5},
	}
	if err := grant.Validate(); err != nil {
		t.Fatalf("the CLI's grant shape must validate: %v", err)
	}
	if got, ok := grant.LinkToken(); !ok || got != token {
		t.Fatalf("token round trip: %q %v", got, ok)
	}
	if !grant.RequiresPassword() || grant.MaxDownloads() != 5 {
		t.Fatal("link options must survive onto the grant")
	}
}
