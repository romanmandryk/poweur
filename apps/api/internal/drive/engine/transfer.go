package engine

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	identity "github.com/poweur/identity"
	"github.com/poweur/identity/drive"
)

// Ownership transfer (E20-T7). Chunks are encrypted to their drive (the AEAD
// context names it), so moving a subtree to another drive — a person's board
// to a Space — is done by a client holding the keys: it re-creates the
// subtree in the target drive with re-encrypted content and re-issues the
// shares there, keeping link IDs. The source drive then journals a transfer:
// the subtree is retired (its versions superseded, so content stays for the
// retention period), its shares end, and the drive remembers where the
// subtree and its links went so old links and node lookups can be
// redirected.

const kindTransfer = "transfer"

// Transfer retires Node, recording that it now lives at ToNode in drive To.
type Transfer struct {
	Node   string `json:"node"`
	To     string `json:"to"`
	ToNode string `json:"to_node"`
}

type transferOp struct {
	Node   string   `json:"node"`
	To     string   `json:"to"`
	ToNode string   `json:"to_node"`
	Nodes  []string `json:"nodes"`
	Shares []string `json:"shares"`
	Author string   `json:"author"`
}

// Forward is where a transferred node or link went.
type Forward struct {
	Drive string `json:"drive"`
	Node  string `json:"node"`
}

func (e *Engine) validateTransfer(h *driveHandle, t Transfer, actor string) (journalOp, error) {
	st := h.st
	n := st.Nodes[t.Node]
	if n == nil || n.Removed {
		return journalOp{}, fmt.Errorf("%w: node %s", ErrNotFound, t.Node)
	}
	if t.Node == st.Root {
		return journalOp{}, invalid("the root cannot be transferred")
	}
	if err := e.permit(st, actor, t.Node, drive.RoleAdmin); err != nil {
		return journalOp{}, err
	}
	if t.To != strings.ToLower(strings.TrimSpace(t.To)) || identity.ValidateIdentityName(t.To) != nil {
		return journalOp{}, invalid("invalid target drive")
	}
	if strings.EqualFold(t.To, st.Drive) || len(t.ToNode) != 32 || !isHex(t.ToNode) {
		return journalOp{}, invalid("a transfer names another drive and the node there")
	}
	var nodes, shares []string
	for id, node := range st.Nodes {
		if !node.Removed && st.isAncestor(t.Node, id) {
			nodes = append(nodes, id)
		}
	}
	for id, s := range st.Shares {
		if st.isAncestor(t.Node, s.Node) {
			shares = append(shares, id)
		}
	}
	sort.Strings(nodes)
	sort.Strings(shares)
	return journalOp{Kind: kindTransfer, Transfer: &transferOp{Node: t.Node, To: strings.ToLower(t.To), ToNode: t.ToNode, Nodes: nodes, Shares: shares, Author: actor}}, nil
}

func applyTransfer(st *state, seq uint64, op journalOp) error {
	t := op.Transfer
	if t == nil {
		return fmt.Errorf("invalid transfer")
	}
	to := Forward{Drive: t.To, Node: t.ToNode}
	for _, id := range t.Shares {
		if s := st.Shares[id]; s != nil {
			if s.Link != "" {
				st.Forwards[s.Link] = to
			}
			delete(st.Shares, id)
			delete(st.ShareUse, id)
		}
	}
	for _, id := range t.Nodes {
		if n := st.Nodes[id]; n != nil {
			retire(st, n, op.At)
		}
	}
	st.Moved[t.Node] = to
	st.Changes = append(st.Changes, Change{Seq: seq, Node: t.Node, Operation: "transfer", To: t.To, Version: t.ToNode, At: op.At})
	return nil
}

// retire removes a node from the live tree: out of its folder's names, its
// head superseded (collected after the retention period) and, for an append
// file, its records' chunks released.
func retire(st *state, n *node, at time.Time) {
	n.Removed, n.RotateRequired = true, false
	if n.NameHash != "" {
		delete(st.Names, nameKey(n.Folder, n.NameHash))
	}
	if v := st.Versions[n.Head]; v != nil && v.Superseded.IsZero() {
		v.Superseded = at
	}
	releaseRecords(st, n)
}

// releaseRecords drops an append file's records and their chunk references.
func releaseRecords(st *state, n *node) {
	for pos, loc := range n.Records {
		freed, _ := st.unrefChunks(loc.Chunks)
		st.Used -= freed
		delete(n.Records, pos)
	}
}

// Forwarded reports where a transferred link went.
func (e *Engine) Forwarded(ctx context.Context, driveID, linkID string) (Forward, bool, error) {
	h, err := e.open(ctx, driveID)
	if err != nil {
		return Forward{}, false, err
	}
	defer h.mu.Unlock()
	f, ok := h.st.Forwards[linkID]
	return f, ok, nil
}

// Moved reports where a transferred node went.
func (e *Engine) Moved(ctx context.Context, driveID, nodeID string) (Forward, bool, error) {
	h, err := e.open(ctx, driveID)
	if err != nil {
		return Forward{}, false, err
	}
	defer h.mu.Unlock()
	f, ok := h.st.Moved[nodeID]
	return f, ok, nil
}
