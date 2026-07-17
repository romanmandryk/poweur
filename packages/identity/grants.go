package identity

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Share grants (EPIC-005): signed documents stored in the owner's tree at
// `poweur-sys/relay/shares/<share-id>.json`. The relay *enforces* grants but
// cannot forge them (owner-key signature); the grant set syncs to the
// owner's devices like any other file. Groups are files too, at
// `poweur-sys/relay/groups/<name>.json`. This lives in the shared identity
// package so CLI/web (writers) and relay (verifier) agree on the format.

// Grant permission vocabulary (v1). "write" implies "read"; delete/move are
// write operations. "share" and "admin" are reserved for later.
const (
	PermRead  = "read"
	PermWrite = "write"
)

// Limits (E05-T1: max sizes).
const (
	MaxGrantAudience = 100
	MaxGroupMembers  = 1000
)

// ShareRoots are the top-level tree roots a grant may cover. /private and
// /poweur-sys are never shareable; /public is already readable by any valid
// ID (write-sharing /public is not supported in v1).
var ShareRoots = []string{"shared", "apps"}

// ShareAudience is one audience entry: a direct identity or an owner-local
// group name. ("link" is reserved for capability-URL shares, E05-T4.)
type ShareAudience struct {
	ID    string `json:"id,omitempty"`
	Group string `json:"group,omitempty"`
}

// ShareGrant is the signed grant document.
type ShareGrant struct {
	ShareID     string          `json:"share_id"`
	Owner       string          `json:"owner"`
	Path        string          `json:"path"` // clean tree path, e.g. "shared/project-x"
	Audience    []ShareAudience `json:"audience"`
	Permissions []string        `json:"permissions"`
	CreatedAt   string          `json:"created_at"`
	ExpiresAt   string          `json:"expires_at,omitempty"`
	Signature   string          `json:"signature"`
}

// NormalizeGrantPath validates and normalizes a grant path: slashes trimmed,
// lowercase-preserving, must sit under a shareable root (not the root
// itself — you share a folder or file, not all of /shared).
func NormalizeGrantPath(raw string) (string, error) {
	p := strings.Trim(strings.TrimSpace(raw), "/")
	if p == "" {
		return "", fmt.Errorf("grant path is empty")
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("invalid grant path segment %q", seg)
		}
	}
	top := p
	if i := strings.IndexByte(p, '/'); i >= 0 {
		top = p[:i]
	}
	for _, root := range ShareRoots {
		if top == root {
			if p == root {
				return "", fmt.Errorf("cannot grant the %s root itself; share a subfolder or file", root)
			}
			return p, nil
		}
	}
	return "", fmt.Errorf("grants may only cover paths under /%s", strings.Join(ShareRoots, "/ or /"))
}

// Validate checks everything except the signature.
func (g ShareGrant) Validate() error {
	if strings.TrimSpace(g.ShareID) == "" {
		return fmt.Errorf("share_id is required")
	}
	if strings.TrimSpace(g.Owner) == "" {
		return fmt.Errorf("owner is required")
	}
	if _, err := NormalizeGrantPath(g.Path); err != nil {
		return err
	}
	if len(g.Audience) == 0 {
		return fmt.Errorf("audience is empty")
	}
	if len(g.Audience) > MaxGrantAudience {
		return fmt.Errorf("audience exceeds %d entries", MaxGrantAudience)
	}
	for _, a := range g.Audience {
		hasID := strings.TrimSpace(a.ID) != ""
		hasGroup := strings.TrimSpace(a.Group) != ""
		if hasID == hasGroup { // neither or both
			return fmt.Errorf("each audience entry needs exactly one of id or group")
		}
	}
	if len(g.Permissions) == 0 {
		return fmt.Errorf("permissions is empty")
	}
	for _, p := range g.Permissions {
		if p != PermRead && p != PermWrite {
			return fmt.Errorf("unknown permission %q (v1 vocabulary: read, write)", p)
		}
	}
	if g.CreatedAt != "" {
		if _, err := time.Parse(time.RFC3339, g.CreatedAt); err != nil {
			return fmt.Errorf("invalid created_at: %w", err)
		}
	}
	if g.ExpiresAt != "" {
		if _, err := time.Parse(time.RFC3339, g.ExpiresAt); err != nil {
			return fmt.Errorf("invalid expires_at: %w", err)
		}
	}
	return nil
}

// Expired reports whether the grant is past its expiry at now.
func (g ShareGrant) Expired(now time.Time) bool {
	if g.ExpiresAt == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, g.ExpiresAt)
	if err != nil {
		return true // unparseable expiry fails closed
	}
	return now.After(t)
}

// AllowsWrite reports whether the grant's permission set includes write.
func (g ShareGrant) AllowsWrite() bool {
	for _, p := range g.Permissions {
		if p == PermWrite {
			return true
		}
	}
	return false
}

