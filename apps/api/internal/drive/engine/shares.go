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

// Groups resolves a group identity hosted on this relay to its verified
// roster. It must not block on a drive: the relay answers from a cache.
// Unknown or non-group names report false.
type Groups func(group string) (members, admins []string, ok bool)

// matches reports whether share s applies to actor: a link share to its link
// holder; an identity share to that identity, or to every member (and
// admin) of the group identity it names.
func (st *state) matches(s *drive.Share, actor string) bool {
	if linkID, isLink := strings.CutPrefix(actor, linkActorPrefix); isLink {
		return s.Link != "" && s.Link == linkID
	}
	if s.Member == "" {
		return false
	}
	if s.Member == actor {
		return true
	}
	if st.groups == nil {
		return false
	}
	members, admins, ok := st.groups(s.Member)
	return ok && (contains(members, actor) || contains(admins, actor))
}

// groupAdmin reports whether actor administers the drive's own group
// identity: on a Space's drive, its admins act with admin everywhere.
func (st *state) groupAdmin(actor string) bool {
	if st.groups == nil {
		return false
	}
	_, admins, ok := st.groups(st.Drive)
	return ok && contains(admins, actor)
}

// roleSet is the roles an actor holds on a node.
func (st *state) roleSet(actor, nodeID string, now nowFunc) []string {
	var roles []string
	for seen, id := 0, nodeID; id != "" && seen <= len(st.Nodes); seen++ {
		for _, s := range st.Shares {
			if s.Node != id || s.ExpiredAt(now()) {
				continue
			}
			if st.matches(s, actor) {
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
	if st.groupAdmin(actor) {
		return true
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
	if strings.EqualFold(actor, st.Drive) || st.groupAdmin(actor) {
		return true
	}
	for _, s := range st.Shares {
		if s.ExpiredAt(now()) {
			continue
		}
		if st.matches(s, actor) {
			return true
		}
	}
	return false
}

// canWriteSomewhere reports whether actor may add bytes anywhere.
func (st *state) canWriteSomewhere(actor string, now nowFunc) bool {
	if strings.EqualFold(actor, st.Drive) || st.groupAdmin(actor) {
		return true
	}
	for _, s := range st.Shares {
		if s.ExpiredAt(now()) || s.Role == drive.RoleRead {
			continue
		}
		if st.matches(s, actor) {
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
	var out []drive.Share
	for _, s := range h.st.Shares {
		if h.st.matches(s, actor) || h.st.allowed(actor, s.Node, drive.RoleAdmin, e.now) {
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
		// A group identity's drive may share with the group itself: every
		// member of the Space. Any other owner needs no share.
		if _, _, group := e.groups(st.Drive); !group {
			return journalOp{}, invalid("the owner needs no share")
		}
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
	delete(st.ShareUse, u.ID)
	// A revoked share still proves what its holder was allowed to write
	// while it stood: readers verify old versions against it.
	st.Revoked[u.ID] = &RevokedShare{Share: *s, RevokedAt: op.At, Seq: seq}
	// A share made before the node's last key rotation carries a retired key:
	// revoking it (e.g. when re-issuing it after a rotation) forces nothing.
	if n := st.Nodes[s.Node]; drive.KeyBearing(s.Role) && n != nil && s.Generation == n.Generation {
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
	case change.Member != "" && (change.Member == actor || st.inGroup(change.Member, actor)):
		return true
	case change.Operation == "share" || change.Operation == "unshare" || change.Operation == "group.revoke":
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

const kindGroupRevoke = "grouprevoke"

type groupRevokeOp struct {
	Group   string   `json:"group"`
	Removed []string `json:"removed"`
}

// inGroup reports whether actor is in the group identity group's roster.
func (st *state) inGroup(group, actor string) bool {
	if st.groups == nil {
		return false
	}
	members, admins, ok := st.groups(group)
	return ok && (contains(members, actor) || contains(admins, actor))
}

// RevokeGroupMembers records that members left a group identity's roster.
// Every node below a key-bearing share to that group becomes
// rotate-required, as if each departed member's own share were revoked. It
// journals nothing when the drive holds no such share. The caller updates
// its roster cache first, so streams of departed members close.
func (e *Engine) RevokeGroupMembers(ctx context.Context, driveID, group string, removed []string) error {
	if len(removed) == 0 {
		return nil
	}
	h, err := e.open(ctx, driveID)
	if err != nil {
		return err
	}
	defer h.mu.Unlock()
	if err := e.catchUp(ctx, h); err != nil {
		return err
	}
	affected := false
	for _, s := range h.st.Shares {
		if s.Member == group && drive.KeyBearing(s.Role) {
			affected = true
			break
		}
	}
	if !affected {
		return nil
	}
	sorted := append([]string(nil), removed...)
	sort.Strings(sorted)
	return e.publish(ctx, h, journalOp{Kind: kindGroupRevoke, At: e.now(), GroupRevoke: &groupRevokeOp{Group: group, Removed: sorted}})
}

func applyGroupRevoke(st *state, seq uint64, op journalOp) error {
	g := op.GroupRevoke
	if g == nil {
		return fmt.Errorf("invalid group revocation")
	}
	for _, s := range st.Shares {
		if s.Member != g.Group || !drive.KeyBearing(s.Role) {
			continue
		}
		for _, n := range st.Nodes {
			if !n.Removed && st.isAncestor(s.Node, n.ID) {
				n.RotateRequired = true
			}
		}
		st.Changes = append(st.Changes, Change{Seq: seq, Node: s.Node, Operation: "group.revoke", Share: s.ID, Member: g.Group, At: op.At})
	}
	return nil
}

// SharesOn lists the shares on a node and its ancestors, nearest first:
// what a reader needs to check that each version's author held the role
// it needed. Sealed node keys are sealed to their members, so listing them
// reveals who has access, not what they can open.
func (e *Engine) SharesOn(ctx context.Context, driveID, nodeID string) ([]drive.Share, []RevokedShare, error) {
	h, err := e.open(ctx, driveID)
	if err != nil {
		return nil, nil, err
	}
	defer h.mu.Unlock()
	var out []drive.Share
	var revoked []RevokedShare
	for seen, id := 0, nodeID; id != "" && seen <= len(h.st.Nodes); seen++ {
		var here []drive.Share
		for _, s := range h.st.Shares {
			if s.Node == id {
				here = append(here, *s)
			}
		}
		sort.Slice(here, func(i, j int) bool { return here[i].ID < here[j].ID })
		out = append(out, here...)
		var gone []RevokedShare
		for _, r := range h.st.Revoked {
			if r.Share.Node == id {
				gone = append(gone, *r)
			}
		}
		sort.Slice(gone, func(i, j int) bool { return gone[i].Share.ID < gone[j].Share.ID })
		revoked = append(revoked, gone...)
		n := h.st.Nodes[id]
		if n == nil {
			break
		}
		id = n.Folder
	}
	return out, revoked, nil
}

// RevokedShare is a share that was revoked, kept as evidence of what its
// holder could write before RevokedAt. It grants nothing.
type RevokedShare struct {
	Share     drive.Share `json:"share"`
	RevokedAt time.Time   `json:"revoked_at"`
	Seq       uint64      `json:"seq"`
}
