package sync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	gopath "path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Entry is one manifest line from the relay.
type Entry struct {
	Path  string    `json:"path"`
	ETag  string    `json:"etag,omitempty"`
	Size  int64     `json:"size,omitempty"`
	MTime time.Time `json:"mtime"`
	Dir   bool      `json:"dir,omitempty"`
}

// Change mirrors the relay's journal record.
type Change struct {
	ChangeID int64  `json:"change_id"`
	Op       string `json:"op"` // put | mkdir | delete
	Path     string `json:"path"`
	ETag     string `json:"etag,omitempty"`
	Size     int64  `json:"size,omitempty"`
	Actor    string `json:"actor,omitempty"`
}

// Remote is the relay surface the engine reconciles against. The HTTP
// implementation lives in remote.go; tests use an in-memory fake.
type Remote interface {
	// Manifest returns the full visible tree + the cursor it corresponds to.
	Manifest(ctx context.Context) ([]Entry, string, error)
	// Changes returns all records after since (drained) + the new cursor.
	// resync=true means the cursor predates the journal — use Manifest.
	Changes(ctx context.Context, since string) (recs []Change, next string, resync bool, err error)
	Get(ctx context.Context, path string) (io.ReadCloser, error)
	Put(ctx context.Context, path string, r io.Reader, size int64) error
	Mkdir(ctx context.Context, path string) error
	Delete(ctx context.Context, path string) error
}

// DefaultRoots are the tree prefixes synced when none are specified:
// everything except poweur-sys (relay-managed config; sync it explicitly
// with --path if desired).
var DefaultRoots = []string{"public", "shared", "private", "apps"}

// Report summarizes one push/pull run.
type Report struct {
	Downloaded []string
	Uploaded   []string
	DeletedL   []string // deleted locally (pull)
	DeletedR   []string // deleted remotely (push)
	Conflicts  []string // conflicted-copy files created
	MkdirL     []string
	MkdirR     []string
}

func (r Report) Empty() bool {
	return len(r.Downloaded)+len(r.Uploaded)+len(r.DeletedL)+len(r.DeletedR)+
		len(r.Conflicts)+len(r.MkdirL)+len(r.MkdirR) == 0
}

// Engine reconciles Root against Remote using State as the common base.
type Engine struct {
	Root   string
	Remote Remote
	State  *State
	Ignore *Ignore
	// Roots are the tree prefixes in scope (DefaultRoots when empty).
	Roots []string
	// Device names this client in conflicted-copy filenames.
	Device string
	Logf   func(format string, args ...any)
}

func (e *Engine) log(format string, args ...any) {
	if e.Logf != nil {
		e.Logf(format, args...)
	}
}

func (e *Engine) roots() []string {
	if len(e.Roots) > 0 {
		return e.Roots
	}
	return DefaultRoots
}

// inScope reports whether a clean tree path belongs to this sync.
func (e *Engine) inScope(path string) bool {
	if path == "" || e.Ignore != nil && e.Ignore.Match(path) {
		return false
	}
	for _, r := range e.roots() {
		if under(path, r) || under(r, path) {
			return true
		}
	}
	return false
}

func under(p, prefix string) bool {
	return p == prefix || strings.HasPrefix(p, prefix+"/")
}

type localEntry struct {
	Size  int64
	MTime time.Time
	Dir   bool
}

// scanLocal walks the sync root and returns in-scope entries keyed by
// clean tree path. Symlinks are skipped (they are invisible by spec).
func (e *Engine) scanLocal() (map[string]localEntry, error) {
	out := map[string]localEntry{}
	err := filepath.WalkDir(e.Root, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(e.Root, p)
		if err != nil || rel == "." {
			return nil
		}
		tree := filepath.ToSlash(rel)
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if !e.inScope(tree) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		out[tree] = localEntry{Size: info.Size(), MTime: info.ModTime(), Dir: d.IsDir()}
		return nil
	})
	if os.IsNotExist(err) {
		err = nil
	}
	return out, err
}

