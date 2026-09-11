package integration_test

import (
	"strings"
	"testing"
)

func TestINT_OUTBOX_01_OfflineThenRelayReturns(t *testing.T) {
	zone := newZone(t)
	_, addrA := newRelay(t, zone)
	relayBServer, addrB := newRelay(t, zone)
	relayA, relayB := "http://"+addrA, "http://"+addrB
	aliceHome, bobHome := t.TempDir(), t.TempDir()
	runCLI(t, aliceHome, "identity", "create", "offlinealice", "--parent-domain", "poweur.net",
		"--relay", relayA, "--dns-provider", "mock", "--dns-token", "integration")
	runCLI(t, bobHome, "identity", "create", "offlinebob", "--parent-domain", "example.org",
		"--relay", relayB, "--dns-provider", "mock", "--dns-token", "integration")

	relayBServer.Close()
	const body = "queued while bob's relay was dark"
	code, stdout, stderr := runCLIFull(t, aliceHome, "send", "offlinebob.example.org", body, "--sign-with", "identity")
	if code != 0 || !strings.Contains(stdout, "queued encrypted message") {
		t.Fatalf("offline send code=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	queued, _ := runCLI(t, aliceHome, "outbox", "list")
	if !strings.Contains(queued, "offlinebob.example.org") {
		t.Fatalf("outbox missing message: %s", queued)
	}

	_, addrB2 := newRelay(t, zone)
	zone.SetHost("offlinebob.example.org", addrB2)
	rewriteRelayURL(t, bobHome, "http://"+addrB2)
	retried, _ := runCLI(t, aliceHome, "outbox", "retry")
	if !strings.Contains(retried, "sent=1 pending=0") {
		t.Fatalf("retry: %s", retried)
	}
	inbox, _ := runCLI(t, bobHome, "inbox")
	assertDecryptedInbox(t, inbox, "offlinealice.poweur.net", body)
}
