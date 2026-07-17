package files

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// File is the handle returned by StorageProvider.OpenFile. It matches the
// subset of webdav.File the DAV layer needs.
type File interface {
	io.Closer
	io.Reader
	io.Seeker
	io.Writer
	Readdir(count int) ([]os.FileInfo, error)
	Stat() (os.FileInfo, error)
}

// StorageProvider abstracts file-body storage per identity. Paths are clean
// tree paths (see CleanPath): slash-separated, no leading slash, "" = root.
//
// The contract deliberately avoids POSIX assumptions so an S3-compatible
// provider can implement it later (reviewed against S3 semantics):
//   - Rename MAY be copy+delete (S3 has no atomic rename); callers must not
//     rely on rename atomicity for correctness, only the metadata index does
//     (and it is rebuilt lazily when stale).
//   - Directories MAY be virtual (S3 prefixes); Mkdir on such a backend may
//     record a zero-byte marker object.
//   - Listings MAY be eventually consistent; the DAV layer treats the
//     metadata index, not the listing, as the sync source of truth.
//   - No *os.File or on-disk path leaks through the interface.
type StorageProvider interface {
	// Name identifies the provider ("relay-fs", "s3").
	Name() string
	// EnsureTree materializes the layout skeleton (five roots + poweur-sys
	// subdirs) so clients see a stable tree after registration. On backends
	// with virtual directories this may be a no-op or write marker objects.
	EnsureTree(ctx context.Context, identity string) error
	// Mkdir creates a directory (parents must exist).
	Mkdir(ctx context.Context, identity, name string) error
	// OpenFile opens a file. flag is os.O_RDONLY / os.O_RDWR|os.O_CREATE|... .
	OpenFile(ctx context.Context, identity, name string, flag int, perm os.FileMode) (File, error)
	// RemoveAll removes a file or directory subtree.
	RemoveAll(ctx context.Context, identity, name string) error
	// Rename moves a file or directory subtree.
	Rename(ctx context.Context, identity, oldName, newName string) error
	// Stat describes a file or directory.
	Stat(ctx context.Context, identity, name string) (os.FileInfo, error)
	// UsedBytes returns the total bytes stored for identity's visible tree.
	UsedBytes(ctx context.Context, identity string) (int64, error)
}

// ErrNotSupported is returned by providers for operations the backend
// cannot express.
var ErrNotSupported = errors.New("operation not supported by storage provider")

// ---------------------------------------------------------------------------
// relay-fs provider: local filesystem under root/identities/<sanitized-id>/.
// The DAV tree root IS the identity home directory, so poweur-sys/public/
// id.json is the same file the identity store writes and /.well-known serves.
// ---------------------------------------------------------------------------

// FSProvider stores identity trees on the local filesystem.
type FSProvider struct {
	root string // POWEUR_DATA
	// homeDir maps an identity to its on-disk home directory.
	homeDir func(identity string) (string, error)
}

// NewFSProvider returns the relay-fs provider rooted at dataDir (POWEUR_DATA).
// homeDir resolves an identity to its home directory and must reject
// path-traversal (the identity store's sanitized mapping).
func NewFSProvider(dataDir string, homeDir func(identity string) (string, error)) *FSProvider {
	return &FSProvider{root: dataDir, homeDir: homeDir}
}

func (p *FSProvider) Name() string { return "relay-fs" }

// resolve maps (identity, clean tree path) to an on-disk path, refusing
// symlinks in every component below the home directory.
func (p *FSProvider) resolve(identity, name string) (string, error) {
	home, err := p.homeDir(identity)
	if err != nil {
		return "", err
	}
	if home == "" {
		return "", errors.New("storage not configured")
	}
	clean, err := CleanPath(name)
	if err != nil {
		return "", err
	}
	if clean == "" {
		return home, nil
	}
	// Refuse symlinks in every existing component below home.
	cur := home
	for _, seg := range strings.Split(clean, "/") {
		cur = filepath.Join(cur, seg)
		fi, err := os.Lstat(cur)
		if err != nil {
			if os.IsNotExist(err) {
				break // rest of the path doesn't exist yet — fine for creates
			}
			return "", err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return "", fs.ErrNotExist // symlinks are invisible by spec
		}
	}
	return filepath.Join(home, filepath.FromSlash(clean)), nil
}

