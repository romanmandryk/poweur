package relay

import (
	"crypto/ed25519"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/poweur/api/internal/config"
	"github.com/poweur/api/internal/dns"
)

// EPIC-007 E07-T5: the requests queue is metered per *sending relay*.
//
// These are the scenario tests behind the acceptance criterion "per-relay
// request throttling tested". They exercise the shape of the actual attack:
// not one identity sending a thousand requests (the per-identity limiter has
// always caught that) but a thousand identities sending one each, all of them
// registered minutes ago on the same cheap relay. A per-identity cap cannot
// see that; the relay that carried them is the one field the attacker cannot
// vary.

// newMeteredRelay hosts poweur.net identities against a fake DNS zone, with
// contact-request admissions capped per sending relay.
func newMeteredRelay(t *testing.T, limits config.RateLimits, zone *fakeResolver) (*Server, *httptest.Server) {
	t.Helper()
	cfg := config.Config{
		ListenAddr:           ":0",
		RelayAddress:         "relay.test",
		RelayScheme:          "http",
		DNSTTL:               time.Minute,
		ChallengeTTL:         time.Minute,
		Version:              "test",
		DataDir:              t.TempDir(),
		HostedDomains:        []string{"poweur.net"},
		ResolverAllowPrivate: true,
		// Deliberately generous: any rejection below must come from the
		// per-relay meter and nothing else.
		RateLimits:         config.RateLimits{PerMinute: 100000, PerHour: 100000, PerDay: 100000},
		RequestRelayLimits: limits,
	}
	server := NewServer(cfg, zone, dns.NewProviderFactory(cfg))
	ts := httptest.NewServer(server.Router())
	t.Cleanup(ts.Close)
	server.cfg.RelayAddress = strings.TrimPrefix(ts.URL, "http://")
	return server, ts
}

func newZone() *fakeResolver {
	return &fakeResolver{txt: map[string][]string{}, hosts: map[string][]string{}}
}

// strangerOn mints an identity hosted somewhere else and publishes it in the
// fake zone: a DNS-resolvable key so the signature verifies, and a relay host
// so the meter has something to bucket on.
func strangerOn(t *testing.T, zone *fakeResolver, name, relayHost string) hostedID {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(nil)
	zone.txt["_poweur."+name] = []string{"poweur-pubkey=ed25519:" + base64.RawURLEncoding.EncodeToString(pub)}
	zone.hosts[name] = []string{relayHost}
	return hostedID{name: name, pub: pub, priv: priv}
}

func TestRequestsQueueMeteredPerSenderRelay(t *testing.T) {
	zone := newZone()
	server, ts := newMeteredRelay(t, config.RateLimits{PerMinute: 2}, zone)
	alice := registerTestIdentity(t, server, ts, "alice.poweur.net")
	setSysFile(t, server, alice.name, ".poweur/relay/inbox-policy.json", `{"version":1,"mode":"contacts_and_requests"}`)

	// Three fresh identities, one relay. Each is a stranger to alice, each
	// sends exactly one request, and none of them exceeds a per-identity cap.
	flood := []hostedID{
		strangerOn(t, zone, "s1.cheap.test", "peer.cheap.test"),
		strangerOn(t, zone, "s2.cheap.test", "peer.cheap.test"),
		strangerOn(t, zone, "s3.cheap.test", "peer.cheap.test"),
	}

	for i, sender := range flood[:2] {
		resp, _ := postTypedMessage(t, ts, sender, alice.name, "sys.contact.request", "hi")
		out := mustStatus(t, resp, http.StatusAccepted, "request within budget")
		if out["status"] != "request_queued" {
			t.Fatalf("request %d: %v", i, out)
		}
	}

	resp, _ := postTypedMessage(t, ts, flood[2], alice.name, "sys.contact.request", "hi")
	out := mustStatus(t, resp, http.StatusTooManyRequests, "request over the relay budget")
	if out["error"] != "rate_limit_exceeded" {
		t.Fatalf("error = %v", out)
	}
	// The scope must name the relay, not the sender: a client told "sender"
	// would go and look at the wrong thing, and its user would be told they
	// are rate limited when the truth is their relay is.
	if out["scope"] != "sender_relay" {
		t.Fatalf("scope = %v, want sender_relay", out["scope"])
	}
	if out["window"] != "minute" {
		t.Fatalf("window = %v", out["window"])
	}
	if limit, _ := out["limit"].(float64); int(limit) != 2 {
		t.Fatalf("limit = %v", out["limit"])
	}
	resetAt, _ := out["reset_at"].(string)
	if _, err := time.Parse(time.RFC3339, resetAt); err != nil {
		t.Fatalf("reset_at = %q: %v", resetAt, err)
	}

	// Blast radius is one relay. Somebody on a well-run relay is unaffected
	// by a neighbour they have never heard of — the property that makes
	// metering the relay tolerable in the first place.
	honest := strangerOn(t, zone, "honest.polite.test", "polite.test")
	resp, _ = postTypedMessage(t, ts, honest, alice.name, "sys.contact.request", "hello")
	mustStatus(t, resp, http.StatusAccepted, "unrelated relay")

	queued := challengeSigned(t, ts, alice, "/requests/"+alice.name)
	list, _ := queued["requests"].([]any)
	if len(list) != 3 {
		t.Fatalf("requests queue holds %d, want 3 (two from the capped relay, one from the honest one)", len(list))
	}
	for _, entry := range list {
		req, _ := entry.(map[string]any)
		if req["sender"] == flood[2].name {
			t.Fatalf("a throttled request must not reach the queue: %v", req)
		}
	}
}

