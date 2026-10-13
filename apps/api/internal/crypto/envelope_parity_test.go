package crypto

import (
	"testing"

	idpkg "github.com/poweur/identity"
)

// The shared envelope canonical (packages/identity) must be the one the relay
// verifies, line for line, with and without every optional field.
func TestCanonicalEnvelopeMatchesShared(t *testing.T) {
	cases := []struct {
		id, session, typ, thread, expires string
		md                                map[string]string
		enc                               bool
	}{
		{},
		{id: "m1"},
		{id: "m1", session: "s1", enc: true},
		{id: "m1", typ: "email.message", thread: "T", expires: "2026-01-01T00:00:00Z", md: map[string]string{"b": "2", "a": "1"}, enc: true},
	}
	for _, c := range cases {
		var enc *EncryptionMeta
		var shared *idpkg.EnvelopeEncryption
		if c.enc {
			enc = &EncryptionMeta{Alg: "x25519-chacha20-poly1305", EphemeralPublicKey: "E", Nonce: "N"}
			shared = &idpkg.EnvelopeEncryption{Alg: enc.Alg, EphemeralPublicKey: "E", Nonce: "N"}
		}
		want := CanonicalMessageEnvelope("a.example.org", "b.example.org", "2026-01-01T00:00:00Z", "P", c.id, c.session, c.typ, c.thread, c.expires, c.md, enc)
		got := idpkg.CanonicalMessageEnvelope("a.example.org", "b.example.org", "2026-01-01T00:00:00Z", "P", c.id, c.session, c.typ, c.thread, c.expires, c.md, shared)
		if got != want {
			t.Fatalf("shared canonical differs:\n%q\n%q", got, want)
		}
	}
}
