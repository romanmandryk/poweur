package sync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	gopath "path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ErrConflict is a remote write refused because the file changed since the
// version the write was based on (the relay's 409). The next pull merges.
var ErrConflict = errors.New("sync: the remote file changed since the last sync")

// Entry is one remote path.
type Entry struct {
	Path    string
	Version string
	Dir     bool
	// Append files: the relay orders records; Records is the last position.
	Append  bool
	Records uint64
}

// Remote is the drive surface the engine reconciles against. DriveRemote
// implements it over the encrypted drive; tests use an in-memory fake.
type Remote interface {
	// Changes reports the drive's change sequence and whether anything the
	// sync cares about moved since since ("" = unknown: always changed).
	// Relay-written system files (devices.json, say) do not count.
	Changes(ctx context.Context, since string) (next string, changed bool, err error)
	// Tree lists every remote path under the synced folder.
	Tree(ctx context.Context) ([]Entry, error)
	// Read writes a replace file's content, or an append file's records
	// concatenated in order.
	Read(ctx context.Context, path string, w io.Writer) error
	ReadVersion(ctx context.Context, path, version string, w io.Writer) error
	// Records returns an append file's records from position from (1-based).
	Records(ctx context.Context, path string, from uint64) ([][]byte, error)
	// Put writes a replace file based on version base ("" creates it and
	// fails if it exists); ErrConflict when base is no longer the head.
	Put(ctx context.Context, path string, r io.Reader, base string) (Entry, error)
	CreateAppend(ctx context.Context, path string, first []byte) (Entry, error)
	Append(ctx context.Context, path string, data []byte) (Entry, error)
	Mkdir(ctx context.Context, path string) error
	Delete(ctx context.Context, path string) error
	// Ack tells the relay how far this device has synced (devices.json).
	Ack(ctx context.Context, seq string) error
}

// Report summarizes one run.
type Report struct {
	Downloaded []string
	Uploaded   []string
	Merged     []string // both sides changed; merged cleanly
	DeletedL   []string // removed locally (moved to the trash)
	DeletedR   []string
	Conflicts  []string // conflicted copies, or merges left with conflict markers
	MkdirL     []string
	MkdirR     []string
	Deferred   []string // still being written locally; pushed on a later pass
}

func (r Report) Empty() bool {
	return len(r.Downloaded)+len(r.Uploaded)+len(r.Merged)+len(r.DeletedL)+len(r.DeletedR)+
		len(r.Conflicts)+len(r.MkdirL)+len(r.MkdirR) == 0
}

func (r *Report) add(o Report) {
	r.Downloaded = append(r.Downloaded, o.Downloaded...)
	r.Uploaded = append(r.Uploaded, o.Uploaded...)
	r.Merged = append(r.Merged, o.Merged...)
	r.DeletedL = append(r.DeletedL, o.DeletedL...)
	r.DeletedR = append(r.DeletedR, o.DeletedR...)
	r.Conflicts = append(r.Conflicts, o.Conflicts...)
	r.MkdirL = append(r.MkdirL, o.MkdirL...)
	r.MkdirR = append(r.MkdirR, o.MkdirR...)
	r.Deferred = append(r.Deferred, o.Deferred...)
}

// Engine reconciles Root against Remote using State as the common base.
type Engine struct {
	Root   string
	Remote Remote
	State  *State
	Ignore *Ignore
	// Roots are the paths in scope (selective sync); empty is everything
	// except .poweur.
	Roots []string
	// Device names this client in conflicted-copy names and merge labels.
	Device string
	// Settle is how long a local file must be unmodified before it is
	// uploaded (the watcher's debounce); zero uploads immediately.
	Settle time.Duration
	Logf   func(format string, args ...any)
	now    func() time.Time
}

func (e *Engine) log(format string, args ...any) {
	if e.Logf != nil {
		e.Logf(format, args...)
	}
}

func (e *Engine) clock() time.Time {
	if e.now != nil {
		return e.now()
	}
	return time.Now()
}

