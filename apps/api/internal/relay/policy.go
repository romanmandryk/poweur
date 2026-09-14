package relay

import (
	"context"
	"crypto/ed25519"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/poweur/api/internal/crypto"
	"github.com/poweur/api/internal/files"
	"github.com/poweur/api/internal/ratelimit"
	"github.com/poweur/api/internal/storage"
	idpkg "github.com/poweur/identity"
)

// Inbox policy enforcement (EPIC-007 E07-T2). Evaluated by the RECIPIENT's
// relay after signature verification — which is also where cross-relay
// forwarded traffic arrives (forwardMessage POSTs to the recipient relay),
// so the enforcement point covers both local and forwarded senders.

// requestCooldown is how long a drained-but-unaccepted contact request
// blocks a re-request from the same sender.
const requestCooldown = 7 * 24 * time.Hour

// maxContactRequestPayload caps the encrypted intro of a contact request
// (spec: short intro; base64 + envelope overhead allowed for).
const maxContactRequestPayload = 4096

// Policy verdicts.
type policyVerdict int

const (
	policyAllow policyVerdict = iota
	policyReject
	policyQueueRequest
)

// readSysJSON reads a relay-readable system document for identity (nil when
// absent or unreadable — absence must fail open per document defaults).
func (s *Server) readSysJSON(ctx context.Context, identity, treePath string) []byte {
	if s.filesProvider == nil {
		return nil
	}
	f, err := s.filesProvider.OpenFile(ctx, identity, treePath, os.O_RDONLY, 0)
	if err != nil {
		return nil
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxSysDocBytes))
	if err != nil {
		return nil
	}
	return raw
}

// recipientPolicy loads the recipient's inbox policy (default open) and
// contacts (default empty).
func (s *Server) recipientPolicy(ctx context.Context, recipient string) (idpkg.InboxPolicy, idpkg.ContactsFile) {
	policy := idpkg.InboxPolicy{Mode: idpkg.DefaultInboxMode}
	if raw := s.readSysJSON(ctx, recipient, files.SysRelay+"/inbox-policy.json"); raw != nil {
		if p, err := idpkg.ParseInboxPolicy(raw); err == nil {
			policy = p
		}
	}
	var contacts idpkg.ContactsFile
	if raw := s.readSysJSON(ctx, recipient, files.SysRelay+"/contacts.json"); raw != nil {
		if c, err := idpkg.ParseContactsFile(raw); err == nil {
			contacts = c
		}
	}
	return policy, contacts
}

// evaluateInboxPolicy decides what happens to a verified inbound message for
// a locally hosted recipient. detail is the client-facing explanation on
// reject. Blocked senders receive the same generic rejection as strangers so
// a block is not distinguishable from a closed inbox.
func (s *Server) evaluateInboxPolicy(ctx context.Context, msg Message) (verdict policyVerdict, detail string) {
	if strings.EqualFold(msg.Sender, msg.Recipient) {
		return policyAllow, "" // own devices always reach the own inbox
	}
	policy, contacts := s.recipientPolicy(ctx, msg.Recipient)
	contact, known := contacts.Find(msg.Sender)

	if known && contact.State == idpkg.ContactBlocked {
		return policyReject, rejectedDetail
	}

	hookCtx := closedInboxCtx{
		msg:          msg,
		mode:         policy.Mode,
		contact:      contact,
		knownContact: known,
	}

	// Contact request/accept is a consent handshake, not chat. It consults
	// the type hooks in every inbox mode — including `open`, and even when
	// the sender is already an accepted contact — so a knock is always a
	// knock the recipient can answer, never a buried inbox line.
	if isContactHandshakeType(msg.Type) {
		return hookFor(msg.Type)(hookCtx)
	}

	if known && contact.State == idpkg.ContactAccepted {
		return policyAllow, ""
	}

	// Non-contact (or pending request state) from here on.
	switch policy.Mode {
	case idpkg.InboxOpen:
		return policyAllow, ""
	case idpkg.InboxContactsOnly, idpkg.InboxContactsAndRequests:
		// A closed inbox is where the message *type* starts to matter, so
		// the decision moves to the per-type hooks in typed.go. Everything
		// without a hook is rejected — the default a closed inbox means.
		return hookFor(msg.Type)(hookCtx)
	default:
		return policyAllow, ""
	}
}

