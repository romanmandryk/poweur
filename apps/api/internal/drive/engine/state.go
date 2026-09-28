// Package engine is the storage-v2 drive engine (E20-T4): one journalled,
// end-to-end encrypted drive per identity over a provider.Store.
//
// The journal is the database. Each accepted commit is one immutable segment
// in the store, chained to the previous segment's hash and published with a
// create-only conditional write. Everything in memory is a cache rebuilt from
// the latest snapshot plus a replay of the segments after it, so a relay that
// loses every local cache comes back with the same state.
//
// The engine never sees names, keys or plaintext: it checks shapes,
// signatures, authority and base versions, sequences commits, assigns append
// positions and accounts for bytes.
package engine

import (
	"errors"
	"sort"
	"time"

	"github.com/poweur/identity/drive"
)

// Errors a caller maps to HTTP statuses.
var (
	ErrNotFound  = errors.New("not found")                                 // 404
	ErrConflict  = errors.New("base version is not the current head")      // 409
	ErrExists    = errors.New("already exists")                            // 409
	ErrForbidden = errors.New("not permitted")                             // 403
	ErrInvalid   = errors.New("invalid request")                           // 400/422
	ErrQuota     = errors.New("storage quota exceeded")                    // 507
	ErrResync    = errors.New("records before this position were trimmed") // 410
	ErrStale     = errors.New("the drive changed under another writer")    // 503, retry
)

const stateFormat = 1

// node is the engine's view of one node at its head version.
type node struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"`
	Mode       string    `json:"mode,omitempty"`
	Folder     string    `json:"folder,omitempty"`
	NameHash   string    `json:"name_hash,omitempty"`
	Head       string    `json:"head"`
	HeadHash   string    `json:"head_hash"`
	Generation uint64    `json:"generation"`
	Count      uint64    `json:"count"`
	Pages      []string  `json:"pages"`
	Removed    bool      `json:"removed,omitempty"`
	Updated    time.Time `json:"updated"`
	// Append files.
	Position uint64 `json:"position,omitempty"`
	// TrimmedBefore is the first retained position; earlier ones are gone.
	TrimmedBefore uint64                  `json:"trimmed_before,omitempty"`
	TrimSnapshot  *SnapshotRef            `json:"trim_snapshot,omitempty"`
	Authors       map[string]authorCursor `json:"authors,omitempty"`
	// Records maps position to where the record is kept in the journal.
	Records map[uint64]recordLoc `json:"records,omitempty"`
}

type authorCursor struct {
	Sequence uint64 `json:"sequence"`
	Hash     string `json:"hash"`
}

type recordLoc struct {
	Segment uint64           `json:"segment"`
	Op      int              `json:"op"`
	Index   int              `json:"index"`
	Chunks  []drive.ChunkRef `json:"chunks,omitempty"`
}

// version is a retained version of a node; heads are never collected.
type version struct {
	Node       string    `json:"node"`
	Hash       string    `json:"hash"`
	Pages      []string  `json:"pages"`
	Superseded time.Time `json:"superseded,omitempty"`
}

type chunk struct {
	Size uint64 `json:"size"`
	Refs int    `json:"refs"`
}

// SnapshotRef names the version of a snapshot file that covers a trimmed
// prefix of an append file.
type SnapshotRef struct {
	Node    string `json:"node"`
	Version string `json:"version"`
}

// Change is one entry of a drive's changes feed.
type Change struct {
	Seq  uint64 `json:"seq"`
	Node string `json:"node,omitempty"`
	// Path is set on system-zone changes.
	Path      string    `json:"path,omitempty"`
	Operation string    `json:"operation"`
	Version   string    `json:"version,omitempty"`
	Position  uint64    `json:"position,omitempty"`
	At        time.Time `json:"at"`
}

type opResult struct {
	RequestHash string   `json:"request_hash"`
	Seq         uint64   `json:"seq"`
	Head        string   `json:"head,omitempty"`
	Positions   []uint64 `json:"positions,omitempty"`
}

