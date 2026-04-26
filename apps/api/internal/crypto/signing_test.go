package crypto

import (
	"crypto/ed25519"
	"encoding/base64"
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

func TestParseEncryptionTXTRecord(t *testing.T) {
	// 32 raw bytes (valid x25519 public key size for the parser)
	raw32 := [32]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32}
	enc := base64.RawURLEncoding.EncodeToString(raw32[:])
	records := []string{`eurything-enckey=x25519:` + enc}
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
	_, err := ParseTXTRecord([]string{`eurything-pubkey=ed25519:` + b64})
	if err != nil {
		t.Fatal(err)
	}
}
