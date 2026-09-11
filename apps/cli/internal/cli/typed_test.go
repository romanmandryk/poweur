package cli

import (
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"

	idpkg "github.com/poweur/identity"
)

// The CLI builds the canonical signing string by hand (it does not import the
// relay's `crypto` package — different module). These tests pin the string it
// produces line by line; `apps/integration` is where the CLI's signature is
// checked against a real relay's verifier end to end.

// verifyCanonical reports whether sig is priv's signature over want.
func verifyCanonical(t *testing.T, pub ed25519.PublicKey, want, sig string) {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		t.Fatalf("signature is not standard base64: %v", err)
	}
	if !ed25519.Verify(pub, []byte(want), raw) {
		t.Fatalf("signature does not cover the expected canonical string:\n%q", want)
	}
}

// TestSignMessageCanonicalLines pins the line order and the omit-when-absent
// rule across every combination that matters — not just the fully populated
// one. The point of the append-only design is that each field is
// independently optional, and only a matrix says so.
func TestSignMessageCanonicalLines(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	enc := &EncryptionMeta{
		Alg:                "x25519-chacha20-poly1305",
		EphemeralPublicKey: "ZXBo",
		Nonce:              "bm9u",
	}
	head := []string{
		"alice.poweur.net", "bob.example.org", "2026-01-15T09:30:00Z", "Y2lwaGVy",
		"id:msg_1", "enc:x25519-chacha20-poly1305:ZXBo:bm9u",
	}
	base := Message{
		ID:         "msg_1",
		Sender:     "alice.poweur.net",
		Recipient:  "bob.example.org",
		Timestamp:  "2026-01-15T09:30:00Z",
		Payload:    "Y2lwaGVy",
		Encryption: enc,
	}

	cases := map[string]struct {
		mutate func(*Message)
		extra  []string
	}{
		// The untyped envelope is the compatibility case: byte-identical to
		// what the pre-E09-T3 CLI signed, so an old relay still verifies it.
		"untyped": {func(*Message) {}, nil},
		"type only": {
			func(m *Message) { m.Type = "chat.attachment" },
			[]string{"type:chat.attachment"},
		},
		// thread with no type before it — the case that proves the new lines
		// are independently optional rather than a block appearing together.
		"thread without type": {
			func(m *Message) { m.ThreadID = "thr_1" },
			[]string{"thread:thr_1"},
		},
		"expires without type or thread": {
			func(m *Message) { m.ExpiresAt = "2026-01-16T09:30:00Z" },
			[]string{"expires:2026-01-16T09:30:00Z"},
		},
		"metadata alone, sorted": {
			func(m *Message) { m.Metadata = map[string]string{"zeta": "z", "bytes": "20480", "mime": "image/png"} },
			[]string{"meta:bytes:20480", "meta:mime:image/png", "meta:zeta:z"},
		},
		"everything": {
			func(m *Message) {
				m.Type = "chat.attachment"
				m.ThreadID = "thr_1"
				m.ExpiresAt = "2026-01-16T09:30:00Z"
				m.Metadata = map[string]string{"mime": "image/png"}
			},
			[]string{"type:chat.attachment", "thread:thr_1", "expires:2026-01-16T09:30:00Z", "meta:mime:image/png"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			msg := base
			tc.mutate(&msg)
			want := strings.Join(append(append([]string(nil), head...), tc.extra...), "\n")
			verifyCanonical(t, pub, want, signMessage(priv, msg))
		})
	}
}

// TestSignMessageSessionLinePosition keeps `session:` where it has always
// been — between `id:` and `enc:` — now that lines are appended after it.
func TestSignMessageSessionLinePosition(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	msg := Message{
		ID: "msg_1", Sender: "alice.poweur.net", Recipient: "bob.example.org",
		Timestamp: "2026-01-15T09:30:00Z", Payload: "Y2lwaGVy",
		SessionID: "sess_1", ThreadID: "thr_1",
		Encryption: &EncryptionMeta{Alg: "x25519-chacha20-poly1305", EphemeralPublicKey: "ZXBo", Nonce: "bm9u"},
	}
	want := strings.Join([]string{
		"alice.poweur.net", "bob.example.org", "2026-01-15T09:30:00Z", "Y2lwaGVy",
		"id:msg_1", "session:sess_1", "enc:x25519-chacha20-poly1305:ZXBo:bm9u", "thread:thr_1",
	}, "\n")
	verifyCanonical(t, pub, want, signMessage(priv, msg))
}

