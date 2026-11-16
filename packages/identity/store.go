package identity

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ErrDocumentRollback is returned by Resolve when a freshly fetched document
// is older (by updated_at) than the one already held for that identity.
// Documents are signed by their owner, so updated_at only moves forward for
// an honest one; an older answer is a replay. The stored document is kept.
var ErrDocumentRollback = errors.New("identity_document_rollback: fetched identity document is older than the stored one")

// StoredDocument is the last verified document Resolve saw for an identity
// (EPIC-029 D1). Raw holds the bytes as they were fetched, so fields this
// build doesn't know about survive a round trip through the store.
type StoredDocument struct {
	Identity  string
	Raw       []byte
	FetchedAt time.Time
}

// Store keeps the last verified identity document per identity. Resolve
// writes to it after every successful web fetch and reads it back only when a
// later fetch fails for a reason that says nothing about the identity itself
// (network error, timeout, 5xx).
//
// Implementations must be safe for concurrent use.
type Store interface {
	Get(identity string) (StoredDocument, bool)
	Put(identity string, raw []byte, fetchedAt time.Time) error
}

// ReadOnlyStore wraps s so that Resolve can fall back to it and apply the
// rollback guard without writing. A caller that wants to store a document
// only after a further check of its own (the relay stores a sender's document
// once the message signature verified) passes this to Resolve and calls
// s.Put itself.
func ReadOnlyStore(s Store) Store {
	if s == nil {
		return nil
	}
	return readOnlyStore{s}
}

type readOnlyStore struct{ inner Store }

func (r readOnlyStore) Get(identity string) (StoredDocument, bool) { return r.inner.Get(identity) }
func (r readOnlyStore) Put(string, []byte, time.Time) error        { return nil }

// MemoryStore is a Store that lives for the life of the process: tests, and
// relays that have no data directory.
type MemoryStore struct {
	mu         sync.Mutex
	items      map[string]StoredDocument
	maxEntries int
}

// NewMemoryStore returns an empty in-memory store. maxEntries caps how many
// identities it holds (the ones fetched longest ago are dropped first); zero
// or negative means no cap.
func NewMemoryStore(maxEntries int) *MemoryStore {
	return &MemoryStore{items: make(map[string]StoredDocument), maxEntries: maxEntries}
}

func (m *MemoryStore) Get(identity string) (StoredDocument, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sd, ok := m.items[storeKey(identity)]
	if !ok {
		return StoredDocument{}, false
	}
	sd.Raw = append([]byte(nil), sd.Raw...)
	return sd, true
}

func (m *MemoryStore) Put(identity string, raw []byte, fetchedAt time.Time) error {
	key := storeKey(identity)
	if err := ValidateIdentityName(key); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[key] = StoredDocument{Identity: key, Raw: append([]byte(nil), raw...), FetchedAt: fetchedAt.UTC()}
	if m.maxEntries > 0 && len(m.items) > m.maxEntries {
		ages := make(map[string]time.Time, len(m.items))
		for k, v := range m.items {
			ages[k] = v.FetchedAt
		}
		for _, k := range oldestEntries(ages, m.maxEntries) {
			delete(m.items, k)
		}
	}
	return nil
}

// FileStore is a Store with one JSON file per identity under a directory:
// `<dir>/<identity>.json`, written with an atomic rename and mode 0600.
type FileStore struct {
	dir        string
	maxEntries int

	mu sync.Mutex
	// index maps identity to fetched_at for eviction. It is built from the
	// directory listing on first use and only when a cap is set.
	index map[string]time.Time
}

// NewFileStore returns a store rooted at dir, which is created on first
// write. maxEntries caps how many identities are kept (the ones fetched
// longest ago are removed first), so names an attacker makes up can't fill
// the disk; zero or negative means no cap.
func NewFileStore(dir string, maxEntries int) *FileStore {
	return &FileStore{dir: dir, maxEntries: maxEntries}
}

type storedFile struct {
	Identity  string          `json:"identity"`
	FetchedAt string          `json:"fetched_at"`
	Document  json.RawMessage `json:"document"`
}

const storeFileSuffix = ".json"

func (f *FileStore) path(identity string) (string, error) {
	key := storeKey(identity)
	if err := ValidateIdentityName(key); err != nil {
		return "", err
	}
	// ValidateIdentityName already limits labels to DNS characters; this is
	// the belt to its braces, because the name becomes a file name.
	if strings.ContainsAny(key, `/\`) || strings.Contains(key, "..") {
		return "", fmt.Errorf("identity %q is not a safe file name", key)
	}
	return filepath.Join(f.dir, key+storeFileSuffix), nil
}

func (f *FileStore) Get(identity string) (StoredDocument, bool) {
	path, err := f.path(identity)
	if err != nil {
		return StoredDocument{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return StoredDocument{}, false
	}
	var file storedFile
	if err := json.Unmarshal(data, &file); err != nil {
		return StoredDocument{}, false
	}
	fetchedAt, err := time.Parse(time.RFC3339, file.FetchedAt)
	if err != nil || len(file.Document) == 0 || storeKey(file.Identity) != storeKey(identity) {
		return StoredDocument{}, false
	}
	return StoredDocument{Identity: storeKey(identity), Raw: []byte(file.Document), FetchedAt: fetchedAt}, true
}

func (f *FileStore) Put(identity string, raw []byte, fetchedAt time.Time) error {
	path, err := f.path(identity)
	if err != nil {
		return err
	}
	key := storeKey(identity)
	fetchedAt = fetchedAt.UTC()
	data, err := json.Marshal(storedFile{
		Identity:  key,
		FetchedAt: fetchedAt.Format(time.RFC3339),
		Document:  json.RawMessage(raw),
	})
	if err != nil {
		return err
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if err := os.MkdirAll(f.dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(f.dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	// The file's mtime doubles as fetched_at, so a restart can rebuild the
	// eviction index from a directory listing without opening every file.
	_ = os.Chtimes(path, fetchedAt, fetchedAt)

	if f.maxEntries > 0 {
		f.loadIndexLocked()
		f.index[key] = fetchedAt
		if len(f.index) > f.maxEntries {
			for _, old := range oldestEntries(f.index, f.maxEntries) {
				_ = os.Remove(filepath.Join(f.dir, old+storeFileSuffix))
				delete(f.index, old)
			}
		}
	}
	return nil
}

func (f *FileStore) loadIndexLocked() {
	if f.index != nil {
		return
	}
	f.index = make(map[string]time.Time)
	entries, err := os.ReadDir(f.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, storeFileSuffix) || strings.HasPrefix(name, ".") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		f.index[strings.TrimSuffix(name, storeFileSuffix)] = info.ModTime()
	}
}

// oldestEntries returns the keys to drop so that ages shrinks to a little
// under max. It removes a batch rather than one entry, so a store at its cap
// doesn't sort the whole index on every write.
func oldestEntries(ages map[string]time.Time, max int) []string {
	keep := max - max/20
	if keep < 1 {
		keep = 1
	}
	drop := len(ages) - keep
	if drop <= 0 {
		return nil
	}
	keys := make([]string, 0, len(ages))
	for k := range ages {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if ages[keys[i]].Equal(ages[keys[j]]) {
			return keys[i] < keys[j]
		}
		return ages[keys[i]].Before(ages[keys[j]])
	})
	return keys[:drop]
}

func storeKey(identity string) string {
	return strings.ToLower(strings.TrimSpace(identity))
}
