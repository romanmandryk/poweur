package sync

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	gopath "path"
	"strings"

	driveclient "github.com/poweur/cli/internal/drive"
	protocol "github.com/poweur/identity/drive"
)

// DriveRemote syncs a folder of an encrypted drive: the owner's own drive
// (Folder "" is the root), or a folder shared with the caller (Folder
// "/<node-id>"). Everything is encrypted and decrypted here; the relay sees
// node IDs, sizes and versions.
type DriveRemote struct {
	Files  *driveclient.Files
	Folder string
	// Owner is true when syncing one's own drive: the drive sequence is
	// readable and the device's position is recorded.
	Owner bool
}

func (r *DriveRemote) remote(tree string) string {
	if tree == "" {
		return r.Folder
	}
	return strings.TrimSuffix(r.Folder, "/") + "/" + tree
}

func (r *DriveRemote) Changes(ctx context.Context, since string) (string, bool, error) {
	if !r.Owner {
		return "", true, nil // members walk the tree every time
	}
	if since == "" {
		var info struct {
			Seq string `json:"seq"`
		}
		if err := r.Files.Client.Get(ctx, "", &info); err != nil {
			return "", false, err
		}
		return info.Seq, true, nil
	}
	cursor, changed := since, false
	for page := 0; page < 100; page++ {
		var body struct {
			Changes []struct {
				Node string `json:"node"`
				Path string `json:"path"`
			} `json:"changes"`
			Cursor string `json:"cursor"`
		}
		// Without the device headers: reading is not acknowledging.
		headers := r.Files.Client.Headers
		r.Files.Client.Headers = nil
		err := r.Files.Client.Get(ctx, "/changes?cursor="+url.QueryEscape(cursor)+"&limit=500", &body)
		r.Files.Client.Headers = headers
		if err != nil {
			return "", false, err
		}
		for _, c := range body.Changes {
			// System-zone files carry a path; the tree the sync mirrors does not.
			if c.Path == "" || !strings.HasPrefix(c.Path, ".poweur/") {
				changed = true
			}
		}
		if body.Cursor == "" || body.Cursor == cursor || len(body.Changes) < 500 {
			if body.Cursor != "" {
				cursor = body.Cursor
			}
			return cursor, changed, nil
		}
		cursor = body.Cursor
	}
	return cursor, true, nil
}

func (r *DriveRemote) Tree(ctx context.Context) ([]Entry, error) {
	top, err := r.Files.Resolve(ctx, r.Folder)
	if err != nil {
		return nil, err
	}
	var out []Entry
	var walk func(folder *driveclient.File, prefix string, depth int) error
	walk = func(folder *driveclient.File, prefix string, depth int) error {
		if depth > 64 {
			return errors.New("folder tree too deep")
		}
		children, err := r.Files.List(ctx, folder)
		if err != nil {
			return err
		}
		for _, child := range children {
			tree := gopath.Join(prefix, child.Name)
			if child.Manifest.Kind == protocol.KindFolder {
				out = append(out, Entry{Path: tree, Dir: true, Version: child.Manifest.Version})
				if err := walk(child, tree, depth+1); err != nil {
					return err
				}
				continue
			}
			en := Entry{Path: tree, Version: child.Manifest.Version}
			if child.Manifest.Mode == protocol.ModeAppend {
				var node driveclient.Node
				if err := r.Files.Client.Get(ctx, "/nodes/"+url.PathEscape(child.Manifest.Node), &node); err != nil {
					return err
				}
				en.Append, en.Records = true, node.Position
			}
			out = append(out, en)
		}
		return nil
	}
	return out, walk(top, "", 0)
}

func (r *DriveRemote) Read(ctx context.Context, tree string, w io.Writer) error {
	file, err := r.Files.Resolve(ctx, r.remote(tree))
	if err != nil {
		return err
	}
	if file.Manifest.Mode == protocol.ModeAppend {
		records, err := r.Files.Tail(ctx, file, 1)
		if err != nil {
			return err
		}
		for _, record := range records {
			if _, err := w.Write(record.Plain); err != nil {
				return err
			}
		}
		return nil
	}
	return r.Files.Read(ctx, file, w)
}

func (r *DriveRemote) ReadVersion(ctx context.Context, tree, version string, w io.Writer) error {
	file, err := r.Files.Resolve(ctx, r.remote(tree))
	if err != nil {
		return err
	}
	return r.Files.ReadVersion(ctx, file, version, w)
}

