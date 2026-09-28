package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/poweur/api/internal/drive/provider"
	"github.com/poweur/identity/drive"
)

// NodeInfo is a node at its head, as the relay sees it (no names or keys).
type NodeInfo struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Mode       string `json:"mode,omitempty"`
	Folder     string `json:"folder,omitempty"`
	NameHash   string `json:"name_hash,omitempty"`
	Head       string `json:"head"`
	Generation uint64 `json:"generation"`
	Count      uint64 `json:"count"`
	Removed    bool   `json:"removed,omitempty"`
	// RotateRequired: a revoked member held this node's key; the next
	// content write must be preceded by a key rotation.
	RotateRequired bool         `json:"rotate_required,omitempty"`
	Position       uint64       `json:"position,omitempty"`
	TrimmedBefore  uint64       `json:"trimmed_before,omitempty"`
	TrimSnapshot   *SnapshotRef `json:"trim_snapshot,omitempty"`
}

func infoOf(n *node) NodeInfo {
	return NodeInfo{ID: n.ID, Kind: n.Kind, Mode: n.Mode, Folder: n.Folder, NameHash: n.NameHash, Head: n.Head,
		Generation: n.Generation, Count: n.Count, Removed: n.Removed, RotateRequired: n.RotateRequired, Position: n.Position,
		TrimmedBefore: n.TrimmedBefore, TrimSnapshot: n.TrimSnapshot}
}

// Root returns the drive's root node ID ("" before it is created).
func (e *Engine) Root(ctx context.Context, driveID string) (string, error) {
	h, err := e.open(ctx, driveID)
	if err != nil {
		return "", err
	}
	defer h.mu.Unlock()
	return h.st.Root, nil
}

// Node returns a node's head.
func (e *Engine) Node(ctx context.Context, driveID, nodeID string) (NodeInfo, error) {
	h, err := e.open(ctx, driveID)
	if err != nil {
		return NodeInfo{}, err
	}
	defer h.mu.Unlock()
	n := h.st.Nodes[nodeID]
	if n == nil {
		return NodeInfo{}, ErrNotFound
	}
	return infoOf(n), nil
}

// Children lists a folder's live children in node-ID order, after cursor.
func (e *Engine) Children(ctx context.Context, driveID, folderID, cursor string, limit int) ([]NodeInfo, string, error) {
	h, err := e.open(ctx, driveID)
	if err != nil {
		return nil, "", err
	}
	defer h.mu.Unlock()
	folder := h.st.Nodes[folderID]
	if folder == nil || folder.Removed || folder.Kind != drive.KindFolder {
		return nil, "", ErrNotFound
	}
	limit = clampLimit(limit)
	var out []NodeInfo
	for _, n := range h.st.children(folderID) {
		if n.ID <= cursor {
			continue
		}
		if len(out) == limit {
			return out, out[len(out)-1].ID, nil
		}
		out = append(out, infoOf(n))
	}
	return out, "", nil
}

// History lists a node's retained versions, oldest first.
func (e *Engine) History(ctx context.Context, driveID, nodeID string) ([]string, error) {
	h, err := e.open(ctx, driveID)
	if err != nil {
		return nil, err
	}
	defer h.mu.Unlock()
	if h.st.Nodes[nodeID] == nil {
		return nil, ErrNotFound
	}
	var out []string
	// Versions appear in the changes feed in commit order.
	for _, change := range h.st.Changes {
		if change.Node == nodeID && change.Version != "" {
			if _, retained := h.st.Versions[change.Version]; retained {
				out = append(out, change.Version)
			}
		}
	}
	return out, nil
}

// Changes returns changes after cursor (a sequence number), oldest first,
// and the cursor to continue from.
func (e *Engine) Changes(ctx context.Context, driveID string, after uint64, limit int) ([]Change, uint64, error) {
	h, err := e.open(ctx, driveID)
	if err != nil {
		return nil, 0, err
	}
	defer h.mu.Unlock()
	limit = clampLimit(limit)
	changes := h.st.Changes
	i := sort.Search(len(changes), func(i int) bool { return changes[i].Seq > after })
	end := min(i+limit, len(changes))
	out := append([]Change(nil), changes[i:end]...)
	next := after
	if len(out) > 0 {
		next = out[len(out)-1].Seq
	}
	return out, next, nil
}

// Version returns a retained version's signed manifest.
func (e *Engine) Version(ctx context.Context, driveID, nodeID, versionID string) (drive.Manifest, error) {
	h, err := e.open(ctx, driveID)
	if err != nil {
		return drive.Manifest{}, err
	}
	v := h.st.Versions[versionID]
	prefix := h.prefix
	h.mu.Unlock()
	if v == nil || v.Node != nodeID {
		return drive.Manifest{}, ErrNotFound
	}
	obj, err := e.opts.Store.Get(ctx, prefix+"versions/"+nodeID+"/"+versionID+".json", nil)
	if err != nil {
		return drive.Manifest{}, notFound(err)
	}
	var m drive.Manifest
	if err := json.Unmarshal(obj.Data, &m); err != nil {
		return drive.Manifest{}, fmt.Errorf("stored manifest %s is corrupt: %w", versionID, err)
	}
	return m, nil
}

