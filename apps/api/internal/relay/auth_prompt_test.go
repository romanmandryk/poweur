package relay

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/poweur/api/internal/crypto"
	idpkg "github.com/poweur/identity"
)

var authPromptSeq int

func postAuthPrompt(t *testing.T, ts *httptest.Server, from davTestIdentity, to string, expires time.Time, payload string) *http.Response {
	t.Helper()
	authPromptSeq++
	msg := Message{
		ID:        "msg_auth_" + strings.Repeat("x", authPromptSeq),
		Sender:    from.name,
		Recipient: to,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Payload:   payload,
		Type:      idpkg.MsgTypeAuthRequest,
		Encryption: &EncryptionMeta{
			Alg: "x25519-chacha20-poly1305", EphemeralPublicKey: "ephemeral-pub", Nonce: "nonce",
		},
	}
	if !expires.IsZero() {
		msg.ExpiresAt = expires.UTC().Format(time.RFC3339)
	}
	canonical := crypto.CanonicalMessageEnvelope(msg.Sender, msg.Recipient, msg.Timestamp, msg.Payload, msg.ID, "",
		msg.Type, "", msg.ExpiresAt, nil, &crypto.EncryptionMeta{
			Alg: msg.Encryption.Alg, EphemeralPublicKey: msg.Encryption.EphemeralPublicKey, Nonce: msg.Encryption.Nonce,
		})
	msg.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(from.priv, []byte(canonical)))
	body, _ := json.Marshal(msg)
	resp, err := http.Post(ts.URL+"/messages", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// EPIC-022 E22-T7: sign-in prompts come only from services the recipient
// trusts, in every inbox mode, and trusting a service admits nothing else.
func TestAuthPromptsOnlyFromTrustedServices(t *testing.T) {
	server, ts := newDAVTestServer(t, 0, 0)
	alice := registerDAVIdentity(t, server, ts, "alice.poweur.net")
	bridge := registerDAVIdentity(t, server, ts, "bridge.poweur.net")
	mallory := registerDAVIdentity(t, server, ts, "mallory.poweur.net")
	tok := mintDAVToken(t, ts, alice, "", "")
	soon := time.Now().Add(3 * time.Minute)

	// Default open inbox, nobody trusted: prompts are refused even there.
	mustStatus(t, postAuthPrompt(t, ts, bridge, alice.name, soon, "p"), http.StatusForbidden, "untrusted under open")
	// …while ordinary chat from the same sender is fine.
	resp, _ := postTypedMessage(t, ts, bridge, alice.name, "", "hello")
	mustStatus(t, resp, http.StatusAccepted, "chat under open")

	putOwnerFile(t, ts, alice, tok, "/poweur-sys/relay/inbox-policy.json",
		`{"version":1,"mode":"contacts_only","trusted_auth_services":["bridge.poweur.net"]}`)
	mustStatus(t, postAuthPrompt(t, ts, bridge, alice.name, soon, "p"), http.StatusAccepted, "trusted prompt, contacts_only")
	// Trust admits prompts, not chat or other system types.
	resp, _ = postTypedMessage(t, ts, bridge, alice.name, "", "hello")
	mustStatus(t, resp, http.StatusForbidden, "chat from a trusted service, contacts_only")
	resp, _ = postTypedMessage(t, ts, bridge, alice.name, idpkg.MsgTypeShareOffer, "x")
	mustStatus(t, resp, http.StatusForbidden, "share offer from a trusted service")
	// Others still cannot prompt.
	mustStatus(t, postAuthPrompt(t, ts, mallory, alice.name, soon, "p"), http.StatusForbidden, "untrusted prompt")
	// Shape: an expiry is required, near, and in the future; payload is capped.
	for name, tc := range map[string]struct {
		exp    time.Time
		status int
	}{
		"no expiry":  {time.Time{}, http.StatusForbidden},
		"expired":    {time.Now().Add(-time.Minute), http.StatusGone}, // refused before policy
		"far future": {time.Now().Add(time.Hour), http.StatusForbidden},
	} {
		mustStatus(t, postAuthPrompt(t, ts, bridge, alice.name, tc.exp, "p"), tc.status, name)
	}
	mustStatus(t, postAuthPrompt(t, ts, bridge, alice.name, soon, strings.Repeat("p", maxAuthPromptPayload+1)), http.StatusForbidden, "oversized")

	// A contact is not thereby a sign-in service.
	putOwnerFile(t, ts, alice, tok, "/poweur-sys/relay/contacts.json",
		`{"version":1,"contacts":[{"identity":"mallory.poweur.net","state":"accepted"}]}`)
	mustStatus(t, postAuthPrompt(t, ts, mallory, alice.name, soon, "p"), http.StatusForbidden, "prompt from a contact")
	// Blocking a trusted service stops its prompts.
	putOwnerFile(t, ts, alice, tok, "/poweur-sys/relay/contacts.json",
		`{"version":1,"contacts":[{"identity":"bridge.poweur.net","state":"blocked"}]}`)
	mustStatus(t, postAuthPrompt(t, ts, bridge, alice.name, soon, "p"), http.StatusForbidden, "blocked trusted service")

	// The accepted prompt is in the inbox, typed, for the app's tray.
	inbox := challengeSigned(t, ts, alice, "/messages/"+alice.name)
	raw, _ := json.Marshal(inbox)
	if !strings.Contains(string(raw), idpkg.MsgTypeAuthRequest) {
		t.Fatalf("inbox = %s", raw)
	}
}
