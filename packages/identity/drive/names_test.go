package drive

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"

	identity "github.com/poweur/identity"
)

func TestNames(t *testing.T) {
	pub, priv, err := identity.GenerateX25519Keypair()
	if err != nil {
		t.Fatal(err)
	}
	context, _ := Context("alice.example", "node", "name", 1)
	for _, name := range []string{"café", "cafe\u0301", "世界 🔒", ".poweur", "A\\B"} {
		normalized, err := NormalizeName(name)
		if err != nil {
			t.Fatal(err)
		}
		sealed, err := SealName(pub, name, context)
		if err != nil {
			t.Fatal(err)
		}
		got, err := OpenName(priv, sealed, context)
		if err != nil || got != normalized {
			t.Fatalf("%q: %q %v", name, got, err)
		}
	}
	a, _ := NameHash(priv, "café")
	b, _ := NameHash(priv, "cafe\u0301")
	if a != b {
		t.Fatal("NFC names have different hashes")
	}
	c, _ := NameHash(priv, "Café")
	if a == c {
		t.Fatal("names are not case-sensitive")
	}
	_, other, _ := identity.GenerateX25519Keypair()
	d, _ := NameHash(other, "café")
	if a == d {
		t.Fatal("names are not parent-bound")
	}
	for _, name := range []string{"", ".", "..", "a/b", "a\x00b", strings.Repeat("x", 256), string([]byte{255})} {
		if _, err := NormalizeName(name); err == nil {
			t.Fatalf("accepted %q", name)
		}
		if _, err := SealName(pub, name, context); err == nil {
			t.Fatal("sealed invalid name")
		}
	}
	raw, _ := identity.Seal(pub, []byte("cafe\u0301"), identity.DriveNameDomain, context)
	if _, err := OpenName(priv, raw, context); err == nil {
		t.Fatal("accepted noncanonical encrypted name")
	}
	if _, err := NameHash(priv[:31], "x"); err == nil {
		t.Fatal("accepted short parent key")
	}
}

func TestVectors_DriveNames(t *testing.T) {
	type vector struct {
		Name       string `json:"name"`
		Normalized string `json:"normalized"`
		Key        string `json:"private_key"`
		Hash       string `json:"hash"`
	}
	key := bytes.Repeat([]byte{7}, 32)
	var vectors []vector
	for _, name := range []string{"café", "cafe\u0301", "Café", "世界 🔒", ".poweur"} {
		normalized, err := NormalizeName(name)
		if err != nil {
			t.Fatal(err)
		}
		hash, err := NameHash(key, name)
		if err != nil {
			t.Fatal(err)
		}
		vectors = append(vectors, vector{name, normalized, base64.RawURLEncoding.EncodeToString(key), hash})
	}
	raw, err := json.MarshalIndent(vectors, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("../testdata/vectors/drive-names.json", append(raw, '\n'), 0644); err != nil {
		t.Fatal(err)
	}
}
