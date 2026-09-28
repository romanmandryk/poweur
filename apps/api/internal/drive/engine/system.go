package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/poweur/api/internal/drive/provider"
)

// The system zone holds the plaintext documents the relay reads or writes:
// `.poweur/public/*` (served to everyone), `.poweur/relay/*` (owner-written
// settings the relay enforces) and `.poweur/state/*` (relay-written records).
// They are drive files in the owner's view, but not encrypted tree nodes: the
// relay could not find an encrypted name, and these files are relay-readable
// by definition. Each write is a journalled operation like any commit, so
// system files share the drive's durability, quota, changes feed and events,
// and a relay rebuilds them from the store alone.
//
// Content is stored once per distinct document at `system/<sha256>`; the
// journal maps paths to hashes. The engine never interprets the bytes —
// callers validate before they write.

const kindSystem = "system"

// Writers of system files.
const (
	WriterOwner = "owner"
	WriterRelay = "relay"
)

// MaxSystemFileBytes caps one system document (an avatar is the largest).
const MaxSystemFileBytes = 2 << 20

// systemFile is a path's current document.
type systemFile struct {
	Hash    string    `json:"hash"`
	Size    int64     `json:"size"`
	Writer  string    `json:"writer"`
	Updated time.Time `json:"updated"`
}

// systemOp writes (Hash set) or deletes (Hash empty) one path.
type systemOp struct {
	Path   string `json:"path"`
	Hash   string `json:"hash,omitempty"`
	Size   int64  `json:"size,omitempty"`
	Writer string `json:"writer"`
}

// SystemInfo describes a stored system document.
type SystemInfo struct {
	Path    string    `json:"path"`
	Hash    string    `json:"hash"`
	Size    int64     `json:"size"`
	Writer  string    `json:"writer"`
	Updated time.Time `json:"updated"`
}

// ValidSystemPath accepts `.poweur/{public,relay,state}/<name>` with a flat
// name of lowercase letters, digits, '-', '_' and '.', not starting with a
// dot and at most 64 bytes.
func ValidSystemPath(path string) bool {
	for _, zone := range []string{".poweur/public/", ".poweur/relay/", ".poweur/state/"} {
		name, ok := strings.CutPrefix(path, zone)
		if !ok {
			continue
		}
		if name == "" || len(name) > 64 || strings.HasPrefix(name, ".") || strings.Contains(name, "..") {
			return false
		}
		for _, c := range name {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
				return false
			}
		}
		return true
	}
	return false
}

// systemBlobCache keeps documents the relay reads on every message (policy,
// contacts) in memory; they are small and immutable by hash.
const systemBlobCacheBytes = 256 << 10

// SystemRead returns a system document and its SHA-256, or ErrNotFound.
func (e *Engine) SystemRead(ctx context.Context, driveID, path string) ([]byte, string, error) {
	if !ValidSystemPath(path) {
		return nil, "", ErrInvalid
	}
	h, err := e.open(ctx, driveID)
	if err != nil {
		return nil, "", err
	}
	file := h.st.System[path]
	if file == nil {
		h.mu.Unlock()
		return nil, "", ErrNotFound
	}
	if raw, ok := h.blobs[file.Hash]; ok {
		h.mu.Unlock()
		return append([]byte(nil), raw...), file.Hash, nil
	}
	prefix, hash := h.prefix, file.Hash
	h.mu.Unlock()
	obj, err := e.opts.Store.Get(ctx, prefix+"system/"+hash, nil)
	if err != nil {
		return nil, "", fmt.Errorf("system file %s: %w", path, notFound(err))
	}
	if sum := sha256.Sum256(obj.Data); hex.EncodeToString(sum[:]) != hash {
		return nil, "", fmt.Errorf("system file %s does not match its journalled hash", path)
	}
	if len(obj.Data) <= systemBlobCacheBytes {
		h.mu.Lock()
		if h.blobs != nil {
			h.blobs[hash] = obj.Data
		}
		h.mu.Unlock()
	}
	return append([]byte(nil), obj.Data...), hash, nil
}

