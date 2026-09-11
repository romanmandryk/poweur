package crypto

import (
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"
)

func TestCanonicalMessageLegacy(t *testing.T) {
	got := CanonicalMessage("a", "b", "t", "p")
	want := "a\nb\nt\np"
	if got != want {
		t.Fatalf("got %q", got)
	}
}

func TestCanonicalMessageFullOmitIdSessionEnc(t *testing.T) {
	if CanonicalMessageFull("a", "b", "t", "p", "", "", nil) != CanonicalMessage("a", "b", "t", "p") {
		t.Fatal("empty id/session/enc should match legacy line")
	}
}

var canonTestEnc = &EncryptionMeta{
	Alg:                "x25519-chacha20-poly1305",
	EphemeralPublicKey: "eph",
	Nonce:              "non",
}

// TestCanonicalMessageEnvelope pins the line order and the omit-when-empty
// rule for every optional field (EPIC-009 E09-T3). The order is the protocol:
// four downstream tasks sign against it, so a reordering here is a wire break
// and must fail loudly.
func TestCanonicalMessageEnvelope(t *testing.T) {
	tests := []struct {
		name      string
		id        string
		sessionID string
		msgType   string
		threadID  string
		expiresAt string
		metadata  map[string]string
		enc       *EncryptionMeta
		want      string
	}{
		{
			name: "bare envelope is the legacy four lines",
			want: "a\nb\nt\np",
		},
		{
			name: "id only",
			id:   "msg_1",
			want: "a\nb\nt\np\nid:msg_1",
		},
		{
			name:      "session and encryption",
			id:        "msg_1",
			sessionID: "sess_1",
			enc:       canonTestEnc,
			want:      "a\nb\nt\np\nid:msg_1\nsession:sess_1\nenc:x25519-chacha20-poly1305:eph:non",
		},
		{
			name:    "type follows encryption",
			id:      "msg_1",
			enc:     canonTestEnc,
			msgType: "sys.contact.request",
			want:    "a\nb\nt\np\nid:msg_1\nenc:x25519-chacha20-poly1305:eph:non\ntype:sys.contact.request",
		},
		{
			name:     "thread follows type",
			id:       "msg_1",
			msgType:  "chat.text",
			threadID: "thr_1",
			want:     "a\nb\nt\np\nid:msg_1\ntype:chat.text\nthread:thr_1",
		},
		{
			name:     "thread stands alone when type is absent",
			id:       "msg_1",
			threadID: "thr_1",
			want:     "a\nb\nt\np\nid:msg_1\nthread:thr_1",
		},
		{
			name:      "expires follows thread",
			id:        "msg_1",
			threadID:  "thr_1",
			expiresAt: "2026-01-15T09:30:00Z",
			want:      "a\nb\nt\np\nid:msg_1\nthread:thr_1\nexpires:2026-01-15T09:30:00Z",
		},
		{
			name:     "metadata lines come last, keys ascending",
			id:       "msg_1",
			metadata: map[string]string{"zeta": "z", "alpha": "a", "mid": "m"},
			want:     "a\nb\nt\np\nid:msg_1\nmeta:alpha:a\nmeta:mid:m\nmeta:zeta:z",
		},
		{
			name:      "everything at once, in order",
			id:        "msg_1",
			sessionID: "sess_1",
			enc:       canonTestEnc,
			msgType:   "chat.attachment",
			threadID:  "thr_1",
			expiresAt: "2026-01-15T09:30:00Z",
			metadata:  map[string]string{"mime": "image/png", "bytes": "1024"},
			want: "a\nb\nt\np\nid:msg_1\nsession:sess_1\n" +
				"enc:x25519-chacha20-poly1305:eph:non\ntype:chat.attachment\n" +
				"thread:thr_1\nexpires:2026-01-15T09:30:00Z\n" +
				"meta:bytes:1024\nmeta:mime:image/png",
		},
		{
			name:     "an empty metadata map contributes nothing",
			id:       "msg_1",
			metadata: map[string]string{},
			want:     "a\nb\nt\np\nid:msg_1",
		},
		{
			name: "an encryption meta with no alg is ignored",
			id:   "msg_1",
			enc:  &EncryptionMeta{EphemeralPublicKey: "eph", Nonce: "non"},
			want: "a\nb\nt\np\nid:msg_1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CanonicalMessageEnvelope("a", "b", "t", "p",
				tc.id, tc.sessionID, tc.msgType, tc.threadID, tc.expiresAt, tc.metadata, tc.enc)
			if got != tc.want {
				t.Fatalf("canonical mismatch\n got: %q\nwant: %q", got, tc.want)
			}
			if strings.HasSuffix(got, "\n") {
				t.Fatal("canonical string must not end with a newline")
			}
		})
	}
}

