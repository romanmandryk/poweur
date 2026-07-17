package files

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// FileMeta is the per-file metadata index entry.
type FileMeta struct {
	ETag     string    `json:"etag"` // hex sha256 of content ("" for directories)
	Size     int64     `json:"size"`
	ModTime  time.Time `json:"mtime"`
	ChangeID int64     `json:"change_id"`
	Dir      bool      `json:"dir,omitempty"`
}

// treeIndex is the persisted per-identity metadata index.
type treeIndex struct {
	ChangeID int64               `json:"change_id"`
	Files    map[string]FileMeta `json:"files"`
}

// Index maintains per-identity content-hash etags, the monotonic change
// counter and the changes journal that back EPIC-004 sync. Persisted as
// JSON at identities/<id>/meta/ — outside the visible tree. (An S3-class
// StorageProvider pairs with its own metadata persistence later; the
// journal/index API is the provider-agnostic surface.)
type Index struct {
	mu       sync.Mutex
	trees    map[string]*treeIndex
	journals map[string]*journalState
	homeDir  func(identity string) (string, error)
}

func NewIndex(homeDir func(identity string) (string, error)) *Index {
	return &Index{
		trees:    make(map[string]*treeIndex),
		journals: make(map[string]*journalState),
		homeDir:  homeDir,
	}
}

func (ix *Index) path(identity string) (string, error) {
	home, err := ix.homeDir(identity)
	if err != nil || home == "" {
		return "", err
	}
	return filepath.Join(home, "meta", "files-index.json"), nil
}

func (ix *Index) tree(identity string) *treeIndex {
	key := strings.ToLower(identity)
	if t, ok := ix.trees[key]; ok {
		return t
	}
	t := &treeIndex{Files: make(map[string]FileMeta)}
	if p, err := ix.path(identity); err == nil && p != "" {
		if raw, err := os.ReadFile(p); err == nil {
			_ = json.Unmarshal(raw, t)
			if t.Files == nil {
				t.Files = make(map[string]FileMeta)
			}
		}
	}
	ix.trees[key] = t
	return t
}

func (ix *Index) persistLocked(identity string) {
	p, err := ix.path(identity)
	if err != nil || p == "" {
		return
	}
	t := ix.trees[strings.ToLower(identity)]
	raw, err := json.Marshal(t)
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, p)
}

// RecordWrite stores fresh metadata for path, bumps the change counter and
// journals a put. actor is the identity that performed the write ("" for
// relay-internal writes).
func (ix *Index) RecordWrite(identity, path, etag string, size int64, mtime time.Time, actor string) FileMeta {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	t := ix.tree(identity)
	t.ChangeID++
	meta := FileMeta{ETag: etag, Size: size, ModTime: mtime, ChangeID: t.ChangeID}
	t.Files[path] = meta
	ix.persistLocked(identity)
	ix.appendJournalLocked(identity, JournalRecord{
		ChangeID: t.ChangeID, Op: OpPut, Path: path,
		ETag: etag, Size: size, ModTime: mtime,
		Actor: actor, Time: time.Now().UTC(),
	})
	return meta
}

// RecordMkdir stores a directory entry, bumps the change counter and
// journals a mkdir.
func (ix *Index) RecordMkdir(identity, path string, mtime time.Time, actor string) FileMeta {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	t := ix.tree(identity)
	t.ChangeID++
	meta := FileMeta{ModTime: mtime, ChangeID: t.ChangeID, Dir: true}
	t.Files[path] = meta
	ix.persistLocked(identity)
	ix.appendJournalLocked(identity, JournalRecord{
		ChangeID: t.ChangeID, Op: OpMkdir, Path: path,
		ModTime: mtime, Actor: actor, Time: time.Now().UTC(),
	})
	return meta
}

// RecordDelete drops path (and any children), bumps the change counter and
// journals one delete covering the subtree.
func (ix *Index) RecordDelete(identity, path, actor string) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	t := ix.tree(identity)
	t.ChangeID++
	for p := range t.Files {
		if Under(p, path) {
			delete(t.Files, p)
		}
	}
	ix.persistLocked(identity)
	ix.appendJournalLocked(identity, JournalRecord{
		ChangeID: t.ChangeID, Op: OpDelete, Path: path,
		Actor: actor, Time: time.Now().UTC(),
	})
}

// RecordRename moves metadata for old subtree to the new prefix. The
// journal sees a move as delete(old) + put/mkdir per moved entry (v1
// moves-as-delete+put), each with its own change_id so cursors stay strict.
func (ix *Index) RecordRename(identity, oldPath, newPath, actor string) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	t := ix.tree(identity)
	now := time.Now().UTC()
	t.ChangeID++
	ix.appendJournalLocked(identity, JournalRecord{
		ChangeID: t.ChangeID, Op: OpDelete, Path: oldPath,
		Actor: actor, Time: now,
	})
	var movedFrom []string
	for p := range t.Files {
		if Under(p, oldPath) {
			movedFrom = append(movedFrom, p)
		}
	}
	sort.Strings(movedFrom)
	for _, p := range movedFrom {
		m := t.Files[p]
		delete(t.Files, p)
		t.ChangeID++
		m.ChangeID = t.ChangeID
		dst := newPath + strings.TrimPrefix(p, oldPath)
		t.Files[dst] = m
		op := OpPut
		if m.Dir {
			op = OpMkdir
		}
		ix.appendJournalLocked(identity, JournalRecord{
			ChangeID: t.ChangeID, Op: op, Path: dst,
			ETag: m.ETag, Size: m.Size, ModTime: m.ModTime,
			Actor: actor, Time: now,
		})
	}
	ix.persistLocked(identity)
}

// Get returns the index entry for path, if present.
func (ix *Index) Get(identity, path string) (FileMeta, bool) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	m, ok := ix.tree(identity).Files[path]
	return m, ok
}

// ChangeID returns the identity's current change counter.
func (ix *Index) ChangeID(identity string) int64 {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return ix.tree(identity).ChangeID
}

// ETagFromReader hashes r fully and returns (etag, bytesRead).
func ETagFromReader(r io.Reader) (string, int64, error) {
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// FormatETag renders a metadata etag as an HTTP entity tag.
func FormatETag(meta FileMeta) string {
	if meta.ETag == "" {
		return ""
	}
	short := meta.ETag
	if len(short) > 32 {
		short = short[:32]
	}
	return `"sha256-` + short + `"`
}
