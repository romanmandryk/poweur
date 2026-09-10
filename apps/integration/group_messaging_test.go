package integration_test

import (
	"strings"
	"testing"

	clipkg "github.com/poweur/cli/pkg/cli"
)

// TestINT_GROUP_01 exercises v1 group messaging across two real relays. The
// group has five members, each recipient gets an independently encrypted
// envelope, and a member added later receives only messages sent after the
// membership epoch changed.
func TestINT_GROUP_01_FiveMembersAcrossRelaysAndLateJoin(t *testing.T) {
	zone := newZone(t)
	tsA, addrA := newHostedRelay(t, zone, t.TempDir())
	defer tsA.Close()
	tsB, addrB := newHostedRelay(t, zone, t.TempDir())
	defer tsB.Close()

	relayA := "http://" + addrA
	relayB := "http://" + addrB
	// Hosted group resolution needs a dial target in this in-process test.
	// Member keys still fall back to the fake DNS zone when relay A correctly
	// answers 404 for identities hosted on relay B.
	clipkg.ConfigureIdentityResolver("http", true, addrA)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	type member struct {
		id   string
		home string
	}
	alice := member{"groupalice.poweur.net", t.TempDir()}
	members := []member{
		alice,
		{"groupbob.example.org", t.TempDir()},
		{"groupcarol.example.org", t.TempDir()},
		{"groupdave.example.org", t.TempDir()},
		{"grouperin.example.org", t.TempDir()},
	}
	createDNSIdentity := func(m member, name, domain, relay string) {
		runCLI(t, m.home, "identity", "create", name,
			"--parent-domain", domain, "--relay", relay,
			"--dns-provider", "mock", "--dns-token", "integration")
	}
	createDNSIdentity(alice, "groupalice", "poweur.net", relayA)
	for _, m := range members[1:] {
		label, domain, _ := strings.Cut(m.id, ".")
		createDNSIdentity(m, label, domain, relayB)
		runCLI(t, m.home, "policy", "set", "open")
	}

	args := []string{"group", "create", "crew.poweur.net", "--relay", relayA}
	for _, m := range members {
		args = append(args, "--member", m.id)
	}
	runCLI(t, alice.home, args...)

	first := "five-member cross-relay hello"
	out, _ := runCLI(t, alice.home, "group", "send", "crew.poweur.net", first)
	if !strings.Contains(out, "4 of 4 delivered") {
		t.Fatalf("group send summary: %s", out)
	}
	for _, m := range members[1:] {
		inbox, _ := runCLI(t, m.home, "inbox")
		assertDecryptedInbox(t, inbox, alice.id, first)
		if !strings.Contains(inbox, "[thread crew.poweur.net]") {
			t.Fatalf("%s inbox did not render the group thread: %s", m.id, inbox)
		}
	}

	frank := member{"groupfrank.example.org", t.TempDir()}
	createDNSIdentity(frank, "groupfrank", "example.org", relayB)
	runCLI(t, frank.home, "policy", "set", "open")
	before, _ := runCLI(t, frank.home, "inbox")
	if strings.Contains(before, first) {
		t.Fatalf("late joiner received pre-join history: %s", before)
	}
	runCLI(t, alice.home, "group", "add", "crew.poweur.net", "--member", frank.id)

	second := "welcome after epoch change"
	out, _ = runCLI(t, alice.home, "group", "send", "crew.poweur.net", second, "--thread", "welcome")
	if !strings.Contains(out, "5 of 5 delivered") {
		t.Fatalf("post-join group send summary: %s", out)
	}
	frankInbox, _ := runCLI(t, frank.home, "inbox")
	assertDecryptedInbox(t, frankInbox, alice.id, second)
	if strings.Contains(frankInbox, first) || !strings.Contains(frankInbox, "[thread crew.poweur.net:welcome]") {
		t.Fatalf("late join inbox has wrong history/thread: %s", frankInbox)
	}
}
