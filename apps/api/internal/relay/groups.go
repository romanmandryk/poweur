package relay

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/poweur/api/internal/crypto"
	"github.com/poweur/api/internal/files"
	idpkg "github.com/poweur/identity"
)

// Group messaging at the relay (EPIC-009 E09-T5).
//
// v1 is server fan-out with per-member encryption. The sender's client seals
// the payload once per member and posts all of those envelopes here in one
// request; this relay — the one hosting the group identity — expands the
// membership and delivers each envelope to the member it names, locally or
// over the existing forward path.
//
// The relay adds no cryptography. Each envelope is an ordinary 1:1 envelope,
// signed over the same canonical string a direct message signs, and is
// verified here exactly as handleMessagesPost verifies one. What this file
// adds is *expansion*: the one thing a group needs that a client cannot do
// safely on its own, because only the group's relay can say who is in the
// group right now.
//
// See apps/docs/docs/protocol/group-messaging.md for the design and
// apps/docs/docs/future/mls-adoption.md for what would replace it.

const (
	// maxGroupBatchBytes caps one fan-out request body.
	//
	// The 512 KB envelope cap is untouched and still applies to every
	// envelope individually — it bounds what lands in any one inbox. A batch
	// is a transport container for up to MaxGroupFanoutMembers of them, so
	// it gets its own, larger cap. The practical consequence, stated in the
	// spec: a group message is bounded by payload x members as well as by
	// payload alone.
	maxGroupBatchBytes = 4 * 1024 * 1024
)

// GroupFanoutRequest is the body of POST /groups/{group}/messages.
//
// Epoch is a precondition rather than a hint: a batch built against a
// membership that has since moved is refused, not delivered to whoever used
// to be in the group.
type GroupFanoutRequest struct {
	Group     string    `json:"group"`
	Epoch     int       `json:"epoch"`
	Envelopes []Message `json:"envelopes"`
}

// GroupDelivery is one member's outcome in a fan-out response.
type GroupDelivery struct {
	Recipient string `json:"recipient"`
	ID        string `json:"id"`
	Status    string `json:"status"`
	Detail    string `json:"detail,omitempty"`
}

// GroupFanoutResponse reports per-member outcomes.
//
// There is no single verdict on purpose. A fan-out that reached three
// members and missed one is not a failure and not a success; per-member
// ticks are the only honest answer, and they are what the sender's client
// rolls up into "3 / 4 delivered". See the ack semantics in the spec.
type GroupFanoutResponse struct {
	Group     string          `json:"group"`
	Epoch     int             `json:"epoch"`
	Delivered []GroupDelivery `json:"delivered"`
	Failed    []GroupDelivery `json:"failed"`
}

// groupMembership resolves the group named in the path, or writes the
// response and returns ok=false.
//
// Every unresolvable case answers 404 with the same words. A relay that
// distinguished "no such group" from "not a group identity" from "hosted
// elsewhere" would answer membership questions it has no business
// answering — the enumeration oracle that cross-relay group resolution was
// deferred to avoid.
func (s *Server) groupMembership(w http.ResponseWriter, r *http.Request, name string) (idpkg.ShareGroup, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		writeError(w, http.StatusBadRequest, "invalid_group", "missing group")
		return idpkg.ShareGroup{}, false
	}
	if s.grants == nil {
		// No file layer, so no group document can exist here. This is a
		// deployment fact, not a statement about the group.
		writeError(w, http.StatusServiceUnavailable, "unavailable", "this relay has no file layer and cannot host groups")
		return idpkg.ShareGroup{}, false
	}
	group, err := s.grants.GroupIdentity(r.Context(), name)
	if err != nil {
		if !errors.Is(err, files.ErrGroupNotResolvable) {
			writeError(w, http.StatusInternalServerError, "group_error", "failed to read group membership")
			return idpkg.ShareGroup{}, false
		}
		writeError(w, http.StatusNotFound, "not_found", "no group identity here")
		return idpkg.ShareGroup{}, false
	}
	return group, true
}

