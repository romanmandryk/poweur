package drive

import (
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	protocol "github.com/poweur/identity/drive"
)

// Files implements encrypted file access. Authors other than the signing
// identity fail closed until Authors resolves their keys; a resolved author
// must also hold a share role that allows the version.
type Files struct {
	Client *Client
	// EncryptionKey is the caller's X25519 private key: the identity's, or
	// for a link holder the link secret. The owner opens the root with it; a
	// member opens the nodes shared with them.
	EncryptionKey []byte
	Authors       func(author string) (ed25519.PublicKey, error)
	// GroupMembers resolves a group identity's roster, so versions written
	// by a group's members can be verified. Nil fails such versions closed.
	GroupMembers func(ctx context.Context, group string) ([]string, error)
	shares       []protocol.Share
	sharesLoaded bool
	nodeShares   map[string][]protocol.Share
}
type File struct {
	Manifest            protocol.Manifest
	Name                string
	Folder              string
	NodeKey, ContentKey []byte
}
type Node struct {
	ID            string    `json:"id"`
	Head          string    `json:"head"`
	Folder        string    `json:"folder"`
	Kind          string    `json:"kind"`
	Mode          string    `json:"mode"`
	Generation    uint64    `json:"generation"`
	Removed       bool      `json:"removed"`
	Position      uint64    `json:"position"`
	TrimmedBefore uint64    `json:"trimmed_before"`
	TrimSnapshot  *Snapshot `json:"trim_snapshot"`
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
func (f *Files) driveID() string { return f.Client.driveID() }
func (f *Files) authorKey(author string) (ed25519.PublicKey, error) {
	if key, ok := protocol.GuestKey(author); ok {
		return key, nil
	}
	if author == f.Client.Identity {
		if len(f.Client.Key) != ed25519.PrivateKeySize {
			return nil, errors.New("invalid signing key")
		}
		return f.Client.Key.Public().(ed25519.PublicKey), nil
	}
	if f.Authors == nil {
		return nil, errors.New("untrusted manifest author")
	}
	return f.Authors(author)
}
func (f *Files) verify(m protocol.Manifest) error {
	if m.Drive != f.driveID() {
		return errors.New("manifest drive mismatch")
	}
	key, err := f.authorKey(m.Author)
	if err != nil {
		return err
	}
	return m.Verify(key)
}

// owner reports whether the caller owns the drive.
func (f *Files) owner() bool { return f.Client.LinkID == "" && f.Client.Identity == f.driveID() }

// sharesOn lists the shares on a node and its ancestors (nearest first).
func (f *Files) sharesOn(ctx context.Context, node string) ([]protocol.Share, error) {
	if cached, ok := f.nodeShares[node]; ok {
		return cached, nil
	}
	var body struct {
		Shares []protocol.Share `json:"shares"`
	}
	if err := f.Client.Get(ctx, "/nodes/"+url.PathEscape(node)+"/shares", &body); err != nil {
		return nil, err
	}
	if f.nodeShares == nil {
		f.nodeShares = map[string][]protocol.Share{}
	}
	f.nodeShares[node] = body.Shares
	return body.Shares, nil
}

// trusted verifies a share's signature and that its issuer could grant it:
// the drive's owner, or an admin through a share that is itself trusted.
func (f *Files) trusted(ctx context.Context, s protocol.Share, depth int) error {
	if s.Drive != f.driveID() || depth > 8 {
		return errors.New("share does not belong to this drive")
	}
	key, err := f.authorKey(s.Issuer)
	if err != nil {
		return err
	}
	if err := s.Verify(key); err != nil {
		return err
	}
	if s.Issuer == f.driveID() {
		return nil
	}
	shares, err := f.sharesOn(ctx, s.Node)
	if err != nil {
		return err
	}
	for _, grant := range shares {
		if grant.Member == s.Issuer && grant.Role == protocol.RoleAdmin && !grant.ExpiredAt(time.Now()) && f.trusted(ctx, grant, depth+1) == nil {
			return nil
		}
	}
	return errors.New("share issuer cannot administer this node")
}

// holds reports whether a share applies to author: its member, a member of
// the group it names, or — for a guest author — the link it grants.
func (f *Files) holds(ctx context.Context, s protocol.Share, author string) bool {
	switch {
	case protocol.IsGuest(author):
		return s.Link != ""
	case s.Member == author:
		return true
	case s.Member == "" || f.GroupMembers == nil:
		return false
	}
	members, err := f.GroupMembers(ctx, s.Member)
	if err != nil {
		return false
	}
	for _, m := range members {
		if strings.EqualFold(m, author) {
			return true
		}
	}
	return false
}

// allowAuthor checks that a version's author held the role it needed, from
// trusted shares on the node (or, for a create or move, its folder).
func (f *Files) allowAuthor(ctx context.Context, m protocol.Manifest) error {
	if m.Author == f.driveID() {
		return nil
	}
	need := protocol.RoleWrite
	switch m.Operation {
	case protocol.OpCreate:
		need = protocol.RoleCreate
	case protocol.OpRotate:
		need = protocol.RoleAdmin
	}
	target := m.Node
	if m.Folder != "" && (m.Operation == protocol.OpCreate || m.Operation == protocol.OpMove) {
		target = m.Folder
	}
	shares, err := f.sharesOn(ctx, target)
	if err != nil {
		return err
	}
	for _, s := range shares {
		if protocol.RoleGrants(s.Role, need) && f.holds(ctx, s, m.Author) && f.trusted(ctx, s, 0) == nil {
			return nil
		}
	}
	return errors.New("author role does not allow this version")
}

// allowAppender checks that a record's author may append to node.
func (f *Files) allowAppender(ctx context.Context, node, author string) error {
	if author == f.driveID() {
		return nil
	}
	shares, err := f.sharesOn(ctx, node)
	if err != nil {
		return err
	}
	for _, s := range shares {
		if protocol.RoleGrants(s.Role, protocol.RoleAppend) && f.holds(ctx, s, author) && f.trusted(ctx, s, 0) == nil {
			return nil
		}
	}
	return errors.New("record author may not append to this file")
}

// myShare finds the caller's own key-bearing share on node.
func (f *Files) myShare(ctx context.Context, node string) (*protocol.Share, error) {
	shares, err := f.shareList(ctx)
	if err != nil {
		return nil, err
	}
	for i := range shares {
		s := shares[i]
		mine := f.Client.LinkID != "" && s.Link == f.Client.LinkID || f.Client.LinkID == "" && s.Member == f.Client.Identity
		if mine && s.Node == node && s.NodeKey != nil && !s.ExpiredAt(time.Now()) {
			if err := f.trusted(ctx, s, 0); err != nil {
				return nil, err
			}
			return &s, nil
		}
	}
	return nil, nil
}

func (f *Files) shareList(ctx context.Context) ([]protocol.Share, error) {
	if f.sharesLoaded {
		return f.shares, nil
	}
	var body struct {
		Shares []protocol.Share `json:"shares"`
	}
	if err := f.Client.Get(ctx, "/shares", &body); err != nil {
		return nil, err
	}
	f.shares, f.sharesLoaded = body.Shares, true
	return f.shares, nil
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
	if err = f.verify(m); err != nil {
		return m, err
	}
	return m, f.allowAuthor(ctx, m)
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
	// A member (or link holder) opens a shared node with the key its share
	// carries, and nodes below it through their parents. Only the owner
	// walks up to the root.
	var key []byte
	var parentKey []byte
	if !f.owner() {
		share, err := f.myShare(ctx, node)
		if err != nil {
			return nil, err
		}
		if share != nil {
			if share.Generation != head.Generation {
				return nil, errors.New("the share predates a key rotation; ask for it to be re-issued")
			}
			ctxBytes, err := protocol.Context(f.driveID(), node, protocol.PurposeNodeKey, share.Generation)
			if err != nil {
				return nil, err
			}
			if key, err = protocol.OpenKey(f.EncryptionKey, *share.NodeKey, ctxBytes); err != nil {
				return nil, err
			}
			if pub, err := public(key); err != nil || base64.RawURLEncoding.EncodeToString(pub) != share.NodePublic {
				return nil, errors.New("share key does not match its node")
			}
		}
	}
	if key == nil {
		parentKey = f.EncryptionKey
		if info.Folder != "" {
			parent, err := f.open(ctx, info.Folder, ancestors)
			if err != nil {
				return nil, err
			}
			if parent.Manifest.Kind != protocol.KindFolder {
				return nil, errors.New("parent is not a folder")
			}
			parentKey = parent.NodeKey
		} else if !f.owner() {
			return nil, errors.New("no share opens this node")
		}
		if key, err = protocol.OpenKey(parentKey, *keyVersion.NodeKey, contextFor(*keyVersion, protocol.PurposeNodeKey)); err != nil {
			return nil, err
		}
	}
	result := &File{Manifest: head, NodeKey: key, Folder: info.Folder}
	// The name is sealed to the parent: a shared node's own name is known
	// only to those who can open its parent.
	if nameVersion != nil && parentKey != nil {
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
	// The owner's paths start at the root. A member's start at a node shared
	// with them, named by its ID: /<node-id>/sub/path.
	var current *File
	var err error
	if f.owner() {
		current, err = f.Root(ctx)
	} else if len(parts) == 0 {
		return nil, errors.New("a shared path starts with the shared node's ID")
	} else {
		current, err = f.Open(ctx, parts[0])
		parts = parts[1:]
	}
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

// Shared opens every node shared with the caller that carries a key.
func (f *Files) Shared(ctx context.Context) ([]*File, error) {
	shares, err := f.shareList(ctx)
	if err != nil {
		return nil, err
	}
	var out []*File
	seen := map[string]bool{}
	for _, s := range shares {
		mine := f.Client.LinkID != "" && s.Link == f.Client.LinkID || f.Client.LinkID == "" && s.Member == f.Client.Identity
		if !mine || s.NodeKey == nil || seen[s.Node] || s.ExpiredAt(time.Now()) {
			continue
		}
		seen[s.Node] = true
		file, err := f.Open(ctx, s.Node)
		if err != nil {
			return nil, fmt.Errorf("shared node %s: %w", s.Node, err)
		}
		out = append(out, file)
	}
	return out, nil
}

func (f *Files) Create(ctx context.Context, parent *File, name, kind string, reader io.Reader) (*File, error) {
	mode := ""
	if kind == protocol.KindFile {
		mode = protocol.ModeReplace
	}
	return f.create(ctx, parent, name, kind, mode, reader)
}

// CreateAppend starts an empty append-mode file. Records are added with Append.
func (f *Files) CreateAppend(ctx context.Context, parent *File, name string) (*File, error) {
	return f.create(ctx, parent, name, protocol.KindFile, protocol.ModeAppend, nil)
}
func (f *Files) create(ctx context.Context, parent *File, name, kind, mode string, reader io.Reader) (*File, error) {
	if parent != nil && parent.Manifest.Kind != protocol.KindFolder {
		return nil, errors.New("not a folder")
	}
	if parent == nil && kind != protocol.KindFolder {
		return nil, errors.New("root must be a folder")
	}
	if mode == protocol.ModeAppend && reader != nil {
		return nil, errors.New("an append file starts empty")
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
	result := &File{Name: name, NodeKey: key, Folder: "", Manifest: protocol.Manifest{Format: 1, Drive: f.driveID(), Node: node, Version: version, Operation: protocol.OpCreate, Author: f.Client.Identity, Generation: 1, Kind: kind, Mode: mode, Pages: []string{}}}
	m := &result.Manifest
	parentKey := f.EncryptionKey
	if parent != nil {
		parentKey = parent.NodeKey
		m.Folder = parent.Manifest.Node
		result.Folder = parent.Manifest.Node
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
		if mode != protocol.ModeAppend {
			pages, err = f.upload(ctx, m, result.ContentKey, reader)
			if err != nil {
				return nil, err
			}
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
	var blobs [][]byte
	if reader != nil {
		buffer := make([]byte, protocol.MaxPlaintext)
		for {
			n, err := io.ReadFull(reader, buffer)
			if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
				return nil, err
			}
			if n > 0 {
				encrypted, e := protocol.EncryptChunk(key, append([]byte(nil), buffer[:n]...), contextFor(*m, protocol.PurposeContent))
				if e != nil {
					return nil, e
				}
				blobs = append(blobs, encrypted)
			}
			if err != nil {
				break
			}
		}
	}
	refs, err := f.Client.Store(ctx, blobs)
	if err != nil {
		return nil, err
	}
	pages, hashes, err := protocol.SplitPages(m.Drive, m.Node, refs)
	if hashes == nil {
		hashes = []string{}
	}
	m.Pages = hashes
	m.Count = uint64(len(refs))
	return pages, err
}

// next starts a new version of file authored by the caller.
func (f *Files) next(file *File, operation string) (protocol.Manifest, error) {
	m := file.Manifest
	m.Author = f.Client.Identity
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
	m, err := f.next(file, protocol.OpReplace)
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

func (f *Files) contentRefs(ctx context.Context, file *File) ([]protocol.ChunkRef, error) {
	if file.Manifest.Mode != protocol.ModeReplace || len(file.ContentKey) != 32 {
		return nil, errors.New("not a replace file")
	}
	m := file.Manifest
	if err := f.verify(m); err != nil {
		return nil, err
	}
	pages := []protocol.ChunkPage{}
	for _, hash := range m.Pages {
		var page protocol.ChunkPage
		if err := f.Client.Get(ctx, "/nodes/"+m.Node+"/versions/"+m.Version+"/pages/"+hash, &page); err != nil {
			return nil, err
		}
		pages = append(pages, page)
	}
	return m.VerifyPages(pages)
}

// Read authenticates each bounded chunk before writing plaintext. Callers
// writing a file should use a temporary file and rename after success.
func (f *Files) Read(ctx context.Context, file *File, writer io.Writer) error {
	refs, err := f.contentRefs(ctx, file)
	if err != nil {
		return err
	}
	for _, ref := range refs {
		plain, err := f.plainChunk(ctx, file, ref)
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

// ReadRange writes plaintext bytes [offset, offset+length). Files written by
// this client use fixed MaxPlaintext chunks, so earlier chunks are not
// downloaded. A short chunk before the last one of the file is refused.
func (f *Files) ReadRange(ctx context.Context, file *File, offset, length int64, writer io.Writer) error {
	if offset < 0 || length == 0 {
		if length == 0 && offset >= 0 {
			return nil
		}
		return errors.New("invalid range")
	}
	refs, err := f.contentRefs(ctx, file)
	if err != nil {
		return err
	}
	if len(refs) == 0 || offset/protocol.MaxPlaintext >= int64(len(refs)) {
		return io.EOF
	}
	start := int(offset / protocol.MaxPlaintext)
	end := len(refs)
	if length > 0 {
		end = int((offset + length + protocol.MaxPlaintext - 1) / protocol.MaxPlaintext)
		if end > len(refs) {
			end = len(refs)
		}
	}
	for i := start; i < end; i++ {
		plain, err := f.plainChunk(ctx, file, refs[i])
		if err != nil {
			return err
		}
		if i != len(refs)-1 && len(plain) != protocol.MaxPlaintext {
			return errors.New("variable chunk size; read from the start")
		}
		chunkStart := int64(i) * protocol.MaxPlaintext
		from, to := 0, len(plain)
		if offset > chunkStart {
			from = int(offset - chunkStart)
		}
		if length > 0 {
			if endByte := offset + length; chunkStart+int64(to) > endByte {
				to = int(endByte - chunkStart)
			}
		}
		if from < to {
			if n, err := writer.Write(plain[from:to]); err != nil {
				return err
			} else if n != to-from {
				return io.ErrShortWrite
			}
		}
	}
	return nil
}
func (f *Files) plainChunk(ctx context.Context, file *File, ref protocol.ChunkRef) ([]byte, error) {
	encrypted, err := f.Client.Chunk(ctx, file.Manifest.Node, file.Manifest.Version, ref)
	if err != nil {
		return nil, err
	}
	return protocol.DecryptChunk(file.ContentKey, encrypted, contextFor(file.Manifest, protocol.PurposeContent))
}
func (f *Files) Move(ctx context.Context, file, parent *File, name string) error {
	if parent.Manifest.Kind != protocol.KindFolder {
		return errors.New("not a folder")
	}
	name, err := protocol.NormalizeName(name)
	if err != nil {
		return err
	}
	m, err := f.next(file, protocol.OpMove)
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
	file.Folder = parent.Manifest.Node
	return nil
}
func (f *Files) Remove(ctx context.Context, file *File) error {
	m, err := f.next(file, protocol.OpRemove)
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
