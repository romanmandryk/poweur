// End-to-end journeys a person actually walks: hold more than one identity on
// one relay, complete a contact handshake so *both* sides call each other a
// contact, and watch every combination of (recipient policy × sender kind)
// land in the one queue it belongs in.
//
// The existing suites each pin one mechanism — `contacts_test.go` the consent
// loop, `anon_test.go` proof-of-work, `hosted_test.go` registration. What none
// of them covered is the *matrix*: that `open`, `contacts_only` and
// `contacts_and_requests` route a contact, a stranger, a contact request and
// an anonymous sender to exactly one of inbox / requests / anon / rejected,
// and that draining one queue never moves a message into another.
package integration_test

import (
	"encoding/json"
	"strings"
	"testing"

	clipkg "github.com/poweur/cli/pkg/cli"

	"github.com/poweur/integration/fakedns"
)

// journeyRelay boots one hosted relay and points the CLI's identity resolver
// at it — the "existing relay" every journey below registers against.
func journeyRelay(t *testing.T, ids ...string) (relayURL string, zone *fakedns.Zone) {
	t.Helper()
	zone = newZone(t)
	ts, addr := newHostedRelay(t, zone, t.TempDir())
	t.Cleanup(ts.Close)
	for _, id := range ids {
		zone.SetHost(id, addr)
	}
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })
	return ts.URL, zone
}

// createIdentity registers one hosted identity in the given HOME.
func createIdentity(t *testing.T, home, identity, relayURL string) {
	t.Helper()
	runCLI(t, home, "identity", "create", identity, "--hosted", "--relay", relayURL, "--json")
}

// contactState reports the state this home records for target, or "" when
// there is no entry at all. Reading contacts.json through `contacts ls --json`
// keeps the assertion on the CLI's own view rather than on relay internals.
func contactState(t *testing.T, home, owner, target string) string {
	t.Helper()
	stdout, _ := runCLI(t, home, "contacts", "ls", "--json", "--use-identity", owner)
	var file struct {
		Contacts []struct {
			Identity string `json:"identity"`
			State    string `json:"state"`
		} `json:"contacts"`
	}
	if err := json.Unmarshal([]byte(stdout), &file); err != nil {
		t.Fatalf("contacts ls --json for %s: %v\n%s", owner, err, stdout)
	}
	for _, c := range file.Contacts {
		if strings.EqualFold(c.Identity, target) {
			return c.State
		}
	}
	return ""
}

