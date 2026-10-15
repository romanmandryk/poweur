package bridge

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/poweur/identity"
)

func pacedBridge(t *testing.T, mutate ...func(*Config)) (*testBridge, *fakeID, *time.Time) {
	t.Helper()
	b := newTestBridge(t, append([]func(*Config){func(c *Config) {
		c.Footer = "off"
		c.OutDailyLimit = 10
		c.OutSpacing = 5 * time.Minute
		c.OutSpacingMax = 2 * time.Hour
		c.OutNewRecipients = 100
		c.OutMaxPending = 20
	}}, mutate...)...)
	john := b.net.add("john.poweur.test", b.self.name)
	now := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	b.Outbound.now = clock
	b.Store.now = clock
	return b, john, &now
}

func sendN(t *testing.T, b *testBridge, from *fakeID, id string) {
	t.Helper()
	b.net.sendToBridge(from, b.self, identity.MsgTypeEmailSend, identity.EmailSend{
		Version: 1, SendID: id, To: []identity.EmailAddress{{Address: "ann@example.com"}}, Subject: id, Text: "x",
	})
}

func TestSendingIsPacedWithDoublingGaps(t *testing.T) {
	b, john, now := pacedBridge(t)
	ctx := context.Background()
	sendN(t, b, john, "A")
	sendN(t, b, john, "B")
	sendN(t, b, john, "C")
	runOutbound(t, b)
	if n := len(readSent(t, b.logDir)); n != 1 {
		t.Fatalf("first run sent %d, want 1 (the rest wait)", n)
	}
	st := statuses(t, b.net, john)
	if len(st) != 3 || st[0].State != identity.EmailStateSent || st[1].State != identity.EmailStateDeferred || st[1].RetryAt != "2026-10-14T09:05:00Z" {
		t.Fatalf("statuses: %+v", st)
	}
	if !strings.Contains(st[1].Reason, "spaced out") {
		t.Fatalf("reason: %q", st[1].Reason)
	}
	// Five minutes later the second goes; the third now waits ten.
	*now = now.Add(5 * time.Minute)
	b.Outbound.ProcessDue(ctx)
	if n := len(readSent(t, b.logDir)); n != 2 {
		t.Fatalf("after 5m sent %d", n)
	}
	*now = now.Add(9 * time.Minute)
	b.Outbound.ProcessDue(ctx)
	if n := len(readSent(t, b.logDir)); n != 2 {
		t.Fatalf("third went before its 10m gap")
	}
	*now = now.Add(time.Minute)
	b.Outbound.ProcessDue(ctx)
	if n := len(readSent(t, b.logDir)); n != 3 {
		t.Fatalf("third did not go after 10m")
	}
	// Waiting is not failing: no deferred-status spam, no attempts spent.
	if got := len(statuses(t, b.net, john)); got != 5 {
		t.Fatalf("statuses after all sent: %d, want 3 sent + 2 deferred", got)
	}
}

func TestDailyLimitAndOperatorRaise(t *testing.T) {
	b, john, now := pacedBridge(t, func(c *Config) { c.OutDailyLimit = 2; c.OutSpacing = 0 })
	ctx := context.Background()
	for _, id := range []string{"A", "B", "C"} {
		sendN(t, b, john, id)
	}
	runOutbound(t, b)
	if n := len(readSent(t, b.logDir)); n != 2 {
		t.Fatalf("sent %d, want the daily 2", n)
	}
	st := statuses(t, b.net, john)
	if last := st[len(st)-1]; last.State != identity.EmailStateDeferred || !strings.Contains(last.Reason, "2 emails a day") || last.RetryAt != "2026-10-15T09:00:00Z" {
		t.Fatalf("third: %+v", last)
	}
	// The operator raises john's limit: the waiting email goes at once.
	if err := b.Store.SetLimits(ctx, Limits{Identity: "john.poweur.test", Daily: 50, Note: "asked for more"}); err != nil {
		t.Fatal(err)
	}
	_ = b.Store.RescheduleAt(ctx, "john.poweur.test\n"+b.net.inbox[2].ID, *now)
	b.Outbound.ProcessDue(ctx)
	if n := len(readSent(t, b.logDir)); n != 3 {
		t.Fatalf("raise did not take: sent %d", n)
	}
}

func TestBridgeWideCapAndPendingBound(t *testing.T) {
	b, john, _ := pacedBridge(t, func(c *Config) { c.OutSpacing = 0; c.OutGlobalDailyLimit = 1; c.OutMaxPending = 1 })
	jane := b.net.add("jane.poweur.test", b.self.name)
	sendN(t, b, john, "J1")
	sendN(t, b, jane, "N1")
	sendN(t, b, jane, "N2")
	runOutbound(t, b)
	if n := len(readSent(t, b.logDir)); n != 1 {
		t.Fatalf("bridge-wide cap: sent %d", n)
	}
	st := statuses(t, b.net, jane)
	if len(st) != 2 || st[0].State != identity.EmailStateDeferred || !strings.Contains(st[0].Reason, "capacity") ||
		st[1].State != identity.EmailStateRefused || !strings.Contains(st[1].Reason, "already waiting") {
		t.Fatalf("jane: %+v", st)
	}
}

func TestReceiveOnlyBridgeRefusesSends(t *testing.T) {
	b := newTestBridge(t, func(c *Config) { c.Transport = "none" })
	john := b.net.add("john.poweur.test", b.self.name)
	sendN(t, b, john, "X")
	runOutbound(t, b)
	if st := statuses(t, b.net, john); len(st) != 1 || st[0].State != identity.EmailStateRefused || !strings.Contains(st[0].Reason, "only receives") {
		t.Fatalf("statuses: %+v", st)
	}
	if err := b.Publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	if raw := string(b.net.sys["/identities/"+b.self.name+"/system/.poweur/public/capabilities.json"]); !strings.Contains(raw, `"email_send": "off"`) {
		t.Fatalf("capabilities: %s", raw)
	}
}
