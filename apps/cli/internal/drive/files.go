package drive

import (
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	protocol "github.com/poweur/identity/drive"
)

// Files implements owner file access. Shared-author histories fail closed until
// an authorized author resolver is supplied by the sharing client.
type Files struct {
	Client        *Client
	EncryptionKey []byte
}
type File struct {
	Manifest            protocol.Manifest
	Name                string
	NodeKey, ContentKey []byte
}
type Node struct {
	ID         string `json:"id"`
	Head       string `json:"head"`
	Folder     string `json:"folder"`
	Kind       string `json:"kind"`
	Generation uint64 `json:"generation"`
	Removed    bool   `json:"removed"`
}

func public(key []byte) ([]byte, error) {
	k, err := ecdh.X25519().NewPrivateKey(key)
	if err != nil {
		return nil, err
	}
	return k.PublicKey().Bytes(), nil
}
func contextFor(m protocol.Manifest, purpose string) []byte {
	b, _ := m.EnvelopeContext(purpose)
	return b
}
func (f *Files) verify(m protocol.Manifest) error {
	if (f.Client.Drive != "" && f.Client.Drive != f.Client.Identity) || m.Drive != f.Client.Identity || m.Author != f.Client.Identity {
		return errors.New("untrusted manifest author or drive")
	}
	if len(f.Client.Key) != ed25519.PrivateKeySize {
		return errors.New("invalid signing key")
	}
	return m.Verify(f.Client.Key.Public().(ed25519.PublicKey))
}
func (f *Files) version(ctx context.Context, node, version string) (protocol.Manifest, error) {
	var m protocol.Manifest
	err := f.Client.Get(ctx, "/nodes/"+url.PathEscape(node)+"/versions/"+url.PathEscape(version), &m)
	if err != nil {
		return m, err
	}
	if m.Node != node || m.Version != version {
		return m, errors.New("manifest reference mismatch")
	}
	return m, f.verify(m)
}
func (f *Files) Open(ctx context.Context, node string) (*File, error) {
	return f.open(ctx, node, map[string]bool{})
}
func (f *Files) open(ctx context.Context, node string, ancestors map[string]bool) (*File, error) {
	if ancestors[node] || len(ancestors) >= 256 {
		return nil, errors.New("invalid folder ancestry")
	}
	ancestors[node] = true
	var info Node
	if err := f.Client.Get(ctx, "/nodes/"+url.PathEscape(node), &info); err != nil {
		return nil, err
	}
	if info.ID != node || info.Removed {
		return nil, errors.New("node is removed or mismatched")
	}
	head, err := f.version(ctx, node, info.Head)
	if err != nil {
		return nil, err
	}
	if head.Generation != info.Generation || head.Kind != info.Kind {
		return nil, errors.New("node metadata mismatch")
	}
	var keyVersion, nameVersion, contentVersion *protocol.Manifest
	m := head
	seen := map[string]bool{}
	for {
		if seen[m.Version] || len(seen) >= 10000 {
			return nil, errors.New("invalid version ancestry")
		}
		seen[m.Version] = true
		copy := m
		if keyVersion == nil && m.NodeKey != nil {
			keyVersion = &copy
		}
		if nameVersion == nil && m.Name != nil {
			nameVersion = &copy
		}
		if contentVersion == nil && m.ContentKey != nil {
			contentVersion = &copy
		}
		if keyVersion != nil && (head.Kind == protocol.KindFolder || contentVersion != nil) && (info.Folder == "" || nameVersion != nil) {
			break
		}
		if m.Parent == "" {
			return nil, errors.New("missing key or name envelope")
		}
		m, err = f.version(ctx, node, m.Parent)
		if err != nil {
			return nil, err
		}
	}
	if keyVersion.Generation != head.Generation || (contentVersion != nil && contentVersion.Generation != head.Generation) {
		return nil, errors.New("key generation mismatch")
	}
	parentKey := f.EncryptionKey
	if info.Folder != "" {
		parent, err := f.open(ctx, info.Folder, ancestors)
		if err != nil {
			return nil, err
		}
		if parent.Manifest.Kind != protocol.KindFolder {
			return nil, errors.New("parent is not a folder")
		}
		parentKey = parent.NodeKey
	}
	key, err := protocol.OpenKey(parentKey, *keyVersion.NodeKey, contextFor(*keyVersion, protocol.PurposeNodeKey))
	if err != nil {
		return nil, err
	}
	result := &File{Manifest: head, NodeKey: key}
	if nameVersion != nil {
		result.Name, err = protocol.OpenName(parentKey, *nameVersion.Name, contextFor(*nameVersion, protocol.PurposeName))
		if err != nil {
			return nil, err
		}
		hash, err := protocol.NameHash(parentKey, result.Name)
		if err != nil || hash != nameVersion.NameHash {
			return nil, errors.New("name index mismatch")
		}
	}
	if contentVersion != nil {
		result.ContentKey, err = protocol.OpenKey(key, *contentVersion.ContentKey, contextFor(*contentVersion, protocol.PurposeContentKey))
	}
	return result, err
}
func (f *Files) Root(ctx context.Context) (*File, error) {
	var info struct {
		Root string `json:"root"`
	}
	if err := f.Client.Get(ctx, "", &info); err != nil {
		return nil, err
	}
	if info.Root != "" {
		return f.Open(ctx, info.Root)
	}
	return f.Create(ctx, nil, "", protocol.KindFolder, nil)
}
func (f *Files) List(ctx context.Context, folder *File) ([]*File, error) {
	if folder.Manifest.Kind != protocol.KindFolder {
		return nil, errors.New("not a folder")
	}
	result := []*File{}
	cursor := ""
	seen := map[string]bool{}
	for {
		if seen[cursor] {
			return nil, errors.New("repeated children cursor")
		}
		seen[cursor] = true
		var page struct {
			Children []Node `json:"children"`
			Cursor   string `json:"cursor"`
		}
		if err := f.Client.Get(ctx, "/nodes/"+folder.Manifest.Node+"/children?cursor="+url.QueryEscape(cursor), &page); err != nil {
			return nil, err
		}
		for _, child := range page.Children {
			entry, err := f.Open(ctx, child.ID)
			if err != nil {
				return nil, err
			}
			result = append(result, entry)
		}
		cursor = page.Cursor
		if cursor == "" {
			return result, nil
		}
	}
}
func (f *Files) Resolve(ctx context.Context, path string) (*File, error) {
	var parts []string
	if path != "" && path != "/" {
		parts = strings.Split(strings.TrimPrefix(path, "/"), "/")
	}
	for i, p := range parts {
		n, err := protocol.NormalizeName(p)
		if err != nil {
			return nil, err
		}
		parts[i] = n
	}
	current, err := f.Root(ctx)
	if err != nil {
		return nil, err
	}
	for _, part := range parts {
		children, err := f.List(ctx, current)
		if err != nil {
			return nil, err
		}
		var next *File
		for _, child := range children {
			if child.Name == part {
				next = child
				break
			}
		}
		if next == nil {
			return nil, fmt.Errorf("drive path not found: %s", part)
		}
		current = next
	}
	return current, nil
}
func (f *Files) Create(ctx context.Context, parent *File, name, kind string, reader io.Reader) (*File, error) {
	if parent != nil && parent.Manifest.Kind != protocol.KindFolder {
		return nil, errors.New("not a folder")
	}
	if parent == nil && kind != protocol.KindFolder {
		return nil, errors.New("root must be a folder")
	}
	node, err := protocol.NewNodeID()
	if err != nil {
		return nil, err
	}
	version, err := protocol.NewVersionID()
	if err != nil {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		return nil, err
	}
	result := &File{Name: name, NodeKey: key, Manifest: protocol.Manifest{Format: 1, Drive: f.Client.Identity, Node: node, Version: version, Operation: protocol.OpCreate, Author: f.Client.Identity, Generation: 1, Kind: kind, Pages: []string{}}}
	m := &result.Manifest
	parentKey := f.EncryptionKey
	if parent != nil {
		parentKey = parent.NodeKey
		m.Folder = parent.Manifest.Node
	}
	pub, err := public(parentKey)
	if err != nil {
		return nil, err
	}
	wrapped, err := protocol.SealKey(pub, key, contextFor(*m, protocol.PurposeNodeKey))
	if err != nil {
		return nil, err
	}
	m.NodeKey = &wrapped
	if parent != nil {
		result.Name, err = protocol.NormalizeName(name)
		if err != nil {
			return nil, err
		}
		sealed, err := protocol.SealName(pub, result.Name, contextFor(*m, protocol.PurposeName))
		if err != nil {
			return nil, err
		}
		m.Name = &sealed
		m.NameHash, err = protocol.NameHash(parentKey, result.Name)
		if err != nil {
			return nil, err
		}
	}
	var pages []protocol.ChunkPage
	if kind == protocol.KindFile {
		m.Mode = protocol.ModeReplace
		result.ContentKey = make([]byte, 32)
		if _, err = rand.Read(result.ContentKey); err != nil {
			return nil, err
		}
		nodePub, _ := public(key)
		sealed, err := protocol.SealKey(nodePub, result.ContentKey, contextFor(*m, protocol.PurposeContentKey))
		if err != nil {
			return nil, err
		}
		m.ContentKey = &sealed
		pages, err = f.upload(ctx, m, result.ContentKey, reader)
		if err != nil {
			return nil, err
		}
	} else if reader != nil {
		return nil, errors.New("folder has no content")
	}
	if err = m.Sign(f.Client.Key); err != nil {
		return nil, err
	}
	if err = f.verify(*m); err != nil {
		return nil, err
	}
	_, err = f.Client.Commit(ctx, Commit{Manifest: m, Pages: pages})
	return result, err
}
func (f *Files) upload(ctx context.Context, m *protocol.Manifest, key []byte, reader io.Reader) ([]protocol.ChunkPage, error) {
	var refs []protocol.ChunkRef
	if reader != nil {
		buffer := make([]byte, protocol.MaxPlaintext)
		for {
			n, err := io.ReadFull(reader, buffer)
			if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
				return nil, err
			}
			if n > 0 {
				encrypted, e := protocol.EncryptChunk(key, buffer[:n], contextFor(*m, protocol.PurposeContent))
				if e != nil {
					return nil, e
				}
				ref, e := f.Client.Upload(ctx, encrypted)
				if e != nil {
					return nil, e
				}
				refs = append(refs, ref)
			}
			if err != nil {
				break
			}
		}
	}
	pages, hashes, err := protocol.SplitPages(m.Drive, m.Node, refs)
	if hashes == nil {
		hashes = []string{}
	}
	m.Pages = hashes
	m.Count = uint64(len(refs))
	return pages, err
}
func next(file *File, operation string) (protocol.Manifest, error) {
	m := file.Manifest
	v, err := protocol.NewVersionID()
	if err != nil {
		return m, err
	}
	m.Version = v
	m.Parent = file.Manifest.Version
	m.Operation = operation
	m.Folder = ""
	m.Name = nil
	m.NameHash = ""
	m.NodeKey = nil
	m.ContentKey = nil
	m.Signature = ""
	return m, nil
}
func (f *Files) Replace(ctx context.Context, file *File, reader io.Reader) error {
	if file.Manifest.Mode != protocol.ModeReplace || len(file.ContentKey) != 32 {
		return errors.New("not a replace file")
	}
	m, err := next(file, protocol.OpReplace)
	if err != nil {
		return err
	}
	pages, err := f.upload(ctx, &m, file.ContentKey, reader)
	if err != nil {
		return err
	}
	if err = m.Sign(f.Client.Key); err != nil {
		return err
	}
	if _, err = f.Client.Commit(ctx, Commit{Manifest: &m, Pages: pages}); err != nil {
		return err
	}
	file.Manifest = m
	return nil
}

