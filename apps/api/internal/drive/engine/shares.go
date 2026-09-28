package engine

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/poweur/identity/drive"
)

// Shares (E20-T7). A signed share grants a member (or a link) a role on a
// node and its descendants; the engine journals shares and revocations like
// any commit and decides every read and write from them. The drive's owner
// holds every role. Revoking a share that carried the node key marks the
// shared subtree rotate-required: until the owner commits a key rotation
// for a node, no new content is written under the key the revoked member
// still holds.

const (
	kindShare   = "share"
	kindUnshare = "unshare"
)

// Actors that are not identities.
const linkActorPrefix = "link:"

// LinkActor is the actor name for requests authorized by a link.
func LinkActor(linkID string) string { return linkActorPrefix + linkID }

type unshareOp struct {
	ID     string `json:"id"`
	Author string `json:"author"`
}

// Unshare revokes one share.
type Unshare struct {
	ID string `json:"id"`
}

// roleSet is the roles an actor holds on a node.
func (st *state) roleSet(actor, nodeID string, now nowFunc) []string {
	var roles []string
	linkID, isLink := strings.CutPrefix(actor, linkActorPrefix)
	for seen, id := 0, nodeID; id != "" && seen <= len(st.Nodes); seen++ {
		for _, s := range st.Shares {
			if s.Node != id || s.ExpiredAt(now()) {
				continue
			}
			if isLink && s.Link == linkID || !isLink && s.Member == actor {
				roles = append(roles, s.Role)
			}
		}
		n := st.Nodes[id]
		if n == nil {
			break
		}
		id = n.Folder
	}
	return roles
}

// allowed reports whether actor may act on nodeID with need.
func (st *state) allowed(actor, nodeID, need string, now nowFunc) bool {
	if strings.EqualFold(actor, st.Drive) {
		return true
	}
	if need == needOwner {
		return false
	}
	for _, role := range st.roleSet(actor, nodeID, now) {
		if drive.RoleGrants(role, need) {
			return true
		}
	}
	return false
}

// needOwner is a requirement only the drive's owner meets.
const needOwner = "owner"

// hasAccess reports whether actor holds any live share on the drive.
func (st *state) hasAccess(actor string, now nowFunc) bool {
	if strings.EqualFold(actor, st.Drive) {
		return true
	}
	linkID, isLink := strings.CutPrefix(actor, linkActorPrefix)
	for _, s := range st.Shares {
		if s.ExpiredAt(now()) {
			continue
		}
		if isLink && s.Link == linkID || !isLink && s.Member == actor {
			return true
		}
	}
	return false
}

// canWriteSomewhere reports whether actor may add bytes anywhere.
func (st *state) canWriteSomewhere(actor string, now nowFunc) bool {
	if strings.EqualFold(actor, st.Drive) {
		return true
	}
	linkID, isLink := strings.CutPrefix(actor, linkActorPrefix)
	for _, s := range st.Shares {
		if s.ExpiredAt(now()) || s.Role == drive.RoleRead {
			continue
		}
		if isLink && s.Link == linkID || !isLink && s.Member == actor {
			return true
		}
	}
	return false
}

type nowFunc = func() time.Time

func (e *Engine) permit(st *state, actor, nodeID, need string) error {
	if !st.allowed(actor, nodeID, need, e.now) {
		return fmt.Errorf("%w: %s needs %s on %s", ErrForbidden, actor, need, nodeID)
	}
	return nil
}

// Authorize checks that actor may use nodeID with need (a drive role, e.g.
// drive.RoleRead). Unknown nodes are ErrNotFound for everyone, so a
// stranger cannot probe which IDs exist beyond what ErrForbidden reveals.
func (e *Engine) Authorize(ctx context.Context, driveID, actor, nodeID, need string) error {
	h, err := e.open(ctx, driveID)
	if err != nil {
		return err
	}
	defer h.mu.Unlock()
	if h.st.Nodes[nodeID] == nil {
		if !h.st.hasAccess(actor, e.now) {
			return ErrForbidden
		}
		return ErrNotFound
	}
	return e.permit(h.st, actor, nodeID, need)
}

