package storage

import (
	"testing"

	"github.com/poweur/api/internal/drive/provider/fs"
)

// objectsAt is a fresh filesystem provider, the relay's default Objects.
func objectsAt(t *testing.T) Objects {
	t.Helper()
	store, err := fs.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return store
}
