package identity

import (
	"crypto/ed25519"
	"encoding/base64"
	"regexp"
	"strings"
	"testing"
)

var fingerprintShape = regexp.MustCompile(`^\d{5} \d{5} \d{5} \d{5}$`)

func testKeyBytes(fill byte) ed25519.PublicKey {
	raw := make([]byte, ed25519.PublicKeySize)
	for i := range raw {
		raw[i] = fill + byte(i)
	}
	return ed25519.PublicKey(raw)
}

func TestKeyFingerprintShape(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want bool // want error
	}{
		{"prefixed", FormatEd25519PublicKey(testKeyBytes(1)), false},
		{"bare base64url", base64.RawURLEncoding.EncodeToString(testKeyBytes(1)), false},
		{"all zeros", FormatEd25519PublicKey(make([]byte, 32)), false},
		{"empty", "", true},
		{"not base64", "ed25519:!!!!", true},
		{"wrong length", "ed25519:" + base64.RawURLEncoding.EncodeToString([]byte("short")), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fp, err := KeyFingerprint(tc.key)
			if tc.want {
				if err == nil {
					t.Fatalf("expected error, got %q", fp)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !fingerprintShape.MatchString(fp) {
				t.Fatalf("fingerprint %q does not match %s", fp, fingerprintShape)
			}
		})
	}
}

// Every encoding a document might carry the same key in must fingerprint
// identically — otherwise a user comparing an out-of-band string against a
// client that happened to store padded base64 would see a false mismatch.
func TestKeyFingerprintEncodingInvariant(t *testing.T) {
	raw := testKeyBytes(7)
	forms := []string{
		"ed25519:" + base64.RawURLEncoding.EncodeToString(raw),
		base64.RawURLEncoding.EncodeToString(raw),
		base64.URLEncoding.EncodeToString(raw),
		base64.StdEncoding.EncodeToString(raw),
		base64.RawStdEncoding.EncodeToString(raw),
		"  ed25519:" + base64.RawURLEncoding.EncodeToString(raw) + "  ",
	}
	want := KeyFingerprintBytes(raw)
	for _, form := range forms {
		got, err := KeyFingerprint(form)
		if err != nil {
			t.Fatalf("%q: %v", form, err)
		}
		if got != want {
			t.Fatalf("%q fingerprinted %q, want %q", form, got, want)
		}
	}
}

// Distinct keys must produce distinct strings; a one-bit change must not
// leave the visible groups untouched.
func TestKeyFingerprintDistinguishesKeys(t *testing.T) {
	a := testKeyBytes(1)
	b := append(ed25519.PublicKey(nil), a...)
	b[31] ^= 0x01

	fpA := KeyFingerprintBytes(a)
	fpB := KeyFingerprintBytes(b)
	if fpA == fpB {
		t.Fatalf("one-bit key change produced the same fingerprint %q", fpA)
	}

	seen := map[string]string{}
	for i := 0; i < 256; i++ {
		key := testKeyBytes(byte(i))
		fp := KeyFingerprintBytes(key)
		if prev, dup := seen[fp]; dup {
			t.Fatalf("collision between %s and %s at %q", prev, FormatEd25519PublicKey(key), fp)
		}
		seen[fp] = FormatEd25519PublicKey(key)
	}
}

// The algorithm prefix is inside the hash, so the same 32 bytes used as a
// signing key and as an encryption key must not share a fingerprint.
func TestFingerprintSeparatesAlgorithms(t *testing.T) {
	raw := testKeyBytes(3)
	signing := KeyFingerprintBytes(raw)
	encryption, err := EncryptionKeyFingerprint(FormatX25519PublicKey(raw))
	if err != nil {
		t.Fatalf("encryption fingerprint: %v", err)
	}
	if signing == encryption {
		t.Fatalf("signing and encryption fingerprints collided at %q", signing)
	}
	if !fingerprintShape.MatchString(encryption) {
		t.Fatalf("encryption fingerprint %q has the wrong shape", encryption)
	}
	if _, err := EncryptionKeyFingerprint("x25519:!!!"); err == nil {
		t.Fatal("expected an error for a malformed encryption key")
	}
}

// The display helper must never swallow a value: an unparseable pin still
// has to reach the user's eyes.
func TestFingerprintOrKey(t *testing.T) {
	good := FormatEd25519PublicKey(testKeyBytes(9))
	if got := FingerprintOrKey(good); got != KeyFingerprintBytes(testKeyBytes(9)) {
		t.Fatalf("FingerprintOrKey(%q) = %q", good, got)
	}
	for _, bad := range []string{"", "garbage", "ed25519:zzz"} {
		if got := FingerprintOrKey(bad); got != bad {
			t.Fatalf("FingerprintOrKey(%q) = %q, want the input back", bad, got)
		}
	}
}

// Pin the derivation itself, so a refactor of the chunking or the domain
// separator cannot quietly change every user's safety number.
func TestKeyFingerprintIsStable(t *testing.T) {
	fp := KeyFingerprintBytes(ed25519.NewKeyFromSeed(VectorSeed).Public().(ed25519.PublicKey))
	const want = "56963 45073 70021 85367"
	if fp != want {
		t.Fatalf("vector-seed fingerprint = %q, want %q\n"+
			"(if this is an intentional format change, update the conformance vectors and the docs)", fp, want)
	}
	if strings.Count(fp, " ") != FingerprintGroups-1 {
		t.Fatalf("expected %d groups in %q", FingerprintGroups, fp)
	}
}
