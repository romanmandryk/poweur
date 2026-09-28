package drive

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strconv"

	identity "github.com/poweur/identity"
)

// Version manifests (E20-T2). A manifest is one signed version of a node: what
// changed (a create, a content replacement, a move, a key rotation or a
// removal) and the complete ordered content of the node at that version.
// Content is not listed inline: it is a list of immutable chunk-list pages of
// at most PageSize references, and the manifest signs the ordered page hashes
// and the total reference count.
//
// The relay stores and sequences manifests but cannot read names or keys; it
// checks shape, the author's role and the base version. Readers verify the
// signature with the author's resolved key and the author's role before using
// a version.

// PageSize is the most chunk references one page holds.
const PageSize = 1024

// Manifest operations.
const (
	OpCreate  = "create"
	OpReplace = "replace"
	OpMove    = "move"
	OpRotate  = "rotate"
	OpRemove  = "remove"
)

// Node kinds and file modes.
const (
	KindFile    = "file"
	KindFolder  = "folder"
	ModeReplace = "replace"
	ModeAppend  = "append"
)

// Context purposes for the envelopes a manifest carries. Each is bound to the
// drive, the node the envelope belongs to and the key generation.
const (
	PurposeName       = "name"
	PurposeNodeKey    = "node-key"
	PurposeContentKey = "content-key"
	PurposeContent    = "content"
)

// ChunkPage is an immutable, content-addressed slice of a node's chunk list.
// It names its drive and node so a page cannot be spliced into another node.
type ChunkPage struct {
	Format int        `json:"format"`
	Drive  string     `json:"drive"`
	Node   string     `json:"node"`
	Chunks []ChunkRef `json:"chunks"`
}

// Manifest is one signed version of a node.
type Manifest struct {
	Format     int    `json:"format"`
	Drive      string `json:"drive"`
	Node       string `json:"node"`
	Version    string `json:"version"`
	Parent     string `json:"parent"`
	Operation  string `json:"operation"`
	Author     string `json:"author"`
	Generation uint64 `json:"generation"`
	Kind       string `json:"kind"`
	Mode       string `json:"mode"`
	// Folder is the containing folder's node ID, set on create and move.
	// Empty only for the drive root.
	Folder string `json:"folder"`
	// Name is the node's name sealed to the containing folder's public key
	// (context PurposeName on this node); NameHash is its lookup token.
	Name     *identity.SealedPayload `json:"name,omitempty"`
	NameHash string                  `json:"name_hash"`
	// NodeKey is this node's private key sealed to the containing folder's
	// public key, or to the identity encryption key for the root.
	NodeKey *identity.SealedPayload `json:"node_key,omitempty"`
	// ContentKey is a file's content key sealed to this node's public key.
	ContentKey *identity.SealedPayload `json:"content_key,omitempty"`
	Count      uint64                  `json:"count"`
	Pages      []string                `json:"pages"`
	Signature  string                  `json:"signature"`
}

// NewNodeID returns a random 16-byte node ID as lowercase hex.
func NewNodeID() (string, error) { return randomHex(16) }

// NewVersionID returns a random 16-byte version ID as lowercase hex.
func NewVersionID() (string, error) { return randomHex(16) }

// NewNameToken returns an opaque name token for a sealed create: a writer
// holding only the folder's public key cannot compute the real name hash, so
// the child is indexed under a random token until the owner renames it.
func NewNameToken() (string, error) { return randomHex(32) }

func randomHex(n int) (string, error) {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// SplitPages cuts refs into ordered pages of at most PageSize references and
// returns them with their hashes. No refs means no pages.
func SplitPages(drive, node string, refs []ChunkRef) ([]ChunkPage, []string, error) {
	var pages []ChunkPage
	var hashes []string
	for start := 0; start < len(refs); start += PageSize {
		end := min(start+PageSize, len(refs))
		page := ChunkPage{Format: 1, Drive: drive, Node: node, Chunks: append([]ChunkRef(nil), refs[start:end]...)}
		hash, err := page.Hash()
		if err != nil {
			return nil, nil, err
		}
		pages = append(pages, page)
		hashes = append(hashes, hash)
	}
	return pages, hashes, nil
}

func validChunkRef(chunk ChunkRef) bool {
	return validHex(chunk.ID, 32) && chunk.Size >= 40+PaddingBucket && chunk.Size <= MaxChunkBytes && (chunk.Size-40)%PaddingBucket == 0
}

func (p ChunkPage) Validate() error {
	if p.Format != 1 {
		return errors.New("unsupported page format")
	}
	if !validIdentity(p.Drive) || !validHex(p.Node, 16) {
		return errors.New("invalid page drive or node")
	}
	if len(p.Chunks) == 0 || len(p.Chunks) > PageSize {
		return errors.New("a page holds 1 to 1024 chunk references")
	}
	for _, chunk := range p.Chunks {
		if !validChunkRef(chunk) {
			return errors.New("invalid chunk reference")
		}
	}
	return nil
}

// Canonical encodes the page domain followed by length-prefixed fields:
// format, drive, node, count, then each chunk's ID and stored size.
func (p ChunkPage) Canonical() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	fields := []string{"1", p.Drive, p.Node, strconv.Itoa(len(p.Chunks))}
	for _, chunk := range p.Chunks {
		fields = append(fields, chunk.ID, strconv.FormatUint(chunk.Size, 10))
	}
	return lengthPrefixed("poweur/drive/page/v1\n", fields), nil
}