// Read authenticates each bounded chunk before writing plaintext. Callers
// writing a file should use a temporary file and rename after success.
func (f *Files) Read(ctx context.Context, file *File, writer io.Writer) error {
	if file.Manifest.Mode != protocol.ModeReplace || len(file.ContentKey) != 32 {
		return errors.New("not a replace file")
	}
	m := file.Manifest
	if err := f.verify(m); err != nil {
		return err
	}
	pages := []protocol.ChunkPage{}
	for _, hash := range m.Pages {
		var page protocol.ChunkPage
		if err := f.Client.Get(ctx, "/nodes/"+m.Node+"/versions/"+m.Version+"/pages/"+hash, &page); err != nil {
			return err
		}
		pages = append(pages, page)
	}
	refs, err := m.VerifyPages(pages)
	if err != nil {
		return err
	}
	for _, ref := range refs {
		encrypted, err := f.Client.Chunk(ctx, m.Node, m.Version, ref)
		if err != nil {
			return err
		}
		plain, err := protocol.DecryptChunk(file.ContentKey, encrypted, contextFor(m, protocol.PurposeContent))
		if err != nil {
			return err
		}
		if n, err := writer.Write(plain); err != nil {
			return err
		} else if n != len(plain) {
			return io.ErrShortWrite
		}
	}
	return nil
}
func (f *Files) Move(ctx context.Context, file, parent *File, name string) error {
	if parent.Manifest.Kind != protocol.KindFolder {
		return errors.New("not a folder")
	}
	name, err := protocol.NormalizeName(name)
	if err != nil {
		return err
	}
	m, err := next(file, protocol.OpMove)
	if err != nil {
		return err
	}
	m.Folder = parent.Manifest.Node
	pub, err := public(parent.NodeKey)
	if err != nil {
		return err
	}
	sealed, err := protocol.SealName(pub, name, contextFor(m, protocol.PurposeName))
	if err != nil {
		return err
	}
	m.Name = &sealed
	m.NameHash, err = protocol.NameHash(parent.NodeKey, name)
	if err != nil {
		return err
	}
	wrapped, err := protocol.SealKey(pub, file.NodeKey, contextFor(m, protocol.PurposeNodeKey))
	if err != nil {
		return err
	}
	m.NodeKey = &wrapped
	if err = m.Sign(f.Client.Key); err != nil {
		return err
	}
	if _, err = f.Client.Commit(ctx, Commit{Manifest: &m}); err != nil {
		return err
	}
	file.Manifest = m
	file.Name = name
	return nil
}
func (f *Files) Remove(ctx context.Context, file *File) error {
	m, err := next(file, protocol.OpRemove)
	if err != nil {
		return err
	}
	m.Count = 0
	m.Pages = []string{}
	if err = m.Sign(f.Client.Key); err != nil {
		return err
	}
	if _, err = f.Client.Commit(ctx, Commit{Manifest: &m}); err != nil {
		return err
	}
	file.Manifest = m
	return nil
}
