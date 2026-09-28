package drive

import (
	"context"
	"errors"
	"io"

	protocol "github.com/poweur/identity/drive"
)

// Scope is a drive handle limited to one folder and any extra picked nodes.
// Descendants of those nodes are included. Paths resolve inside the folder.
type Scope struct {
	Files  *Files
	Root   string
	picked map[string]bool
}

func (f *Files) Scope(root *File, picked ...*File) *Scope {
	s := &Scope{Files: f, picked: map[string]bool{}}
	if root != nil {
		s.Root = root.Manifest.Node
	}
	for _, file := range picked {
		if file != nil {
			s.picked[file.Manifest.Node] = true
		}
	}
	return s
}
func (s *Scope) allow(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("node is outside the scoped handle")
	}
	seen := map[string]bool{}
	for id != "" && !seen[id] && len(seen) < 256 {
		if id == s.Root || s.picked[id] {
			return nil
		}
		seen[id] = true
		info, err := s.Files.nodeInfo(ctx, id)
		if err != nil {
			return err
		}
		id = info.Folder
	}
	return errors.New("node is outside the scoped handle")
}
func (s *Scope) Open(ctx context.Context, node string) (*File, error) {
	if err := s.allow(ctx, node); err != nil {
		return nil, err
	}
	return s.Files.Open(ctx, node)
}
func (s *Scope) rootFile(ctx context.Context) (*File, error) {
	if s.Root == "" {
		return nil, errors.New("scoped handle has no folder")
	}
	return s.Files.Open(ctx, s.Root)
}
func (s *Scope) List(ctx context.Context, folder *File) ([]*File, error) {
	if err := s.allow(ctx, folder.Manifest.Node); err != nil {
		return nil, err
	}
	return s.Files.List(ctx, folder)
}
func (s *Scope) Resolve(ctx context.Context, path string) (*File, error) {
	current, err := s.rootFile(ctx)
	if err != nil {
		return nil, err
	}
	if path == "" || path == "/" {
		return current, nil
	}
	parts := splitPath(path)
	for _, part := range parts {
		name, err := protocol.NormalizeName(part)
		if err != nil {
			return nil, err
		}
		children, err := s.Files.List(ctx, current)
		if err != nil {
			return nil, err
		}
		var next *File
		for _, child := range children {
			if child.Name == name {
				next = child
				break
			}
		}
		if next == nil {
			return nil, errors.New("drive path not found: " + name)
		}
		if err = s.allow(ctx, next.Manifest.Node); err != nil {
			return nil, err
		}
		current = next
	}
	return current, nil
}
func splitPath(path string) []string {
	var parts []string
	for _, part := range splitSlash(path) {
		if part != "" {
			parts = append(parts, part)
		}
	}
	return parts
}
func splitSlash(path string) []string {
	path = trimSlash(path)
	if path == "" {
		return nil
	}
	return split(path)
}
func trimSlash(path string) string {
	for len(path) > 0 && path[0] == '/' {
		path = path[1:]
	}
	return path
}
func split(path string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(path); i++ {
		if i == len(path) || path[i] == '/' {
			out = append(out, path[start:i])
			start = i + 1
		}
	}
	return out
}
func (s *Scope) Create(ctx context.Context, parent *File, name, kind string, reader io.Reader) (*File, error) {
	if err := s.allow(ctx, parent.Manifest.Node); err != nil {
		return nil, err
	}
	return s.Files.Create(ctx, parent, name, kind, reader)
}
func (s *Scope) Replace(ctx context.Context, file *File, reader io.Reader) error {
	if err := s.allow(ctx, file.Manifest.Node); err != nil {
		return err
	}
	return s.Files.Replace(ctx, file, reader)
}
func (s *Scope) Read(ctx context.Context, file *File, writer io.Writer) error {
	if err := s.allow(ctx, file.Manifest.Node); err != nil {
		return err
	}
	return s.Files.Read(ctx, file, writer)
}
func (s *Scope) ReadRange(ctx context.Context, file *File, offset, length int64, writer io.Writer) error {
	if err := s.allow(ctx, file.Manifest.Node); err != nil {
		return err
	}
	return s.Files.ReadRange(ctx, file, offset, length, writer)
}
func (s *Scope) Move(ctx context.Context, file, parent *File, name string) error {
	if file.Manifest.Node == s.Root {
		return errors.New("the scoped folder cannot be moved")
	}
	if err := s.allow(ctx, file.Manifest.Node); err != nil {
		return err
	}
	if err := s.allow(ctx, parent.Manifest.Node); err != nil {
		return err
	}
	return s.Files.Move(ctx, file, parent, name)
}
func (s *Scope) Remove(ctx context.Context, file *File) error {
	if file.Manifest.Node == s.Root {
		return errors.New("the scoped folder cannot be removed")
	}
	if err := s.allow(ctx, file.Manifest.Node); err != nil {
		return err
	}
	return s.Files.Remove(ctx, file)
}
func (s *Scope) Append(ctx context.Context, file *File, plaintext []byte) (uint64, error) {
	if err := s.allow(ctx, file.Manifest.Node); err != nil {
		return 0, err
	}
	return s.Files.Append(ctx, file, plaintext)
}