func (e *Engine) inScope(path string) bool {
	if path == "" || e.Ignore != nil && e.Ignore.Match(path) {
		return false
	}
	if len(e.Roots) == 0 {
		return !under(path, ".poweur")
	}
	for _, r := range e.Roots {
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

func (e *Engine) full(tree string) string { return filepath.Join(e.Root, filepath.FromSlash(tree)) }

// scanLocal walks the root; symlinks are skipped.
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

func sha(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// localSHA hashes a local file, reusing the state's hash when size and
// mtime are unchanged since the last sync.
func (e *Engine) localSHA(tree string, le localEntry) (string, error) {
	if st, ok := e.State.Files[tree]; ok && !st.Dir && st.Size == le.Size && st.MTimeUnix == le.MTime.UnixNano() {
		return st.SHA, nil
	}
	raw, err := os.ReadFile(e.full(tree))
	if err != nil {
		return "", err
	}
	return sha(raw), nil
}

// remember records tree as in sync with remote entry en.
func (e *Engine) remember(tree string, en Entry) {
	info, err := os.Stat(e.full(tree))
	if err != nil {
		return
	}
	raw, err := os.ReadFile(e.full(tree))
	if err != nil {
		return
	}
	e.State.Files[tree] = FileState{SHA: sha(raw), Size: info.Size(), MTimeUnix: info.ModTime().UnixNano(), Version: en.Version, Append: en.Append, Records: en.Records}
}

func (e *Engine) device() string {
	if e.Device != "" {
		return e.Device
	}
	if host, _ := os.Hostname(); host != "" {
		return host
	}
	return "this device"
}

// conflictedName is the name the losing local version is kept under.
func (e *Engine) conflictedName(tree string) string {
	dir, base := gopath.Split(tree)
	ext := gopath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	stamp := e.clock().Format("2006-01-02 1504")
	candidate := dir + fmt.Sprintf("%s (conflicted copy %s %s)%s", stem, e.device(), stamp, ext)
	for i := 2; ; i++ {
		if _, err := os.Stat(e.full(candidate)); os.IsNotExist(err) {
			return candidate
		}
		candidate = dir + fmt.Sprintf("%s (conflicted copy %s %s %d)%s", stem, e.device(), stamp, i, ext)
	}
}

// writeAtomic writes data at tree through a temporary file and a rename.
func (e *Engine) writeAtomic(tree string, data []byte) error {
	full := e.full(tree)
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		return err
	}
	tmp := full + ".poweur-tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, full)
}

func (e *Engine) fetch(ctx context.Context, tree string) ([]byte, error) {
	var buf bytes.Buffer
	err := e.Remote.Read(ctx, tree, &buf)
	return buf.Bytes(), err
}

func (e *Engine) download(ctx context.Context, en Entry) error {
	data, err := e.fetch(ctx, en.Path)
	if err != nil {
		return err
	}
	if err := e.writeAtomic(en.Path, data); err != nil {
		return err
	}
	e.remember(en.Path, en)
	return nil
}

// trash moves a local file the remote deleted into .poweur-trash/<date>/,
// never deleting it outright.
func (e *Engine) trash(tree string) error {
	dest := filepath.Join(e.Root, TrashDir, e.clock().Format("2006-01-02"), filepath.FromSlash(tree))
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(dest); err == nil {
		dest += "." + e.clock().Format("150405.000")
	}
	return os.Rename(e.full(tree), dest)
}

// keepAsConflict renames the local version aside and takes the remote one.
func (e *Engine) keepAsConflict(ctx context.Context, en Entry, rep *Report) error {
	loser := e.conflictedName(en.Path)
	if err := os.Rename(e.full(en.Path), e.full(loser)); err != nil {
		return err
	}
	if err := e.download(ctx, en); err != nil {
		return err
	}
	rep.Conflicts = append(rep.Conflicts, loser)
	rep.Downloaded = append(rep.Downloaded, en.Path)
	e.State.Conflicts = appendUnique(e.State.Conflicts, loser)
	e.log("conflict on %s: your version is kept as %s", en.Path, loser)
	return nil
}

func appendUnique(list []string, value string) []string {
	for _, item := range list {
		if item == value {
			return list
		}
	}
	return append(list, value)
}

// applyRemote reconciles one remote file against the local copy.
func (e *Engine) applyRemote(ctx context.Context, en Entry, rep *Report) error {
	base, tracked := e.State.Files[en.Path]
	info, statErr := os.Stat(e.full(en.Path))
	localMissing := statErr != nil || info.IsDir()
	if tracked && remoteUnchanged(base, en) {
		return nil // local edits or deletes, if any, are pushed
	}
	if localMissing {
		// Remote changed after a local delete: the edit wins.
		if err := e.download(ctx, en); err != nil {
			return err
		}
		rep.Downloaded = append(rep.Downloaded, en.Path)
		return nil
	}
	local, err := os.ReadFile(e.full(en.Path))
	if err != nil {
		return err
	}
	if en.Append {
		return e.applyRemoteAppend(ctx, en, base, tracked, local, rep)
	}
	if tracked && sha(local) == base.SHA {
		if err := e.download(ctx, en); err != nil {
			return err
		}
		rep.Downloaded = append(rep.Downloaded, en.Path)
		return nil
	}
	remote, err := e.fetch(ctx, en.Path)
	if err != nil {
		return err
	}
	if bytes.Equal(remote, local) {
		e.remember(en.Path, en)
		return nil
	}
	if !tracked {
		// Created on both sides with different content: nothing to merge from.
		return e.keepAsConflict(ctx, en, rep)
	}
	var baseContent bytes.Buffer
	if err := e.Remote.ReadVersion(ctx, en.Path, base.Version, &baseContent); err != nil {
		e.log("%s: the common version is gone (%v); keeping a conflicted copy", en.Path, err)
		return e.keepAsConflict(ctx, en, rep)
	}
	var merged []byte
	conflicts := 0
	switch DriverFor(en.Path) {
	case DriverText:
		text, n := MergeText(baseContent.String(), string(local), string(remote), e.device(), "remote")
		merged, conflicts = []byte(text), n
	case DriverJSON:
		out, ok := MergeJSON(baseContent.Bytes(), local, remote)
		if !ok {
			return e.keepAsConflict(ctx, en, rep)
		}
		merged = out
	default:
		return e.keepAsConflict(ctx, en, rep)
	}
	// The merge is based on the remote version: record that as the base so
	// the push uploads the merge on top of it.
	if err := e.writeAtomic(en.Path, remote); err != nil {
		return err
	}
	e.remember(en.Path, en)
	if err := e.writeAtomic(en.Path, merged); err != nil {
		return err
	}
	if conflicts > 0 {
		rep.Conflicts = append(rep.Conflicts, en.Path)
		e.State.Conflicts = appendUnique(e.State.Conflicts, en.Path)
		e.log("%s: merged with %d conflict(s) marked in the file", en.Path, conflicts)
	} else {
		rep.Merged = append(rep.Merged, en.Path)
	}
	return nil
}

func remoteUnchanged(base FileState, en Entry) bool {
	if en.Append || base.Append {
		return base.Append == en.Append && base.Records == en.Records
	}
	return base.Version == en.Version
}

// applyRemoteAppend brings new remote records into a local append file. Local
// growth after the synced prefix stays at the end and is appended by push; a
// rewritten prefix cannot be merged and becomes a conflicted copy.
func (e *Engine) applyRemoteAppend(ctx context.Context, en Entry, base FileState, tracked bool, local []byte, rep *Report) error {
	if !tracked || int64(len(local)) < base.Size || sha(local[:base.Size]) != base.SHA {
		return e.keepAsConflict(ctx, en, rep)
	}
	records, err := e.Remote.Records(ctx, en.Path, base.Records+1)
	if err != nil {
		return err
	}
	prefix := append([]byte(nil), local[:base.Size]...)
	for _, record := range records {
		prefix = append(prefix, record...)
	}
	pending := local[base.Size:]
	if err := e.writeAtomic(en.Path, prefix); err != nil {
		return err
	}
	e.remember(en.Path, en)
	if len(pending) > 0 {
		if err := e.writeAtomic(en.Path, append(prefix, pending...)); err != nil {
			return err
		}
	}
	rep.Downloaded = append(rep.Downloaded, en.Path)
	return nil
}

// applyRemoteDelete handles a tracked path the remote no longer has.
func (e *Engine) applyRemoteDelete(tree string, rep *Report) error {
	base := e.State.Files[tree]
	info, err := os.Stat(e.full(tree))
	switch {
	case err != nil:
		// Gone on both sides.
	case base.Dir || info.IsDir():
		if os.Remove(e.full(tree)) == nil { // only when emptied
			rep.DeletedL = append(rep.DeletedL, tree)
		}
	default:
		local, err := os.ReadFile(e.full(tree))
		if err != nil {
			return err
		}
		if sha(local) == base.SHA {
			if err := e.trash(tree); err != nil {
				return err
			}
			rep.DeletedL = append(rep.DeletedL, tree)
		} else {
			// Local edit against a remote delete: the edit wins and is
			// uploaded again as a new file.
			e.log("%s was deleted remotely but changed here; keeping it", tree)
		}
	}
	delete(e.State.Files, tree)
	return nil
}

// Pull applies remote changes. An unchanged drive sequence costs one request.
func (e *Engine) Pull(ctx context.Context) (Report, error) {
	var rep Report
	seq, changed, err := e.Remote.Changes(ctx, e.State.Cursor)
	if err != nil {
		return rep, err
	}
	if !changed && e.State.Cursor != "" {
		if seq != e.State.Cursor {
			e.State.Cursor = seq
			return rep, e.State.Save(e.Root)
		}
		return rep, nil
	}
	entries, err := e.Remote.Tree(ctx)
	if err != nil {
		return rep, err
	}
	remote := map[string]Entry{}
	var paths []string
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
			if _, err := os.Stat(e.full(p)); os.IsNotExist(err) {
				if st, tracked := e.State.Files[p]; tracked && st.Dir {
					continue // deleted locally; push removes it remotely
				}
				if err := os.MkdirAll(e.full(p), 0o700); err != nil {
					return rep, err
				}
				rep.MkdirL = append(rep.MkdirL, p)
			}
			e.State.Files[p] = FileState{Dir: true}
			continue
		}
		if err := e.applyRemote(ctx, en, &rep); err != nil {
			return rep, fmt.Errorf("pull %s: %w", p, err)
		}
	}
	var gone []string
	for p := range e.State.Files {
		if _, ok := remote[p]; !ok && e.inScope(p) {
			gone = append(gone, p)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(gone))) // children first
	for _, p := range gone {
		if err := e.applyRemoteDelete(p, &rep); err != nil {
			return rep, err
		}
	}
	e.State.Cursor = seq
	return rep, e.State.Save(e.Root)
}