// localSHA returns the content hash of a local file, reusing the state's
// cached hash when size+mtime are unchanged since the last sync.
func (e *Engine) localSHA(tree string, le localEntry) (string, error) {
	if st, ok := e.State.Files[tree]; ok && !st.Dir &&
		st.Size == le.Size && st.MTimeUnix == le.MTime.UnixNano() {
		return st.SHA, nil
	}
	f, err := os.Open(filepath.Join(e.Root, filepath.FromSlash(tree)))
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (e *Engine) rememberFile(tree, sha string) {
	full := filepath.Join(e.Root, filepath.FromSlash(tree))
	info, err := os.Stat(full)
	if err != nil {
		return
	}
	e.State.Files[tree] = FileState{SHA: sha, Size: info.Size(), MTimeUnix: info.ModTime().UnixNano()}
}

// conflictedName renders the Dropbox/Syncthing-style rename for the losing
// version of a conflict.
func (e *Engine) conflictedName(tree string) string {
	device := e.Device
	if device == "" {
		device, _ = os.Hostname()
	}
	if device == "" {
		device = "this device"
	}
	dir, base := gopath.Split(tree)
	ext := gopath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	stamp := time.Now().Format("2006-01-02")
	name := fmt.Sprintf("%s (conflicted copy from %s %s)%s", stem, device, stamp, ext)
	candidate := dir + name
	for i := 2; ; i++ {
		if _, err := os.Stat(filepath.Join(e.Root, filepath.FromSlash(candidate))); os.IsNotExist(err) {
			return candidate
		}
		candidate = dir + fmt.Sprintf("%s (conflicted copy from %s %s %d)%s", stem, device, stamp, i, ext)
	}
}

// download fetches path into the tree (atomic tmp+rename) and records it.
func (e *Engine) download(ctx context.Context, tree string) error {
	rc, err := e.Remote.Get(ctx, tree)
	if err != nil {
		return err
	}
	defer rc.Close()
	full := filepath.Join(e.Root, filepath.FromSlash(tree))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		return err
	}
	tmp := full + ".poweur-tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	h := sha256.New()
	_, err = io.Copy(f, io.TeeReader(rc, h))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, full); err != nil {
		return err
	}
	e.rememberFile(tree, hex.EncodeToString(h.Sum(nil)))
	return nil
}

// applyRemotePut reconciles one remote file version against the local copy.
func (e *Engine) applyRemotePut(ctx context.Context, tree, etag string, rep *Report) error {
	full := filepath.Join(e.Root, filepath.FromSlash(tree))
	base, tracked := e.State.Files[tree]
	info, statErr := os.Stat(full)
	localMissing := statErr != nil || info.IsDir()

	if localMissing {
		if tracked && base.SHA == etag {
			// We deleted locally and the remote copy is exactly our base:
			// the local delete wins (push propagates it).
			return nil
		}
		if err := e.download(ctx, tree); err != nil {
			return err
		}
		rep.Downloaded = append(rep.Downloaded, tree)
		return nil
	}

	lsha, err := e.localSHA(tree, localEntry{Size: info.Size(), MTime: info.ModTime()})
	if err != nil {
		return err
	}
	switch {
	case lsha == etag:
		// Both sides already agree (e.g. our own pushed change echoing back).
		e.rememberFile(tree, lsha)
	case tracked && lsha == base.SHA:
		// Local unchanged since base → remote edit applies cleanly.
		if err := e.download(ctx, tree); err != nil {
			return err
		}
		rep.Downloaded = append(rep.Downloaded, tree)
	default:
		// Both sides changed (or both created differently): keep the local
		// version as a conflicted copy, then take the remote version.
		loser := e.conflictedName(tree)
		if err := os.Rename(full, filepath.Join(e.Root, filepath.FromSlash(loser))); err != nil {
			return err
		}
		if err := e.download(ctx, tree); err != nil {
			return err
		}
		rep.Conflicts = append(rep.Conflicts, loser)
		rep.Downloaded = append(rep.Downloaded, tree)
		e.log("conflict on %s — local version kept as %s", tree, loser)
	}
	return nil
}

