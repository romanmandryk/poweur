package files

import (
	"context"
	"crypto/ed25519"
	"io"
	"os"
	gopath "path"
	"strings"
	"time"

	idpkg "github.com/poweur/identity"
)

// Grant engine (EPIC-005 E05-T2). Grants and groups are signed documents in
// the owner's own tree (poweur-sys/relay/shares|groups); the relay reads
// them through the StorageProvider — never the OS — so any provider works.
// Documents are re-read per request (the app-password pattern): revocation
// is a file delete and takes effect on the next request, and signature
// verification means a relay compromise of the files alone cannot forge a
// grant the owner didn't sign.

const (
	sharesDir       = SysRelay + "/shares"
	groupsDir       = SysRelay + "/groups"
	maxGrantDocSize = 64 * 1024
)

// GrantStore loads verified grant snapshots for the permission engine.
type GrantStore struct {
	Provider StorageProvider
	// OwnerKey resolves a hosted identity's signing key (grants must be
	// signed by the tree owner).
	OwnerKey func(owner string) (ed25519.PublicKey, bool)
	// Logf reports malformed/rejected documents loudly (owner notification
	// via sys.* message is EPIC-009 territory).
	Logf func(format string, args ...any)
}

func (s *GrantStore) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}

// readDocs returns the raw bytes of every .json document in dir.
func (s *GrantStore) readDocs(ctx context.Context, owner, dir string) map[string][]byte {
	out := map[string][]byte{}
	d, err := s.Provider.OpenFile(ctx, owner, dir, os.O_RDONLY, 0)
	if err != nil {
		return out // no shares/groups dir yet
	}
	entries, err := d.Readdir(-1)
	_ = d.Close()
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if e.Size() > maxGrantDocSize {
			s.logf("grant engine: %s/%s/%s exceeds %d bytes, skipped", owner, dir, e.Name(), maxGrantDocSize)
			continue
		}
		f, err := s.Provider.OpenFile(ctx, owner, dir+"/"+e.Name(), os.O_RDONLY, 0)
		if err != nil {
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(f, maxGrantDocSize+1))
		_ = f.Close()
		if err != nil {
			continue
		}
		out[e.Name()] = raw
	}
	return out
}

// readDoc returns the raw bytes of one document in an identity's tree.
func (s *GrantStore) readDoc(ctx context.Context, identity, path string) ([]byte, bool) {
	f, err := s.Provider.OpenFile(ctx, identity, path, os.O_RDONLY, 0)
	if err != nil {
		return nil, false
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxGrantDocSize+1))
	_ = f.Close()
	if err != nil || len(raw) > maxGrantDocSize {
		return nil, false
	}
	return raw, true
}

// loadGroupIdentities resolves the addressable group identities a grant set
// names (E05-T5).
//
// A group identity is a hosted identity whose own tree carries
// idpkg.GroupSelfDoc, signed by the *group's* identity key — so the relay
// verifies it exactly as it verifies any other identity's document, and an
// owner naming a group in a grant confers no power to edit that group.
//
// v1 resolves only groups hosted on this relay: a group whose key this
// relay cannot produce is skipped, so the grant denies. Cross-relay
// resolution needs a membership-check endpoint and is deferred — see
// apps/docs/docs/files/group-identities.md.
func (s *GrantStore) loadGroupIdentities(ctx context.Context, set *GrantSet) {
	seen := map[string]bool{}
	for _, g := range set.grants {
		for _, a := range g.Audience {
			name := strings.ToLower(strings.TrimSpace(a.Group))
			if name == "" || !idpkg.IsGroupIdentityName(name) || seen[name] {
				continue
			}
			seen[name] = true
			// An owner-local group of the same name would be ambiguous, but
			// cannot exist: owner-local names may not contain a dot.
			pub, ok := s.OwnerKey(name)
			if !ok {
				s.logf("grant engine: group identity %s is not resolvable here (cross-relay groups are deferred)", name)
				continue
			}
			raw, ok := s.readDoc(ctx, name, idpkg.GroupSelfDoc)
			if !ok {
				s.logf("grant engine: group identity %s has no %s", name, idpkg.GroupSelfDoc)
				continue
			}
			gr, err := idpkg.ParseShareGroup(raw)
			if err != nil {
				s.logf("grant engine: rejecting group identity %s: %v", name, err)
				continue
			}
			if !gr.IsGroupIdentity() {
				s.logf("grant engine: rejecting group identity %s: %s is an owner-local group document", name, idpkg.GroupSelfDoc)
				continue
			}
			// The document must claim to be this group, or one group's
			// membership could be served for another.
			if !strings.EqualFold(gr.Group, name) || !strings.EqualFold(gr.Owner, name) {
				s.logf("grant engine: rejecting group identity %s: document names %q/%q", name, gr.Group, gr.Owner)
				continue
			}
			if err := gr.VerifySignature(pub); err != nil {
				s.logf("grant engine: rejecting group identity %s: %v", name, err)
				continue
			}
			set.groups[name] = gr
		}
	}
}

