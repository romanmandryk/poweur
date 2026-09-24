// Package files implements the per-identity home filesystem (EPIC-003):
// path rules, the StorageProvider abstraction, the metadata index
// (content-hash etags + change counter), and the layout permission engine.
package files

import (
	"fmt"
	gopath "path"
	"strings"
	"unicode/utf8"
)

const (
	MaxSegmentBytes = 255
	MaxPathBytes    = 4096
	MaxDepth        = 32

	// Documented protocol markers are the only .poweur-* names allowed in
	// user trees. Everything else stays reserved for forward-compatible use.
	WebPublicMarker  = ".poweur-web-public"
	ShareMountMarker = ".poweur-mount.json"
)

// Top-level roots of every identity tree. Nothing else may exist at the top
// level; the roots are virtual (always present, never created or deleted).
const (
	RootSys     = "poweur-sys"
	RootPublic  = "public"
	RootShared  = "shared"
	RootPrivate = "private"
	RootApps    = "apps"
)

// Roots lists the five top-level roots in display order.
var Roots = []string{RootSys, RootPublic, RootShared, RootPrivate, RootApps}

// Sys subdirectories (EPIC-006).
const (
	SysPublic  = "poweur-sys/public"
	SysRelay   = "poweur-sys/relay"   // owner + relay (config the relay enforces from)
	SysPrivate = "poweur-sys/private" // owner only; relay stores but must not read
)

// CleanPath validates and normalizes a slash path from a DAV URL into the
// internal form: no leading/trailing slash, "" for the tree root.
func CleanPath(raw string) (string, error) {
	p := gopath.Clean("/" + strings.ReplaceAll(raw, "\\", "/"))
	if p == "/" {
		return "", nil
	}
	p = strings.TrimPrefix(p, "/")
	if len(p) > MaxPathBytes {
		return "", fmt.Errorf("path too long")
	}
	segs := strings.Split(p, "/")
	if len(segs) > MaxDepth {
		return "", fmt.Errorf("path too deep (max %d segments)", MaxDepth)
	}
	for _, seg := range segs {
		if err := validateSegment(seg); err != nil {
			return "", err
		}
	}
	return p, nil
}

func validateSegment(seg string) error {
	if seg == "" || seg == "." || seg == ".." {
		return fmt.Errorf("invalid path segment %q", seg)
	}
	if len(seg) > MaxSegmentBytes {
		return fmt.Errorf("path segment too long")
	}
	if !utf8.ValidString(seg) {
		return fmt.Errorf("path segment is not valid UTF-8")
	}
	for _, r := range seg {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("path segment contains control characters")
		}
	}
	if strings.HasPrefix(seg, ".poweur-") && seg != WebPublicMarker && seg != ShareMountMarker {
		return fmt.Errorf("the .poweur- name prefix is reserved")
	}
	return nil
}

// TopRoot returns the first segment of a clean path ("" for root).
func TopRoot(clean string) string {
	if clean == "" {
		return ""
	}
	if i := strings.IndexByte(clean, '/'); i >= 0 {
		return clean[:i]
	}
	return clean
}

// IsRoot reports whether name is one of the five top-level roots.
func IsRoot(name string) bool {
	for _, r := range Roots {
		if name == r {
			return true
		}
	}
	return false
}

// ValidateTreePath applies the layout rules on top of CleanPath: the first
// segment must be a known root, and the root itself cannot be the target of
// a create/delete/rename.
func ValidateTreePath(raw string) (string, error) {
	clean, err := CleanPath(raw)
	if err != nil {
		return "", err
	}
	if clean == "" {
		return "", nil
	}
	if !IsRoot(TopRoot(clean)) {
		return "", fmt.Errorf("unknown top-level root %q (want one of %s)", TopRoot(clean), strings.Join(Roots, ", "))
	}
	return clean, nil
}

// Under reports whether clean path p is prefix itself or below it.
func Under(p, prefix string) bool {
	if prefix == "" {
		return true
	}
	return p == prefix || strings.HasPrefix(p, prefix+"/")
}