// handleGroupGet serves the signed membership document to someone in the
// group (E09-T5).
//
// A sender needs the roster to encrypt per member, so this endpoint exists;
// it hands back the document **as signed** rather than a rendering of it, so
// the caller verifies the group's own signature instead of trusting this
// relay's summary of it. Member encryption keys are deliberately absent: the
// client resolves those the normal way, so a group message is sealed to keys
// the sender resolved and pinned, not to keys the group's relay supplied.
//
// Authentication is the challenge-signed header set used for an inbox
// pickup, with one difference — the caller need not be hosted here. A member
// on another relay must be able to read the roster of a group they are in,
// and their key resolves over the ordinary web-first path.
func (s *Server) handleGroupGet(w http.ResponseWriter, r *http.Request) {
	name := strings.ToLower(strings.TrimSpace(r.PathValue("group")))
	caller, ok := s.authorizeGroupReader(w, r)
	if !ok {
		return
	}
	group, ok := s.groupMembership(w, r, name)
	if !ok {
		return
	}
	// Members and admins may both read: a member needs the roster to send,
	// and an admin needs it to administer a group they may not be in. This
	// is the one place the two lists are deliberately unioned, and it is
	// about reading the document, never about who receives a message.
	if !group.HasMember(caller) && !group.HasAdmin(caller) {
		// Same answer as "no such group": otherwise this endpoint tells a
		// stranger that a group exists and that they are not in it, which is
		// half of a membership oracle.
		writeError(w, http.StatusNotFound, "not_found", "no group identity here")
		return
	}
	requestAction(r, "group.read")
	writeJSON(w, http.StatusOK, group)
}

// authorizeGroupReader authenticates the caller of a group read.
//
// It is deliberately not authorizeInboxRead: that helper requires the
// identity to be hosted locally (it is proving ownership of a local inbox),
// and a group's members are routinely somewhere else. Here the identity's
// key is resolved the same way a message sender's is — locally if hosted,
// otherwise over web-first resolution — and the proof is the same one-shot
// challenge signature.
func (s *Server) authorizeGroupReader(w http.ResponseWriter, r *http.Request) (string, bool) {
	caller := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Poweur-Identity")))
	if caller == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "identity header missing")
		return "", false
	}
	signature := r.Header.Get("X-Poweur-Signature")
	if signature == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "signature header missing")
		return "", false
	}
	challenge, ok := s.consumeChallenge(caller, r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "challenge missing or expired")
		return "", false
	}
	publicKey, source, err := s.resolveSigningKey(r.Context(), caller, r.Header.Get("X-Poweur-Session-Id"), nil)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", err.Error())
		return "", false
	}
	if err := crypto.VerifySignature(publicKey, challenge.Value, signature); err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "challenge signature invalid (key source: "+source+")")
		return "", false
	}
	verifiedActor(r, caller)
	return caller, true
}

// handleGroupMessagesPost fans one message out to a group's members.
//
// The at-least-one-local rule is satisfied by the **group**, not by the
// sender: this relay accepts the batch because it hosts the group whose
// membership it is about to expand. The sender may be a member on any relay,
// and each outbound envelope then travels the ordinary path — local inbox,
// or forward to the member's own relay.
func (s *Server) handleGroupMessagesPost(w http.ResponseWriter, r *http.Request) {
	name := strings.ToLower(strings.TrimSpace(r.PathValue("group")))
	var req GroupFanoutRequest
	if !decodeGroupBatch(w, r, &req) {
		return
	}
	if req.Group != "" && !strings.EqualFold(strings.TrimSpace(req.Group), name) {
		writeError(w, http.StatusBadRequest, "invalid_group_message",
			"body names a different group than the path")
		return
	}
	if len(req.Envelopes) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_group_message", "envelopes must not be empty")
		return
	}
	group, ok := s.groupMembership(w, r, name)
	if !ok {
		return
	}
	if len(group.Members) > idpkg.MaxGroupFanoutMembers {
		// The cap is the design's revisit threshold, enforced so it cannot
		// rot into "whatever the timeouts allow". Past it the answer is MLS,
		// not a bigger loop — apps/docs/docs/future/mls-adoption.md.
		writeError(w, http.StatusRequestEntityTooLarge, "group_too_large",
			"v1 fan-out is limited to 100 members; larger groups need the v2 group protocol")
		return
	}
	// Epoch first: a batch built against a membership that has since moved
	// must be rebuilt, not delivered to whoever used to be in the group.
	// The current document goes back so the client can re-encrypt without a
	// second round trip.
	if req.Epoch != group.Epoch {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":  "group_epoch_stale",
			"detail": "the group's membership has changed; re-encrypt against the epoch in this response",
			"group":  group,
		})
		return
	}

	sender := strings.ToLower(strings.TrimSpace(req.Envelopes[0].Sender))
	if !group.HasMember(sender) {
		// `members` is the access list. An admin who is not a member
		// administers the roster and has no seat in the conversation.
		writeError(w, http.StatusForbidden, "not_a_member",
			"only members of this group may send to it")
		return
	}
	threadID := req.Envelopes[0].ThreadID
	if err := idpkg.ValidateGroupThreadID(name, threadID); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_group_message", err.Error())
		return
	}

	// The batch must cover the membership exactly: no member left out, and
	// nobody added who is not in the group. A relay that accepted a subset
	// would let a sender quietly exclude someone from a conversation they
	// are in, and one that accepted extras would let a member use the group
	// as a signed cover for messaging a stranger.
	want := idpkg.GroupFanoutRecipients(group, sender)
	if err := checkFanoutCoverage(want, req.Envelopes); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_group_message", err.Error())
		return
	}

	// Verify every envelope before delivering any of them. A batch that is
	// half-signed is a batch nobody sent.
	for i := range req.Envelopes {
		if !s.validateGroupEnvelope(w, r, group, req.Envelopes[i], req.Envelopes[0]) {
			return
		}
	}

	// Rate limit once per fan-out, charged to the verified sender. Charging
	// per envelope would make a large group unusable for its own members;
	// the real bound on amplification is MaxGroupFanoutMembers above.
	decision := s.rateLimit.Allow(sender)
	if !decision.Allowed {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error":    "rate_limit_exceeded",
			"scope":    decision.Scope,
			"window":   decision.Window,
			"limit":    decision.Limit,
			"reset_at": decision.ResetAt.UTC().Format(time.RFC3339),
		})
		return
	}
	verifiedActor(r, sender)
	requestAction(r, "group.fanout")

	resp := GroupFanoutResponse{
		Group:     group.Group,
		Epoch:     group.Epoch,
		Delivered: []GroupDelivery{},
		Failed:    []GroupDelivery{},
	}
	for _, msg := range req.Envelopes {
		out := s.deliverGroupEnvelope(r.Context(), group, msg)
		if out.Status == "accepted" {
			resp.Delivered = append(resp.Delivered, out)
		} else {
			resp.Failed = append(resp.Failed, out)
		}
	}
	s.event(r.Context(), "group.fanout", "success")
	writeJSON(w, http.StatusAccepted, resp)
}

