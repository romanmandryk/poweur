package cli

import (
	"fmt"
	"strings"

	idpkg "github.com/poweur/identity"
)

// Typed messages & threads in the CLI (EPIC-009 E09-T3).
//
// The CLI's job here is small: turn `--thread`, `--expires` and repeated
// `--meta k=v` flags into the envelope fields, refuse a malformed one before
// anything is signed, and render an inbound message according to its type.
// The rules themselves live in `packages/identity/msgtypes.go` — this file
// must not grow a second opinion about them.

// metaFlag collects repeated `--meta key=value` flags into the envelope's
// metadata map. It implements flag.Value.
//
// Rejecting a duplicate key matters: a map silently keeps the last write, and
// a caller who typed `--meta mime=image/png --meta mime=image/jpeg` meant one
// of them, so guessing is worse than asking.
type metaFlag struct{ values map[string]string }

func (m *metaFlag) String() string {
	if m == nil || len(m.values) == 0 {
		return ""
	}
	return strings.Join(idpkg.MetadataLines(m.values), " ")
}

func (m *metaFlag) Set(raw string) error {
	key, value, ok := strings.Cut(raw, "=")
	if !ok {
		return fmt.Errorf("--meta expects key=value, got %q", raw)
	}
	key = strings.TrimSpace(key)
	if m.values == nil {
		m.values = map[string]string{}
	}
	if _, dup := m.values[key]; dup {
		return fmt.Errorf("--meta %s given twice", key)
	}
	m.values[key] = value
	return nil
}

// Map returns the collected metadata, or nil when none was given — nil is
// what keeps the field off the wire and off the canonical string.
func (m *metaFlag) Map() map[string]string {
	if m == nil || len(m.values) == 0 {
		return nil
	}
	return m.values
}

// textRenderedTypes are the types whose payload this client knows is prose
// meant for a human to read.
//
// `chat.text` is the obvious one. `sys.contact.request` is the other: its
// payload is the intro a stranger wrote to introduce themselves, and hiding
// it behind a generic line would hide the only thing that helps the
// recipient decide. Every other type — an application's own, a `sys.*`
// notification whose payload is machine-readable — gets the fallback.
var textRenderedTypes = map[string]bool{
	idpkg.MsgTypeChatText:       true,
	idpkg.MsgTypeContactRequest: true,
}

// describeTypedMessage renders one inbound message body for a human.
//
// A type this client implements shows its text as it always has. Anything
// else gets a generic line naming the sender and the type, because the CLI
// has no idea how to present an application's payload and pretending
// otherwise would show a user a blob of someone else's JSON.
//
// `decrypted` matters: a message we could not open has nothing to render
// whatever its type says, so the decrypt-failure text wins.
func describeTypedMessage(sender, msgType, body string, decrypted bool) string {
	if !decrypted {
		return body
	}
	normalized := idpkg.NormalizeMessageType(msgType)
	if textRenderedTypes[normalized] {
		return body
	}
	return fmt.Sprintf("app message from %s (%s)", sender, normalized)
}

// threadSuffix renders the thread marker appended to an inbox line. Empty for
// an unthreaded message, so ordinary chat looks exactly as it did.
func threadSuffix(threadID string) string {
	if threadID == "" {
		return ""
	}
	return " [thread " + threadID + "]"
}

// validateOutgoingEnvelope is the sender-side gate. The relay checks these
// too, but failing here means the user gets the error before a message is
// encrypted, signed, journalled and posted — and before a `sys.*` typo has
// been recorded as a send attempt.
func validateOutgoingEnvelope(msgType, threadID, expiresAt string, metadata map[string]string) error {
	if err := idpkg.ValidateEnvelopeExtensions(msgType, threadID, expiresAt, metadata); err != nil {
		return err
	}
	// The relay refuses an unregistered `sys.*` at ingress; catching it here
	// turns a 400 from a remote relay into a local error message that can
	// name the alternatives.
	if idpkg.IsSystemType(msgType) && !idpkg.IsKnownSystemType(msgType) {
		return fmt.Errorf("%q is not a known system message type (sys.* is reserved; known: %s)",
			msgType, strings.Join(idpkg.SystemMessageTypes(), ", "))
	}
	return nil
}
