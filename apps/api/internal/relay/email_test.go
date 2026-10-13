package relay

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/poweur/api/internal/config"
	"github.com/poweur/api/internal/ratelimit"
	idpkg "github.com/poweur/identity"
)

// EPIC-023 E23-T5: the bridge a user opted into delivers email.* in every
// inbox mode; nobody else gains anything from the type name.
func TestEmailAdmittedOnlyFromNamedBridge(t *testing.T) {
	server, ts := newTestRelay(t)
	alice := registerTestIdentity(t, server, ts, "alice.poweur.net")
	bridge := registerTestIdentity(t, server, ts, "mailbridge.poweur.net")
	mallory := registerTestIdentity(t, server, ts, "mallory.poweur.net")

	setSysFile(t, server, alice.name, inboxPolicyPath, `{"version":1,"mode":"contacts_only"}`)
	for _, typ := range []string{idpkg.MsgTypeEmailMessage, idpkg.MsgTypeEmailStatus} {
		resp, _ := postTypedMessage(t, ts, bridge, alice.name, typ, "mail")
		mustStatus(t, resp, http.StatusForbidden, "bridge not opted into, "+typ)
	}

	setSysFile(t, server, alice.name, inboxPolicyPath,
		`{"version":1,"mode":"contacts_only","email":{"allow":true,"bridge":"mailbridge.poweur.net"}}`)
	for _, typ := range []string{idpkg.MsgTypeEmailMessage, idpkg.MsgTypeEmailStatus} {
		resp, _ := postTypedMessage(t, ts, bridge, alice.name, typ, "mail")
		mustStatus(t, resp, http.StatusAccepted, "named bridge, "+typ)
	}
	// Being the bridge is permission to deliver mail, not to chat.
	resp, _ := postTypedMessage(t, ts, bridge, alice.name, "", "hello")
	mustStatus(t, resp, http.StatusForbidden, "chat from the bridge, contacts_only")
	// Someone else naming the type gets nothing.
	resp, _ = postTypedMessage(t, ts, mallory, alice.name, idpkg.MsgTypeEmailMessage, "forged")
	mustStatus(t, resp, http.StatusForbidden, "email.message from a stranger")

	// Switched off: the bridge is a stranger again.
	setSysFile(t, server, alice.name, inboxPolicyPath,
		`{"version":1,"mode":"contacts_and_requests","email":{"allow":false,"bridge":"mailbridge.poweur.net"}}`)
	resp, _ = postTypedMessage(t, ts, bridge, alice.name, idpkg.MsgTypeEmailMessage, "mail")
	mustStatus(t, resp, http.StatusForbidden, "disabled email block")

	// A blocked bridge stays blocked whatever the policy says.
	setSysFile(t, server, alice.name, inboxPolicyPath,
		`{"version":1,"mode":"open","email":{"allow":true,"bridge":"mailbridge.poweur.net"}}`)
	setSysFile(t, server, alice.name, contactsPath,
		`{"version":1,"contacts":[{"identity":"mailbridge.poweur.net","state":"blocked","added_at":"2026-10-13T00:00:00Z"}]}`)
	resp, _ = postTypedMessage(t, ts, bridge, alice.name, idpkg.MsgTypeEmailMessage, "mail")
	mustStatus(t, resp, http.StatusForbidden, "blocked bridge")
}