// HasAccess reports whether actor owns the drive or holds a live share on it.
func (e *Engine) HasAccess(ctx context.Context, driveID, actor string) (bool, error) {
	h, err := e.open(ctx, driveID)
	if err != nil {
		return false, err
	}
	defer h.mu.Unlock()
	return h.st.hasAccess(actor, e.now), nil
}

// IsOwner reports whether actor owns the drive.
func IsOwner(driveID, actor string) bool { return strings.EqualFold(driveID, actor) }

// CanUpload reports whether actor may upload chunks to the drive: its owner,
// or a member or link holding any role that adds content.
func (e *Engine) CanUpload(ctx context.Context, driveID, actor string) (bool, error) {
	h, err := e.open(ctx, driveID)
	if err != nil {
		return false, err
	}
	defer h.mu.Unlock()
	return h.st.canWriteSomewhere(actor, e.now), nil
}

// Shares lists the shares actor may see: all of them for the owner; for
// anyone else the shares naming them and the shares on nodes they
// administer.
func (e *Engine) Shares(ctx context.Context, driveID, actor string) ([]drive.Share, error) {
	h, err := e.open(ctx, driveID)
	if err != nil {
		return nil, err
	}
	defer h.mu.Unlock()
	linkID, isLink := strings.CutPrefix(actor, linkActorPrefix)
	var out []drive.Share
	for _, s := range h.st.Shares {
		mine := isLink && s.Link == linkID || !isLink && s.Member == actor
		if mine || h.st.allowed(actor, s.Node, drive.RoleAdmin, e.now) {
			out = append(out, *s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Share returns one share by ID.
func (e *Engine) Share(ctx context.Context, driveID, id string) (drive.Share, error) {
	h, err := e.open(ctx, driveID)
	if err != nil {
		return drive.Share{}, err
	}
	defer h.mu.Unlock()
	s := h.st.Shares[id]
	if s == nil {
		return drive.Share{}, ErrNotFound
	}
	return *s, nil
}

func (e *Engine) validateShare(ctx context.Context, h *driveHandle, s drive.Share, actor string) (journalOp, error) {
	st := h.st
	if err := s.Validate(); err != nil {
		return journalOp{}, invalid("%v", err)
	}
	if s.Drive != st.Drive || s.Issuer != actor {
		return journalOp{}, invalid("a share is issued by its committer for this drive")
	}
	if strings.EqualFold(s.Member, st.Drive) {
		return journalOp{}, invalid("the owner needs no share")
	}
	n := st.Nodes[s.Node]
	if n == nil || n.Removed {
		return journalOp{}, fmt.Errorf("%w: node %s", ErrNotFound, s.Node)
	}
	if err := e.permit(st, actor, s.Node, drive.RoleAdmin); err != nil {
		return journalOp{}, err
	}
	if _, exists := st.Shares[s.ID]; exists {
		return journalOp{}, fmt.Errorf("%w: share %s", ErrExists, s.ID)
	}
	if s.Link != "" {
		for _, other := range st.Shares {
			if other.Link == s.Link {
				return journalOp{}, fmt.Errorf("%w: link %s", ErrExists, s.Link)
			}
		}
	}
	if s.Generation != n.Generation {
		return journalOp{}, fmt.Errorf("%w: share seals generation %d, node is at %d", ErrConflict, s.Generation, n.Generation)
	}
	if n.RotateRequired && drive.KeyBearing(s.Role) {
		return journalOp{}, fmt.Errorf("%w: rotate the node key before sharing it again", ErrConflict)
	}
	if s.ExpiredAt(e.now()) {
		return journalOp{}, invalid("share has already expired")
	}
	key, err := e.opts.Keys(ctx, s.Issuer)
	if err != nil {
		return journalOp{}, fmt.Errorf("%w: cannot resolve %s: %v", ErrForbidden, s.Issuer, err)
	}
	if err := s.Verify(key); err != nil {
		return journalOp{}, invalid("%v", err)
	}
	return journalOp{Kind: kindShare, Share: &s}, nil
}

func (e *Engine) validateUnshare(h *driveHandle, id, actor string) (journalOp, error) {
	s := h.st.Shares[id]
	if s == nil {
		return journalOp{}, fmt.Errorf("%w: share %s", ErrNotFound, id)
	}
	leaving := s.Member != "" && s.Member == actor
	if !leaving {
		if err := e.permit(h.st, actor, s.Node, drive.RoleAdmin); err != nil {
			return journalOp{}, err
		}
	}
	return journalOp{Kind: kindUnshare, Unshare: &unshareOp{ID: id, Author: actor}}, nil
}

func applyShare(st *state, seq uint64, op journalOp) error {
	s := op.Share
	if s == nil || st.Shares[s.ID] != nil {
		return fmt.Errorf("invalid share operation")
	}
	copied := *s
	st.Shares[s.ID] = &copied
	st.Changes = append(st.Changes, Change{Seq: seq, Node: s.Node, Operation: "share", Share: s.ID, Member: shareMember(s), At: op.At})
	return nil
}

func applyUnshare(st *state, seq uint64, op journalOp) error {
	u := op.Unshare
	if u == nil || st.Shares[u.ID] == nil {
		return fmt.Errorf("invalid unshare operation")
	}
	s := st.Shares[u.ID]
	delete(st.Shares, u.ID)
	if drive.KeyBearing(s.Role) {
		// Everything the member could decrypt is below the shared node.
		for _, n := range st.Nodes {
			if !n.Removed && st.isAncestor(s.Node, n.ID) {
				n.RotateRequired = true
			}
		}
	}
	st.Changes = append(st.Changes, Change{Seq: seq, Node: s.Node, Operation: "unshare", Share: s.ID, Member: shareMember(s), At: op.At})
	return nil
}

func shareMember(s *drive.Share) string {
	if s.Link != "" {
		return LinkActor(s.Link)
	}
	return s.Member
}

// Audience answers, while a drive is locked, who may see one change.
type Audience struct {
	// CanRead reports whether actor may see the change.
	CanRead func(actor string) bool
	// HasAccess reports whether actor still holds any share on the drive;
	// a stream whose actor lost it should close.
	HasAccess func(actor string) bool
}

func (e *Engine) audience(st *state, change Change) Audience {
	return Audience{
		CanRead:   func(actor string) bool { return visible(st, change, actor, e.now) },
		HasAccess: func(actor string) bool { return st.hasAccess(actor, e.now) },
	}
}

// visible decides whether actor may see a change: the owner sees all; a
// member sees changes to nodes they can read, and the granting or revoking
// of their own shares; system-zone changes are the owner's.
func visible(st *state, change Change, actor string, now nowFunc) bool {
	switch {
	case strings.EqualFold(actor, st.Drive):
		return true
	case change.Node == "":
		return false
	case change.Member != "" && change.Member == actor:
		return true
	case change.Operation == "share" || change.Operation == "unshare":
		return st.allowed(actor, change.Node, drive.RoleAdmin, now)
	}
	return st.allowed(actor, change.Node, drive.RoleRead, now)
}

// ChangesFor is Changes filtered to what actor may see. The cursor advances
// past invisible changes, so a member's paging never stalls.
func (e *Engine) ChangesFor(ctx context.Context, driveID, actor string, after uint64, limit int) ([]Change, uint64, error) {
	h, err := e.open(ctx, driveID)
	if err != nil {
		return nil, 0, err
	}
	defer h.mu.Unlock()
	if !h.st.hasAccess(actor, e.now) {
		return nil, 0, ErrForbidden
	}
	limit = clampLimit(limit)
	changes := h.st.Changes
	i := sort.Search(len(changes), func(i int) bool { return changes[i].Seq > after })
	next := after
	var out []Change
	for ; i < len(changes) && len(out) < limit; i++ {
		next = changes[i].Seq
		if visible(h.st, changes[i], actor, e.now) {
			out = append(out, changes[i])
		}
	}
	if i == len(changes) && h.st.Seq > next {
		next = h.st.Seq
	}
	return out, next, nil
}

// rotationRequired refuses new content under a key a revoked member holds.
func rotationRequired(st *state, nodeID string) error {
	if n := st.Nodes[nodeID]; n != nil && n.RotateRequired {
		return fmt.Errorf("%w: node %s must rotate its key after a revocation", ErrConflict, nodeID)
	}
	return nil
}