// canonicalAudience renders the audience deterministically for signing.
func canonicalAudience(audience []ShareAudience) string {
	parts := make([]string, 0, len(audience))
	for _, a := range audience {
		if a.ID != "" {
			parts = append(parts, "id:"+strings.ToLower(strings.TrimSpace(a.ID)))
		} else {
			parts = append(parts, "group:"+strings.ToLower(strings.TrimSpace(a.Group)))
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// Canonical returns the string the owner signs. Field order is fixed;
// audience and permissions are sorted so JSON ordering doesn't matter.
func (g ShareGrant) Canonical() string {
	perms := append([]string(nil), g.Permissions...)
	sort.Strings(perms)
	path, _ := NormalizeGrantPath(g.Path)
	return strings.Join([]string{
		"poweur-share-grant",
		strings.TrimSpace(g.ShareID),
		strings.ToLower(strings.TrimSpace(g.Owner)),
		path,
		canonicalAudience(g.Audience),
		strings.Join(perms, ","),
		g.CreatedAt,
		g.ExpiresAt,
	}, "\n")
}

// Sign fills Signature using the owner's identity key.
func (g *ShareGrant) Sign(priv ed25519.PrivateKey) error {
	if err := g.Validate(); err != nil {
		return err
	}
	g.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, []byte(g.Canonical())))
	return nil
}

// VerifySignature checks the signature against the owner's public key.
func (g ShareGrant) VerifySignature(pub ed25519.PublicKey) error {
	sig, err := base64.RawURLEncoding.DecodeString(g.Signature)
	if err != nil {
		return fmt.Errorf("invalid signature encoding: %w", err)
	}
	if !ed25519.Verify(pub, []byte(g.Canonical()), sig) {
		return fmt.Errorf("grant signature verification failed")
	}
	return nil
}

// ParseShareGrant decodes and validates one grant document (signature is
// verified separately by the caller, which knows the owner key).
func ParseShareGrant(raw []byte) (ShareGrant, error) {
	var g ShareGrant
	if err := json.Unmarshal(raw, &g); err != nil {
		return ShareGrant{}, fmt.Errorf("invalid grant document: %w", err)
	}
	if err := g.Validate(); err != nil {
		return ShareGrant{}, err
	}
	if strings.TrimSpace(g.Signature) == "" {
		return ShareGrant{}, fmt.Errorf("grant is unsigned")
	}
	return g, nil
}

// ShareGroup is an owner-local named member list (v1 groups). Cross-owner
// group identities are a later layer on the same format (E05-T5).
type ShareGroup struct {
	Group     string   `json:"group"`
	Owner     string   `json:"owner"`
	Members   []string `json:"members"`
	UpdatedAt string   `json:"updated_at"`
	Signature string   `json:"signature"`
}

// Validate checks everything except the signature.
func (gr ShareGroup) Validate() error {
	if strings.TrimSpace(gr.Group) == "" {
		return fmt.Errorf("group name is required")
	}
	if strings.ContainsAny(gr.Group, "/\\") {
		return fmt.Errorf("group name must not contain slashes")
	}
	if strings.TrimSpace(gr.Owner) == "" {
		return fmt.Errorf("owner is required")
	}
	if len(gr.Members) > MaxGroupMembers {
		return fmt.Errorf("group exceeds %d members", MaxGroupMembers)
	}
	if gr.UpdatedAt != "" {
		if _, err := time.Parse(time.RFC3339, gr.UpdatedAt); err != nil {
			return fmt.Errorf("invalid updated_at: %w", err)
		}
	}
	return nil
}

// Canonical returns the string the owner signs (members sorted).
func (gr ShareGroup) Canonical() string {
	members := make([]string, 0, len(gr.Members))
	for _, m := range gr.Members {
		members = append(members, strings.ToLower(strings.TrimSpace(m)))
	}
	sort.Strings(members)
	return strings.Join([]string{
		"poweur-share-group",
		strings.ToLower(strings.TrimSpace(gr.Group)),
		strings.ToLower(strings.TrimSpace(gr.Owner)),
		strings.Join(members, ","),
		gr.UpdatedAt,
	}, "\n")
}

// Sign fills Signature using the owner's identity key.
func (gr *ShareGroup) Sign(priv ed25519.PrivateKey) error {
	if err := gr.Validate(); err != nil {
		return err
	}
	gr.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, []byte(gr.Canonical())))
	return nil
}

// VerifySignature checks the signature against the owner's public key.
func (gr ShareGroup) VerifySignature(pub ed25519.PublicKey) error {
	sig, err := base64.RawURLEncoding.DecodeString(gr.Signature)
	if err != nil {
		return fmt.Errorf("invalid signature encoding: %w", err)
	}
	if !ed25519.Verify(pub, []byte(gr.Canonical()), sig) {
		return fmt.Errorf("group signature verification failed")
	}
	return nil
}

// ParseShareGroup decodes and validates one group document.
func ParseShareGroup(raw []byte) (ShareGroup, error) {
	var gr ShareGroup
	if err := json.Unmarshal(raw, &gr); err != nil {
		return ShareGroup{}, fmt.Errorf("invalid group document: %w", err)
	}
	if err := gr.Validate(); err != nil {
		return ShareGroup{}, err
	}
	if strings.TrimSpace(gr.Signature) == "" {
		return ShareGroup{}, fmt.Errorf("group document is unsigned")
	}
	return gr, nil
}

// HasMember reports whether identity is in the group (case-insensitive).
func (gr ShareGroup) HasMember(identity string) bool {
	for _, m := range gr.Members {
		if strings.EqualFold(strings.TrimSpace(m), identity) {
			return true
		}
	}
	return false
}
