package integration_test

import (
	"strings"
	"testing"

	clipkg "github.com/poweur/cli/pkg/cli"
)

// EPIC-009 E09-T1. The failure being fixed: a message posted to a relay and
// not yet picked up was gone the moment the process restarted, with the sender
// holding a delivery tick that meant nothing.
func TestINT_SPOOL_01_UndeliveredMailSurvivesRestart(t *testing.T) {
	zone := newZone(t)
	dataDir := t.TempDir()
	ts, addr := newHostedRelay(t, zone, dataDir)
	relayURL := "http://" + addr

	zone.SetHost("spoolalice.poweur.net", addr)
	zone.SetHost("spoolbob.poweur.net", addr)
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	aliceHome := t.TempDir()
	bobHome := t.TempDir()
	runCLI(t, aliceHome, "identity", "create", "spoolalice.poweur.net", "--hosted", "--relay", relayURL, "--json")
	runCLI(t, bobHome, "identity", "create", "spoolbob.poweur.net", "--hosted", "--relay", relayURL, "--json")

	const msg = "still here after the lights went out"
	runCLI(t, aliceHome, "send", "spoolbob.poweur.net", msg)

	// Bob never picks it up. The relay goes down and comes back on the same
	// data dir, which is what a deploy looks like.
	ts.Close()
	_, addr2 := newHostedRelay(t, zone, dataDir)
	zone.SetHost("spoolalice.poweur.net", addr2)
	zone.SetHost("spoolbob.poweur.net", addr2)
	clipkg.ConfigureIdentityResolver("http", true, addr2)
	relayURL2 := "http://" + addr2
	rewriteRelayURL(t, aliceHome, relayURL2)
	rewriteRelayURL(t, bobHome, relayURL2)

	inbox, _ := runCLI(t, bobHome, "inbox")
	assertDecryptedInbox(t, inbox, "spoolalice.poweur.net", msg)

	// …and it is not delivered twice: the pickup consumed it.
	second, _ := runCLI(t, bobHome, "inbox")
	if strings.Contains(second, msg) {
		t.Fatalf("message delivered again after pickup:\n%s", second)
	}
}