// applyRemoteDelete reconciles a remote delete of a subtree.
func (e *Engine) applyRemoteDelete(tree string, rep *Report) error {
	local, err := e.scanLocal()
	if err != nil {
		return err
	}
	var affected []string
	for p := range local {
		if under(p, tree) {
			affected = append(affected, p)
		}
	}
	// Children before parents so directories empty out.
	sort.Sort(sort.Reverse(sort.StringSlice(affected)))
	for _, p := range affected {
		le := local[p]
		full := filepath.Join(e.Root, filepath.FromSlash(p))
		if le.Dir {
			// Only removed when emptied (dirty children survive inside).
			if err := os.Remove(full); err == nil {
				rep.DeletedL = append(rep.DeletedL, p)
				delete(e.State.Files, p)
			}
			continue
		}
		base, tracked := e.State.Files[p]
		lsha, err := e.localSHA(p, le)
		if err != nil {
			return err
		}
		if tracked && lsha == base.SHA {
			if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
				return err
			}
			rep.DeletedL = append(rep.DeletedL, p)
		} else {
			// Local edit vs remote delete: the edit wins — file becomes
			// untracked and push re-uploads it.
			e.log("remote deleted %s but local copy changed — keeping local", p)
		}
		delete(e.State.Files, p)
	}
	// Forget any tracked entries under the subtree with no local file left.
	for p := range e.State.Files {
		if under(p, tree) {
			delete(e.State.Files, p)
		}
	}
	return nil
}

func (e *Engine) applyRemoteMkdir(tree string, rep *Report) error {
	full := filepath.Join(e.Root, filepath.FromSlash(tree))
	if _, err := os.Stat(full); os.IsNotExist(err) {
		if base, tracked := e.State.Files[tree]; tracked && base.Dir {
			// We deleted this directory locally — the delete is pending
			// push; recreating it would resurrect it forever.
			return nil
		}
		if err := os.MkdirAll(full, 0o700); err != nil {
			return err
		}
		rep.MkdirL = append(rep.MkdirL, tree)
	}
	e.State.Files[tree] = FileState{Dir: true}
	return nil
}

// Pull brings the local tree up to date with the remote: incremental via
// the changes feed, or a manifest full-resync on first run / journal gap.
func (e *Engine) Pull(ctx context.Context) (Report, error) {
	var rep Report
	recs, next, resync, err := e.Remote.Changes(ctx, e.State.Cursor)
	if err != nil {
		return rep, err
	}
	if resync || e.State.Cursor == "" {
		return e.pullFromManifest(ctx)
	}
	for _, rec := range recs {
		if !e.inScope(rec.Path) {
			continue
		}
		switch rec.Op {
		case "put":
			err = e.applyRemotePut(ctx, rec.Path, rec.ETag, &rep)
		case "mkdir":
			err = e.applyRemoteMkdir(rec.Path, &rep)
		case "delete":
			err = e.applyRemoteDelete(rec.Path, &rep)
		}
		if err != nil {
			return rep, fmt.Errorf("applying %s %s: %w", rec.Op, rec.Path, err)
		}
	}
	e.State.Cursor = next
	return rep, e.State.Save(e.Root)
}

// pullFromManifest is the full-resync path (E04-T1): diff the remote
// manifest against the state DB, treating differences as puts/deletes.
func (e *Engine) pullFromManifest(ctx context.Context) (Report, error) {
	var rep Report
	entries, cursor, err := e.Remote.Manifest(ctx)
	if err != nil {
		return rep, err
	}
	remote := map[string]Entry{}
	paths := make([]string, 0, len(entries))
	for _, en := range entries {
		if !e.inScope(en.Path) {
			continue
		}
		remote[en.Path] = en
		paths = append(paths, en.Path)
	}
	sort.Strings(paths) // parents before children
	for _, p := range paths {
		en := remote[p]
		if en.Dir {
			err = e.applyRemoteMkdir(p, &rep)
		} else {
			err = e.applyRemotePut(ctx, p, en.ETag, &rep)
		}
		if err != nil {
			return rep, fmt.Errorf("resync %s: %w", p, err)
		}
	}
	// Tracked paths that vanished remotely are remote deletes.
	var gone []string
	for p := range e.State.Files {
		if _, ok := remote[p]; !ok && e.inScope(p) {
			gone = append(gone, p)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(gone)))
	for _, p := range gone {
		if _, ok := e.State.Files[p]; !ok {
			continue // already handled by an ancestor delete
		}
		if err := e.applyRemoteDelete(p, &rep); err != nil {
			return rep, err
		}
	}
	e.State.Cursor = cursor
	return rep, e.State.Save(e.Root)
}