// TestCanonicalMessageEnvelopeIsBackwardCompatible is the compatibility
// contract in one assertion: with none of the E09-T3 fields set, the new
// canonical string is byte-identical to what the previous revision produced.
// That is what lets an old client's envelope verify on a new relay, and a new
// client's envelope verify on an old one.
func TestCanonicalMessageEnvelopeIsBackwardCompatible(t *testing.T) {
	// The pre-E09-T3 construction, frozen here rather than called, so a
	// change to the live code cannot quietly change the baseline too.
	legacy := func(sender, recipient, timestamp, payload, id, sessionID, msgType string, enc *EncryptionMeta) string {
		parts := []string{sender, recipient, timestamp, payload}
		if id != "" {
			parts = append(parts, "id:"+id)
		}
		if sessionID != "" {
			parts = append(parts, "session:"+sessionID)
		}
		if enc != nil && enc.Alg != "" {
			parts = append(parts, "enc:"+enc.Alg+":"+enc.EphemeralPublicKey+":"+enc.Nonce)
		}
		s := strings.Join(parts, "\n")
		if msgType != "" {
			s += "\ntype:" + msgType
		}
		return s
	}

	for _, tc := range []struct {
		id, sessionID, msgType string
		enc                    *EncryptionMeta
	}{
		{},
		{id: "msg_1"},
		{id: "msg_1", sessionID: "sess_1"},
		{id: "msg_1", enc: canonTestEnc},
		{id: "msg_1", sessionID: "sess_1", enc: canonTestEnc},
		{id: "msg_1", sessionID: "sess_1", enc: canonTestEnc, msgType: "sys.contact.request"},
	} {
		want := legacy("alice.poweur.net", "bob.example.org", "2026-01-15T09:30:00Z", "cipher",
			tc.id, tc.sessionID, tc.msgType, tc.enc)
		got := CanonicalMessageEnvelope("alice.poweur.net", "bob.example.org", "2026-01-15T09:30:00Z", "cipher",
			tc.id, tc.sessionID, tc.msgType, "", "", nil, tc.enc)
		if got != want {
			t.Fatalf("new canonical diverged from the pre-E09-T3 string\n got: %q\nwant: %q", got, want)
		}
		// The narrow wrapper must stay the same string too.
		if CanonicalMessageTyped("alice.poweur.net", "bob.example.org", "2026-01-15T09:30:00Z", "cipher",
			tc.id, tc.sessionID, tc.msgType, tc.enc) != want {
			t.Fatal("CanonicalMessageTyped diverged from the pre-E09-T3 string")
		}
	}
}

// TestCanonicalMessageEnvelopeDistinguishes covers the failure modes the
// signature exists to prevent: adding, removing or changing any of the new
// fields must change the string that gets signed.
func TestCanonicalMessageEnvelopeDistinguishes(t *testing.T) {
	base := func(threadID, expiresAt string, md map[string]string) string {
		return CanonicalMessageEnvelope("a", "b", "t", "p", "msg_1", "", "chat.text",
			threadID, expiresAt, md, canonTestEnc)
	}
	seen := map[string]string{}
	for name, s := range map[string]string{
		"no extensions":  base("", "", nil),
		"thread added":   base("thr_1", "", nil),
		"other thread":   base("thr_2", "", nil),
		"expiry added":   base("", "2026-01-15T09:30:00Z", nil),
		"metadata added": base("", "", map[string]string{"k": "v"}),
		"other value":    base("", "", map[string]string{"k": "w"}),
		"other key":      base("", "", map[string]string{"j": "v"}),
		"extra key":      base("", "", map[string]string{"k": "v", "j": "v"}),
		"all three":      base("thr_1", "2026-01-15T09:30:00Z", map[string]string{"k": "v"}),
	} {
		if prior, dup := seen[s]; dup {
			t.Fatalf("%q and %q produce the same canonical string %q", name, prior, s)
		}
		seen[s] = name
	}

	// Reordering the same map must not: Go map iteration order is random,
	// so this would be flaky rather than wrong if the sort were missing.
	forward := base("", "", map[string]string{"a": "1", "b": "2", "c": "3"})
	for i := 0; i < 50; i++ {
		if again := base("", "", map[string]string{"c": "3", "a": "1", "b": "2"}); again != forward {
			t.Fatalf("metadata ordering is not stable: %q vs %q", again, forward)
		}
	}
}

func TestParseEncryptionTXTRecord(t *testing.T) {
	// 32 raw bytes (valid x25519 public key size for the parser)
	raw32 := [32]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32}
	enc := base64.RawURLEncoding.EncodeToString(raw32[:])
	records := []string{`poweur-enckey=x25519:` + enc}
	raw, err := ParseEncryptionTXTRecord(records)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 32 {
		t.Fatalf("len %d", len(raw))
	}
	if _, err := ParseEncryptionTXTRecord([]string{`other=1`}); err == nil {
		t.Fatal("expected error for missing enc record")
	}
}

func TestParseTXTRecordRounds(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	b64 := base64.RawURLEncoding.EncodeToString(pub)
	_, err := ParseTXTRecord([]string{`poweur-pubkey=ed25519:` + b64})
	if err != nil {
		t.Fatal(err)
	}
}