// senderRelayKey names the relay accountable for a sender, for metering
// (E07-T5). Returns "" when the sender is one of ours.
//
// Local senders are deliberately not metered here. This relay already knows
// exactly who they are, meters them per identity, and gates their creation
// (invite codes or proof-of-work, EPIC-014) — it has levers over its own
// users. Against a *peer* relay it has none of that, which is precisely why
// the peer, and not the identity behind it, is the unit that gets a budget.
//
// The key is the host that serves the sender's identity — the same lookup
// (and the same DNS-TTL cache) the forwarding path already uses to find a
// recipient's relay, so metering costs one map read in the common case. When
// that lookup fails, the sender's parent domain stands in: everyone on a
// hosted domain still shares one bucket, which is the property that matters.
func (s *Server) senderRelayKey(ctx context.Context, sender string) string {
	sender = strings.ToLower(strings.TrimSpace(sender))
	if sender == "" || s.isLocalIdentity(ctx, sender) {
		return ""
	}
	if host, err := s.resolveRelayHost(ctx, sender); err == nil && host != "" {
		return strings.ToLower(strings.TrimSuffix(host, "."))
	}
	if i := strings.Index(sender, "."); i >= 0 && i+1 < len(sender) {
		return sender[i+1:]
	}
	return sender
}

// meterRequestRelay charges one requests-queue attempt to the sender's relay.
// ok=false means the caller must reject with the returned decision.
//
// The attempt is charged, not the admission: a meter that only counted the
// requests it let through would be bypassed by sending requests designed to
// fail (a duplicate, a sender in cooldown) — the relay does the resolution
// work either way, which is the work being rationed.
func (s *Server) meterRequestRelay(ctx context.Context, sender string) (ratelimit.Decision, bool) {
	decision := s.requestRelayLimit.Allow(s.senderRelayKey(ctx, sender))
	return decision, decision.Allowed
}

// storedFromMessage converts the wire envelope for queue storage.
func storedFromMessage(msg Message) storage.StoredMessage {
	stored := storage.StoredMessage{
		ID:        msg.ID,
		Sender:    msg.Sender,
		Recipient: msg.Recipient,
		Timestamp: msg.Timestamp,
		Payload:   msg.Payload,
		Signature: msg.Signature,
		Type:      msg.Type,
		ThreadID:  msg.ThreadID,
		ExpiresAt: msg.ExpiresAt,
		Metadata:  msg.Metadata,
		SessionID: msg.SessionID,
	}
	if msg.Encryption != nil {
		stored.Encryption = &storage.StoredEncryptionMeta{
			Alg:                msg.Encryption.Alg,
			EphemeralPublicKey: msg.Encryption.EphemeralPublicKey,
			Nonce:              msg.Encryption.Nonce,
		}
	}
	return stored
}

// authChallengeSignedGet authenticates an owner-drain GET (inbox /
// requests / anon queues): the caller proves control of {identity} by
// signing a previously issued challenge with their session or identity
// key. Returns ok=false with the response already written on failure.
func (s *Server) authChallengeSignedGet(w http.ResponseWriter, r *http.Request) (string, bool) {
	identity := r.PathValue("identity")
	if identity == "" {
		writeError(w, http.StatusBadRequest, "invalid_identity", "missing identity")
		return "", false
	}
	headerIdentity := r.Header.Get("X-Poweur-Identity")
	if headerIdentity == "" || headerIdentity != identity {
		writeError(w, http.StatusUnauthorized, "unauthorized", "identity header missing or mismatch")
		return "", false
	}
	signature := r.Header.Get("X-Poweur-Signature")
	if signature == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "signature header missing")
		return "", false
	}
	sessionID := r.Header.Get("X-Poweur-Session-Id")

	challenge, ok := s.consumeChallenge(identity, r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "challenge missing or expired")
		return "", false
	}

	var publicKey ed25519.PublicKey
	if sessionID != "" {
		session, ok := s.sessions.Get(sessionID)
		if !ok {
			writeError(w, http.StatusUnauthorized, "session_expired", "session expired or not found")
			return "", false
		}
		if session.Identity != identity {
			writeError(w, http.StatusUnauthorized, "unauthorized", "session does not belong to identity")
			return "", false
		}
		publicKey = session.PublicKeyBytes
	} else {
		if !s.isLocalIdentity(r.Context(), identity) {
			writeError(w, http.StatusNotFound, "not_found", "identity not hosted on this relay")
			return "", false
		}
		pub, err := s.resolveIdentityPublicKey(r.Context(), identity)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "cannot resolve identity public key: "+err.Error())
			return "", false
		}
		s.warmIdentityCache(identity, pub)
		publicKey = pub
	}

	if err := crypto.VerifySignature(publicKey, challenge.Value, signature); err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "challenge signature invalid")
		return "", false
	}
	return identity, true
}

// handleRequestsGet drains the recipient's pending contact-request queue.
func (s *Server) handleRequestsGet(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.authChallengeSignedGet(w, r)
	if !ok {
		return
	}
	requests := s.requests.Drain(identity)
	if requests == nil {
		requests = []storage.StoredMessage{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": requests})
}