// state is one drive. It is serialized whole as a snapshot.
type state struct {
	Format      int                         `json:"format"`
	Drive       string                      `json:"drive"`
	Seq         uint64                      `json:"seq"`
	SegmentHash string                      `json:"segment_hash"`
	Root        string                      `json:"root,omitempty"`
	Nodes       map[string]*node            `json:"nodes"`
	Names       map[string]string           `json:"names"`
	Versions    map[string]*version         `json:"versions"`
	Pages       map[string][]drive.ChunkRef `json:"pages"`
	PageRefs    map[string]int              `json:"page_refs"`
	Chunks      map[string]*chunk           `json:"chunks"`
	Used        int64                       `json:"used"`
	Ops         map[string]opResult         `json:"ops"`
	System      map[string]*systemFile      `json:"system"`
	Changes     []Change                    `json:"changes"`
}

func newState(driveID string) *state {
	return &state{
		Format: stateFormat, Drive: driveID,
		Nodes: map[string]*node{}, Names: map[string]string{}, Versions: map[string]*version{},
		Pages: map[string][]drive.ChunkRef{}, PageRefs: map[string]int{}, Chunks: map[string]*chunk{},
		Ops: map[string]opResult{}, Changes: []Change{}, System: map[string]*systemFile{},
	}
}

func nameKey(folder, hash string) string { return folder + "/" + hash }

// refPage counts one more reference to a page, and to its chunks the first
// time the page is referenced. It returns the bytes that became referenced.
func (s *state) refPage(hash string) int64 {
	s.PageRefs[hash]++
	if s.PageRefs[hash] > 1 {
		return 0
	}
	var added int64
	for _, ref := range s.Pages[hash] {
		c := s.Chunks[ref.ID]
		if c == nil {
			c = &chunk{Size: ref.Size}
			s.Chunks[ref.ID] = c
		}
		c.Refs++
		if c.Refs == 1 {
			added += int64(c.Size)
		}
	}
	return added
}

// unrefPage drops a page reference; the last reference releases its chunks.
// It returns the bytes no longer referenced and the chunks now unreferenced.
func (s *state) unrefPage(hash string) (int64, []string) {
	s.PageRefs[hash]--
	if s.PageRefs[hash] > 0 {
		return 0, nil
	}
	delete(s.PageRefs, hash)
	var freed int64
	var dead []string
	for _, ref := range s.Pages[hash] {
		c := s.Chunks[ref.ID]
		if c == nil {
			continue
		}
		c.Refs--
		if c.Refs <= 0 {
			freed += int64(c.Size)
			dead = append(dead, ref.ID)
			delete(s.Chunks, ref.ID)
		}
	}
	delete(s.Pages, hash)
	return freed, dead
}

// refChunks references record chunks directly (append records are not paged).
func (s *state) refChunks(refs []drive.ChunkRef) int64 {
	var added int64
	for _, ref := range refs {
		c := s.Chunks[ref.ID]
		if c == nil {
			c = &chunk{Size: ref.Size}
			s.Chunks[ref.ID] = c
		}
		c.Refs++
		if c.Refs == 1 {
			added += int64(c.Size)
		}
	}
	return added
}

func (s *state) unrefChunks(refs []drive.ChunkRef) (int64, []string) {
	var freed int64
	var dead []string
	for _, ref := range refs {
		c := s.Chunks[ref.ID]
		if c == nil {
			continue
		}
		c.Refs--
		if c.Refs <= 0 {
			freed += int64(c.Size)
			dead = append(dead, ref.ID)
			delete(s.Chunks, ref.ID)
		}
	}
	return freed, dead
}

// children lists live nodes directly in folder, by node ID.
func (s *state) children(folder string) []*node {
	var out []*node
	for _, n := range s.Nodes {
		if n.Folder == folder && !n.Removed && n.ID != folder {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// isAncestor reports whether a is folder or one of folder's ancestors.
func (s *state) isAncestor(a, folder string) bool {
	for seen := 0; folder != "" && seen <= len(s.Nodes); seen++ {
		if folder == a {
			return true
		}
		n := s.Nodes[folder]
		if n == nil {
			return false
		}
		folder = n.Folder
	}
	return false
}
