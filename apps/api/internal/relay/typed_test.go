package relay

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/poweur/api/internal/crypto"
	idpkg "github.com/poweur/identity"
)

// EPIC-009 E09-T3 at the relay: the envelope extensions are validated,
// signed, stored and handed back verbatim; `sys.*` is defended; everything
// else is opaque.

var typedMsgSeq int

// envelopeOpts is the shape of a message a test wants to post. `tamper` runs
// after signing, which is how the binding tests add or change a field the
// signature does not cover.
type envelopeOpts struct {
	msgType   string
	threadID  string
	expiresAt string
	metadata  map[string]string
	payload   string
	tamper    func(*Message)
}

func postEnvelope(t *testing.T, ts *httptest.Server, from hostedID, to string, opts envelopeOpts) (*http.Response, Message) {
	t.Helper()
	typedMsgSeq++
	payload := opts.payload
	if payload == "" {
		payload = "Y2lwaGVydGV4dA"
	}
	msg := Message{
		ID:        "msg_typed_" + time.Now().Format("150405.000000000") + "_" + string(rune('a'+typedMsgSeq%26)),
		Sender:    from.name,
		Recipient: to,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Payload:   payload,
		Type:      opts.msgType,
		ThreadID:  opts.threadID,
		ExpiresAt: opts.expiresAt,
		Metadata:  opts.metadata,
		Encryption: &EncryptionMeta{
			Alg:                "x25519-chacha20-poly1305",
			EphemeralPublicKey: "ephemeral-pub",
			Nonce:              "nonce",
		},
	}
	canonical := crypto.CanonicalMessageEnvelope(
		msg.Sender, msg.Recipient, msg.Timestamp, msg.Payload, msg.ID, "",
		msg.Type, msg.ThreadID, msg.ExpiresAt, msg.Metadata,
		&crypto.EncryptionMeta{
			Alg:                msg.Encryption.Alg,
			EphemeralPublicKey: msg.Encryption.EphemeralPublicKey,
			Nonce:              msg.Encryption.Nonce,
		})
	msg.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(from.priv, []byte(canonical)))
	if opts.tamper != nil {
		opts.tamper(&msg)
	}
	body, _ := json.Marshal(msg)
	resp, err := http.Post(ts.URL+"/messages", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return resp, msg
}

// TestTypedEnvelopeRoundTrip: the four extension fields survive the relay
// unchanged. They have to — the recipient recomputes the canonical string to
// verify the signature, so a relay that dropped `thread_id` on the floor
// would turn every threaded message into a signature failure downstream.
func TestTypedEnvelopeRoundTrip(t *testing.T) {
	server, ts := newTestRelay(t)
	alice := registerTestIdentity(t, server, ts, "alice.poweur.net")
	bob := registerTestIdentity(t, server, ts, "bob.poweur.net")

	metadata := map[string]string{"mime": "image/png", "bytes": "20480"}
	resp, sent := postEnvelope(t, ts, bob, alice.name, envelopeOpts{
		msgType:   "chat.attachment",
		threadID:  "thr_project_x",
		expiresAt: "2126-01-16T09:30:00Z",
		metadata:  metadata,
	})
	mustStatus(t, resp, http.StatusAccepted, "typed envelope")

	inbox := challengeSigned(t, ts, alice, "/messages/"+alice.name)
	msgs, _ := inbox["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("inbox: %v", inbox)
	}
	got, _ := msgs[0].(map[string]any)
	if got["id"] != sent.ID {
		t.Fatalf("id: %v", got)
	}
	if got["type"] != "chat.attachment" {
		t.Fatalf("type not preserved: %v", got["type"])
	}
	if got["thread_id"] != "thr_project_x" {
		t.Fatalf("thread_id not preserved: %v", got["thread_id"])
	}
	if got["expires_at"] != "2126-01-16T09:30:00Z" {
		t.Fatalf("expires_at not preserved: %v", got["expires_at"])
	}
	gotMeta, _ := got["metadata"].(map[string]any)
	if len(gotMeta) != 2 || gotMeta["mime"] != "image/png" || gotMeta["bytes"] != "20480" {
		t.Fatalf("metadata not preserved: %v", got["metadata"])
	}
}