// Push uploads local changes against the bases the last pull recorded. A
// file someone else changed meanwhile is left for the next pull to merge.
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
	sort.Strings(paths)
	for _, p := range paths {
		le := local[p]
		if le.Dir {
			if st, ok := e.State.Files[p]; !ok || !st.Dir {
				if err := e.Remote.Mkdir(ctx, p); err != nil {
					return rep, fmt.Errorf("mkdir %s: %w", p, err)
				}
				e.State.Files[p] = FileState{Dir: true}
				rep.MkdirR = append(rep.MkdirR, p)
			}
			continue
		}
		if e.Settle > 0 && e.clock().Sub(le.MTime) < e.Settle {
			rep.Deferred = append(rep.Deferred, p)
			continue
		}
		lsha, err := e.localSHA(p, le)
		if err != nil {
			return rep, err
		}
		base, tracked := e.State.Files[p]
		if tracked && base.SHA == lsha {
			continue
		}
		data, err := os.ReadFile(e.full(p))
		if err != nil {
			return rep, err
		}
		var en Entry
		switch {
		case tracked && base.Append:
			if int64(len(data)) <= base.Size || sha(data[:base.Size]) != base.SHA {
				// A rewrite of an append file cannot be uploaded; keep it aside.
				loser := e.conflictedName(p)
				if err := os.Rename(e.full(p), e.full(loser)); err != nil {
					return rep, err
				}
				rep.Conflicts = append(rep.Conflicts, loser)
				e.State.Conflicts = appendUnique(e.State.Conflicts, loser)
				delete(e.State.Files, p) // the next pull restores the remote file
				e.State.Cursor = ""
				continue
			}
			en, err = e.Remote.Append(ctx, p, data[base.Size:])
		case !tracked && CreatesAppend(p):
			en, err = e.Remote.CreateAppend(ctx, p, data)
		case tracked:
			en, err = e.Remote.Put(ctx, p, bytes.NewReader(data), base.Version)
		default:
			en, err = e.Remote.Put(ctx, p, bytes.NewReader(data), "")
		}
		if errors.Is(err, ErrConflict) {
			e.State.Cursor = "" // make the next pull look
			rep.Deferred = append(rep.Deferred, p)
			continue
		}
		if err != nil {
			return rep, fmt.Errorf("upload %s: %w", p, err)
		}
		e.remember(p, en)
		rep.Uploaded = append(rep.Uploaded, p)
	}
	// Local deletions, deepest first; a deleted folder covers its children.
	var gone []string
	for p := range e.State.Files {
		if _, ok := local[p]; !ok && e.inScope(p) {
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

// Run pulls and pushes until both sides agree (a push that lost a race is
// merged by the next pull), then acknowledges the cursor to the relay.
func (e *Engine) Run(ctx context.Context) (Report, error) {
	var total Report
	firstSync := e.State.Cursor == ""
	// .poweurignore is read per run: editing it takes effect on the next pass.
	e.Ignore = LoadIgnore(e.Root)
	for round := 0; round < 4; round++ {
		pulled, err := e.Pull(ctx)
		total.add(pulled)
		if err != nil {
			return total, err
		}
		pushed, err := e.Push(ctx)
		total.add(pushed)
		if err != nil {
			return total, err
		}
		// Done when the push changed nothing and left the pulled state valid
		// (a lost race or a rewritten append file clears the cursor).
		if len(pushed.Uploaded)+len(pushed.DeletedR)+len(pushed.MkdirR) == 0 && e.State.Cursor != "" {
			break
		}
		// Our own writes moved the sequence: re-read so the cursor and the
		// recorded versions describe the drive as it is now.
		e.State.Cursor = ""
	}
	// Acknowledge only a sync that moved something: the acknowledgement is
	// itself a (relay-written) drive change, and a watcher must not wake
	// itself up forever.
	if e.State.Cursor != "" && (firstSync || !total.Empty()) {
		if err := e.Remote.Ack(ctx, e.State.Cursor); err != nil {
			e.log("could not report the sync position: %v", err)
		}
	}
	return total, nil
}

// Status describes pending work without changing anything.
type Status struct {
	LocalNew, LocalModified, LocalDeleted []string
	RemoteChanged                         bool
	Conflicts                             []string
}

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
		if _, ok := local[p]; !ok && !base.Dir {
			st.LocalDeleted = append(st.LocalDeleted, p)
		}
	}
	sort.Strings(st.LocalNew)
	sort.Strings(st.LocalModified)
	sort.Strings(st.LocalDeleted)
	for _, c := range e.State.Conflicts {
		if _, err := os.Stat(e.full(c)); err == nil {
			st.Conflicts = append(st.Conflicts, c)
		}
	}
	_, changed, err := e.Remote.Changes(ctx, e.State.Cursor)
	if err != nil {
		return st, err
	}
	st.RemoteChanged = changed
	return st, nil
}

// LocalSnapshot summarizes the in-scope local tree (paths, sizes, mtimes):
// a watcher compares two snapshots to notice local edits without reading
// file contents.
func (e *Engine) LocalSnapshot() (string, error) {
	local, err := e.scanLocal()
	if err != nil {
		return "", err
	}
	paths := make([]string, 0, len(local))
	for p := range local {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, p := range paths {
		le := local[p]
		fmt.Fprintf(h, "%s\x00%d\x00%d\x00%v\n", p, le.Size, le.MTime.UnixNano(), le.Dir)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
