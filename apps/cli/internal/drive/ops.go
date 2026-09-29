package drive

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"unicode/utf8"

	protocol "github.com/poweur/identity/drive"
	"golang.org/x/crypto/argon2"
)

type Record struct {
	Position uint64 `json:"position"`
	Author   string `json:"author"`
	Sequence uint64 `json:"sequence"`
	Plain    []byte `json:"-"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
}
type positioned struct {
	Position uint64                `json:"position"`
	Record   protocol.AppendRecord `json:"record"`
}

// CiphertextHash is the SHA-256 and byte length of a replace file's stored
// chunks, in page order. Callers put this in plaintext attachment metadata.
func (f *Files) CiphertextHash(ctx context.Context, file *File) (string, uint64, error) {
	refs, err := f.contentRefs(ctx, file)
	if err != nil {
		return "", 0, err
	}
	sum := sha256.New()
	var size uint64
	for _, ref := range refs {
		blob, err := f.Client.Chunk(ctx, file.Manifest.Node, file.Manifest.Version, ref)
		if err != nil {
			return "", 0, err
		}
		sum.Write(blob)
		size += uint64(len(blob))
	}
	return hex.EncodeToString(sum.Sum(nil)), size, nil
}

// ReadContent downloads a replace file the caller can read by share, decrypting
// with a content key that arrived inside an encrypted message. author is the
// manifest signer's public key; the caller does not need the sender's folder keys.
func (f *Files) ReadContent(ctx context.Context, node string, contentKey []byte, author ed25519.PublicKey) ([]byte, string, uint64, error) {
	if len(contentKey) != 32 {
		return nil, "", 0, errors.New("invalid content key")
	}
	var info Node
	if err := f.Client.Get(ctx, "/nodes/"+url.PathEscape(node), &info); err != nil {
		return nil, "", 0, err
	}
	if info.ID != node || info.Removed || info.Head == "" {
		return nil, "", 0, errors.New("attachment node is missing")
	}
	var manifest protocol.Manifest
	if err := f.Client.Get(ctx, "/nodes/"+url.PathEscape(node)+"/versions/"+url.PathEscape(info.Head), &manifest); err != nil {
		return nil, "", 0, err
	}
	if manifest.Node != node || manifest.Version != info.Head || manifest.Mode != protocol.ModeReplace {
		return nil, "", 0, errors.New("attachment manifest mismatch")
	}
	if err := manifest.Verify(author); err != nil {
		return nil, "", 0, err
	}
	file := &File{Manifest: manifest, ContentKey: contentKey}
	refs, err := fileRefs(ctx, f, file)
	if err != nil {
		return nil, "", 0, err
	}
	sum := sha256.New()
	var plain []byte
	var size uint64
	for _, ref := range refs {
		blob, err := f.Client.Chunk(ctx, node, manifest.Version, ref)
		if err != nil {
			return nil, "", 0, err
		}
		sum.Write(blob)
		size += uint64(len(blob))
		part, err := protocol.DecryptChunk(contentKey, blob, contextFor(manifest, protocol.PurposeContent))
		if err != nil {
			return nil, "", 0, err
		}
		plain = append(plain, part...)
	}
	return plain, hex.EncodeToString(sum.Sum(nil)), size, nil
}

func fileRefs(ctx context.Context, f *Files, file *File) ([]protocol.ChunkRef, error) {
	pages := []protocol.ChunkPage{}
	for _, hash := range file.Manifest.Pages {
		var page protocol.ChunkPage
		if err := f.Client.Get(ctx, "/nodes/"+file.Manifest.Node+"/versions/"+file.Manifest.Version+"/pages/"+hash, &page); err != nil {
			return nil, err
		}
		pages = append(pages, page)
	}
	return file.Manifest.VerifyPages(pages)
}

func (c *Client) History(ctx context.Context, node string) ([]string, error) {
	var body struct {
		Versions []string `json:"versions"`
	}
	err := c.Get(ctx, "/nodes/"+url.PathEscape(node)+"/history", &body)
	return body.Versions, err
}
func (c *Client) Shares(ctx context.Context) ([]protocol.Share, error) {
	var body struct {
		Shares []protocol.Share `json:"shares"`
	}
	err := c.Get(ctx, "/shares", &body)
	return body.Shares, err
}
func (c *Client) Unshare(ctx context.Context, id string) error {
	_, err := c.Commit(ctx, Commit{Unshare: &Unshare{ID: id}})
	return err
}
func (f *Files) nodeInfo(ctx context.Context, id string) (Node, error) {
	var info Node
	err := f.Client.Get(ctx, "/nodes/"+url.PathEscape(id), &info)
	return info, err
}
func (f *Files) recordsFrom(ctx context.Context, node string, from uint64) ([]positioned, error) {
	var out []positioned
	seen := map[uint64]bool{}
	for {
		if seen[from] {
			return nil, errors.New("repeated record cursor")
		}
		seen[from] = true
		var page struct {
			Records []positioned `json:"records"`
			Next    uint64       `json:"next"`
		}
		if err := f.Client.Get(ctx, fmt.Sprintf("/nodes/%s/records?from=%d&limit=1000", url.PathEscape(node), from), &page); err != nil {
			return nil, err
		}
		out = append(out, page.Records...)
		if len(page.Records) == 0 || page.Next == from {
			return out, nil
		}
		from = page.Next
	}
}
func (f *Files) authorCursor(ctx context.Context, file *File) (uint64, string, error) {
	info, err := f.nodeInfo(ctx, file.Manifest.Node)
	if err != nil {
		return 0, "", err
	}
	key, err := f.authorKey(f.Client.Identity)
	if err != nil {
		return 0, "", err
	}
	seq, prev, _ := f.snapshotCursor(ctx, file.Manifest.Node, info.TrimSnapshot)
	from := uint64(1)
	if info.TrimmedBefore > 1 {
		from = info.TrimmedBefore
	}
	records, err := f.recordsFrom(ctx, file.Manifest.Node, from)
	if err != nil {
		return 0, "", err
	}
	for _, item := range records {
		if item.Record.Author != f.Client.Identity {
			continue
		}
		if seq == 0 && item.Record.Sequence != 1 {
			if err = item.Record.Verify(key); err != nil {
				return 0, "", err
			}
		} else if err = item.Record.VerifyNext(key, seq, prev); err != nil {
			return 0, "", err
		}
		seq = item.Record.Sequence
		if prev, err = item.Record.Hash(); err != nil {
			return 0, "", err
		}
	}
	return seq, prev, nil
}
func (f *Files) openRecord(ctx context.Context, file *File, record protocol.AppendRecord) ([]byte, error) {
	if record.Sealed != nil {
		return record.OpenContent(file.NodeKey)
	}
	var plain []byte
	for _, ref := range record.Chunks {
		encrypted, err := f.Client.Chunk(ctx, file.Manifest.Node, "", ref)
		if err != nil {
			return nil, err
		}
		// A record is encrypted under the key generation it was written at;
		// an append file keeps its content key across rotations.
		context, err := protocol.Context(record.Drive, record.Node, protocol.PurposeContent, record.Generation)
		if err != nil {
			return nil, err
		}
		part, err := protocol.DecryptChunk(file.ContentKey, encrypted, context)
		if err != nil {
			return nil, err
		}
		plain = append(plain, part...)
	}
	return plain, nil
}

// Append adds one signed record. The relay orders concurrent appends.
func (f *Files) Append(ctx context.Context, file *File, plaintext []byte) (uint64, error) {
	if file.Manifest.Mode != protocol.ModeAppend || len(file.ContentKey) != 32 {
		return 0, errors.New("not an append file")
	}
	if len(plaintext) == 0 {
		return 0, errors.New("empty append")
	}
	seq, prev, err := f.authorCursor(ctx, file)
	if err != nil {
		return 0, err
	}
	record := protocol.AppendRecord{Format: 1, Drive: f.driveID(), Node: file.Manifest.Node, Author: f.Client.Identity, Generation: file.Manifest.Generation, Sequence: seq + 1, Previous: prev, Chunks: []protocol.ChunkRef{}}
	var blobs [][]byte
	for offset := 0; offset < len(plaintext); offset += protocol.MaxPlaintext {
		end := min(offset+protocol.MaxPlaintext, len(plaintext))
		encrypted, err := protocol.EncryptChunk(file.ContentKey, plaintext[offset:end], contextFor(file.Manifest, protocol.PurposeContent))
		if err != nil {
			return 0, err
		}
		blobs = append(blobs, encrypted)
	}
	refs, err := f.Client.Store(ctx, blobs)
	if err != nil {
		return 0, err
	}
	record.Chunks = refs
	if err = record.Sign(f.Client.Key); err != nil {
		return 0, err
	}
	result, err := f.Client.Commit(ctx, Commit{Records: []protocol.AppendRecord{record}})
	if err != nil {
		return 0, err
	}
	if len(result.Positions) != 1 {
		return 0, errors.New("append returned no position")
	}
	return result.Positions[0], nil
}

// Tail reads decrypted records from a position. from 0 starts at the first retained record.
func (f *Files) Tail(ctx context.Context, file *File, from uint64) ([]Record, error) {
	if file.Manifest.Mode != protocol.ModeAppend || len(file.ContentKey) != 32 {
		return nil, errors.New("not an append file")
	}
	info, err := f.nodeInfo(ctx, file.Manifest.Node)
	if err != nil {
		return nil, err
	}
	if from == 0 {
		from = 1
	}
	if info.TrimmedBefore > from {
		return nil, errors.New("record prefix was trimmed; load the snapshot")
	}
	raw, err := f.recordsFrom(ctx, file.Manifest.Node, from)
	if err != nil {
		return nil, err
	}
	type cursor struct {
		seq  uint64
		prev string
	}
	cursors := map[string]cursor{}
	var out []Record
	for _, item := range raw {
		key, err := f.authorKey(item.Record.Author)
		if err != nil {
			return nil, err
		}
		if err := f.allowAppender(ctx, file.Manifest.Node, item.Record.Author); err != nil {
			return nil, err
		}
		cur := cursors[item.Record.Author]
		if cur.seq == 0 && item.Record.Sequence != 1 {
			err = item.Record.Verify(key)
		} else {
			err = item.Record.VerifyNext(key, cur.seq, cur.prev)
		}
		if err != nil {
			return nil, err
		}
		hash, err := item.Record.Hash()
		if err != nil {
			return nil, err
		}
		cursors[item.Record.Author] = cursor{item.Record.Sequence, hash}
		plain, err := f.openRecord(ctx, file, item.Record)
		if err != nil {
			return nil, err
		}
		row := Record{Position: item.Position, Author: item.Record.Author, Sequence: item.Record.Sequence, Plain: plain}
		if utf8.Valid(plain) {
			row.Text = string(plain)
		} else {
			row.Data = base64.StdEncoding.EncodeToString(plain)
		}
		out = append(out, row)
	}
	if out == nil {
		out = []Record{}
	}
	return out, nil
}

// ShareWith seals this node's key to memberPublic when the role can read.
func (f *Files) ShareWith(ctx context.Context, file *File, member string, memberPublic []byte, role, expires string) (protocol.Share, error) {
	return f.grant(ctx, file, member, "", role, expires, memberPublic, nil, nil)
}

// LinkSecret mixes a URL fragment with the first half of a password KDF.
// Without a password the fragment is the decryption key.
func LinkSecret(fragment, passwordHalf []byte) ([]byte, error) {
	if len(fragment) != 32 || (len(passwordHalf) != 0 && len(passwordHalf) != 32) {
		return nil, errors.New("link secret must be 32 bytes")
	}
	out := append([]byte(nil), fragment...)
	for i := range passwordHalf {
		out[i] ^= passwordHalf[i]
	}
	return out, nil
}

// Link publishes a key-in-fragment share. fragment is the URL secret. A
// password is stretched with the share KDF and mixed into the sealing key;
// the relay stores only the verifier hash.
func (f *Files) Link(ctx context.Context, file *File, role, expires, password string) (protocol.Share, []byte, error) {
	linkID, err := protocol.NewShareID()
	if err != nil {
		return protocol.Share{}, nil, err
	}
	fragment := make([]byte, 32)
	if _, err = rand.Read(fragment); err != nil {
		return protocol.Share{}, nil, err
	}
	secret := fragment
	var salt, verifier []byte
	if password != "" {
		salt = make([]byte, 16)
		if _, err = rand.Read(salt); err != nil {
			return protocol.Share{}, nil, err
		}
		derived := argon2.IDKey([]byte(password), salt, 3, 64*1024, 1, 64)
		secret, err = LinkSecret(fragment, derived[:32])
		if err != nil {
			return protocol.Share{}, nil, err
		}
		verifier = derived[32:]
	}
	pub, err := public(secret)
	if err != nil {
		return protocol.Share{}, nil, err
	}
	share, err := f.grant(ctx, file, "", linkID, role, expires, pub, salt, verifier)
	return share, fragment, err
}
func (f *Files) grant(ctx context.Context, file *File, member, link, role, expires string, recipient, salt, verifier []byte) (protocol.Share, error) {
	return f.issue(ctx, file, protocol.Share{Member: member, Link: link, Role: role, Expires: expires}, recipient, salt, verifier)
}

// issue signs and commits a share on file shaped like s (member or link,
// role, expiry, caps, proof of work) at the node's current generation.
func (f *Files) issue(ctx context.Context, file *File, s protocol.Share, recipient, salt, verifier []byte) (protocol.Share, error) {
	id, err := protocol.NewShareID()
	if err != nil {
		return protocol.Share{}, err
	}
	nodePub, err := public(file.NodeKey)
	if err != nil {
		return protocol.Share{}, err
	}
	role := s.Role
	share := protocol.Share{Format: 1, Drive: f.driveID(), ID: id, Node: file.Manifest.Node, Member: s.Member, Link: s.Link, Role: role, Generation: file.Manifest.Generation, NodePublic: base64.RawURLEncoding.EncodeToString(nodePub), Expires: s.Expires, Caps: s.Caps, PoW: s.PoW, Issuer: f.Client.Identity, Issued: nowRFC3339()}
	if protocol.KeyBearing(role) {
		if len(recipient) != 32 {
			return protocol.Share{}, errors.New("share recipient key must be 32 bytes")
		}
		sealed, err := protocol.SealKey(recipient, file.NodeKey, contextFor(file.Manifest, protocol.PurposeNodeKey))
		if err != nil {
			return protocol.Share{}, err
		}
		share.NodeKey = &sealed
	}
	if salt != nil {
		share.KDF = protocol.ShareKDF
		share.Salt = base64.RawURLEncoding.EncodeToString(salt)
		share.VerifierHash = protocol.VerifierHash(verifier)
	}
	if err = share.Sign(f.Client.Key); err != nil {
		return protocol.Share{}, err
	}
	if _, err = f.Client.Commit(ctx, Commit{Share: &share}); err != nil {
		return protocol.Share{}, err
	}
	f.sharesLoaded = false
	return share, nil
}

// Transfer re-encrypts node (and its descendants) into parent on dst, then
// retires it on the source drive. Shares are not copied.
func (src *Files) Transfer(ctx context.Context, node *File, dst *Files, parent *File) (*File, error) {
	if src.driveID() == dst.driveID() {
		return nil, errors.New("a transfer names another drive")
	}
	if node.Folder == "" {
		return nil, errors.New("the root cannot be transferred")
	}
	if parent != nil && parent.Manifest.Kind != protocol.KindFolder {
		return nil, errors.New("not a folder")
	}
	copied, err := src.copyNode(ctx, dst, parent, node, map[string]bool{})
	if err != nil {
		return nil, err
	}
	_, err = src.Client.Commit(ctx, Commit{Transfer: &Transfer{Node: node.Manifest.Node, To: dst.driveID(), ToNode: copied.Manifest.Node}})
	return copied, err
}
func (src *Files) copyNode(ctx context.Context, dst *Files, parent, node *File, seen map[string]bool) (*File, error) {
	if seen[node.Manifest.Node] || len(seen) >= 10000 {
		return nil, errors.New("invalid copy")
	}
	seen[node.Manifest.Node] = true
	switch {
	case node.Manifest.Kind == protocol.KindFolder:
		created, err := dst.Create(ctx, parent, node.Name, protocol.KindFolder, nil)
		if err != nil {
			return nil, err
		}
		children, err := src.List(ctx, node)
		if err != nil {
			return nil, err
		}
		for _, child := range children {
			if _, err = src.copyNode(ctx, dst, created, child, seen); err != nil {
				return nil, err
			}
		}
		return created, nil
	case node.Manifest.Mode == protocol.ModeAppend:
		created, err := dst.CreateAppend(ctx, parent, node.Name)
		if err != nil {
			return nil, err
		}
		records, err := src.Tail(ctx, node, 1)
		if err != nil {
			return nil, err
		}
		for _, record := range records {
			if _, err = dst.Append(ctx, created, record.Plain); err != nil {
				return nil, err
			}
		}
		return created, nil
	default:
		var buf bytes.Buffer
		if err := src.Read(ctx, node, &buf); err != nil {
			return nil, err
		}
		return dst.Create(ctx, parent, node.Name, protocol.KindFile, bytes.NewReader(buf.Bytes()))
	}
}

// Rotated reports a rotation: shares re-issued to remaining members, and the
// key-bearing shares that could not be (links, or members whose encryption
// key did not resolve) and must be recreated.
type Rotated struct {
	File     *File
	Reissued int
	Stale    []protocol.Share
}

type subtree struct {
	node     *File
	content  []byte
	children []*subtree
}

// Rotate re-keys file and everything below it, as the relay requires after a
// revocation (rotate_required): new node keys, a new content key and
// re-encrypted content for replace files, names and keys re-sealed under
// each re-keyed folder, and the remaining members' shares re-issued at the
// new generation (when EncryptionKeys resolves them). Append files keep
// their node and content keys — past records, including guests' sealed ones,
// cannot be re-encrypted — so new records there are protected by access
// control, not re-keying. Needs the parent's key: the drive's owner, or an
// admin of the parent.
func (f *Files) Rotate(ctx context.Context, file *File) (*Rotated, error) {
	parentKey := f.EncryptionKey
	if file.Folder != "" {
		parent, err := f.Open(ctx, file.Folder)
		if err != nil {
			return nil, err
		}
		parentKey = parent.NodeKey
	} else if !f.owner() {
		return nil, errors.New("only the owner rotates the root")
	}
	// Read the whole subtree with the old keys first: once a folder is
	// re-keyed, its children no longer open through the relay's tree.
	var collect func(*File) (*subtree, error)
	collect = func(n *File) (*subtree, error) {
		t := &subtree{node: n}
		if n.Manifest.Kind == protocol.KindFile && n.Manifest.Mode == protocol.ModeReplace {
			var buf bytes.Buffer
			if err := f.Read(ctx, n, &buf); err != nil {
				return nil, err
			}
			t.content = buf.Bytes()
		}
		if n.Manifest.Kind == protocol.KindFolder {
			children, err := f.List(ctx, n)
			if err != nil {
				return nil, err
			}
			for _, child := range children {
				c, err := collect(child)
				if err != nil {
					return nil, err
				}
				t.children = append(t.children, c)
			}
		}
		return t, nil
	}
	tree, err := collect(file)
	if err != nil {
		return nil, err
	}
	shares, err := f.Client.Shares(ctx)
	if err != nil {
		return nil, err
	}
	out := &Rotated{}
	var rekey func(t *subtree, parentKey []byte, parent *File) (*File, error)
	rekey = func(t *subtree, parentKey []byte, parent *File) (*File, error) {
		old := t.node
		m, err := f.next(old, protocol.OpRotate)
		if err != nil {
			return nil, err
		}
		m.Generation = old.Manifest.Generation + 1
		appendFile := old.Manifest.Kind == protocol.KindFile && old.Manifest.Mode == protocol.ModeAppend
		nodeKey := old.NodeKey
		if !appendFile {
			if nodeKey, err = randomKey(); err != nil {
				return nil, err
			}
		}
		parentPub, err := public(parentKey)
		if err != nil {
			return nil, err
		}
		wrapped, err := protocol.SealKey(parentPub, nodeKey, contextFor(m, protocol.PurposeNodeKey))
		if err != nil {
			return nil, err
		}
		m.NodeKey = &wrapped
		contentKey := old.ContentKey
		var pages []protocol.ChunkPage
		if old.Manifest.Kind == protocol.KindFile {
			if !appendFile {
				if contentKey, err = randomKey(); err != nil {
					return nil, err
				}
				if pages, err = f.upload(ctx, &m, contentKey, bytes.NewReader(t.content)); err != nil {
					return nil, err
				}
			}
			nodePub, err := public(nodeKey)
			if err != nil {
				return nil, err
			}
			sealed, err := protocol.SealKey(nodePub, contentKey, contextFor(m, protocol.PurposeContentKey))
			if err != nil {
				return nil, err
			}
			m.ContentKey = &sealed
		}
		if err = m.Sign(f.Client.Key); err != nil {
			return nil, err
		}
		if _, err = f.Client.Commit(ctx, Commit{Manifest: &m, Pages: pages}); err != nil {
			return nil, err
		}
		rotated := &File{Manifest: m, Name: old.Name, Folder: old.Folder, NodeKey: nodeKey, ContentKey: contentKey}
		// The name was sealed to the parent's retired key: re-seal it in place.
		if parent != nil {
			if err := f.Move(ctx, rotated, parent, rotated.Name); err != nil {
				return nil, err
			}
		}
		for _, s := range shares {
			if s.Node != old.Manifest.Node || !protocol.KeyBearing(s.Role) || s.Generation >= m.Generation {
				continue
			}
			if s.Link != "" || f.EncryptionKeys == nil {
				out.Stale = append(out.Stale, s)
				continue
			}
			recipient, err := f.EncryptionKeys(ctx, s.Member)
			if err != nil {
				out.Stale = append(out.Stale, s)
				continue
			}
			if _, err := f.issue(ctx, rotated, s, recipient, nil, nil); err != nil {
				return nil, err
			}
			if err := f.Client.Unshare(ctx, s.ID); err != nil {
				return nil, err
			}
			out.Reissued++
		}
		for _, child := range t.children {
			if _, err := rekey(child, nodeKey, rotated); err != nil {
				return nil, err
			}
		}
		return rotated, nil
	}
	if out.File, err = rekey(tree, parentKey, nil); err != nil {
		return nil, err
	}
	f.sharesLoaded, f.nodeShares = false, nil
	return out, nil
}

// RotateIfRequired rotates node when a revocation left it waiting for new
// keys; it returns nil when no rotation was needed.
func (f *Files) RotateIfRequired(ctx context.Context, node string) (*Rotated, error) {
	info, err := f.nodeInfo(ctx, node)
	if err != nil || !info.RotateRequired || info.Removed {
		return nil, err
	}
	file, err := f.Open(ctx, node)
	if err != nil {
		return nil, err
	}
	return f.Rotate(ctx, file)
}

func randomKey() ([]byte, error) {
	key := make([]byte, 32)
	_, err := rand.Read(key)
	return key, err
}