// TestTypedEnvelopeSignatureBinding: each new field is inside the signature.
// Adding one after signing, or changing one, must fail verification — the
// whole point of extending the canonical string rather than leaving these
// fields as unsigned hints a relay could rewrite.
func TestTypedEnvelopeSignatureBinding(t *testing.T) {
	server, ts := newTestRelay(t)
	alice := registerTestIdentity(t, server, ts, "alice.poweur.net")
	bob := registerTestIdentity(t, server, ts, "bob.poweur.net")

	tampers := map[string]func(*Message){
		"thread added after signing":   func(m *Message) { m.ThreadID = "thr_injected" },
		"expiry added after signing":   func(m *Message) { m.ExpiresAt = "2126-01-16T09:30:00Z" },
		"metadata added after signing": func(m *Message) { m.Metadata = map[string]string{"k": "v"} },
	}
	for name, tamper := range tampers {
		t.Run(name, func(t *testing.T) {
			resp, _ := postEnvelope(t, ts, bob, alice.name, envelopeOpts{tamper: tamper})
			mustStatus(t, resp, http.StatusUnauthorized, name)
		})
	}

	t.Run("metadata value changed after signing", func(t *testing.T) {
		resp, _ := postEnvelope(t, ts, bob, alice.name, envelopeOpts{
			metadata: map[string]string{"k": "v"},
			tamper:   func(m *Message) { m.Metadata["k"] = "w" },
		})
		mustStatus(t, resp, http.StatusUnauthorized, "metadata value changed")
	})

	t.Run("thread changed after signing", func(t *testing.T) {
		resp, _ := postEnvelope(t, ts, bob, alice.name, envelopeOpts{
			threadID: "thr_one",
			tamper:   func(m *Message) { m.ThreadID = "thr_two" },
		})
		mustStatus(t, resp, http.StatusUnauthorized, "thread changed")
	})

	t.Run("thread removed after signing", func(t *testing.T) {
		resp, _ := postEnvelope(t, ts, bob, alice.name, envelopeOpts{
			threadID: "thr_one",
			tamper:   func(m *Message) { m.ThreadID = "" },
		})
		mustStatus(t, resp, http.StatusUnauthorized, "thread removed")
	})
}

// TestSysNamespaceIsReserved: the relay refuses an unregistered `sys.*` type.
// An application that could mint one would borrow the relay's own routing
// authority — every client treats `sys.contact.request` as a consent gesture.
func TestSysNamespaceIsReserved(t *testing.T) {
	server, ts := newTestRelay(t)
	alice := registerTestIdentity(t, server, ts, "alice.poweur.net")
	bob := registerTestIdentity(t, server, ts, "bob.poweur.net")

	resp, _ := postEnvelope(t, ts, bob, alice.name, envelopeOpts{msgType: "sys.made.up"})
	out := mustStatus(t, resp, http.StatusBadRequest, "unregistered sys type")
	if out["error"] != "unsupported_type" {
		t.Fatalf("expected unsupported_type, got %v", out)
	}

	// Registered `sys.*` types are not `unsupported_type`. Handshake
	// envelopes have their own policy (a request is queued; an unsolicited
	// accept is refused); everything else still lands in an open inbox.
	for _, known := range idpkg.SystemMessageTypes() {
		resp, _ := postEnvelope(t, ts, bob, alice.name, envelopeOpts{msgType: known})
		switch known {
		case idpkg.MsgTypeContactRequest:
			out := mustStatus(t, resp, http.StatusAccepted, "registered type "+known)
			if out["status"] != "request_queued" {
				t.Fatalf("%s: %v", known, out)
			}
		case idpkg.MsgTypeContactAccept:
			mustStatus(t, resp, http.StatusForbidden, "unsolicited "+known)
		case idpkg.MsgTypeAuthRequest:
			// Only from a service the recipient trusts (auth_prompt_test.go).
			mustStatus(t, resp, http.StatusForbidden, "untrusted "+known)
		default:
			mustStatus(t, resp, http.StatusAccepted, "registered type "+known)
		}
	}
}

// TestUnknownTypesAreOpaque: anything outside `sys.*` is stored and served
// unchanged, whether or not the relay has ever heard of it. That is what lets
// applications ship a message type without a relay release.
func TestUnknownTypesAreOpaque(t *testing.T) {
	server, ts := newTestRelay(t)
	alice := registerTestIdentity(t, server, ts, "alice.poweur.net")
	bob := registerTestIdentity(t, server, ts, "bob.poweur.net")

	for _, msgType := range []string{"", "chat.text", "chat.attachment", "net.example.widget.poked"} {
		resp, _ := postEnvelope(t, ts, bob, alice.name, envelopeOpts{msgType: msgType})
		mustStatus(t, resp, http.StatusAccepted, "opaque type "+msgType)
	}

	inbox := challengeSigned(t, ts, alice, "/messages/"+alice.name)
	msgs, _ := inbox["messages"].([]any)
	if len(msgs) != 4 {
		t.Fatalf("expected all four opaque messages, got %d", len(msgs))
	}
	// The absent type stays absent: the relay does not normalize `chat.text`
	// into the envelope, because the envelope is what the sender signed.
	first, _ := msgs[0].(map[string]any)
	if _, present := first["type"]; present {
		t.Fatalf("relay invented a type for an untyped message: %v", first)
	}
	last, _ := msgs[3].(map[string]any)
	if last["type"] != "net.example.widget.poked" {
		t.Fatalf("unknown type not preserved: %v", last)
	}
}