// email.json is the bridge's proof of whose address this is, so the relay
// stores only one about the owner, signed by the owner.
func TestEmailOptInWriteValidated(t *testing.T) {
	server, ts := newTestRelay(t)
	alice := registerTestIdentity(t, server, ts, "alice.poweur.net")
	bob := registerTestIdentity(t, server, ts, "bob.poweur.net")
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

	sign := func(id hostedID, about string) []byte {
		o, err := idpkg.NewEmailOptIn(about, "mailbridge.poweur.net", now)
		if err != nil {
			t.Fatal(err)
		}
		if err := o.Sign(id.priv); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(o)
		return raw
	}
	put := func(raw []byte) int {
		resp := sysReq(t, ts, http.MethodPut, alice, alice.name, idpkg.EmailOptInPath, raw, nil)
		defer resp.Body.Close()
		return resp.StatusCode
	}
	if code := put(sign(bob, alice.name)); code == http.StatusOK || code == http.StatusNoContent || code == http.StatusCreated {
		t.Fatalf("opt-in signed by someone else stored: %d", code)
	}
	if code := put(sign(alice, bob.name)); code < 400 {
		t.Fatalf("opt-in about someone else stored: %d", code)
	}
	if code := put([]byte(`{"version":1}`)); code < 400 {
		t.Fatalf("malformed opt-in stored: %d", code)
	}
	if code := put(sign(alice, alice.name)); code >= 300 {
		t.Fatalf("valid opt-in refused: %d", code)
	}
	req, _ := http.NewRequest(http.MethodGet, ts.URL+idpkg.WellKnownEmailPath, nil)
	req.Host = alice.name
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var served idpkg.EmailOptIn
	_ = json.NewDecoder(resp.Body).Decode(&served)
	if resp.StatusCode != 200 || served.Address != "alice@poweur.net" || resp.Header.Get("Cache-Control") != "public, max-age=60" {
		t.Fatalf("served opt-in = %d %+v %q", resp.StatusCode, served, resp.Header.Get("Cache-Control"))
	}
	if err := served.VerifySignature(alice.pub); err != nil {
		t.Fatal(err)
	}
}

func TestCapabilitiesAdvertiseEmailBridge(t *testing.T) {
	server, ts := newTestRelay(t)
	registerTestIdentity(t, server, ts, "alice.poweur.net")
	registerTestIdentity(t, server, ts, "mailbridge.poweur.net")
	server.cfg.EmailBridgeID = "mailbridge.poweur.net"
	capsURL := ts.URL + "/.well-known/poweur/capabilities.json"
	_, caps := getWellKnown(t, capsURL, "alice.poweur.net")
	endpoints, _ := caps["endpoints"].(map[string]any)
	if endpoints["email_bridge"] != "mailbridge.poweur.net" {
		t.Fatalf("capabilities = %v", caps)
	}
	// The bridge does not advertise itself to itself.
	resp, caps := getWellKnown(t, capsURL, "mailbridge.poweur.net")
	endpoints, _ = caps["endpoints"].(map[string]any)
	if resp.StatusCode == 200 && endpoints["email_bridge"] != nil {
		t.Fatalf("bridge advertises itself: %v", caps)
	}
}

func TestEmailBridgeIDValidated(t *testing.T) {
	base := config.Config{RelayAddress: "relay.test", ListenAddr: ":0"}
	c := base
	c.EmailBridgeID = "https://email.poweur.net"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "EMAIL_BRIDGE_ID") {
		t.Fatalf("bad EMAIL_BRIDGE_ID accepted: %v", err)
	}
	c.EmailBridgeID = "email.poweur.net"
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

// The bridge delivers everyone's mail, so it has its own (larger) budget
// instead of the per-person one.
func TestEmailBridgeHasItsOwnRateLimit(t *testing.T) {
	server, ts := newTestRelay(t)
	alice := registerTestIdentity(t, server, ts, "alice.poweur.net")
	bridge := registerTestIdentity(t, server, ts, "mailbridge.poweur.net")
	other := registerTestIdentity(t, server, ts, "other.poweur.net")
	server.rateLimit = ratelimit.NewLimiter(config.RateLimits{PerMinute: 2, PerHour: 100, PerDay: 100}, config.GlobalRateLimits{PerMinute: 1000, PerHour: 1000, PerDay: 1000})
	server.bridgeRateLimit = ratelimit.NewLimiter(config.RateLimits{PerMinute: 50, PerHour: 100, PerDay: 100}, config.GlobalRateLimits{PerMinute: 50, PerHour: 100, PerDay: 100})
	server.cfg.EmailBridgeID = "mailbridge.poweur.net"
	setSysFile(t, server, alice.name, inboxPolicyPath, `{"version":1,"mode":"open","email":{"allow":true,"bridge":"mailbridge.poweur.net"}}`)
	for i := 0; i < 10; i++ {
		resp, _ := postTypedMessage(t, ts, bridge, alice.name, idpkg.MsgTypeEmailMessage, "mail")
		mustStatus(t, resp, http.StatusAccepted, "bridge send")
	}
	codes := []int{}
	for i := 0; i < 3; i++ {
		resp, _ := postTypedMessage(t, ts, other, alice.name, "", "hi")
		codes = append(codes, resp.StatusCode)
		resp.Body.Close()
	}
	if codes[2] != http.StatusTooManyRequests {
		t.Fatalf("ordinary sender not limited: %v", codes)
	}
}
