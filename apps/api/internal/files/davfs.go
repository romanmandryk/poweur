package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io/fs"
	"os"
	gopath "path"
	"strings"
	"time"

	"golang.org/x/net/webdav"
)

type principalKey struct{}

// WithPrincipal attaches the authenticated principal to a request context.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom extracts the principal (Anonymous when absent).
func PrincipalFrom(ctx context.Context) Principal {
	if p, ok := ctx.Value(principalKey{}).(Principal); ok {
		return p
	}
	return Anonymous
}

// DavFS adapts a StorageProvider + Index + Permissions to webdav.FileSystem
// for a single identity's tree. The principal is read from the request
// context on every operation.
type DavFS struct {
	Owner    string
	Provider StorageProvider
	Index    *Index
	Perms    Permissions
}

var _ webdav.FileSystem = (*DavFS)(nil)

func (d *DavFS) allowed(ctx context.Context, path string, access Access) bool {
	return d.Perms.Allowed(d.Owner, PrincipalFrom(ctx), path, access)
}

// checkCaseCollision rejects creating a name that differs from an existing
// sibling only by case (keeps trees portable to case-insensitive backends).
func (d *DavFS) checkCaseCollision(ctx context.Context, clean string) error {
	parent := gopath.Dir(clean)
	if parent == "." {
		parent = ""
	}
	base := gopath.Base(clean)
	f, err := d.Provider.OpenFile(ctx, d.Owner, parent, os.O_RDONLY, 0)
	if err != nil {
		return nil // parent missing → provider will surface the real error
	}
	defer f.Close()
	entries, err := f.Readdir(-1)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if e.Name() != base && strings.EqualFold(e.Name(), base) {
			return fs.ErrExist
		}
	}
	return nil
}

func (d *DavFS) Mkdir(ctx context.Context, name string, perm os.FileMode) error {
	clean, err := ValidateTreePath(name)
	if err != nil {
		return fs.ErrInvalid
	}
	if clean == "" || IsRoot(clean) {
		return fs.ErrPermission
	}
	if !d.allowed(ctx, clean, AccessWrite) {
		return fs.ErrPermission
	}
	if err := d.checkCaseCollision(ctx, clean); err != nil {
		return err
	}
	if err := d.Provider.Mkdir(ctx, d.Owner, clean); err != nil {
		return err
	}
	d.Index.RecordMkdir(d.Owner, clean, time.Now().UTC(), PrincipalFrom(ctx).Identity)
	return nil
}

func (d *DavFS) OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (webdav.File, error) {
	clean, err := ValidateTreePath(name)
	if err != nil {
		return nil, fs.ErrInvalid
	}
	writing := flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_TRUNC|os.O_APPEND) != 0
	access := AccessRead
	if writing {
		access = AccessWrite
	}
	if !d.allowed(ctx, clean, access) {
		return nil, fs.ErrPermission
	}
	if writing && flag&os.O_CREATE != 0 {
		if _, err := d.Provider.Stat(ctx, d.Owner, clean); err != nil {
			if err := d.checkCaseCollision(ctx, clean); err != nil {
				return nil, err
			}
		}
	}
	f, err := d.Provider.OpenFile(ctx, d.Owner, clean, flag, perm)
	if err != nil {
		return nil, err
	}
	if writing {
		created := flag&os.O_TRUNC != 0
		return &hashingFile{File: f, fs: d, ctx: ctx, path: clean, h: sha256.New(), created: created}, nil
	}
	return &davFile{File: f, fs: d, ctx: ctx, path: clean}, nil
}

func (d *DavFS) RemoveAll(ctx context.Context, name string) error {
	clean, err := ValidateTreePath(name)
	if err != nil {
		return fs.ErrInvalid
	}
	if clean == "" || IsRoot(clean) {
		return fs.ErrPermission
	}
	if !d.allowed(ctx, clean, AccessWrite) {
		return fs.ErrPermission
	}
	if err := d.Provider.RemoveAll(ctx, d.Owner, clean); err != nil {
		return err
	}
	d.Index.RecordDelete(d.Owner, clean, PrincipalFrom(ctx).Identity)
	return nil
}