// TestEnvelopeExtensionValidation: malformed extensions are refused at
// ingress with a 400 that names the field, before any signature work.
func TestEnvelopeExtensionValidation(t *testing.T) {
	server, ts := newTestRelay(t)
	alice := registerTestIdentity(t, server, ts, "alice.poweur.net")
	bob := registerTestIdentity(t, server, ts, "bob.poweur.net")

	cases := map[string]envelopeOpts{
		"unnamespaced type":     {msgType: "widget"},
		"uppercase type":        {msgType: "Chat.Text"},
		"thread with a space":   {threadID: "thr one"},
		"thread with a newline": {threadID: "thr\none"},
		"expires not RFC3339":   {expiresAt: "next tuesday"},
		"metadata upper key":    {metadata: map[string]string{"Mime": "image/png"}},
		"metadata newline":      {metadata: map[string]string{"mime": "image\n/png"}},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			resp, _ := postEnvelope(t, ts, bob, alice.name, opts)
			out := mustStatus(t, resp, http.StatusBadRequest, name)
			if out["error"] != "invalid_message" {
				t.Fatalf("%s: expected invalid_message, got %v", name, out)
			}
		})
	}

	t.Run("too many metadata keys", func(t *testing.T) {
		md := map[string]string{}
		for i := 0; i <= idpkg.MaxMetadataKeys; i++ {
			md[string(rune('a'+i))+"key"] = "v"
		}
		resp, _ := postEnvelope(t, ts, bob, alice.name, envelopeOpts{metadata: md})
		mustStatus(t, resp, http.StatusBadRequest, "too many metadata keys")
	})
}

func TestExpiredEnvelopeIsGone(t *testing.T) {
	server, ts := newTestRelay(t)
	alice := registerTestIdentity(t, server, ts, "alice.poweur.net")
	bob := registerTestIdentity(t, server, ts, "bob.poweur.net")

	resp, _ := postEnvelope(t, ts, bob, alice.name, envelopeOpts{
		expiresAt: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
	})
	out := mustStatus(t, resp, http.StatusGone, "expired envelope")
	if out["error"] != "message_expired" {
		t.Fatalf("expected message_expired, got %v", out)
	}

	resp, _ = postEnvelope(t, ts, bob, alice.name, envelopeOpts{
		expiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339),
	})
	mustStatus(t, resp, http.StatusAccepted, "future envelope")
}

// TestClosedInboxTypeHooks: the per-type hook table is what a closed inbox
// consults. A registered system type with no hook of its own gets the safe
// default — rejected — while the contact types keep their own behaviour.
func TestClosedInboxTypeHooks(t *testing.T) {
	server, ts := newTestRelay(t)
	alice := registerTestIdentity(t, server, ts, "alice.poweur.net")
	bob := registerTestIdentity(t, server, ts, "bob.poweur.net")
	carol := registerTestIdentity(t, server, ts, "carol.poweur.net")

	setSysFile(t, server, alice.name, ".poweur/relay/inbox-policy.json",
		`{"version":1,"mode":"contacts_and_requests"}`)
	setSysFile(t, server, alice.name, ".poweur/relay/contacts.json",
		`{"version":1,"contacts":[{"identity":"bob.poweur.net","state":"accepted"}]}`)

	// An accepted contact reaches the inbox with any type — the hooks only
	// run for senders the policy has not already settled.
	resp, _ := postEnvelope(t, ts, bob, alice.name, envelopeOpts{msgType: idpkg.MsgTypeSyncChanged})
	mustStatus(t, resp, http.StatusAccepted, "sync notice from an accepted contact")

	// A stranger's registered-but-unhooked system type falls to the default
	// hook: rejected, exactly like an ordinary message would be.
	resp, _ = postEnvelope(t, ts, carol, alice.name, envelopeOpts{msgType: idpkg.MsgTypeShareOffer})
	out := mustStatus(t, resp, http.StatusForbidden, "share offer from a stranger")
	if out["error"] != "policy_rejected" {
		t.Fatalf("expected policy_rejected, got %v", out)
	}

	// …and the contact-request hook still admits the one gesture that is
	// meant to get through a closed inbox.
	resp, _ = postEnvelope(t, ts, carol, alice.name, envelopeOpts{
		msgType: idpkg.MsgTypeContactRequest, payload: "hi, it's carol",
	})
	out = mustStatus(t, resp, http.StatusAccepted, "contact request from a stranger")
	if out["status"] != "request_queued" {
		t.Fatalf("expected request_queued, got %v", out)
	}
}