func TestMetaFlag(t *testing.T) {
	m := &metaFlag{}
	if m.Map() != nil {
		t.Fatal("an unused --meta flag must produce nil, not an empty map")
	}
	if err := m.Set("mime=image/png"); err != nil {
		t.Fatal(err)
	}
	// Values may contain '=' — only the first one separates key from value.
	if err := m.Set("token=a=b"); err != nil {
		t.Fatal(err)
	}
	if err := m.Set(" bytes =20480"); err != nil {
		t.Fatal(err)
	}
	got := m.Map()
	for key, want := range map[string]string{"mime": "image/png", "token": "a=b", "bytes": "20480"} {
		if got[key] != want {
			t.Fatalf("--meta %s = %q, want %q (map %v)", key, got[key], want, got)
		}
	}
	if err := m.Set("no-equals-sign"); err == nil {
		t.Fatal("--meta without '=' must be an error")
	}
	if err := m.Set("mime=image/jpeg"); err == nil {
		t.Fatal("a duplicate --meta key must be an error rather than a silent last-write-wins")
	}
	if got["mime"] != "image/png" {
		t.Fatalf("the rejected duplicate must not have been applied: %v", got)
	}
}

func TestDescribeTypedMessage(t *testing.T) {
	for name, tc := range map[string]struct {
		msgType   string
		body      string
		decrypted bool
		want      string
	}{
		"absent type is chat.text": {"", "hello", true, "hello"},
		"explicit chat.text":       {"chat.text", "hello", true, "hello"},
		// A contact request's payload is the intro a stranger wrote to
		// introduce themselves: hiding it behind the fallback would hide the
		// one thing that helps the recipient decide.
		"contact request intro":  {idpkg.MsgTypeContactRequest, "hi, it's carol", true, "hi, it's carol"},
		"unknown app type":       {"net.example.widget.poked", `{"widget":1}`, true, "app message from carol.poweur.net (net.example.widget.poked)"},
		"system type":            {idpkg.MsgTypeSyncChanged, "{}", true, "app message from carol.poweur.net (sys.sync.changed)"},
		"undecrypted keeps body": {"net.example.widget.poked", "[decrypt failed: bad key]", false, "[decrypt failed: bad key]"},
	} {
		got := describeTypedMessage("carol.poweur.net", tc.msgType, tc.body, tc.decrypted)
		if got != tc.want {
			t.Fatalf("%s: got %q, want %q", name, got, tc.want)
		}
	}
}

func TestThreadSuffix(t *testing.T) {
	if threadSuffix("") != "" {
		t.Fatal("an unthreaded message must render exactly as before")
	}
	if got := threadSuffix("thr_1"); got != " [thread thr_1]" {
		t.Fatalf("threadSuffix = %q", got)
	}
}

func TestValidateOutgoingEnvelope(t *testing.T) {
	if err := validateOutgoingEnvelope("", "", "", nil); err != nil {
		t.Fatalf("a bare envelope must validate: %v", err)
	}
	if err := validateOutgoingEnvelope(idpkg.MsgTypeContactRequest, "thr_1",
		"2026-01-16T09:30:00Z", map[string]string{"k": "v"}); err != nil {
		t.Fatalf("a registered system envelope must validate: %v", err)
	}
	// An unregistered sys.* is caught locally, with the known set named, so
	// the user does not have to read a remote relay's 400 to find out.
	err := validateOutgoingEnvelope("sys.made.up", "", "", nil)
	if err == nil {
		t.Fatal("an unregistered sys.* type must be refused")
	}
	if !strings.Contains(err.Error(), "sys.contact.request") {
		t.Fatalf("the error should name the known types, got: %v", err)
	}
	// Applications outside sys.* need no registration at all.
	if err := validateOutgoingEnvelope("net.example.widget.poked", "", "", nil); err != nil {
		t.Fatalf("an application type must not need registering: %v", err)
	}
	for name, args := range map[string][3]string{
		"bare type":      {"widget", "", ""},
		"bad thread":     {"", "thr one", ""},
		"bad expiry":     {"", "", "next tuesday"},
		"uppercase":      {"Chat.Text", "", ""},
		"thread too big": {"", strings.Repeat("t", idpkg.MaxThreadIDLen+1), ""},
	} {
		if err := validateOutgoingEnvelope(args[0], args[1], args[2], nil); err == nil {
			t.Fatalf("%s: expected an error", name)
		}
	}
	if err := validateOutgoingEnvelope("", "", "", map[string]string{"Bad": "v"}); err == nil {
		t.Fatal("an invalid metadata key must be refused")
	}
}