// deliverGroupEnvelope delivers one member's envelope and reports the
// outcome. A member this relay hosts is spooled directly; anyone else is
// forwarded over the existing privacy-proxy path.
//
// One member's failure never fails the others. Retry is the sender's
// business, exactly as it is for a direct send.
func (s *Server) deliverGroupEnvelope(ctx context.Context, group idpkg.ShareGroup, msg Message) GroupDelivery {
	out := GroupDelivery{Recipient: msg.Recipient, ID: msg.ID}
	if s.isLocalIdentity(ctx, msg.Recipient) {
		// Inbox policy is skipped for a member of a group this relay hosts
		// and has just verified: joining a group is the consent a closed
		// inbox is asking about, and requiring every member to also be a
		// contact of every other member would make groups useless for the
		// case they exist for. This relay can only make that call because it
		// verified the membership document itself, moments ago.
		if !s.inbox.Add(msg.Recipient, storedFromMessage(msg), s.cfg.MaxInboxPerIdentity) {
			out.Status = "inbox_full"
			out.Detail = "recipient inbox is full"
			return out
		}
		s.event(ctx, "message.enqueue", "success")
		s.notify(msg.Recipient, "message", msg.ID)
		out.Status = "accepted"
		return out
	}
	if err := s.forwardMessage(ctx, msg); err != nil {
		out.Status = "forward_failed"
		out.Detail = err.Error()
		return out
	}
	out.Status = "accepted"
	return out
}