// TestHookForFallsBackAndNormalizes is the unit-level companion: an absent
// type resolves as `chat.text`, and anything without an entry gets the
// closed-inbox default rather than an accidental allow.
func TestHookForFallsBackAndNormalizes(t *testing.T) {
	ctx := closedInboxCtx{mode: idpkg.InboxContactsAndRequests}
	for _, msgType := range []string{"", "chat.text", "chat.attachment", idpkg.MsgTypeSyncChanged} {
		verdict, detail := hookFor(msgType)(ctx)
		if verdict != policyReject {
			t.Fatalf("type %q: verdict %v, want reject", msgType, verdict)
		}
		if detail != rejectedDetail {
			t.Fatalf("type %q: detail %q, want the generic rejection", msgType, detail)
		}
	}
	if verdict, _ := hookFor(idpkg.MsgTypeContactRequest)(ctx); verdict != policyQueueRequest {
		t.Fatalf("contact request under contacts_and_requests: verdict %v", verdict)
	}
	// contacts_only takes no requests at all.
	if verdict, _ := hookFor(idpkg.MsgTypeContactRequest)(closedInboxCtx{mode: idpkg.InboxContactsOnly}); verdict != policyReject {
		t.Fatalf("contact request under contacts_only must be rejected, got %v", verdict)
	}
	offer := Message{
		Type: idpkg.MsgTypeShareOffer, Payload: "cipher", Metadata: map[string]string{"share_id": "shr_1"},
		ExpiresAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
	}
	if verdict, _ := hookFor(idpkg.MsgTypeShareOffer)(closedInboxCtx{mode: idpkg.InboxContactsAndRequests, msg: offer}); verdict != policyQueueRequest {
		t.Fatalf("bounded share offer under contacts_and_requests: verdict %v", verdict)
	}
	if verdict, _ := hookFor(idpkg.MsgTypeShareOffer)(closedInboxCtx{mode: idpkg.InboxContactsOnly, msg: offer}); verdict != policyReject {
		t.Fatalf("share offer under contacts_only must be rejected, got %v", verdict)
	}
	claim := offer
	claim.Type = idpkg.MsgTypeShareClaim
	if verdict, _ := hookFor(idpkg.MsgTypeShareClaim)(closedInboxCtx{mode: idpkg.InboxContactsAndRequests, msg: claim}); verdict != policyQueueRequest {
		t.Fatalf("bounded share claim under contacts_and_requests: verdict %v", verdict)
	}
	if verdict, _ := hookFor(idpkg.MsgTypeShareClaim)(closedInboxCtx{mode: idpkg.InboxContactsOnly, msg: claim}); verdict != policyReject {
		t.Fatalf("share claim under contacts_only must be rejected, got %v", verdict)
	}
}

func TestPolicyQueuesBoundedShareOffersByGrant(t *testing.T) {
	server, ts := newTestRelay(t)
	alice := registerTestIdentity(t, server, ts, "alice.poweur.net")
	bob := registerTestIdentity(t, server, ts, "bob.poweur.net")
	setSysFile(t, server, alice.name, ".poweur/relay/inbox-policy.json",
		`{"version":1,"mode":"contacts_and_requests"}`)

	expires := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	for _, shareID := range []string{"shr_one", "shr_two"} {
		resp, _ := postEnvelope(t, ts, bob, alice.name, envelopeOpts{
			msgType: idpkg.MsgTypeShareOffer, expiresAt: expires,
			metadata: map[string]string{"share_id": shareID},
		})
		out := mustStatus(t, resp, http.StatusAccepted, "share offer")
		if out["status"] != "request_queued" {
			t.Fatalf("share offer outcome: %v", out)
		}
	}
	queued := challengeSigned(t, ts, alice, "/requests/"+alice.name)
	requests, _ := queued["requests"].([]any)
	if len(requests) != 2 {
		t.Fatalf("independent grant offers must have independent slots: %v", queued)
	}

	resp, _ := postEnvelope(t, ts, bob, alice.name, envelopeOpts{
		msgType:  idpkg.MsgTypeShareOffer,
		metadata: map[string]string{"share_id": "shr_no_expiry"},
	})
	mustStatus(t, resp, http.StatusForbidden, "non-expiring share offer")
}