// Push uploads local changes (new/modified files, new dirs, deletions)
// to the remote. Run Pull first for conflict detection — push itself is
// last-writer-wins by design (the spec's server-side contract).
func (e *Engine) Push(ctx context.Context) (Report, error) {
	var rep Report
	local, err := e.scanLocal()
	if err != nil {
		return rep, err
	}
	paths := make([]string, 0, len(local))
	for p := range local {
		paths = append(paths, p)
	}
	sort.Strings(paths) // parents first so remote dirs exist before children
	for _, p := range paths {
		le := local[p]
		if le.Dir {
			// The five top-level roots always exist remotely (and MKCOL on
			// them is forbidden); only subdirectories need creating.
			if !strings.Contains(p, "/") {
				continue
			}
			if st, ok := e.State.Files[p]; !ok || !st.Dir {
				if err := e.Remote.Mkdir(ctx, p); err != nil {
					return rep, fmt.Errorf("mkdir %s: %w", p, err)
				}
				e.State.Files[p] = FileState{Dir: true}
				rep.MkdirR = append(rep.MkdirR, p)
			}
			continue
		}
		lsha, err := e.localSHA(p, le)
		if err != nil {
			return rep, err
		}
		if st, ok := e.State.Files[p]; ok && st.SHA == lsha {
			e.rememberFile(p, lsha) // refresh the mtime cache
			continue
		}
		f, err := os.Open(filepath.Join(e.Root, filepath.FromSlash(p)))
		if err != nil {
			return rep, err
		}
		err = e.Remote.Put(ctx, p, f, le.Size)
		f.Close()
		if err != nil {
			return rep, fmt.Errorf("upload %s: %w", p, err)
		}
		e.rememberFile(p, lsha)
		rep.Uploaded = append(rep.Uploaded, p)
	}
	// Local deletions: tracked paths with no local counterpart. Deepest
	// first; ancestors already deleted subsume their children.
	var gone []string
	for p := range e.State.Files {
		if _, ok := local[p]; !ok {
			gone = append(gone, p)
		}
	}
	sort.Strings(gone)
	deleted := map[string]bool{}
	for _, p := range gone {
		covered := false
		for probe := gopath.Dir(p); probe != "." && probe != "/"; probe = gopath.Dir(probe) {
			if deleted[probe] {
				covered = true
				break
			}
		}
		if !covered {
			if err := e.Remote.Delete(ctx, p); err != nil {
				return rep, fmt.Errorf("delete %s: %w", p, err)
			}
			deleted[p] = true
			rep.DeletedR = append(rep.DeletedR, p)
		}
		delete(e.State.Files, p)
	}
	return rep, e.State.Save(e.Root)
}

// Status describes pending work without changing anything.
type Status struct {
	LocalNew      []string
	LocalModified []string
	LocalDeleted  []string
	RemotePending int  // journal records after our cursor
	NeedsResync   bool // cursor predates the journal
}

// Status computes both sides' pending changes.
func (e *Engine) Status(ctx context.Context) (Status, error) {
	var st Status
	local, err := e.scanLocal()
	if err != nil {
		return st, err
	}
	for p, le := range local {
		if le.Dir {
			continue
		}
		base, tracked := e.State.Files[p]
		if !tracked {
			st.LocalNew = append(st.LocalNew, p)
			continue
		}
		lsha, err := e.localSHA(p, le)
		if err != nil {
			return st, err
		}
		if lsha != base.SHA {
			st.LocalModified = append(st.LocalModified, p)
		}
	}
	for p, base := range e.State.Files {
		if base.Dir {
			continue
		}
		if _, ok := local[p]; !ok {
			st.LocalDeleted = append(st.LocalDeleted, p)
		}
	}
	sort.Strings(st.LocalNew)
	sort.Strings(st.LocalModified)
	sort.Strings(st.LocalDeleted)
	recs, _, resync, err := e.Remote.Changes(ctx, e.State.Cursor)
	if err != nil {
		return st, err
	}
	st.NeedsResync = resync || e.State.Cursor == ""
	for _, rec := range recs {
		if e.inScope(rec.Path) {
			st.RemotePending++
		}
	}
	return st, nil
}