func (d *DavFS) Rename(ctx context.Context, oldName, newName string) error {
	oldClean, err := ValidateTreePath(oldName)
	if err != nil {
		return fs.ErrInvalid
	}
	newClean, err := ValidateTreePath(newName)
	if err != nil {
		return fs.ErrInvalid
	}
	if oldClean == "" || IsRoot(oldClean) || newClean == "" || IsRoot(newClean) {
		return fs.ErrPermission
	}
	if !d.allowed(ctx, oldClean, AccessWrite) || !d.allowed(ctx, newClean, AccessWrite) {
		return fs.ErrPermission
	}
	if err := d.checkCaseCollision(ctx, newClean); err != nil {
		return err
	}
	if err := d.Provider.Rename(ctx, d.Owner, oldClean, newClean); err != nil {
		return err
	}
	d.Index.RecordRename(d.Owner, oldClean, newClean, PrincipalFrom(ctx).Identity)
	return nil
}

func (d *DavFS) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	clean, err := ValidateTreePath(name)
	if err != nil {
		return nil, fs.ErrInvalid
	}
	if !d.allowed(ctx, clean, AccessRead) {
		return nil, fs.ErrPermission
	}
	fi, err := d.Provider.Stat(ctx, d.Owner, clean)
	if err != nil {
		return nil, err
	}
	return d.decorate(clean, fi), nil
}

// decorate attaches the index etag so webdav serves strong content-hash ETags.
func (d *DavFS) decorate(clean string, fi os.FileInfo) os.FileInfo {
	if fi.IsDir() {
		return fi
	}
	meta, ok := d.Index.Get(d.Owner, clean)
	if !ok || meta.Size != fi.Size() {
		return fi
	}
	return etagFileInfo{FileInfo: fi, etag: FormatETag(meta)}
}

type etagFileInfo struct {
	os.FileInfo
	etag string
}

// ETag implements the webdav.ETager-detected interface.
func (e etagFileInfo) ETag(ctx context.Context) (string, error) { return e.etag, nil }

// davFile filters directory listings to entries the principal may read.
type davFile struct {
	File
	fs   *DavFS
	ctx  context.Context
	path string
}

// Stat decorates with the content-hash etag; webdav's GET path stats the
// opened file handle (not the FileSystem), so this override is what makes
// ETags content hashes on downloads.
func (f *davFile) Stat() (os.FileInfo, error) {
	fi, err := f.File.Stat()
	if err != nil {
		return nil, err
	}
	return f.fs.decorate(f.path, fi), nil
}

func (f *davFile) Readdir(count int) ([]os.FileInfo, error) {
	entries, err := f.File.Readdir(count)
	if err != nil {
		return nil, err
	}
	out := entries[:0]
	for _, e := range entries {
		child := gopath.Join(f.path, e.Name())
		if f.path == "" {
			child = e.Name()
		}
		if !f.fs.allowed(f.ctx, child, AccessRead) {
			continue
		}
		out = append(out, f.fs.decorate(child, e))
	}
	return out, nil
}

// hashingFile computes the content etag incrementally during a straight
// streaming write (the webdav PUT path). If the client seeks mid-write the
// incremental hash is invalid, so the file is re-hashed on Close.
type hashingFile struct {
	File
	fs      *DavFS
	ctx     context.Context
	path    string
	h       hash.Hash
	size    int64
	seeked  bool
	wrote   bool
	created bool
}

func (f *hashingFile) Write(p []byte) (int, error) {
	n, err := f.File.Write(p)
	if n > 0 {
		f.wrote = true
		f.size += int64(n)
		if !f.seeked {
			_, _ = f.h.Write(p[:n])
		}
	}
	return n, err
}

func (f *hashingFile) Seek(offset int64, whence int) (int64, error) {
	// A seek before any write (e.g. stat probes) is harmless; after writes it
	// invalidates the running hash.
	if f.wrote {
		f.seeked = true
	}
	return f.File.Seek(offset, whence)
}

func (f *hashingFile) Close() error {
	if err := f.File.Close(); err != nil {
		return err
	}
	// An O_TRUNC open with no writes is an empty-body PUT — it still
	// changed the file and must be indexed and journaled.
	if !f.wrote && !f.created {
		return nil
	}
	etag := hex.EncodeToString(f.h.Sum(nil))
	size := f.size
	if f.seeked {
		// Re-read for a correct hash.
		rf, err := f.fs.Provider.OpenFile(f.ctx, f.fs.Owner, f.path, os.O_RDONLY, 0)
		if err == nil {
			if e, n, err := ETagFromReader(rf); err == nil {
				etag, size = e, n
			}
			_ = rf.Close()
		}
	}
	f.fs.Index.RecordWrite(f.fs.Owner, f.path, etag, size, time.Now().UTC(), PrincipalFrom(f.ctx).Identity)
	return nil
}
