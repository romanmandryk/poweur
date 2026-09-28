// Package fs implements the single-process filesystem object provider.
// One instance owns a root; multiple writers/processes require E20-T17 leases.
package fs

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/poweur/api/internal/drive/provider"
)

type Store struct {
	root *os.Root
	mu   sync.RWMutex
}

func Open(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("provider directory required")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return &Store{root: root}, nil
}
func (s *Store) Close() error {
	if s == nil || s.root == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.root.Close()
}

func etag(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func storageError(err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return provider.ErrNotFound
	}
	return err
}

func (s *Store) Get(ctx context.Context, key string, r *provider.Range) (provider.Object, error) {
	if err := ctx.Err(); err != nil {
		return provider.Object{}, err
	}
	if err := provider.ValidateKey(key); err != nil {
		return provider.Object{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	raw, err := s.root.ReadFile(key)
	if err != nil {
		return provider.Object{}, storageError(err)
	}
	size := int64(len(raw))
	if err := provider.CheckRange(r, size); err != nil {
		return provider.Object{}, err
	}
	result := provider.Object{Data: raw, ETag: etag(raw), Size: size}
	if r != nil {
		result.Data = raw[r.Offset : r.Offset+r.Length]
	}
	if err := ctx.Err(); err != nil {
		return provider.Object{}, err
	}
	return result, nil
}

func (s *Store) Put(ctx context.Context, key string, data []byte) (string, error) {
	return s.put(ctx, key, data, nil)
}
func (s *Store) PutIf(ctx context.Context, key string, data []byte, match string) (string, error) {
	return s.put(ctx, key, data, &match)
}

func (s *Store) put(ctx context.Context, key string, data []byte, match *string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := provider.ValidateKey(key); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if match != nil {
		current, err := s.root.ReadFile(key)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if *match == "" && err == nil || *match != "" && (err != nil || etag(current) != *match) {
			// Exactly these bytes already stored: the write is in place.
			if err == nil && bytes.Equal(current, data) {
				return etag(current), nil
			}
			return "", provider.ErrPrecondition
		}
	}
	dir := path.Dir(key)
	if err := s.makeDirs(dir); err != nil {
		return "", err
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	tmp := path.Join(dir, ".tmp-"+hex.EncodeToString(random))
	file, err := s.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	defer s.root.Remove(tmp)
	if _, err := file.Write(data); err != nil {
		file.Close()
		return "", err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := s.root.Rename(tmp, key); err != nil {
		return "", err
	}
	if err := s.syncDir(dir); err != nil {
		return "", err
	}
	return etag(data), nil
}

// Sync each created directory's parent as well as the final object directory:
// syncing only the leaf could lose a newly created ancestor after a power loss.
func (s *Store) makeDirs(dir string) error {
	if dir == "." {
		return nil
	}
	current := "."
	for _, part := range strings.Split(dir, "/") {
		next := path.Join(current, part)
		if err := s.root.Mkdir(next, 0700); err != nil {
			if !errors.Is(err, os.ErrExist) {
				return err
			}
			info, err := s.root.Stat(next)
			if err != nil {
				return err
			}
			if !info.IsDir() {
				return errors.New("object parent is not a directory")
			}
		} else if err := s.syncDir(current); err != nil {
			return err
		}
		current = next
	}
	return nil
}
func (s *Store) syncDir(dir string) error {
	f, err := s.root.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func (s *Store) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := provider.ValidateKey(key); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	info, err := s.root.Lstat(key)
	if err != nil {
		return storageError(err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("object is not a regular file")
	}
	if err := s.root.Remove(key); err != nil {
		return storageError(err)
	}
	return s.syncDir(path.Dir(key))
}

func (s *Store) List(ctx context.Context, prefix, cursor string, limit int) (provider.Page, error) {
	if err := provider.ValidateList(prefix, cursor, limit); err != nil {
		return provider.Page{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	objects := []provider.Info{}
	err := fs.WalkDir(s.root.FS(), ".", func(key string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if key == "." {
			return nil
		}
		if strings.HasPrefix(entry.Name(), ".tmp-") {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		// POWEUR_DATA also holds identity and spool trees. A listing of
		// drives/ must not fail on, or walk, those siblings.
		match, descend := listScope(key, prefix)
		if !match {
			if entry.IsDir() && !descend {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return errors.New("non-regular object in provider")
		}
		if err := provider.ValidateKey(key); err != nil {
			return err
		}
		if strings.HasPrefix(key, prefix) && key > cursor {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			// Directory walk order is not full-key order (a.b precedes a/x).
			// Keep only the smallest limit+1 keys, in order, bounding page memory.
			index := sort.Search(len(objects), func(i int) bool { return objects[i].Key >= key })
			if index <= limit {
				objects = append(objects, provider.Info{})
				copy(objects[index+1:], objects[index:])
				objects[index] = provider.Info{Key: key, Size: info.Size(), Modified: info.ModTime().UTC()}
				if len(objects) > limit+1 {
					objects = objects[:limit+1]
				}
			}
		}
		return nil
	})
	if err != nil {
		return provider.Page{}, err
	}
	page := provider.Page{Objects: objects}
	if len(objects) > limit {
		page.Objects = objects[:limit]
		page.Next = page.Objects[limit-1].Key
	}
	return page, nil
}

// listScope reports whether key is inside prefix, and whether a directory
// that is not inside it is an ancestor the walk still has to enter.
func listScope(key, prefix string) (match, descend bool) {
	if prefix == "" || strings.HasPrefix(key, prefix) {
		return true, true
	}
	if strings.HasPrefix(prefix, key+"/") {
		return false, true
	}
	return false, false
}

func (*Store) PresignGet(context.Context, string, time.Duration) (provider.SignedURL, error) {
	return provider.SignedURL{}, provider.ErrUnsupported
}
func (*Store) PresignPut(context.Context, string, string, int64, time.Duration) (provider.SignedURL, error) {
	return provider.SignedURL{}, provider.ErrUnsupported
}

var _ provider.Store = (*Store)(nil)
