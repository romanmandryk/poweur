package integration_test

import (
	"os"
	"path/filepath"
	"testing"

	idpkg "github.com/poweur/identity"
)

// relaySysFilePath is where a relay with POWEUR_DATA=dataDir keeps one
// identity's system file (EPIC-020 E20-T6).
func relaySysFilePath(t *testing.T, dataDir, identity, path string) string {
	t.Helper()
	dir, err := idpkg.SanitizeIdentityDirName(identity)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dataDir, "identities", dir, filepath.FromSlash(path))
}

// readRelaySysFile reads a system file straight off the relay's disk.
func readRelaySysFile(t *testing.T, dataDir, identity, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(relaySysFilePath(t, dataDir, identity, path))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// writeRelaySysFile overwrites a system file on the relay's disk, the way a
// compromised relay could — tests use it to simulate tampering.
func writeRelaySysFile(t *testing.T, dataDir, identity, path string, raw []byte) {
	t.Helper()
	name := relaySysFilePath(t, dataDir, identity, path)
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}