// EnsureTree materializes the five roots (and poweur-sys subdirs) for an
// identity so DAV clients see a stable skeleton.
func (p *FSProvider) EnsureTree(ctx context.Context, identity string) error {
	_ = ctx
	for _, dir := range []string{RootPublic, RootShared, RootPrivate, RootApps, SysPublic, SysRelay, SysPrivate, sharesDir, groupsDir} {
		onDisk, err := p.resolve(identity, dir)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(onDisk, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func (p *FSProvider) Mkdir(ctx context.Context, identity, name string) error {
	onDisk, err := p.resolve(identity, name)
	if err != nil {
		return err
	}
	return os.Mkdir(onDisk, 0o700)
}

func (p *FSProvider) OpenFile(ctx context.Context, identity, name string, flag int, perm os.FileMode) (File, error) {
	clean, err := CleanPath(name)
	if err != nil {
		return nil, err
	}
	onDisk, err := p.resolve(identity, clean)
	if err != nil {
		return nil, err
	}
	if clean == "" || IsRoot(clean) || clean == SysPublic || clean == SysRelay || clean == SysPrivate {
		// Root directories are stable: open read-only for listing.
		if flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE) != 0 {
			return nil, fs.ErrPermission
		}
		_ = p.EnsureTree(ctx, identity)
	}
	f, err := os.OpenFile(onDisk, flag, perm)
	if err != nil {
		return nil, err
	}
	if clean == "" {
		return &rootDir{f: f}, nil
	}
	return f, nil
}

func (p *FSProvider) RemoveAll(ctx context.Context, identity, name string) error {
	clean, err := CleanPath(name)
	if err != nil {
		return err
	}
	if clean == "" || IsRoot(clean) {
		return fs.ErrPermission
	}
	onDisk, err := p.resolve(identity, clean)
	if err != nil {
		return err
	}
	return os.RemoveAll(onDisk)
}

func (p *FSProvider) Rename(ctx context.Context, identity, oldName, newName string) error {
	oldClean, err := CleanPath(oldName)
	if err != nil {
		return err
	}
	newClean, err := CleanPath(newName)
	if err != nil {
		return err
	}
	if oldClean == "" || IsRoot(oldClean) || newClean == "" || IsRoot(newClean) {
		return fs.ErrPermission
	}
	oldDisk, err := p.resolve(identity, oldClean)
	if err != nil {
		return err
	}
	newDisk, err := p.resolve(identity, newClean)
	if err != nil {
		return err
	}
	return os.Rename(oldDisk, newDisk)
}

func (p *FSProvider) Stat(ctx context.Context, identity, name string) (os.FileInfo, error) {
	onDisk, err := p.resolve(identity, name)
	if err != nil {
		return nil, err
	}
	fi, err := os.Lstat(onDisk)
	if err != nil {
		return nil, err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return nil, fs.ErrNotExist
	}
	return fi, nil
}

func (p *FSProvider) UsedBytes(ctx context.Context, identity string) (int64, error) {
	home, err := p.homeDir(identity)
	if err != nil || home == "" {
		return 0, err
	}
	var total int64
	for _, root := range Roots {
		dir := filepath.Join(home, root)
		_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil || d.IsDir() {
				return nil //nolint:nilerr — missing subtrees contribute zero
			}
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
			return nil
		})
	}
	return total, nil
}

// rootDir wraps the identity home directory so Readdir only exposes the five
// layout roots (operational dirs like meta/ or spool/ stay invisible).
type rootDir struct {
	f *os.File
}

func (r *rootDir) Close() error                                 { return r.f.Close() }
func (r *rootDir) Read(p []byte) (int, error)                   { return 0, fs.ErrInvalid }
func (r *rootDir) Write(p []byte) (int, error)                  { return 0, fs.ErrPermission }
func (r *rootDir) Seek(offset int64, whence int) (int64, error) { return 0, fs.ErrInvalid }
func (r *rootDir) Stat() (os.FileInfo, error)                   { return r.f.Stat() }

func (r *rootDir) Readdir(count int) ([]os.FileInfo, error) {
	all, err := r.f.Readdir(-1)
	if err != nil {
		return nil, err
	}
	var out []os.FileInfo
	for _, fi := range all {
		if fi.IsDir() && IsRoot(fi.Name()) {
			out = append(out, fi)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	if count > 0 && len(out) > count {
		out = out[:count]
	}
	if count > 0 && len(out) == 0 {
		return nil, io.EOF
	}
	return out, nil
}

// virtualDirInfo lets callers synthesize directory entries when needed.
type virtualDirInfo struct{ name string }

func (v virtualDirInfo) Name() string       { return v.name }
func (v virtualDirInfo) Size() int64        { return 0 }
func (v virtualDirInfo) Mode() os.FileMode  { return os.ModeDir | 0o700 }
func (v virtualDirInfo) ModTime() time.Time { return time.Time{} }
func (v virtualDirInfo) IsDir() bool        { return true }
func (v virtualDirInfo) Sys() any           { return nil }