// validateGroupEnvelope applies every check handleMessagesPost applies to a
// direct message, plus the ones that make a batch one message rather than N
// unrelated ones.
//
// first is the envelope the batch's shared fields are taken from; every
// envelope must agree with it on sender, timestamp, type, thread, expiry and
// metadata. Only id, recipient, payload and encryption may differ — that is
// what "one message, sealed N times" means, and without it a batch could
// carry a different type or thread to each member under one authorization.
func (s *Server) validateGroupEnvelope(w http.ResponseWriter, r *http.Request, group idpkg.ShareGroup, msg, first Message) bool {
	if msg.ID == "" || msg.Sender == "" || msg.Recipient == "" || msg.Timestamp == "" || msg.Payload == "" || msg.Signature == "" {
		writeError(w, http.StatusBadRequest, "invalid_message",
			"missing required message fields (id, sender, recipient, timestamp, payload, signature)")
		return false
	}
	if _, err := time.Parse(time.RFC3339, msg.Timestamp); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_message", "timestamp must be RFC3339")
		return false
	}
	if msg.Encryption == nil || msg.Encryption.Alg == "" ||
		msg.Encryption.EphemeralPublicKey == "" || msg.Encryption.Nonce == "" {
		writeError(w, http.StatusBadRequest, "encryption_required",
			"messages must be end-to-end encrypted (alg, ephemeral_public_key, nonce required)")
		return false
	}
	if !validateEnvelopeExtensions(w, msg) {
		return false
	}
	// The 512 KB envelope cap is per envelope, not per batch: it bounds what
	// lands in one inbox, and that is unchanged by a message being addressed
	// to a group.
	if encoded, err := json.Marshal(msg); err != nil || len(encoded) > maxMessageBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "content_too_large",
			"an envelope exceeds the 512 KB message limit")
		return false
	}
	if !strings.EqualFold(msg.Sender, first.Sender) {
		writeError(w, http.StatusBadRequest, "invalid_group_message", "every envelope in a batch must have the same sender")
		return false
	}
	if msg.Timestamp != first.Timestamp || msg.Type != first.Type || msg.ThreadID != first.ThreadID || msg.ExpiresAt != first.ExpiresAt {
		writeError(w, http.StatusBadRequest, "invalid_group_message",
			"every envelope in a batch must share the same timestamp, type, thread_id and expires_at")
		return false
	}
	if !sameMetadata(msg.Metadata, first.Metadata) {
		writeError(w, http.StatusBadRequest, "invalid_group_message",
			"every envelope in a batch must carry the same metadata")
		return false
	}
	if err := idpkg.ValidateGroupMessageMetadata(group.Group, group.Epoch, msg.Metadata); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_group_message", err.Error())
		return false
	}

	publicKey, source, err := s.resolveSigningKey(r.Context(), msg.Sender, msg.SessionID, msg.SessionProof)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", err.Error())
		return false
	}
	encMeta := &crypto.EncryptionMeta{
		Alg:                msg.Encryption.Alg,
		EphemeralPublicKey: msg.Encryption.EphemeralPublicKey,
		Nonce:              msg.Encryption.Nonce,
	}
	canonical := crypto.CanonicalMessageEnvelope(
		msg.Sender, msg.Recipient, msg.Timestamp, msg.Payload,
		msg.ID, msg.SessionID, msg.Type, msg.ThreadID, msg.ExpiresAt, msg.Metadata, encMeta)
	if err := crypto.VerifySignature(publicKey, canonical, msg.Signature); err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized",
			"signature verification failed for the envelope addressed to "+msg.Recipient+" (key source: "+source+")")
		return false
	}
	if !refuseExpiredEnvelope(w, msg) {
		return false
	}
	return true
}

// checkFanoutCoverage asserts the batch addresses exactly the membership.
func checkFanoutCoverage(want []string, envelopes []Message) error {
	got := make(map[string]bool, len(envelopes))
	for _, msg := range envelopes {
		recipient := strings.ToLower(strings.TrimSpace(msg.Recipient))
		if got[recipient] {
			return errors.New("batch addresses " + recipient + " twice")
		}
		got[recipient] = true
	}
	for _, member := range want {
		if !got[member] {
			return errors.New("batch does not cover group member " + member)
		}
		delete(got, member)
	}
	for extra := range got {
		return errors.New(extra + " is not a member of this group")
	}
	return nil
}

// sameMetadata compares two metadata maps by value. Both may be nil.
func sameMetadata(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if other, ok := b[k]; !ok || other != v {
			return false
		}
	}
	return true
}

// decodeGroupBatch reads a fan-out body under the batch cap.
//
// It exists rather than reusing decodeJSON because decodeJSON enforces the
// 512 KB *envelope* cap on the whole body, which is the right limit for one
// message and the wrong one for a container of up to 100 of them.
func decodeGroupBatch(w http.ResponseWriter, r *http.Request, target *GroupFanoutRequest) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxGroupBatchBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "content_too_large", "group fan-out exceeds max size")
		return false
	}
	if err := json.Unmarshal(body, target); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "failed to parse JSON body")
		return false
	}
	return true
}

// refuseForgedLocalGroup keeps the direct-send path out of a locally hosted
// group's conversations.
//
// A message claiming `metadata.group` for a group **this relay hosts** must
// come through the fan-out endpoint, where membership and epoch are checked.
// Otherwise a stranger could sign an ordinary message naming the group and
// have every recipient's client file it into that conversation.
//
// The check is deliberately limited to local groups. For a group hosted
// elsewhere this relay cannot resolve the membership at all (cross-relay
// group resolution is deferred), so refusing on the strength of a claim it
// cannot check would break legitimate forwarded fan-outs — which is exactly
// how a remote group's messages arrive. For those, the metadata is a hint
// and the recipient's *client* verifies it against the roster it can read as
// a member. That split is stated in the spec.
func (s *Server) refuseForgedLocalGroup(w http.ResponseWriter, r *http.Request, msg Message) bool {
	group, ok := idpkg.GroupOfMessage(msg.Metadata)
	if !ok || s.grants == nil {
		return true
	}
	if _, err := s.grants.GroupIdentity(r.Context(), group); err != nil {
		return true // not a group this relay can speak for
	}
	writeError(w, http.StatusBadRequest, "invalid_message",
		"messages to "+group+" must be sent through POST /groups/"+group+"/messages")
	return false
}
