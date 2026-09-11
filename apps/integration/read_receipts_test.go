package integration_test

import (
	"strings"
	"testing"

	clipkg "github.com/poweur/cli/pkg/cli"
)

func TestINT_READ_RECEIPT_01_PerContactOptOut(t *testing.T) {
	zone := newZone(t)
	_, addr := newHostedRelay(t, zone, t.TempDir())
	relayURL := "http://" + addr
	zone.SetHost("readalice.poweur.net", addr)
	zone.SetHost("readbob.poweur.net", addr)
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })
	aliceHome, bobHome := t.TempDir(), t.TempDir()
	runCLI(t, aliceHome, "identity", "create", "readalice.poweur.net", "--hosted", "--relay", relayURL)
	runCLI(t, bobHome, "identity", "create", "readbob.poweur.net", "--hosted", "--relay", relayURL)

	runCLI(t, bobHome, "policy", "set", "open", "--no-read-receipt-for", "readalice.poweur.net")
	runCLI(t, aliceHome, "send", "readbob.poweur.net", "private read", "--sign-with", "identity")
	runCLI(t, bobHome, "inbox")
	aliceAcks, _ := runCLI(t, aliceHome, "inbox")
	if !strings.Contains(aliceAcks, "delivered_client") || strings.Contains(aliceAcks, " read read by ") {
		t.Fatalf("opt-out receipt set was wrong: %s", aliceAcks)
	}

	runCLI(t, bobHome, "policy", "set", "open")
	runCLI(t, aliceHome, "send", "readbob.poweur.net", "public read", "--sign-with", "identity")
	runCLI(t, bobHome, "inbox")
	aliceAcks, _ = runCLI(t, aliceHome, "inbox")
	if !strings.Contains(aliceAcks, " read read by ") {
		t.Fatalf("enabled read receipt missing: %s", aliceAcks)
	}
}
