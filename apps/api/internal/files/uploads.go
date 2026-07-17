package files

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// UploadTTL is how long a partial upload survives without completing.
const UploadTTL = 24 * time.Hour

// ErrUploadOffset is returned when a chunk's declared offset does not match
// the bytes already spooled (the client must HEAD and resume from there).
var ErrUploadOffset = errors.New("upload offset mismatch")

// ErrUploadOverflow is returned when a chunk would exceed the declared
// upload length.
var ErrUploadOverflow = errors.New("chunk exceeds declared upload length")

// UploadInfo describes a chunked upload in progress (E04-T3). Spool bytes
// live outside the visible tree at meta/uploads/<id>.part with a JSON
// sidecar; only final assembly goes through the StorageProvider, so an
// S3-class provider can plug in without touching this layer (or later swap
// the spool for native multipart uploads behind the same endpoints).
type UploadInfo struct {
	ID        string    `json:"id"`
	Path      string    `json:"path"` // clean tree path of the final file
	Length    int64     `json:"length"`
	Offset    int64     `json:"offset"`
	Actor     string    `json:"actor,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Uploads manages partial-upload spools per identity.
type Uploads struct {
	mu      sync.Mutex
	homeDir func(identity string) (string, error)
}

func NewUploads(homeDir func(identity string) (string, error)) *Uploads {
	return &Uploads{homeDir: homeDir}
}

func (u *Uploads) dir(identity string) (string, error) {
	home, err := u.homeDir(identity)
	if err != nil || home == "" {
		return "", err
	}
	return filepath.Join(home, "meta", "uploads"), nil
}

func (u *Uploads) paths(identity, id string) (part, sidecar string, err error) {
	if id == "" || strings.ContainsAny(id, "/\\.") {
		return "", "", errors.New("invalid upload id")
	}
	dir, err := u.dir(identity)
	if err != nil || dir == "" {
		return "", "", errors.New("storage not configured")
	}
	return filepath.Join(dir, id+".part"), filepath.Join(dir, id+".json"), nil
}

func (u *Uploads) writeSidecarLocked(identity string, info UploadInfo) error {
	_, sidecar, err := u.paths(identity, info.ID)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(info)
	if err != nil {
		return err
	}
	tmp := sidecar + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, sidecar)
}

// Create starts a new upload for path with a declared total length and
// sweeps this identity's expired spools as a side effect.
func (u *Uploads) Create(identity, path string, length int64, actor string) (UploadInfo, error) {
	if length < 0 {
		return UploadInfo{}, errors.New("upload length required")
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	dir, err := u.dir(identity)
	if err != nil || dir == "" {
		return UploadInfo{}, errors.New("storage not configured")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return UploadInfo{}, err
	}
	u.sweepLocked(dir)
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return UploadInfo{}, err
	}
	info := UploadInfo{
		ID:        "up" + hex.EncodeToString(buf),
		Path:      path,
		Length:    length,
		Actor:     actor,
		CreatedAt: time.Now().UTC(),
	}
	part, _, err := u.paths(identity, info.ID)
	if err != nil {
		return UploadInfo{}, err
	}
	if err := os.WriteFile(part, nil, 0o600); err != nil {
		return UploadInfo{}, err
	}
	if err := u.writeSidecarLocked(identity, info); err != nil {
		return UploadInfo{}, err
	}
	return info, nil
}

// Get returns the upload's current state.
func (u *Uploads) Get(identity, id string) (UploadInfo, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.getLocked(identity, id)
}

func (u *Uploads) getLocked(identity, id string) (UploadInfo, error) {
	_, sidecar, err := u.paths(identity, id)
	if err != nil {
		return UploadInfo{}, err
	}
	raw, err := os.ReadFile(sidecar)
	if err != nil {
		return UploadInfo{}, fmt.Errorf("upload not found")
	}
	var info UploadInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return UploadInfo{}, err
	}
	return info, nil
}

// Append spools a chunk at offset (which must equal the bytes already
// received) and returns the new offset.
func (u *Uploads) Append(identity, id string, offset int64, r io.Reader) (int64, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	info, err := u.getLocked(identity, id)
	if err != nil {
		return 0, err
	}
	if offset != info.Offset {
		return info.Offset, ErrUploadOffset
	}
	part, _, err := u.paths(identity, id)
	if err != nil {
		return info.Offset, err
	}
	f, err := os.OpenFile(part, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return info.Offset, err
	}
	// Never spool past the declared length (quota was checked against it).
	n, err := io.Copy(f, io.LimitReader(r, info.Length-info.Offset+1))
	closeErr := f.Close()
	info.Offset += n
	if info.Offset > info.Length {
		_ = os.Truncate(part, info.Length)
		info.Offset = info.Length
		_ = u.writeSidecarLocked(identity, info)
		return info.Offset, ErrUploadOverflow
	}
	if werr := u.writeSidecarLocked(identity, info); werr != nil && err == nil {
		err = werr
	}
	if err == nil {
		err = closeErr
	}
	return info.Offset, err
}

// Assemble streams the completed spool into the target path through the
// StorageProvider, records the index/journal entry and removes the spool.
func (u *Uploads) Assemble(ctx context.Context, provider StorageProvider, index *Index, identity, id string) (FileMeta, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	info, err := u.getLocked(identity, id)
	if err != nil {
		return FileMeta{}, err
	}
	if info.Offset != info.Length {
		return FileMeta{}, fmt.Errorf("upload incomplete (%d of %d bytes)", info.Offset, info.Length)
	}
	part, sidecar, err := u.paths(identity, id)
	if err != nil {
		return FileMeta{}, err
	}
	src, err := os.Open(part)
	if err != nil {
		return FileMeta{}, err
	}
	defer src.Close()
	dst, err := provider.OpenFile(ctx, identity, info.Path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return FileMeta{}, err
	}
	h := sha256.New()
	size, err := io.Copy(dst, io.TeeReader(src, h))
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return FileMeta{}, err
	}
	meta := index.RecordWrite(identity, info.Path, hex.EncodeToString(h.Sum(nil)), size, time.Now().UTC(), info.Actor)
	_ = os.Remove(part)
	_ = os.Remove(sidecar)
	return meta, nil
}

// Cancel removes a partial upload.
func (u *Uploads) Cancel(identity, id string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	part, sidecar, err := u.paths(identity, id)
	if err != nil {
		return err
	}
	if _, err := os.Stat(sidecar); err != nil {
		return fmt.Errorf("upload not found")
	}
	_ = os.Remove(part)
	return os.Remove(sidecar)
}

// sweepLocked deletes spools older than UploadTTL.
func (u *Uploads) sweepLocked(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-UploadTTL)
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, e.Name()))
	}
}