func (r *DriveRemote) Records(ctx context.Context, tree string, from uint64) ([][]byte, error) {
	file, err := r.Files.Resolve(ctx, r.remote(tree))
	if err != nil {
		return nil, err
	}
	records, err := r.Files.Tail(ctx, file, from)
	if err != nil {
		return nil, err
	}
	out := make([][]byte, 0, len(records))
	for _, record := range records {
		out = append(out, record.Plain)
	}
	return out, nil
}

// parent resolves the folder that holds tree and the file's name in it.
func (r *DriveRemote) parent(ctx context.Context, tree string) (*driveclient.File, string, error) {
	dir, name := gopath.Split(tree)
	folder, err := r.Files.Resolve(ctx, r.remote(strings.TrimSuffix(dir, "/")))
	if err != nil {
		return nil, "", err
	}
	name, err = protocol.NormalizeName(name)
	return folder, name, err
}

// child finds name in folder, or nil.
func (r *DriveRemote) child(ctx context.Context, folder *driveclient.File, name string) (*driveclient.File, error) {
	children, err := r.Files.List(ctx, folder)
	if err != nil {
		return nil, err
	}
	for _, c := range children {
		if c.Name == name {
			return c, nil
		}
	}
	return nil, nil
}

func conflictOr(err error) error {
	var driveErr *driveclient.Error
	if errors.As(err, &driveErr) && driveErr.Status == http.StatusConflict {
		return ErrConflict
	}
	return err
}

func (r *DriveRemote) Put(ctx context.Context, tree string, content io.Reader, base string) (Entry, error) {
	folder, name, err := r.parent(ctx, tree)
	if err != nil {
		return Entry{}, err
	}
	existing, err := r.child(ctx, folder, name)
	if err != nil {
		return Entry{}, err
	}
	switch {
	case existing == nil && base != "":
		return Entry{}, ErrConflict // deleted remotely since the base
	case existing == nil:
		file, err := r.Files.Create(ctx, folder, name, protocol.KindFile, content)
		if err != nil {
			return Entry{}, conflictOr(err)
		}
		return Entry{Path: tree, Version: file.Manifest.Version}, nil
	case existing.Manifest.Version != base:
		return Entry{}, ErrConflict
	}
	if err := r.Files.Replace(ctx, existing, content); err != nil {
		return Entry{}, conflictOr(err)
	}
	return Entry{Path: tree, Version: existing.Manifest.Version}, nil
}

func (r *DriveRemote) CreateAppend(ctx context.Context, tree string, first []byte) (Entry, error) {
	folder, name, err := r.parent(ctx, tree)
	if err != nil {
		return Entry{}, err
	}
	if existing, err := r.child(ctx, folder, name); err != nil || existing != nil {
		if err == nil {
			err = ErrConflict
		}
		return Entry{}, err
	}
	file, err := r.Files.CreateAppend(ctx, folder, name)
	if err != nil {
		return Entry{}, conflictOr(err)
	}
	position := uint64(0)
	if len(first) > 0 {
		if position, err = r.Files.Append(ctx, file, first); err != nil {
			return Entry{}, err
		}
	}
	return Entry{Path: tree, Version: file.Manifest.Version, Append: true, Records: position}, nil
}

func (r *DriveRemote) Append(ctx context.Context, tree string, data []byte) (Entry, error) {
	file, err := r.Files.Resolve(ctx, r.remote(tree))
	if err != nil {
		return Entry{}, err
	}
	position, err := r.Files.Append(ctx, file, data)
	if err != nil {
		return Entry{}, err
	}
	return Entry{Path: tree, Version: file.Manifest.Version, Append: true, Records: position}, nil
}

func (r *DriveRemote) Mkdir(ctx context.Context, tree string) error {
	folder, name, err := r.parent(ctx, tree)
	if err != nil {
		return err
	}
	if existing, err := r.child(ctx, folder, name); err != nil || existing != nil {
		return err
	}
	_, err = r.Files.Create(ctx, folder, name, protocol.KindFolder, nil)
	return err
}

func (r *DriveRemote) Delete(ctx context.Context, tree string) error {
	file, err := r.Files.Resolve(ctx, r.remote(tree))
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil // already gone
		}
		return err
	}
	return r.Files.Remove(ctx, file)
}

// Ack reads the change feed at seq with the device headers, which records
// this device's sync position in the owner's devices.json.
func (r *DriveRemote) Ack(ctx context.Context, seq string) error {
	if !r.Owner || seq == "" {
		return nil
	}
	var out struct {
		Cursor string `json:"cursor"`
	}
	return r.Files.Client.Get(ctx, "/changes?cursor="+url.QueryEscape(seq)+"&limit=1", &out)
}