// SystemList lists the system documents under prefix (e.g. ".poweur/public/").
func (e *Engine) SystemList(ctx context.Context, driveID, prefix string) ([]SystemInfo, error) {
	h, err := e.open(ctx, driveID)
	if err != nil {
		return nil, err
	}
	defer h.mu.Unlock()
	var out []SystemInfo
	for path, f := range h.st.System {
		if strings.HasPrefix(path, prefix) {
			out = append(out, SystemInfo{Path: path, Hash: f.Hash, Size: f.Size, Writer: f.Writer, Updated: f.Updated})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// SystemWrite stores a document at path for writer. With base non-nil the
// write is conditional: *base "" requires the path to be absent, otherwise
// it must equal the current hash (ErrConflict when not). The owner's writes
// count against quota; the relay's own records do not.
func (e *Engine) SystemWrite(ctx context.Context, driveID, path string, data []byte, writer string, base *string) (string, error) {
	if !ValidSystemPath(path) || (writer != WriterOwner && writer != WriterRelay) {
		return "", ErrInvalid
	}
	if len(data) > MaxSystemFileBytes {
		return "", fmt.Errorf("%w: system file exceeds %d bytes", ErrInvalid, MaxSystemFileBytes)
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	h, err := e.open(ctx, driveID)
	if err != nil {
		return "", err
	}
	defer h.mu.Unlock()
	if err := e.catchUp(ctx, h); err != nil {
		return "", err
	}
	current := h.st.System[path]
	if err := checkBase(current, base); err != nil {
		return "", err
	}
	if current != nil && current.Hash == hash {
		return hash, nil
	}
	if writer == WriterOwner {
		if quota := e.opts.Quota(h.st.Drive); quota > 0 {
			grow := int64(len(data))
			if current != nil {
				grow -= current.Size
			}
			if grow > 0 && h.st.Used+grow > quota {
				return "", ErrQuota
			}
		}
	}
	if _, err := e.opts.Store.PutIf(ctx, h.prefix+"system/"+hash, data, ""); err != nil && !isPrecondition(err) {
		return "", err
	}
	old := ""
	if current != nil {
		old = current.Hash
	}
	op := journalOp{Kind: kindSystem, At: e.now(), System: &systemOp{Path: path, Hash: hash, Size: int64(len(data)), Writer: writer}}
	if err := e.publish(ctx, h, op); err != nil {
		return "", err
	}
	if len(data) <= systemBlobCacheBytes {
		h.blobs[hash] = append([]byte(nil), data...)
	}
	e.releaseBlob(ctx, h, old)
	return hash, nil
}

// SystemDelete removes a document; ErrNotFound when it is absent.
func (e *Engine) SystemDelete(ctx context.Context, driveID, path, writer string, base *string) error {
	if !ValidSystemPath(path) || (writer != WriterOwner && writer != WriterRelay) {
		return ErrInvalid
	}
	h, err := e.open(ctx, driveID)
	if err != nil {
		return err
	}
	defer h.mu.Unlock()
	if err := e.catchUp(ctx, h); err != nil {
		return err
	}
	current := h.st.System[path]
	if current == nil {
		return ErrNotFound
	}
	if err := checkBase(current, base); err != nil {
		return err
	}
	old := current.Hash
	if err := e.publish(ctx, h, journalOp{Kind: kindSystem, At: e.now(), System: &systemOp{Path: path, Writer: writer}}); err != nil {
		return err
	}
	e.releaseBlob(ctx, h, old)
	return nil
}

func isPrecondition(err error) bool { return errors.Is(err, provider.ErrPrecondition) }

func checkBase(current *systemFile, base *string) error {
	switch {
	case base == nil:
		return nil
	case *base == "" && current != nil:
		return ErrExists
	case *base != "" && (current == nil || current.Hash != *base):
		return ErrConflict
	}
	return nil
}

// releaseBlob deletes a document's bytes once no path refers to them. A
// failed delete leaves an orphan for Collect. Caller holds h.mu.
func (e *Engine) releaseBlob(ctx context.Context, h *driveHandle, hash string) {
	if hash == "" || h.st == nil {
		return
	}
	for _, f := range h.st.System {
		if f.Hash == hash {
			return
		}
	}
	delete(h.blobs, hash)
	_ = e.opts.Store.Delete(ctx, h.prefix+"system/"+hash)
}

// applySystem applies a system operation to st.
func applySystem(st *state, seq uint64, op journalOp) error {
	s := op.System
	if s == nil || !ValidSystemPath(s.Path) {
		return fmt.Errorf("invalid system operation")
	}
	if prev := st.System[s.Path]; prev != nil {
		st.Used -= prev.Size
	}
	change := Change{Seq: seq, Path: s.Path, At: op.At}
	if s.Hash == "" {
		if st.System[s.Path] == nil {
			return fmt.Errorf("system delete of absent %s", s.Path)
		}
		delete(st.System, s.Path)
		change.Operation = "system.delete"
	} else {
		st.System[s.Path] = &systemFile{Hash: s.Hash, Size: s.Size, Writer: s.Writer, Updated: op.At}
		st.Used += s.Size
		change.Operation, change.Version = "system.put", s.Hash
	}
	st.Changes = append(st.Changes, change)
	return nil
}