// TestINT_JOURNEY_01_TwoIdentitiesOnOneRelay: one person, one machine, two
// separate identities registered against a relay that is already running —
// listed together, switchable, and cleanly separated in state. The second
// half is the part worth having: `--use-identity` must address the right
// mailbox, so a message to one of them is invisible to the other.
func TestINT_JOURNEY_01_TwoIdentitiesOnOneRelay(t *testing.T) {
	const work = "worklife.poweur.net"
	const home_ = "homelife.poweur.net"
	const outsider = "j1outsider.poweur.net"
	relayURL, _ := journeyRelay(t, work, home_, outsider)

	mine := t.TempDir()
	theirs := t.TempDir()
	createIdentity(t, mine, work, relayURL)
	createIdentity(t, mine, home_, relayURL)
	createIdentity(t, theirs, outsider, relayURL)

	// Both live in the same home, and the most recently created one is active.
	stdout, _ := runCLI(t, mine, "identity", "list", "--json")
	var list struct {
		Identities []string `json:"identities"`
		Active     string   `json:"active"`
	}
	if err := json.Unmarshal([]byte(stdout), &list); err != nil {
		t.Fatalf("identity list --json: %v\n%s", err, stdout)
	}
	if len(list.Identities) != 2 {
		t.Fatalf("want both identities in one home, got %v", list.Identities)
	}
	for _, want := range []string{work, home_} {
		found := false
		for _, got := range list.Identities {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("identity %s missing from %v", want, list.Identities)
		}
	}
	if list.Active != home_ {
		t.Fatalf("active identity = %q, want the most recently created %q", list.Active, home_)
	}

	// Switching is durable: it is written to config, not held for one command.
	runCLI(t, mine, "identity", "use", work)
	stdout, _ = runCLI(t, mine, "identity", "list", "--json")
	if err := json.Unmarshal([]byte(stdout), &list); err != nil {
		t.Fatal(err)
	}
	if list.Active != work {
		t.Fatalf("after `identity use %s`, active = %q", work, list.Active)
	}

	// Each identity registered independently: both resolve, with different keys.
	workDoc, _ := runCLI(t, mine, "identity", "lookup", work, "--json")
	homeDoc, _ := runCLI(t, mine, "identity", "lookup", home_, "--json")
	if keyOf(t, workDoc) == keyOf(t, homeDoc) {
		t.Fatal("two identities must not share a signing key")
	}

	// The mailboxes are separate. An outsider writes to one of them…
	runCLI(t, theirs, "send", work, "quarterly numbers")
	// …and only that one has it. The active identity is `work`, so the
	// bare `inbox` reads it; the other is addressed explicitly.
	homeInbox, _ := runCLI(t, mine, "inbox", "--use-identity", home_)
	if strings.Contains(homeInbox, "quarterly numbers") {
		t.Fatalf("message to %s leaked into %s's inbox:\n%s", work, home_, homeInbox)
	}
	workInbox, _ := runCLI(t, mine, "inbox")
	assertDecryptedInbox(t, workInbox, outsider, "quarterly numbers")

	// And they can message each other, which is the smallest proof that the
	// two are genuinely distinct principals to the relay rather than aliases.
	runCLI(t, mine, "send", home_, "reminder: dentist", "--use-identity", work)
	homeInbox, _ = runCLI(t, mine, "inbox", "--use-identity", home_)
	assertDecryptedInbox(t, homeInbox, work, "reminder: dentist")
}

