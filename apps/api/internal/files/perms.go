package files

import (
	"fmt"
	"strings"
)

// Access is the kind of operation being authorized.
type Access int

const (
	AccessRead Access = iota
	AccessWrite
)

// Scope is the parsed form of the DAV token scope grammar (E03-T3):
//
//	dav:full            read+write everywhere the layout allows
//	dav:read            read-only everywhere the layout allows
//	dav:rw:<prefix>     read+write under a path prefix
//	dav:read:<prefix>   read-only under a path prefix
type Scope struct {
	Write  bool
	Prefix string // clean tree path, "" = whole tree
}

// ParseScope validates a scope string.
func ParseScope(s string) (Scope, error) {
	switch {
	case s == "dav:full":
		return Scope{Write: true}, nil
	case s == "dav:read":
		return Scope{Write: false}, nil
	case strings.HasPrefix(s, "dav:rw:"):
		p, err := ValidateTreePath(strings.TrimPrefix(s, "dav:rw:"))
		if err != nil {
			return Scope{}, fmt.Errorf("invalid scope path: %w", err)
		}
		return Scope{Write: true, Prefix: p}, nil
	case strings.HasPrefix(s, "dav:read:"):
		p, err := ValidateTreePath(strings.TrimPrefix(s, "dav:read:"))
		if err != nil {
			return Scope{}, fmt.Errorf("invalid scope path: %w", err)
		}
		return Scope{Write: false, Prefix: p}, nil
	default:
		return Scope{}, fmt.Errorf("invalid scope %q (want dav:full, dav:read, dav:rw:<path>, dav:read:<path>)", s)
	}
}

// Allows reports whether the scope covers the requested access on path.
func (sc Scope) Allows(path string, access Access) bool {
	if access == AccessWrite && !sc.Write {
		return false
	}
	return Under(path, sc.Prefix)
}

// Principal is an authenticated caller of the files layer.
type Principal struct {
	// Identity is the caller's Poweur ID ("" = anonymous).
	Identity string
	// Owner is true when Identity == the tree owner (verified at auth time).
	Owner bool
	// Scope bounds what the credential may touch. For visitors the scope
	// caps what the layout + grants would otherwise allow (a dav:read
	// visitor token cannot write even into a write-granted share).
	Scope Scope
}

// Anonymous is the unauthenticated principal.
var Anonymous = Principal{}

// GrantChecker answers "may visitor access path in owner's tree?" for the
// grant-based roots. EPIC-005 supplies the real implementation; v1 denies.
type GrantChecker interface {
	Allowed(owner, visitor, path string, access Access) bool
}

// denyAllGrants is the v1 stand-in until EPIC-005 lands.
type denyAllGrants struct{}

func (denyAllGrants) Allowed(owner, visitor, path string, access Access) bool { return false }

// DenyAllGrants is the default GrantChecker.
var DenyAllGrants GrantChecker = denyAllGrants{}

// Permissions evaluates the layout rules from the storage-model spec.
type Permissions struct {
	Grants GrantChecker
}

// relayManagedFiles are files only the relay may write (owner writes 403).
var relayManagedFiles = map[string]bool{
	SysPublic + "/id.json": true, // changed via registration/rotation endpoints only
}

// Allowed reports whether principal p may perform access on path within
// owner's tree. path must already be a clean tree path ("" = root).
func (pe Permissions) Allowed(owner string, p Principal, path string, access Access) bool {
	grants := pe.Grants
	if grants == nil {
		grants = DenyAllGrants
	}

	// Directory listings of the root and the roots themselves are readable
	// whenever the principal can read anything at all; write to roots is
	// never allowed (handled by the provider as well).
	if p.Owner {
		if access == AccessWrite && relayManagedFiles[path] {
			return false
		}
		return p.Scope.Allows(path, access)
	}

	// The credential scope caps visitors too — layout and grants can only
	// allow what the token was minted for.
	if !p.Scope.Allows(path, access) {
		return false
	}

	// poweur-sys/public is world-readable (it backs /.well-known).
	if Under(path, SysPublic) || path == RootSys {
		return access == AccessRead
	}

	if p.Identity == "" {
		return false // anonymous: nothing beyond poweur-sys/public
	}

	// Any valid Poweur ID may read /public.
	if (Under(path, RootPublic) || path == "") && access == AccessRead {
		return true
	}

	// /shared and /apps go through grants (EPIC-005).
	if Under(path, RootShared) || Under(path, RootApps) {
		return grants.Allowed(owner, p.Identity, path, access)
	}

	return false
}