// Page returns a chunk-list page of a retained version.
func (e *Engine) Page(ctx context.Context, driveID, nodeID, versionID, pageHash string) (drive.ChunkPage, error) {
	h, err := e.open(ctx, driveID)
	if err != nil {
		return drive.ChunkPage{}, err
	}
	defer h.mu.Unlock()
	v := h.st.Versions[versionID]
	refs, known := h.st.Pages[pageHash]
	if v == nil || v.Node != nodeID || !known || !contains(v.Pages, pageHash) {
		return drive.ChunkPage{}, ErrNotFound
	}
	return drive.ChunkPage{Format: 1, Drive: h.st.Drive, Node: nodeID, Chunks: append([]drive.ChunkRef(nil), refs...)}, nil
}

// ChunkKey authorizes reading a chunk through a retained version (or an
// append record of the node) and returns its provider key. Knowing a
// chunk's hash is never enough to read it.
func (e *Engine) ChunkKey(ctx context.Context, driveID, nodeID, versionID, chunkID string) (string, error) {
	h, err := e.open(ctx, driveID)
	if err != nil {
		return "", err
	}
	defer h.mu.Unlock()
	n := h.st.Nodes[nodeID]
	if n == nil {
		return "", ErrNotFound
	}
	if versionID != "" {
		v := h.st.Versions[versionID]
		if v == nil || v.Node != nodeID {
			return "", ErrNotFound
		}
		for _, page := range v.Pages {
			for _, ref := range h.st.Pages[page] {
				if ref.ID == chunkID {
					return h.prefix + "chunks/" + chunkID, nil
				}
			}
		}
		return "", ErrNotFound
	}
	for _, loc := range n.Records {
		for _, ref := range loc.Chunks {
			if ref.ID == chunkID {
				return h.prefix + "chunks/" + chunkID, nil
			}
		}
	}
	return "", ErrNotFound
}

// PositionedRecord is an append record with its position.
type PositionedRecord struct {
	Position uint64             `json:"position"`
	Record   drive.AppendRecord `json:"record"`
}

// Records returns an append file's records from position from (inclusive),
// and the position after the last one returned. Reading a trimmed position
// returns ErrResync; the node's TrimSnapshot names the snapshot to load.
func (e *Engine) Records(ctx context.Context, driveID, nodeID string, from uint64, limit int) ([]PositionedRecord, uint64, error) {
	h, err := e.open(ctx, driveID)
	if err != nil {
		return nil, 0, err
	}
	defer h.mu.Unlock()
	n := h.st.Nodes[nodeID]
	if n == nil || n.Mode != drive.ModeAppend {
		return nil, 0, ErrNotFound
	}
	if from == 0 {
		from = 1
	}
	if from < n.TrimmedBefore {
		return nil, 0, ErrResync
	}
	limit = clampLimit(limit)
	var out []PositionedRecord
	pos := from
	for ; pos <= n.Position && len(out) < limit; pos++ {
		loc, ok := n.Records[pos]
		if !ok {
			return nil, 0, fmt.Errorf("record %d is missing from the index", pos)
		}
		seg, err := e.segment(ctx, h, loc.Segment)
		if err != nil {
			return nil, 0, err
		}
		if loc.Op >= len(seg.Ops) || loc.Index >= len(seg.Ops[loc.Op].Records) {
			return nil, 0, fmt.Errorf("record %d is outside its segment", pos)
		}
		out = append(out, PositionedRecord{Position: pos, Record: seg.Ops[loc.Op].Records[loc.Index].Record})
	}
	return out, pos, nil
}

// segment returns a journal segment, cached per drive. Caller holds h.mu.
func (e *Engine) segment(ctx context.Context, h *driveHandle, seq uint64) (segment, error) {
	if seg, ok := h.segments[seq]; ok {
		return seg, nil
	}
	seg, _, err := loadSegment(ctx, e.opts.Store, h.prefix, seq)
	if err != nil {
		return segment{}, err
	}
	if len(h.segments) > 256 {
		h.segments = map[uint64]segment{}
	}
	h.segments[seq] = seg
	return seg, nil
}

// Usage returns a drive's referenced bytes.
func (e *Engine) Usage(ctx context.Context, driveID string) (int64, error) {
	h, err := e.open(ctx, driveID)
	if err != nil {
		return 0, err
	}
	defer h.mu.Unlock()
	return h.st.Used, nil
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func clampLimit(limit int) int {
	if limit <= 0 || limit > 1000 {
		return 1000
	}
	return limit
}

func notFound(err error) error {
	if errors.Is(err, provider.ErrNotFound) {
		return ErrNotFound
	}
	return err
}
