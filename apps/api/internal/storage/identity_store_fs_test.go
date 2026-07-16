package storage_test

import (
	"crypto/ed25519"
	"path/filepath"
	"testing"
	"time"

	idpkg "github.com/poweur/identity"

	"github.com/poweur/api/internal/storage"
)

func TestIdentityStoreDurableRestart(t *testing.T) {
	dir := t.TempDir()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	doc := idpkg.NewDocument("alice.poweur.net", idpkg.FormatEd25519PublicKey(pub), "", "relay.example", nil)
	doc.UpdatedAt = "2026-01-01T00:00:00Z"
	if err := doc.Sign(priv); err != nil {
		t.Fatal(err)
	}
	raw, err := doc.MarshalJSONDocument()
	if err != nil {
		t.Fatal(err)
	}

	s1, err := storage.OpenIdentityStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	ident := storage.Identity{
		Identity:       "alice.poweur.net",
		PublicKey:      idpkg.NormalizePublicKeyKey(doc.PublicKey),
		PublicKeyBytes: pub,
		Relay:          "relay.example",
		DocumentJSON:   raw,
		CreatedAt:      time.Now().UTC(),
	}
	if !s1.Add(ident) {
		t.Fatal("add")
	}

	s2, err := storage.OpenIdentityStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := s2.Get("alice.poweur.net")
	if !ok {
		t.Fatal("missing after reload")
	}
	if got.PublicKey != ident.PublicKey {
		t.Fatalf("key %s vs %s", got.PublicKey, ident.PublicKey)
	}
	path := filepath.Join(dir, "identities", "alice__poweur__net", "poweur-sys", "public", "id.json")
	if _, err := filepath.Glob(path); err != nil {
		t.Fatal(err)
	}
}

func TestSanitizeRejectsTraversal(t *testing.T) {
	if _, err := idpkg.SanitizeIdentityDirName("../evil.poweur.net"); err == nil {
		t.Fatal("expected error")
	}
}