// The meter is aimed at one door. It must not become a tax on conversation,
// and it must not charge this relay's own users — whom it already meters per
// identity and gates at registration (EPIC-014).
func TestRequestMeterSparesLocalSendersAndConversation(t *testing.T) {
	zone := newZone()
	server, ts := newMeteredRelay(t, config.RateLimits{PerMinute: 1}, zone)
	alice := registerTestIdentity(t, server, ts, "alice.poweur.net")
	setSysFile(t, server, alice.name, ".poweur/relay/inbox-policy.json", `{"version":1,"mode":"contacts_and_requests"}`)

	// friend is on the same cheap relay as the flood, and alice already chose
	// them. That choice must outrank anything the relay's neighbours do.
	friend := strangerOn(t, zone, "friend.cheap.test", "peer.cheap.test")
	setSysFile(t, server, alice.name, ".poweur/relay/contacts.json",
		`{"version":1,"contacts":[{"identity":"friend.cheap.test","state":"accepted"}]}`)

	// Burn the peer relay's whole minute budget.
	resp, _ := postTypedMessage(t, ts, strangerOn(t, zone, "flood1.cheap.test", "peer.cheap.test"),
		alice.name, "sys.contact.request", "hi")
	mustStatus(t, resp, http.StatusAccepted, "first request")
	resp, _ = postTypedMessage(t, ts, strangerOn(t, zone, "flood2.cheap.test", "peer.cheap.test"),
		alice.name, "sys.contact.request", "hi")
	mustStatus(t, resp, http.StatusTooManyRequests, "second request")

	// A local stranger still gets their one request slot: this relay knows
	// exactly who they are and has its own levers over them.
	bob := registerTestIdentity(t, server, ts, "bob.poweur.net")
	resp, _ = postTypedMessage(t, ts, bob, alice.name, "sys.contact.request", "it's bob")
	out := mustStatus(t, resp, http.StatusAccepted, "local sender request")
	if out["status"] != "request_queued" {
		t.Fatalf("local sender: %v", out)
	}

	// And an accepted contact on the exhausted relay talks normally. If this
	// ever 429s, the meter has stopped being an anti-spam measure and started
	// being a way for a stranger to cut two people off from each other.
	resp, _ = postTypedMessage(t, ts, friend, alice.name, "", "still here")
	mustStatus(t, resp, http.StatusAccepted, "accepted contact on a throttled relay")
	if got := server.inbox.Drain(alice.name); len(got) != 1 {
		t.Fatalf("conversation from an accepted contact must deliver: %v", got)
	}
}
