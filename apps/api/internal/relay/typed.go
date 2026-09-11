package relay

import (
	"net/http"

	idpkg "github.com/poweur/identity"
)

// Typed messages & threads at the relay (EPIC-009 E09-T3).
//
// Two jobs live here, and they are deliberately the *only* two the relay does
// with a message type:
//
//  1. Ingress validation — the four plaintext envelope extensions are checked
//     for shape, and the reserved `sys.*` namespace is defended.
//  2. Per-type inbox policy — the hooks a closed inbox consults to decide
//     whether an envelope of some particular type may pass.
//
// Everything else is opaque. A `chat.text`, a `chat.attachment`, an
// application's own `net.example.thing` — the relay stores and forwards them
// identically and has no opinion about their contents. That is the property
// that lets EPIC-010 automations and third-party apps ship without a relay
// release, and it is why the type set below is small and closed rather than a
// growing switch.

// validateEnvelopeExtensions checks type/thread_id/expires_at/metadata and
// defends the reserved namespace. Returns ok=false with the response already
// written.
//
// Validation runs **before** signature verification on purpose: these are
// cheap syntactic checks on plaintext fields, and an envelope whose metadata
// carries a newline has an ambiguous signing input — there is nothing to
// verify against.
func validateEnvelopeExtensions(w http.ResponseWriter, msg Message) bool {
	if err := idpkg.ValidateEnvelopeExtensions(msg.Type, msg.ThreadID, msg.ExpiresAt, msg.Metadata); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_message", err.Error())
		return false
	}
	// `sys.*` belongs to the platform. Letting an application invent one
	// would let it borrow the relay's own routing authority — a message that
	// claims to be `sys.contact.request` gets treated as a consent gesture by
	// every client that sees it. An unregistered one is refused rather than
	// forwarded, so the namespace means something.
	if idpkg.IsSystemType(msg.Type) && !idpkg.IsKnownSystemType(msg.Type) {
		writeError(w, http.StatusBadRequest, "unsupported_type",
			"sys.* is reserved for platform message types; "+msg.Type+" is not one this relay knows")
		return false
	}
	return true
}

// closedInboxCtx is everything a per-type hook may decide with. It is passed
// by value: a hook inspects, it does not mutate delivery state.
type closedInboxCtx struct {
	msg Message
	// mode is the recipient's inbox policy mode — a hook usually differs
	// between contacts_only and contacts_and_requests.
	mode string
	// contact is the recipient's entry for the sender, if any. Blocked and
	// accepted senders never reach a hook: evaluateInboxPolicy settles those
	// before the type matters.
	contact      idpkg.Contact
	knownContact bool
}

// answersOurRequest reports whether this envelope is the reply to a contact
// request the recipient themselves sent.
func (c closedInboxCtx) answersOurRequest() bool {
	return c.knownContact && c.contact.State == idpkg.ContactRequested
}

// inboxTypeHook decides the fate of one message type for a recipient whose
// inbox is closed to this sender. The detail string is shown to the client on
// a reject.
type inboxTypeHook func(closedInboxCtx) (policyVerdict, string)

// rejectedDetail is the single answer a closed inbox gives, whatever the
// reason. A blocked sender and a stranger get the same words on purpose: a
// distinguishable rejection turns "am I blocked?" into a probe.
const rejectedDetail = "recipient does not accept messages from this sender (send a contact request if their policy allows it)"

// inboxTypeHooks is the registry a closed inbox consults. EPIC-005's
// `sys.share.*` and EPIC-004's `sys.sync.changed` add entries here rather than
// growing a second switch somewhere else; a type with no entry falls to
// rejectClosedInbox, which is the safe default for anything the relay has no
// specific reason to admit.
var inboxTypeHooks = map[string]inboxTypeHook{
	idpkg.MsgTypeContactRequest: hookContactRequest,
	idpkg.MsgTypeContactAccept:  hookContactAccept,
}

// hookFor returns the hook for a type, or the closed-inbox default.
func hookFor(msgType string) inboxTypeHook {
	if hook, ok := inboxTypeHooks[idpkg.NormalizeMessageType(msgType)]; ok {
		return hook
	}
	return rejectClosedInbox
}

func rejectClosedInbox(closedInboxCtx) (policyVerdict, string) {
	return policyReject, rejectedDetail
}

// hookContactRequest: a stranger's opening gesture. Only
// `contacts_and_requests` takes them, and only into the requests queue —
// never the message stream, which is the difference between "someone asked"
// and "someone messaged you".
func hookContactRequest(c closedInboxCtx) (policyVerdict, string) {
	if c.mode != idpkg.InboxContactsAndRequests {
		return policyReject, rejectedDetail
	}
	if len(c.msg.Payload) > maxContactRequestPayload {
		return policyReject, "contact request intro too large"
	}
	return policyQueueRequest, ""
}

// hookContactAccept: the answer to a request *we* sent, admitted whenever we
// already list the sender as `requested` — but only then, and only into the
// requests queue.
//
// Without this a `contacts_only` inbox could send a contact request and never
// hear back: their accept bounces off our policy, our contacts stay
// `requested`, and our policy then bounces every message they send. Both sides
// see silence and neither can tell it apart from being ignored.
func hookContactAccept(c closedInboxCtx) (policyVerdict, string) {
	if c.answersOurRequest() {
		return policyQueueRequest, ""
	}
	return policyReject, rejectedDetail
}
