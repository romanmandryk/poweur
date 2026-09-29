package engine

import (
	"context"
	"sort"
	"sync"

	"github.com/poweur/identity/drive"
)

// ListedNode is a node with the signed versions a reader needs to open it:
// the head first, then the versions carrying its key, name and content-key
// envelopes when they are older than the head. The relay still sees no
// names or keys; it only saves the reader a walk through the history.
type ListedNode struct {
	NodeInfo
	Versions []drive.Manifest `json:"versions"`
}

// Listing is a folder and one page of its children, with the shares (active
// and revoked) that stood on the folder, its ancestors and those children:
// the evidence a reader checks version authors against.
type Listing struct {
	Folder   ListedNode     `json:"folder"`
	Children []ListedNode   `json:"children"`
	Cursor   string         `json:"cursor"`
	Shares   []drive.Share  `json:"shares"`
	Revoked  []RevokedShare `json:"revoked"`
}

// fillEnvelopes finds a node's envelope versions by walking back from its
// head, for nodes loaded from a snapshot written before they were tracked.
// The caller holds h.mu.
func (e *Engine) fillEnvelopes(ctx context.Context, h *driveHandle, n *node) {
	// A public node has no key envelope; it has a plaintext name and key.
	hasKey := func() bool { return n.KeyVersion != "" || n.Public }
	if hasKey() && n.NameVersion != "" && (n.Kind == drive.KindFolder || n.ContentVersion != "") {
		return
	}
	if n.Folder == "" && hasKey() && n.Kind == drive.KindFolder {
		return // the root has no name
	}
	id := n.Head
	for seen := 0; id != "" && seen < 10000; seen++ {
		if h.st.Versions[id] == nil {
			return
		}
		m, err := e.manifest(ctx, h, n.ID, id)
		if err != nil {
			return
		}
		if n.KeyVersion == "" && m.NodeKey != nil {
			n.KeyVersion = m.Version
		}
		if n.NameVersion == "" && (m.Name != nil || m.PlainName != "") {
			n.NameVersion = m.Version
		}
		if n.ContentVersion == "" && (m.ContentKey != nil || m.PlainKey != "") {
			n.ContentVersion = m.Version
		}
		if hasKey() && (n.NameVersion != "" || n.Folder == "") && (n.Kind == drive.KindFolder || n.ContentVersion != "") {
			return
		}
		id = m.Parent
	}
}

// envelopeVersions lists the versions to send for n, head first.
func envelopeVersions(n *node) []string {
	out := []string{n.Head}
	for _, id := range []string{n.KeyVersion, n.NameVersion, n.ContentVersion} {
		if id == "" {
			continue
		}
		dup := false
		for _, have := range out {
			dup = dup || have == id
		}
		if !dup {
			out = append(out, id)
		}
	}
	return out
}

// listed loads the versions of each node, in parallel and through the
// manifest cache. The caller does not hold h.mu.
func (e *Engine) listed(ctx context.Context, h *driveHandle, infos []NodeInfo, versions [][]string) ([]ListedNode, error) {
	out := make([]ListedNode, len(infos))
	type job struct{ i, j int }
	var jobs []job
	for i := range infos {
		out[i] = ListedNode{NodeInfo: infos[i], Versions: make([]drive.Manifest, len(versions[i]))}
		for j := range versions[i] {
			jobs = append(jobs, job{i, j})
		}
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var first error
	work := make(chan job)
	workers := min(16, len(jobs))
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for jb := range work {
				m, err := e.manifest(ctx, h, infos[jb.i].ID, versions[jb.i][jb.j])
				mu.Lock()
				if err != nil && first == nil {
					first = err
				}
				out[jb.i].Versions[jb.j] = m
				mu.Unlock()
			}
		}()
	}
	for _, jb := range jobs {
		work <- jb
	}
	close(work)
	wg.Wait()
	return out, first
}

// Listing returns a folder and a page of its live children, each with the
// versions needed to open it, and the share evidence for them.
func (e *Engine) Listing(ctx context.Context, driveID, folderID, cursor string, limit int) (Listing, error) {
	h, err := e.open(ctx, driveID)
	if err != nil {
		return Listing{}, err
	}
	folder := h.st.Nodes[folderID]
	if folder == nil || folder.Removed || folder.Kind != drive.KindFolder {
		h.mu.Unlock()
		return Listing{}, ErrNotFound
	}
	limit = clampLimit(limit)
	nodes := []*node{folder}
	next := ""
	for _, n := range h.st.children(folderID) {
		if n.ID <= cursor {
			continue
		}
		if len(nodes)-1 >= limit {
			next = nodes[len(nodes)-1].ID
			break
		}
		nodes = append(nodes, n)
	}
	infos := make([]NodeInfo, len(nodes))
	versions := make([][]string, len(nodes))
	onPage := map[string]bool{}
	for i, n := range nodes {
		e.fillEnvelopes(ctx, h, n)
		infos[i], versions[i] = infoOf(n), envelopeVersions(n)
		onPage[n.ID] = true
	}
	// Evidence: shares on the folder's chain and on the listed children.
	chain := map[string]bool{}
	for id, seen := folderID, 0; id != "" && seen <= len(h.st.Nodes); seen++ {
		chain[id] = true
		n := h.st.Nodes[id]
		if n == nil {
			break
		}
		id = n.Folder
	}
	listing := Listing{Cursor: next, Shares: []drive.Share{}, Revoked: []RevokedShare{}}
	for _, s := range h.st.Shares {
		if chain[s.Node] || onPage[s.Node] {
			listing.Shares = append(listing.Shares, *s)
		}
	}
	for _, r := range h.st.Revoked {
		if chain[r.Share.Node] || onPage[r.Share.Node] {
			listing.Revoked = append(listing.Revoked, *r)
		}
	}
	h.mu.Unlock()
	sort.Slice(listing.Shares, func(i, j int) bool { return listing.Shares[i].ID < listing.Shares[j].ID })
	sort.Slice(listing.Revoked, func(i, j int) bool { return listing.Revoked[i].Share.ID < listing.Revoked[j].Share.ID })
	all, err := e.listed(ctx, h, infos, versions)
	if err != nil {
		return Listing{}, err
	}
	listing.Folder, listing.Children = all[0], all[1:]
	return listing, nil
}

// Path returns a node and its ancestors up to the root, nearest first, each
// with the versions needed to open it. A caller keeps the part it may read.
func (e *Engine) Path(ctx context.Context, driveID, nodeID string) ([]ListedNode, error) {
	h, err := e.open(ctx, driveID)
	if err != nil {
		return nil, err
	}
	var infos []NodeInfo
	var versions [][]string
	for id, seen := nodeID, 0; id != "" && seen <= len(h.st.Nodes); seen++ {
		n := h.st.Nodes[id]
		if n == nil || n.Removed {
			if seen == 0 {
				h.mu.Unlock()
				return nil, ErrNotFound
			}
			break
		}
		e.fillEnvelopes(ctx, h, n)
		infos = append(infos, infoOf(n))
		versions = append(versions, envelopeVersions(n))
		id = n.Folder
	}
	h.mu.Unlock()
	return e.listed(ctx, h, infos, versions)
}