// Hash is the page's content address: SHA-256 of its canonical bytes.
func (p ChunkPage) Hash() (string, error) {
	raw, err := p.Canonical()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func lengthPrefixed(prefix string, fields []string) []byte {
	out := []byte(prefix)
	for _, field := range fields {
		out = binary.BigEndian.AppendUint32(out, uint32(len(field)))
		out = append(out, field...)
	}
	return out
}

func sealedFields(p *identity.SealedPayload) []string {
	if p == nil {
		return []string{"", "", ""}
	}
	return []string{p.EphemeralPublicKey, p.Nonce, p.Ciphertext}
}

func (m Manifest) Validate() error {
	if m.Format != 1 {
		return errors.New("unsupported manifest format")
	}
	if !validIdentity(m.Drive) || !validIdentity(m.Author) {
		return errors.New("invalid drive or author")
	}
	if !validHex(m.Node, 16) || !validHex(m.Version, 16) {
		return errors.New("invalid node or version ID")
	}
	if m.Generation == 0 || m.Generation > MaxCounter || m.Count > MaxCounter {
		return errors.New("invalid generation or count")
	}
	if m.Folder != "" && (!validHex(m.Folder, 16) || m.Folder == m.Node) {
		return errors.New("invalid containing folder")
	}
	if m.Parent != "" && !validHex(m.Parent, 16) || m.Parent == m.Version {
		return errors.New("invalid parent version")
	}
	switch m.Kind {
	case KindFolder:
		if m.Mode != "" || m.ContentKey != nil || m.Count != 0 || len(m.Pages) != 0 {
			return errors.New("a folder has no mode, content key or content")
		}
	case KindFile:
		if m.Mode != ModeReplace && m.Mode != ModeAppend {
			return errors.New("a file's mode is replace or append")
		}
	default:
		return errors.New("kind is file or folder")
	}
	if m.Pages == nil || len(m.Pages) > int((MaxCounter+PageSize-1)/PageSize) {
		return errors.New("invalid page list")
	}
	for _, page := range m.Pages {
		if !validHex(page, 32) {
			return errors.New("invalid page hash")
		}
	}
	// Every page but the last is full; the last holds the remainder.
	pages := uint64(len(m.Pages))
	if pages == 0 && m.Count != 0 || pages > 0 && (m.Count <= (pages-1)*PageSize || m.Count > pages*PageSize) {
		return errors.New("page list does not match the chunk count")
	}
	hasName := m.Name != nil || m.NameHash != ""
	if (m.Name != nil) != (m.NameHash != "") || m.NameHash != "" && !validHex(m.NameHash, 32) {
		return errors.New("name and name hash go together")
	}
	for _, payload := range []*identity.SealedPayload{m.Name, m.NodeKey, m.ContentKey} {
		if payload != nil {
			if err := validatePayload(*payload); err != nil {
				return err
			}
		}
	}
	switch m.Operation {
	case OpCreate:
		if m.Parent != "" || m.NodeKey == nil {
			return errors.New("a create has no parent version and carries the node key")
		}
		// Only the root has no containing folder, and the root has no name.
		if (m.Folder == "") == hasName {
			return errors.New("a create names its folder and name, except the root")
		}
		if m.Kind == KindFile && m.ContentKey == nil {
			return errors.New("a file create carries its content key")
		}
	case OpReplace:
		if m.Parent == "" || m.Kind != KindFile || hasName || m.NodeKey != nil || m.ContentKey != nil || m.Folder != "" {
			return errors.New("a replace changes only a file's content")
		}
	case OpMove:
		if m.Parent == "" || m.Folder == "" || !hasName || m.NodeKey == nil || m.ContentKey != nil {
			return errors.New("a move carries the new folder, name and re-sealed node key")
		}
	case OpRotate:
		if m.Parent == "" || hasName || m.Folder != "" || m.NodeKey == nil || (m.Kind == KindFile) != (m.ContentKey != nil) {
			return errors.New("a rotation carries new keys and nothing else")
		}
	case OpRemove:
		if m.Parent == "" || hasName || m.Folder != "" || m.NodeKey != nil || m.ContentKey != nil || m.Count != 0 {
			return errors.New("a removal carries no keys, name or content")
		}
	default:
		return errors.New("unknown manifest operation")
	}
	return nil
}

// Canonical encodes the manifest domain followed by length-prefixed fields:
// format, drive, node, version, parent, operation, author, generation, kind,
// mode, folder, name (ephemeral key, nonce, ciphertext), name hash, node key
// (three fields), content key (three fields), count, page count, then each
// page hash. Absent envelopes are three empty fields; numbers are decimal.
func (m Manifest) Canonical() ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	fields := []string{"1", m.Drive, m.Node, m.Version, m.Parent, m.Operation, m.Author,
		strconv.FormatUint(m.Generation, 10), m.Kind, m.Mode, m.Folder}
	fields = append(fields, sealedFields(m.Name)...)
	fields = append(fields, m.NameHash)
	fields = append(fields, sealedFields(m.NodeKey)...)
	fields = append(fields, sealedFields(m.ContentKey)...)
	fields = append(fields, strconv.FormatUint(m.Count, 10), strconv.Itoa(len(m.Pages)))
	fields = append(fields, m.Pages...)
	return lengthPrefixed("poweur/drive/manifest-sign/v1\n", fields), nil
}