// Snapshot loads and verifies the owner's grant + group documents once; the
// returned GrantSet answers every permission probe for one request.
func (s *GrantStore) Snapshot(ctx context.Context, owner string) *GrantSet {
	set := &GrantSet{groups: map[string]idpkg.ShareGroup{}, now: time.Now()}
	if s == nil || s.Provider == nil || s.OwnerKey == nil {
		return set
	}
	pub, ok := s.OwnerKey(owner)
	if !ok {
		return set
	}
	for name, raw := range s.readDocs(ctx, owner, sharesDir) {
		g, err := idpkg.ParseShareGrant(raw)
		if err != nil {
			s.logf("grant engine: rejecting %s share %s: %v", owner, name, err)
			continue
		}
		if !strings.EqualFold(g.Owner, owner) {
			s.logf("grant engine: rejecting %s share %s: owner field is %q", owner, name, g.Owner)
			continue
		}
		if err := g.VerifySignature(pub); err != nil {
			s.logf("grant engine: rejecting %s share %s: %v", owner, name, err)
			continue
		}
		g.Path, _ = idpkg.NormalizeGrantPath(g.Path)
		set.grants = append(set.grants, g)
	}
	for name, raw := range s.readDocs(ctx, owner, groupsDir) {
		gr, err := idpkg.ParseShareGroup(raw)
		if err != nil {
			s.logf("grant engine: rejecting %s group %s: %v", owner, name, err)
			continue
		}
		if !strings.EqualFold(gr.Owner, owner) {
			s.logf("grant engine: rejecting %s group %s: owner field is %q", owner, name, gr.Owner)
			continue
		}
		if err := gr.VerifySignature(pub); err != nil {
			s.logf("grant engine: rejecting %s group %s: %v", owner, name, err)
			continue
		}
		set.groups[strings.ToLower(gr.Group)] = gr
	}
	// Group identities named by those grants resolve out of their own trees,
	// verified with their own keys (E05-T5).
	s.loadGroupIdentities(ctx, set)
	return set
}

// GrantSet is one owner's verified grants at a point in time. It implements
// GrantChecker for the layout permission engine.
type GrantSet struct {
	grants []idpkg.ShareGrant
	groups map[string]idpkg.ShareGroup
	now    time.Time
}

var _ GrantChecker = (*GrantSet)(nil)

func (gs *GrantSet) audienceMatches(g idpkg.ShareGrant, visitor string) bool {
	for _, a := range g.Audience {
		if a.Link != "" {
			// A capability token is not an identity: link grants are
			// reachable only through /s/<token>, never by an authenticated
			// visitor whose id happens to look like the token.
			continue
		}
		if a.ID != "" && strings.EqualFold(strings.TrimSpace(a.ID), visitor) {
			return true
		}
		if a.Group != "" {
			if gr, ok := gs.groups[strings.ToLower(strings.TrimSpace(a.Group))]; ok && gr.HasMember(visitor) {
				return true
			}
		}
	}
	return false
}

// Allowed evaluates (visitor, path, access) against the grant set:
//   - a grant covers its path and the whole subtree under it (parent→child
//     propagation; no per-file exceptions in v1)
//   - read additionally covers the *ancestor directories* of a granted
//     path, so grant-holders can traverse to their share (listings filter
//     siblings via this same check, so nothing else leaks)
//   - write requires the write permission; delete/move/mkcol are writes
//   - expired grants deny; multiple grants union
func (gs *GrantSet) Allowed(owner, visitor, path string, access Access) bool {
	if gs == nil || visitor == "" {
		return false
	}
	for _, g := range gs.grants {
		if g.Expired(gs.now) || !gs.audienceMatches(g, visitor) {
			continue
		}
		if access == AccessWrite {
			if g.AllowsWrite() && Under(path, g.Path) {
				return true
			}
			continue
		}
		if Under(path, g.Path) || isAncestorDir(path, g.Path) {
			return true
		}
	}
	return false
}

// VisibleShares returns the verified, unexpired grants matching a visitor
// (used by share-listing endpoints; empty visitor lists nothing).
func (gs *GrantSet) VisibleShares(visitor string) []idpkg.ShareGrant {
	var out []idpkg.ShareGrant
	for _, g := range gs.grants {
		if !g.Expired(gs.now) && gs.audienceMatches(g, visitor) {
			out = append(out, g)
		}
	}
	return out
}

// LinkGrant returns the verified link grant whose capability token matches
// (E05-T4). Tokens are compared in constant time, so a wrong guess reveals
// nothing about how close it was.
//
// Expired link grants are reported separately: whoever holds the token
// already knows the link existed, so telling them "this link has expired"
// leaks nothing and is the difference between a usable error page and a
// mystery 404. A *revoked* grant is simply gone and is indistinguishable
// from one that never existed.
func (gs *GrantSet) LinkGrant(token string) (grant idpkg.ShareGrant, found, expired bool) {
	if gs == nil || token == "" {
		return idpkg.ShareGrant{}, false, false
	}
	for _, g := range gs.grants {
		if !g.MatchesLinkToken(token) {
			continue
		}
		if !g.Expired(gs.now) {
			return g, true, false // a live grant always wins
		}
		grant, found, expired = g, true, true
	}
	return grant, found, expired
}

// isAncestorDir reports whether path is a strict ancestor directory of
// grantPath (e.g. "shared" or "shared/team" for grant "shared/team/docs").
func isAncestorDir(path, grantPath string) bool {
	if path == "" {
		return true
	}
	for probe := gopath.Dir(grantPath); probe != "." && probe != "/"; probe = gopath.Dir(probe) {
		if probe == path {
			return true
		}
	}
	return false
}
