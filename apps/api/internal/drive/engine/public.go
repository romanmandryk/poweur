package engine

import (
	"context"
	"sort"

	"github.com/poweur/identity/drive"
)

// PublicPath resolves a /pub path (E20-T5): segments name public nodes from
// the drive root down, through their public name hashes. Only public, live
// nodes resolve; everything else is ErrNotFound, private or not.
func (e *Engine) PublicPath(ctx context.Context, driveID string, segments []string) (ListedNode, error) {
	h, err := e.open(ctx, driveID)
	if err != nil {
		return ListedNode{}, err
	}
	if len(segments) == 0 || h.st.Root == "" {
		h.mu.Unlock()
		return ListedNode{}, ErrNotFound
	}
	var n *node
	folder := h.st.Root
	for _, segment := range segments {
		id, ok := h.st.Names[nameKey(folder, drive.PublicNameHash(folder, segment))]
		n = h.st.Nodes[id]
		if !ok || n == nil || n.Removed || !n.Public {
			h.mu.Unlock()
			return ListedNode{}, ErrNotFound
		}
		folder = id
	}
	e.fillEnvelopes(ctx, h, n)
	info, versions := infoOf(n), envelopeVersions(n)
	h.mu.Unlock()
	listed, err := e.listed(ctx, h, []NodeInfo{info}, [][]string{versions})
	if err != nil {
		return ListedNode{}, err
	}
	return listed[0], nil
}

// PublicChildren lists a public folder's live children, or with folder ""
// the public folders at the top of the drive, each with its versions, in
// name order.
func (e *Engine) PublicChildren(ctx context.Context, driveID, folder string) ([]ListedNode, error) {
	h, err := e.open(ctx, driveID)
	if err != nil {
		return nil, err
	}
	parent := folder
	if parent == "" {
		parent = h.st.Root
	} else if f := h.st.Nodes[folder]; f == nil || f.Removed || !f.Public || f.Kind != drive.KindFolder {
		h.mu.Unlock()
		return nil, ErrNotFound
	}
	var infos []NodeInfo
	var versions [][]string
	for _, n := range h.st.children(parent) {
		if !n.Public {
			continue
		}
		e.fillEnvelopes(ctx, h, n)
		infos = append(infos, infoOf(n))
		versions = append(versions, envelopeVersions(n))
	}
	h.mu.Unlock()
	listed, err := e.listed(ctx, h, infos, versions)
	if err != nil {
		return nil, err
	}
	sort.Slice(listed, func(i, j int) bool { return PublicName(listed[i]) < PublicName(listed[j]) })
	return listed, nil
}

// PublicName is a listed public node's plaintext name.
func PublicName(n ListedNode) string {
	for _, m := range n.Versions {
		if m.Version == n.NameVersion {
			return m.PlainName
		}
	}
	return ""
}

// PublicContentKey is a listed public file's content key (base64url).
func PublicContentKey(n ListedNode) string {
	for _, m := range n.Versions {
		if m.Version == n.ContentVersion {
			return m.PlainKey
		}
	}
	return ""
}