func (m *Manifest) Sign(privateKey ed25519.PrivateKey) error {
	if len(privateKey) != ed25519.PrivateKeySize {
		return errors.New("invalid signing key length")
	}
	raw, err := m.Canonical()
	if err != nil {
		return err
	}
	m.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, raw))
	return nil
}

func (m Manifest) Verify(publicKey ed25519.PublicKey) error {
	raw, err := m.Canonical()
	if err != nil {
		return err
	}
	sig, err := base64.RawURLEncoding.Strict().DecodeString(m.Signature)
	if err != nil || base64.RawURLEncoding.EncodeToString(sig) != m.Signature || len(publicKey) != ed25519.PublicKeySize || len(sig) != ed25519.SignatureSize || !ed25519.Verify(publicKey, raw, sig) {
		return errors.New("manifest signature verification failed")
	}
	return nil
}

// Hash identifies the signed manifest: SHA-256 of the hash domain, the
// canonical bytes and the raw signature. Verify before trusting it.
func (m Manifest) Hash() (string, error) {
	raw, err := m.Canonical()
	if err != nil {
		return "", err
	}
	sig, err := base64.RawURLEncoding.Strict().DecodeString(m.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize || base64.RawURLEncoding.EncodeToString(sig) != m.Signature {
		return "", errors.New("invalid signature encoding")
	}
	data := append([]byte("poweur/drive/manifest-hash/v1\n"), raw...)
	data = append(data, sig...)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// VerifyPages checks that pages are exactly the manifest's content: the same
// hashes in the same order, each page for this drive and node, and the total
// reference count. It returns the ordered chunk references.
func (m Manifest) VerifyPages(pages []ChunkPage) ([]ChunkRef, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	if len(pages) != len(m.Pages) {
		return nil, errors.New("page count does not match the manifest")
	}
	var refs []ChunkRef
	for i, page := range pages {
		if page.Drive != m.Drive || page.Node != m.Node {
			return nil, errors.New("page belongs to another node")
		}
		hash, err := page.Hash()
		if err != nil {
			return nil, err
		}
		if hash != m.Pages[i] {
			return nil, errors.New("page does not match the manifest")
		}
		refs = append(refs, page.Chunks...)
	}
	if uint64(len(refs)) != m.Count {
		return nil, errors.New("chunk count does not match the manifest")
	}
	return refs, nil
}

// EnvelopeContext is the Context an envelope in a manifest is bound to:
// the drive, this manifest's node, the purpose and the key generation.
func (m Manifest) EnvelopeContext(purpose string) ([]byte, error) {
	return Context(m.Drive, m.Node, purpose, m.Generation)
}
