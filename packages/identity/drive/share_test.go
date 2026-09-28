package drive

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	identity "github.com/poweur/identity"
)

func testShare() Share {
	enc := base64.RawURLEncoding.EncodeToString
	return Share{
		Format: 1, Drive: "alice.poweur.net", ID: strings.Repeat("a1", 16), Node: strings.Repeat("b2", 16),
		Member: "bob.poweur.net", Role: RoleWrite, Generation: 2,
		NodeKey:    &identity.SealedPayload{EphemeralPublicKey: enc(bytes.Repeat([]byte{3}, 32)), Nonce: enc(bytes.Repeat([]byte{4}, 12)), Ciphertext: enc(bytes.Repeat([]byte{5}, 48))},
		NodePublic: enc(bytes.Repeat([]byte{6}, 32)),
		Expires:    "2027-01-01T00:00:00Z",
		Caps:       Caps{Bytes: 1 << 30, Files: 100},
		Issuer:     "alice.poweur.net", Issued: "2026-09-28T12:00:00Z",
	}
}

func TestShareSignVerify(t *testing.T) {
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{9}, 32))
	s := testShare()
	if err := s.Sign(priv); err != nil {
		t.Fatal(err)
	}
	if err := s.Verify(priv.Public().(ed25519.PublicKey)); err != nil {
		t.Fatal(err)
	}
	tampered := s
	tampered.Role = RoleAdmin
	if tampered.Verify(priv.Public().(ed25519.PublicKey)) == nil {
		t.Fatal("role change verified")
	}
	if !s.ExpiredAt(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)) || s.ExpiredAt(time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("expiry")
	}
}

func TestShareShapes(t *testing.T) {
	enc := base64.RawURLEncoding.EncodeToString
	for name, mutate := range map[string]func(*Share){
		"member and link":  func(s *Share) { s.Link = strings.Repeat("c3", 16) },
		"neither member":   func(s *Share) { s.Member = "" },
		"bad role":         func(s *Share) { s.Role = "owner" },
		"append with key":  func(s *Share) { s.Role = RoleAppend },
		"read without key": func(s *Share) { s.Role = RoleRead; s.NodeKey = nil },
		"link admin":       func(s *Share) { s.Member, s.Link, s.Role = "", strings.Repeat("c3", 16), RoleAdmin },
		"password on member": func(s *Share) {
			s.KDF, s.Salt, s.VerifierHash = ShareKDF, enc(make([]byte, 16)), strings.Repeat("0", 64)
		},
		"unknown kdf": func(s *Share) {
			s.Member, s.Link, s.KDF, s.Salt, s.VerifierHash = "", strings.Repeat("c3", 16), "pbkdf2", enc(make([]byte, 16)), strings.Repeat("0", 64)
		},
		"short node public":     func(s *Share) { s.NodePublic = enc(make([]byte, 31)) },
		"non-canonical time":    func(s *Share) { s.Issued = "2026-09-28T12:00:00+00:00" },
		"zero generation":       func(s *Share) { s.Generation = 0 },
		"excessive pow":         func(s *Share) { s.PoW = 33 },
		"uppercase member name": func(s *Share) { s.Member = "Bob.poweur.net" },
	} {
		s := testShare()
		mutate(&s)
		if s.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	link := testShare()
	link.Member, link.Link, link.Role, link.NodeKey = "", strings.Repeat("c3", 16), RoleCreate, nil
	link.KDF, link.Salt, link.VerifierHash, link.PoW = ShareKDF, enc(make([]byte, 16)), VerifierHash([]byte("verifier")), 12
	if err := link.Validate(); err != nil {
		t.Fatalf("create link with password: %v", err)
	}
}

func TestRoleGrants(t *testing.T) {
	for _, c := range []struct {
		role, need string
		want       bool
	}{
		{RoleRead, RoleRead, true}, {RoleAppend, RoleRead, false}, {RoleCreate, RoleRead, false},
		{RoleWrite, RoleAppend, true}, {RoleWrite, RoleCreate, true}, {RoleWrite, RoleAdmin, false},
		{RoleAdmin, RoleWrite, true}, {RoleAppend, RoleCreate, false}, {RoleRead, RoleWrite, false},
	} {
		if got := RoleGrants(c.role, c.need); got != c.want {
			t.Errorf("%s grants %s: %v", c.role, c.need, got)
		}
	}
}

func TestVectors_DriveShares(t *testing.T) {
	type vector struct {
		Name      string `json:"name"`
		Share     Share  `json:"share"`
		Seed      string `json:"seed"`
		PublicKey string `json:"public_key"`
		Canonical string `json:"canonical"`
		Hash      string `json:"hash"`
	}
	enc := base64.RawURLEncoding.EncodeToString
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{9}, 32))
	member := testShare()
	link := testShare()
	link.Member, link.Link, link.Role, link.NodeKey, link.Expires = "", strings.Repeat("c3", 16), RoleCreate, nil, ""
	link.KDF, link.Salt, link.VerifierHash, link.PoW, link.Caps = ShareKDF, enc(bytes.Repeat([]byte{7}, 16)), VerifierHash([]byte("verifier")), 12, Caps{Files: 1, PerHour: 10}
	var out struct {
		Shares []vector `json:"shares"`
	}
	for _, c := range []struct {
		name string
		s    Share
	}{{"member-write", member}, {"link-create-password", link}} {
		s := c.s
		if err := s.Sign(priv); err != nil {
			t.Fatal(err)
		}
		raw, _ := s.Canonical()
		hash, err := s.Hash()
		if err != nil {
			t.Fatal(err)
		}
		out.Shares = append(out.Shares, vector{c.name, s, enc(priv.Seed()), enc(priv.Public().(ed25519.PublicKey)), hex.EncodeToString(raw), hash})
	}
	raw, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("../testdata/vectors/drive-shares.json", append(raw, '\n'), 0644); err != nil {
		t.Fatal(err)
	}
}
