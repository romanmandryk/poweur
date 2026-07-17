// Integration tests for sharing (EPIC-005): a cross-relay share managed
// with the `poweur share` CLI, enforced end-to-end — grant, read+write
// through the share, group membership, revocation.
package integration_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	clipkg "github.com/poweur/cli/pkg/cli"
)

// TestINT_SHARE_01: alice (hosted on relay A) shares a folder with bob
// (DNS identity on relay B) via `poweur share add`; bob reads and writes
// through the share with a token minted at alice's relay; `poweur share
// revoke` kills the access.
func TestINT_SHARE_01_CrossRelayShareLifecycle(t *testing.T) {
	zone := newZone(t)
	tsA, addrA := newHostedRelay(t, zone, t.TempDir())
	defer tsA.Close()
	_, addrB := newRelay(t, zone)

	relayA := "http://" + addrA
	zone.SetHost("sharealice.poweur.net", addrA)
	clipkg.ConfigureIdentityResolver("http", true, addrA)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	aliceHome := t.TempDir()
	bobHome := t.TempDir()
	runCLI(t, aliceHome, "identity", "create", "sharealice.poweur.net", "--hosted", "--relay", relayA, "--json")
	runCLI(t, bobHome, "identity", "create", "bob",
		"--parent-domain", "example.org", "--relay", "http://"+addrB,
		"--dns-provider", "mock", "--dns-token", "integration")

	// Alice seeds the shared folder and a sibling that must stay closed.
	aliceToken := mintTokenViaCLI(t, aliceHome, "--use-identity", "sharealice.poweur.net")
	base := relayA + "/dav/sharealice.poweur.net"
	for path, body := range map[string]string{
		"/shared/project/plan.md":    "the plan",
		"/shared/internal/notes.txt": "internal only",
	} {
		segs := strings.Split(strings.Trim(path, "/"), "/")
		for i := 1; i < len(segs); i++ {
			resp := davDo(t, "MKCOL", base+"/"+strings.Join(segs[:i], "/"), aliceToken, nil, nil)
			resp.Body.Close()
		}
		resp := davDo(t, http.MethodPut, base+path, aliceToken, []byte(body), nil)
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			t.Fatalf("seed %s: %d", path, resp.StatusCode)
		}
	}

	// Share via CLI: rw for bob.
	stdout, _ := runCLI(t, aliceHome, "share", "add", "/shared/project",
		"--with", "bob.example.org", "--perm", "rw", "--json")
	if !strings.Contains(stdout, "shared/project") {
		t.Fatalf("share add output: %s", stdout)
	}
	lsOut, _ := runCLI(t, aliceHome, "share", "ls")
	if !strings.Contains(lsOut, "bob.example.org") || !strings.Contains(lsOut, "perm=read,write") {
		t.Fatalf("share ls output: %s", lsOut)
	}
	shareID := strings.Fields(lsOut)[0]
	if !strings.HasPrefix(shareID, "shr_") {
		t.Fatalf("share id not found in ls output: %s", lsOut)
	}

	// Bob (relay B identity) mints a writable visitor token at alice's relay.
	bobToken := mintTokenViaCLI(t, bobHome,
		"--use-identity", "bob.example.org",
		"--audience", "sharealice.poweur.net",
		"--scope", "dav:full",
		"--relay", relayA)

	// Read through the share.
	resp := davDo(t, http.MethodGet, base+"/shared/project/plan.md", bobToken, nil, nil)
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(got) != "the plan" {
		t.Fatalf("cross-relay share read: %d %q", resp.StatusCode, got)
	}
	// Write through the share; alice sees it.
	resp = davDo(t, http.MethodPut, base+"/shared/project/from-bob.md", bobToken, []byte("bob's edit"), nil)
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("cross-relay share write: %d", resp.StatusCode)
	}
	resp = davDo(t, http.MethodGet, base+"/shared/project/from-bob.md", aliceToken, nil, nil)
	got, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(got) != "bob's edit" {
		t.Fatalf("owner read of shared write: %q", got)
	}
	// The ungranted sibling stays closed.
	resp = davDo(t, http.MethodGet, base+"/shared/internal/notes.txt", bobToken, nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("sibling read: %d want 403", resp.StatusCode)
	}

	// Revoke via CLI; bob loses access on the next request.
	runCLI(t, aliceHome, "share", "revoke", shareID)
	resp = davDo(t, http.MethodGet, base+"/shared/project/plan.md", bobToken, nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("post-revoke read: %d want 403", resp.StatusCode)
	}
}

// TestINT_SHARE_02: group shares via CLI — membership changes take effect
// without touching the grant.
func TestINT_SHARE_02_GroupShare(t *testing.T) {
	zone := newZone(t)
	tsA, addrA := newHostedRelay(t, zone, t.TempDir())
	defer tsA.Close()

	relayA := "http://" + addrA
	clipkg.ConfigureIdentityResolver("http", true, addrA)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	aliceHome := t.TempDir()
	bobHome := t.TempDir()
	carolHome := t.TempDir()
	runCLI(t, aliceHome, "identity", "create", "groupalice.poweur.net", "--hosted", "--relay", relayA, "--json")
	runCLI(t, bobHome, "identity", "create", "groupbob.poweur.net", "--hosted", "--relay", relayA, "--json")
	runCLI(t, carolHome, "identity", "create", "groupcarol.poweur.net", "--hosted", "--relay", relayA, "--json")

	aliceToken := mintTokenViaCLI(t, aliceHome, "--use-identity", "groupalice.poweur.net")
	base := relayA + "/dav/groupalice.poweur.net"
	resp := davDo(t, "MKCOL", base+"/shared/team-docs", aliceToken, nil, nil)
	resp.Body.Close()
	resp = davDo(t, http.MethodPut, base+"/shared/team-docs/handbook.md", aliceToken, []byte("team handbook"), nil)
	resp.Body.Close()

	// Group with bob only; grant to the group.
	runCLI(t, aliceHome, "share", "group", "set", "team", "--members", "groupbob.poweur.net")
	runCLI(t, aliceHome, "share", "add", "/shared/team-docs", "--with-group", "team", "--perm", "read")

	bobToken := mintTokenViaCLI(t, bobHome,
		"--use-identity", "groupbob.poweur.net", "--audience", "groupalice.poweur.net", "--relay", relayA)
	carolToken := mintTokenViaCLI(t, carolHome,
		"--use-identity", "groupcarol.poweur.net", "--audience", "groupalice.poweur.net", "--relay", relayA)

	resp = davDo(t, http.MethodGet, base+"/shared/team-docs/handbook.md", bobToken, nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("member read: %d", resp.StatusCode)
	}
	resp = davDo(t, http.MethodGet, base+"/shared/team-docs/handbook.md", carolToken, nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-member read: %d want 403", resp.StatusCode)
	}

	// One membership update: carol in, bob out.
	runCLI(t, aliceHome, "share", "group", "set", "team", "--members", "groupcarol.poweur.net")
	resp = davDo(t, http.MethodGet, base+"/shared/team-docs/handbook.md", carolToken, nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("added member read: %d", resp.StatusCode)
	}
	resp = davDo(t, http.MethodGet, base+"/shared/team-docs/handbook.md", bobToken, nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("removed member read: %d want 403", resp.StatusCode)
	}
}