// keyOf pulls the signing key out of an `identity lookup --json` document.
func keyOf(t *testing.T, stdout string) string {
	t.Helper()
	var doc struct {
		Document struct {
			PublicKey string `json:"public_key"`
		} `json:"document"`
		PublicKey string `json:"public_key"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("identity lookup --json: %v\n%s", err, stdout)
	}
	if doc.Document.PublicKey != "" {
		return doc.Document.PublicKey
	}
	if doc.PublicKey == "" {
		t.Fatalf("no public key in lookup output: %s", stdout)
	}
	return doc.PublicKey
}

// TestINT_JOURNEY_02_MutualContactHandshake: the handshake seen from both
// sides. `contacts_test.go` asserts the accepter's view; the half that
// decides whether the relationship actually works is the *requester's* —
// until Bob's own contacts say `accepted`, his `contacts_only` policy still
// bounces the person who just accepted him, and neither side can see why.
func TestINT_JOURNEY_02_MutualContactHandshake(t *testing.T) {
	const alice = "mutalice.poweur.net"
	const bob = "mutbob.poweur.net"
	relayURL, _ := journeyRelay(t, alice, bob)

	aliceHome := t.TempDir()
	bobHome := t.TempDir()
	createIdentity(t, aliceHome, alice, relayURL)
	createIdentity(t, bobHome, bob, relayURL)

	// Both lock down: Alice takes requests, Bob takes only contacts. Bob's
	// side is the one that traps a half-finished handshake.
	runCLI(t, aliceHome, "policy", "set", "contacts_and_requests")
	runCLI(t, bobHome, "policy", "set", "contacts_only")

	// Before the handshake, neither knows the other.
	if state := contactState(t, aliceHome, alice, bob); state != "" {
		t.Fatalf("alice already knows bob as %q", state)
	}

	// Bob asks. His side records `requested` immediately — the pin is taken
	// when you address someone, not when they answer.
	runCLI(t, bobHome, "contacts", "request", alice, "hi, this is bob")
	if state := contactState(t, bobHome, bob, alice); state != "requested" {
		t.Fatalf("bob's own record after requesting = %q, want requested", state)
	}

	// Alice sees exactly one request, from Bob, and nothing in her inbox:
	// under contacts_and_requests the queue is not the inbox.
	stdout, _ := runCLI(t, aliceHome, "requests", "--json")
	var queue struct {
		Requests []struct {
			Sender string `json:"sender"`
			Type   string `json:"type"`
		} `json:"requests"`
	}
	if err := json.Unmarshal([]byte(stdout), &queue); err != nil {
		t.Fatalf("requests --json: %v\n%s", err, stdout)
	}
	if len(queue.Requests) != 1 || !strings.EqualFold(queue.Requests[0].Sender, bob) {
		t.Fatalf("alice's request queue = %+v, want one from %s", queue.Requests, bob)
	}
	if queue.Requests[0].Type != "sys.contact.request" {
		t.Fatalf("queued request type = %q", queue.Requests[0].Type)
	}
	inbox, _ := runCLI(t, aliceHome, "inbox")
	if strings.Contains(inbox, "hi, this is bob") {
		t.Fatalf("the contact request also landed in the inbox:\n%s", inbox)
	}

	// Alice accepts. That pins Bob on her side and sends him the answer.
	runCLI(t, aliceHome, "contacts", "accept", bob, "--petname", "Bob")
	if state := contactState(t, aliceHome, alice, bob); state != "accepted" {
		t.Fatalf("alice's record after accepting = %q, want accepted", state)
	}

	// Bob's policy is closed, so her answer rides his requests queue rather
	// than his message stream — and reading that queue must finish the
	// handshake on his side too, not just show him a line about it.
	stdout, _ = runCLI(t, bobHome, "requests")
	if !strings.Contains(stdout, alice) {
		t.Fatalf("bob was never told that %s accepted:\n%s", alice, stdout)
	}
	if state := contactState(t, bobHome, bob, alice); state != "accepted" {
		t.Fatalf("bob still records alice as %q after her accept reached him — "+
			"the handshake is one-sided and his own contacts_only policy will "+
			"bounce her", state)
	}

	// An answered request is no longer a pending one: the queue is empty and
	// stays empty, so the badge a UI hangs off it can actually clear.
	stdout, _ = runCLI(t, bobHome, "requests")
	if !strings.Contains(stdout, "no pending requests") {
		t.Fatalf("bob's request queue did not clear after the handshake:\n%s", stdout)
	}

	// The recipient also stays clear on subsequent reads; no second approval.
	for _, home := range []string{aliceHome, bobHome} {
		for range 2 {
			out, _ := runCLI(t, home, "requests")
			if !strings.Contains(out, "no pending requests") {
				t.Fatalf("answered request still pending: %s", out)
			}
		}
	}

	// Mutual means both directions carry, against two closed policies.
	runCLI(t, aliceHome, "send", bob, "glad we connected")
	bobInbox, _ := runCLI(t, bobHome, "inbox")
	assertDecryptedInbox(t, bobInbox, alice, "glad we connected")

	runCLI(t, bobHome, "send", alice, "likewise")
	aliceInbox, _ := runCLI(t, aliceHome, "inbox")
	assertDecryptedInbox(t, aliceInbox, bob, "likewise")
}

// landing is where a message ended up, read from the recipient's own three
// queues. Each read drains, so all three are read every time and in the same
// order — which is also how the test proves isolation: a message that shows
// up in two of them, or moves from one to another on a second read, fails.
type landing struct{ inbox, requests, anon string }

func (l landing) String() string {
	return "inbox=" + strings.TrimSpace(l.inbox) +
		" | requests=" + strings.TrimSpace(l.requests) +
		" | anon=" + strings.TrimSpace(l.anon)
}

func drainAll(t *testing.T, home, identity string) landing {
	t.Helper()
	inbox, _ := runCLI(t, home, "inbox", "--use-identity", identity)
	requests, _ := runCLI(t, home, "requests", "--use-identity", identity)
	anon, _ := runCLI(t, home, "anon", "--use-identity", identity)
	return landing{inbox: inbox, requests: requests, anon: anon}
}

// TestINT_JOURNEY_03_SignedPolicyMatrix walks every (inbox policy × signed
// sender) pair and asserts the one queue the message is allowed to reach.
//
// The point of the table is the negative half. Each case says not only where
// the message landed but where it did *not*: a contact request that also
// copies itself into the inbox, or a stranger the relay rejects but whose
// text turns up in a queue anyway, is a policy that does not hold.
func TestINT_JOURNEY_03_SignedPolicyMatrix(t *testing.T) {
	type senderKind string
	const (
		asContact  senderKind = "contact"
		asStranger senderKind = "stranger"
		asRequest  senderKind = "contact-request"
	)

	cases := []struct {
		name   string
		policy string
		kind   senderKind
		want   string // "inbox" | "requests" | "rejected"
		tag    string // dns label prefix, unique per case
	}{
		{"open/contact", "open", asContact, "inbox", "moc"},
		{"open/stranger", "open", asStranger, "inbox", "mos"},
		{"open/request", "open", asRequest, "requests", "mor"},

		{"contacts_only/contact", "contacts_only", asContact, "inbox", "mcc"},
		{"contacts_only/stranger", "contacts_only", asStranger, "rejected", "mcs"},
		{"contacts_only/request", "contacts_only", asRequest, "rejected", "mcr"},

		{"contacts_and_requests/contact", "contacts_and_requests", asContact, "inbox", "mqc"},
		{"contacts_and_requests/stranger", "contacts_and_requests", asStranger, "rejected", "mqs"},
		{"contacts_and_requests/request", "contacts_and_requests", asRequest, "requests", "mqr"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rcpt := tc.tag + "rcpt.poweur.net"
			sender := tc.tag + "send.poweur.net"
			relayURL, _ := journeyRelay(t, rcpt, sender)

			rcptHome := t.TempDir()
			senderHome := t.TempDir()
			createIdentity(t, rcptHome, rcpt, relayURL)
			createIdentity(t, senderHome, sender, relayURL)
			runCLI(t, rcptHome, "policy", "set", tc.policy)

			body := "matrix probe " + tc.tag
			var code int
			switch tc.kind {
			case asContact:
				runCLI(t, rcptHome, "contacts", "add", sender)
				code = runCLICode(t, senderHome, "send", rcpt, body)
			case asStranger:
				code = runCLICode(t, senderHome, "send", rcpt, body)
			case asRequest:
				code = runCLICode(t, senderHome, "contacts", "request", rcpt, body)
			}

			if tc.want == "rejected" {
				if code == 0 {
					t.Fatalf("%s under %s must be refused at send time", tc.kind, tc.policy)
				}
				// Refused means refused everywhere, not merely unreported.
				got := drainAll(t, rcptHome, rcpt)
				if strings.Contains(got.String(), body) {
					t.Fatalf("refused message still reached a queue: %s", got)
				}
				return
			}
			if code != 0 {
				t.Fatalf("%s under %s should have been accepted, exit %d", tc.kind, tc.policy, code)
			}

			got := drainAll(t, rcptHome, rcpt)
			reached := map[string]bool{
				"inbox":    strings.Contains(got.inbox, body),
				"requests": strings.Contains(got.requests, sender),
				"anon":     strings.Contains(got.anon, body),
			}
			// A queued request is stored encrypted and listed by sender, so
			// the requests tray is matched on the sender rather than the text.
			if tc.want == "requests" && strings.Contains(got.inbox, body) {
				t.Fatalf("a queued contact request must not also reach the inbox: %s", got)
			}
			if !reached[tc.want] {
				t.Fatalf("want %s in %s, got: %s", body, tc.want, got)
			}
			if reached["anon"] {
				t.Fatalf("a signed message reached the anonymous queue: %s", got)
			}

			// Drained means drained: a second read finds nothing, so a UI
			// counting what it holds cannot double-count.
			second := drainAll(t, rcptHome, rcpt)
			if strings.Contains(second.String(), body) {
				t.Fatalf("message survived the drain and was delivered twice: %s", second)
			}
		})
	}
}

// TestINT_JOURNEY_04_AnonymousTiers covers the three anonymous settings a
// recipient can choose — closed, open, and priced in proof-of-work — against
// an inbox that is otherwise shut. Anonymous ingress is orthogonal to the
// contact policy, and the assertion that matters is that it stays that way:
// an unsigned message never reaches the signed inbox, whatever the mode.
func TestINT_JOURNEY_04_AnonymousTiers(t *testing.T) {
	const rcpt = "anontiers.poweur.net"
	const sender = "anontsend.poweur.net"
	relayURL, _ := journeyRelay(t, rcpt, sender)

	rcptHome := t.TempDir()
	senderHome := t.TempDir()
	createIdentity(t, rcptHome, rcpt, relayURL)
	createIdentity(t, senderHome, sender, relayURL)

	// The signed inbox is shut for strangers throughout.
	runCLI(t, rcptHome, "policy", "set", "contacts_only")
	if code := runCLICode(t, senderHome, "send", rcpt, "signed stranger"); code == 0 {
		t.Fatal("contacts_only must refuse a signed stranger")
	}

	// Tier 1 — anonymous off (the default): refused, and nothing is queued.
	if code := runCLICode(t, senderHome, "send", rcpt, "anon while closed", "--anon"); code == 0 {
		t.Fatal("anonymous send must be refused while the recipient has not opted in")
	}
	if got := drainAll(t, rcptHome, rcpt); strings.Contains(got.String(), "anon while closed") {
		t.Fatalf("refused anonymous message was queued anyway: %s", got)
	}

	// Tier 2 — anonymous allowed, no challenge: it goes straight through.
	stdout, _ := runCLI(t, rcptHome, "policy", "set", "contacts_only",
		"--anon-allow", "--anon-challenge", "none")
	if !strings.Contains(stdout, "anonymous allowed") {
		t.Fatalf("policy set: %s", stdout)
	}
	_, stderr := runCLI(t, senderHome, "send", rcpt, "free anonymous tip", "--anon")
	if strings.Contains(stderr, "proof-of-work") {
		t.Fatalf("a free anonymous tier must not charge proof-of-work: %s", stderr)
	}
	got := drainAll(t, rcptHome, rcpt)
	if !strings.Contains(got.anon, "free anonymous tip") {
		t.Fatalf("free anonymous message did not reach the anon queue: %s", got)
	}
	if strings.Contains(got.inbox, "free anonymous tip") || strings.Contains(got.requests, "free anonymous tip") {
		t.Fatalf("anonymous message leaked out of its queue: %s", got)
	}

	// Tier 3 — anonymous priced in proof-of-work: the sender pays, and the
	// message is marked unauthenticated when it is read.
	runCLI(t, rcptHome, "policy", "set", "contacts_only",
		"--anon-allow", "--anon-challenge", "pow", "--anon-bits", "8")
	_, stderr = runCLI(t, senderHome, "send", rcpt, "paid anonymous tip", "--anon")
	if !strings.Contains(stderr, "proof-of-work") || !strings.Contains(stderr, "solved in") {
		t.Fatalf("pow tier must make the sender solve a challenge: %s", stderr)
	}
	got = drainAll(t, rcptHome, rcpt)
	if !strings.Contains(got.anon, "paid anonymous tip") {
		t.Fatalf("pow anonymous message did not reach the anon queue: %s", got)
	}
	if !strings.Contains(got.anon, "ANONYMOUS") || !strings.Contains(got.anon, "unauthenticated") {
		t.Fatalf("anon queue must mark who it cannot vouch for: %s", got.anon)
	}
	if strings.Contains(got.inbox, "anonymous tip") {
		t.Fatalf("anonymous message leaked into the signed inbox: %s", got)
	}

	// An accepted contact is still the accepted contact they were: opting
	// into anonymous mail changes nothing about the signed path.
	runCLI(t, rcptHome, "contacts", "add", sender)
	runCLI(t, senderHome, "send", rcpt, "signed and known")
	got = drainAll(t, rcptHome, rcpt)
	assertDecryptedInbox(t, got.inbox, sender, "signed and known")
}

// TestINT_JOURNEY_05_OpenPolicyHandshake is journey 02's other half: the
// default `open` policy still parks the handshake in the requests queue,
// never the inbox. Chat from anyone is a separate decision from "someone
// asked to be a contact".
func TestINT_JOURNEY_05_OpenPolicyHandshake(t *testing.T) {
	const alice = "openalice.poweur.net"
	const bob = "openbob.poweur.net"
	relayURL, _ := journeyRelay(t, alice, bob)

	aliceHome := t.TempDir()
	bobHome := t.TempDir()
	createIdentity(t, aliceHome, alice, relayURL)
	createIdentity(t, bobHome, bob, relayURL)

	runCLI(t, bobHome, "contacts", "request", alice, "hello from bob")

	stdout, _ := runCLI(t, aliceHome, "requests")
	if !strings.Contains(stdout, bob) {
		t.Fatalf("alice's open policy hid bob's request:\n%s", stdout)
	}
	inbox, _ := runCLI(t, aliceHome, "inbox")
	if strings.Contains(inbox, "hello from bob") {
		t.Fatalf("the contact request also landed in the open inbox:\n%s", inbox)
	}
	runCLI(t, aliceHome, "contacts", "accept", bob)

	if state := contactState(t, bobHome, bob, alice); state != "requested" {
		t.Fatalf("precondition: bob should still be waiting, got %q", state)
	}
	stdout, _ = runCLI(t, bobHome, "requests")
	if !strings.Contains(stdout, alice) {
		t.Fatalf("bob was not told the handshake completed:\n%s", stdout)
	}
	if state := contactState(t, bobHome, bob, alice); state != "accepted" {
		t.Fatalf("bob records alice as %q after reading her accept", state)
	}
}

// TestINT_JOURNEY_06_RequestReachesAcceptedContact is the asymmetric case:
// Alice already lists Bob as a contact, Bob still asks. The request must
// reach her requests queue so she can answer and finish *his* handshake.
func TestINT_JOURNEY_06_RequestReachesAcceptedContact(t *testing.T) {
	const alice = "havealice.poweur.net"
	const bob = "havebob.poweur.net"
	relayURL, _ := journeyRelay(t, alice, bob)

	aliceHome := t.TempDir()
	bobHome := t.TempDir()
	createIdentity(t, aliceHome, alice, relayURL)
	createIdentity(t, bobHome, bob, relayURL)

	runCLI(t, aliceHome, "contacts", "add", bob)
	if state := contactState(t, aliceHome, alice, bob); state != "accepted" {
		t.Fatalf("precondition: alice should already have bob, got %q", state)
	}

	runCLI(t, bobHome, "contacts", "request", alice, "please add me back")
	if state := contactState(t, bobHome, bob, alice); state != "requested" {
		t.Fatalf("bob's own record after requesting = %q, want requested", state)
	}

	stdout, _ := runCLI(t, aliceHome, "requests")
	if !strings.Contains(stdout, bob) {
		t.Fatalf("alice already listing bob as a contact hid his request:\n%s", stdout)
	}
	inbox, _ := runCLI(t, aliceHome, "inbox")
	if strings.Contains(inbox, "please add me back") {
		t.Fatalf("the contact request leaked into chat:\n%s", inbox)
	}

	runCLI(t, aliceHome, "contacts", "accept", bob)
	stdout, _ = runCLI(t, bobHome, "requests")
	if !strings.Contains(stdout, alice) {
		t.Fatalf("bob was not told alice answered:\n%s", stdout)
	}
	if state := contactState(t, bobHome, bob, alice); state != "accepted" {
		t.Fatalf("bob records alice as %q after her accept", state)
	}
}
