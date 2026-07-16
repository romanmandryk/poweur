package integration_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	clipkg "github.com/poweur/cli/pkg/cli"
	idpkg "github.com/poweur/identity"
)

func TestINT_ROTATE_01_KeyRotateUpdatesDocument(t *testing.T) {
	zone := newZone(t)
	dataDir := t.TempDir()
	ts, addr := newHostedRelay(t, zone, dataDir)
	defer ts.Close()
	relayURL := "http://" + addr
	zone.SetHost("rotate.poweur.net", addr)
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	home := t.TempDir()
	runCLI(t, home,
		"identity", "create", "rotate.poweur.net",
		"--hosted", "--relay", relayURL, "--json",
	)

	before := fetchDoc(t, relayURL+"/identities/rotate.poweur.net")
	oldKey := idpkg.NormalizePublicKeyKey(before.PublicKey)

	runCLI(t, home, "key", "rotate", "--use-identity", "rotate.poweur.net", "--grace", "1h", "--json")

	after := fetchDoc(t, relayURL+"/identities/rotate.poweur.net")
	newKey := idpkg.NormalizePublicKeyKey(after.PublicKey)
	if newKey == oldKey {
		t.Fatal("expected public key to change after rotate")
	}
	found := false
	for _, pk := range after.PreviousKeys {
		if idpkg.NormalizePublicKeyKey(pk.PublicKey) == oldKey {
			found = true
			if pk.ValidUntil == "" {
				t.Fatal("expected valid_until on previous key")
			}
		}
	}
	if !found {
		t.Fatalf("old key missing from previous_keys: %+v", after.PreviousKeys)
	}
	now := time.Now().UTC()
	if !after.KeyValidAt(newKey, now) {
		t.Fatal("new key should be valid")
	}
	if !after.KeyValidAt(oldKey, now) {
		t.Fatal("old key should remain valid within grace window")
	}
	if after.KeyValidAt(oldKey, now.Add(48*time.Hour)) {
		t.Fatal("old key should be invalid after grace")
	}
}

func TestINT_EXPORT_01_IdentityExportArchive(t *testing.T) {
	zone := newZone(t)
	dataDir := t.TempDir()
	ts, addr := newHostedRelay(t, zone, dataDir)
	defer ts.Close()
	relayURL := "http://" + addr
	zone.SetHost("exportme.poweur.net", addr)
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	home := t.TempDir()
	runCLI(t, home,
		"identity", "create", "exportme.poweur.net",
		"--hosted", "--relay", relayURL, "--json",
	)
	out := filepath.Join(home, "export.tar.gz")
	runCLI(t, home,
		"identity", "export",
		"--use-identity", "exportme.poweur.net",
		"--out", out,
	)
	st, err := os.Stat(out)
	if err != nil || st.Size() < 32 {
		t.Fatalf("expected export archive, err=%v size=%v", err, st)
	}
}

func fetchDoc(t *testing.T, url string) idpkg.IdentityDocument {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s → %d", url, resp.StatusCode)
	}
	var wrap struct {
		IdentityDocument json.RawMessage `json:"identity_document"`
		PublicKey        string          `json:"public_key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wrap); err != nil {
		t.Fatal(err)
	}
	if len(wrap.IdentityDocument) > 0 {
		doc, err := idpkg.ParseDocument(wrap.IdentityDocument, true)
		if err != nil {
			t.Fatal(err)
		}
		return doc
	}
	return idpkg.IdentityDocument{PublicKey: wrap.PublicKey}
}
