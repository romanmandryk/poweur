package drive

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	protocol "github.com/poweur/identity/drive"
)

type ChunkCache interface {
	Get(string) ([]byte, error)
	Put(string, []byte) error
}

// FileCache stores immutable ciphertext under the shared Go/TS CLI cache.
// Missing/corrupt cache entries are misses; callers verify network bytes too.
type FileCache struct{ Dir string }

var chunkName = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (c FileCache) path(id string) (string, error) {
	if !chunkName.MatchString(id) {
		return "", fmt.Errorf("invalid chunk ID")
	}
	return filepath.Join(c.Dir, id), nil
}
func (c FileCache) Get(id string) ([]byte, error) {
	path, err := c.path(id)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > protocol.MaxChunkBytes {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if protocol.ChunkID(data) != id {
		return nil, nil
	}
	return data, nil
}
func (c FileCache) Put(id string, data []byte) error {
	path, err := c.path(id)
	if err != nil {
		return err
	}
	if len(data) > protocol.MaxChunkBytes || protocol.ChunkID(data) != id {
		return fmt.Errorf("invalid cached chunk")
	}
	if err = os.MkdirAll(c.Dir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(c.Dir, ".chunk-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
