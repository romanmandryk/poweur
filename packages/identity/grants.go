package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
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

// Link-share tokens (E05-T4). A token is 16 random bytes rendered as
// lowercase unpadded base32 — 26 characters of [a-z2-7], 128 bits of
// entropy, safe in a URL path, case-insensitive by construction (so a mail
// client that lowercases the URL cannot break it) and free of the
// look-alike characters that make hand-copied links fail.
const (
	LinkTokenBytes = 16
	LinkTokenLen   = 26
	// MaxLinkDownloads bounds the download counter a grant may ask the
	// relay to track.
	MaxLinkDownloads = 1_000_000
)

var linkTokenEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// GenerateLinkToken returns a fresh capability-URL token.
func GenerateLinkToken() (string, error) {
	buf := make([]byte, LinkTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return linkTokenEncoding.EncodeToString(buf), nil
}

// ValidateLinkToken checks a token's shape. Tokens are lowercase by
// definition: the canonical signing string lowercases the audience, so a
// mixed-case token would sign as something other than what is stored.
func ValidateLinkToken(token string) error {
	if len(token) != LinkTokenLen {
		return fmt.Errorf("link token must be %d characters", LinkTokenLen)
	}
	for _, r := range token {
		if !(r >= 'a' && r <= 'z') && !(r >= '2' && r <= '7') {
			return fmt.Errorf("link token contains an invalid character %q", r)
		}
	}
	return nil
}

// HashLinkPassword hashes a link-share password. Link passwords reuse the
// PHC argon2id encoding app passwords already use (E03-T3): one hash
// format in the system, one verifier to audit.
func HashLinkPassword(password string) (string, error) { return HashAppPassword(password) }

// VerifyLinkPassword checks a password against a PHC argon2id hash.
func VerifyLinkPassword(password, encoded string) bool { return VerifyAppPassword(password, encoded) }

// ShareRoots are the top-level tree roots a grant may cover. /private and
// /poweur-sys are never shareable; /public is already readable by any valid
// ID (write-sharing /public is not supported in v1).
var ShareRoots = []string{"shared", "apps"}

// ShareAudience is one audience entry: a direct identity, an owner-local
// group name, or a capability-URL token (E05-T4 link shares).
type ShareAudience struct {
	ID    string `json:"id,omitempty"`
	Group string `json:"group,omitempty"`
	Link  string `json:"link,omitempty"`
}

// ShareLink carries the options that only make sense for a link share
// (E05-T4). Its presence in a grant is what makes the canonical string
// cover them, so the relay cannot strip the password or the download cap
// off a signed grant.
type ShareLink struct {
	// Password is a PHC-format argon2id hash (same encoding app passwords
	// use). Empty = no password.
	Password string `json:"password,omitempty"`
	// MaxDownloads caps successful file downloads through the link.
	// 0 = unlimited.
	MaxDownloads int `json:"max_downloads,omitempty"`
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
	// Link is set only on link-share grants (audience = one link token).
	Link      *ShareLink `json:"link,omitempty"`
	Signature string     `json:"signature"`
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
	links := 0
	for _, a := range g.Audience {
		set := 0
		for _, v := range []string{a.ID, a.Group, a.Link} {
			if strings.TrimSpace(v) != "" {
				set++
			}
		}
		if set != 1 {
			return fmt.Errorf("each audience entry needs exactly one of id, group or link")
		}
		if strings.TrimSpace(a.Link) != "" {
			links++
			if err := ValidateLinkToken(strings.TrimSpace(a.Link)); err != nil {
				return err
			}
		}
	}
	if err := g.validateLink(links); err != nil {
		return err
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

// validateLink applies the link-share rules (E05-T4). links is how many
// audience entries carry a token.
//
// A link grant is a *capability*: whoever holds the URL is the audience.
// That is why v1 keeps it deliberately narrow — one token per grant, no
// mixing with identity/group audiences (the two are enforced on completely
// different code paths), and read-only, so a leaked URL can never mutate
// the owner's tree.
func (g ShareGrant) validateLink(links int) error {
	switch {
	case links > 1:
		return fmt.Errorf("a grant carries at most one link token")
	case links == 1:
		if len(g.Audience) != 1 {
			return fmt.Errorf("a link grant's audience is the link alone (no ids or groups)")
		}
		if g.AllowsWrite() {
			return fmt.Errorf("link shares are read-only in v1")
		}
	case g.Link != nil:
		return fmt.Errorf("link options require a link audience entry")
	}
	if g.Link == nil {
		return nil
	}
	if g.Link.Password != "" && !strings.HasPrefix(g.Link.Password, "$argon2id$") {
		return fmt.Errorf("link password must be a PHC argon2id hash, never a plaintext password")
	}
	if g.Link.MaxDownloads < 0 || g.Link.MaxDownloads > MaxLinkDownloads {
		return fmt.Errorf("max_downloads must be between 0 (unlimited) and %d", MaxLinkDownloads)
	}
	return nil
}

// LinkToken returns the grant's capability token, if it is a link grant.
func (g ShareGrant) LinkToken() (string, bool) {
	for _, a := range g.Audience {
		if tok := strings.TrimSpace(a.Link); tok != "" {
			return strings.ToLower(tok), true
		}
	}
	return "", false
}

// IsLink reports whether this is a link-share grant.
func (g ShareGrant) IsLink() bool {
	_, ok := g.LinkToken()
	return ok
}

// MatchesLinkToken compares a presented token against the grant's in
// constant time (the token is the whole credential).
func (g ShareGrant) MatchesLinkToken(presented string) bool {
	tok, ok := g.LinkToken()
	if !ok {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(tok), []byte(strings.ToLower(strings.TrimSpace(presented)))) == 1
}

// RequiresPassword reports whether the link is password-protected.
func (g ShareGrant) RequiresPassword() bool {
	return g.Link != nil && g.Link.Password != ""
}

// CheckLinkPassword verifies a submitted link password.
func (g ShareGrant) CheckLinkPassword(password string) bool {
	if !g.RequiresPassword() {
		return true
	}
	return VerifyLinkPassword(password, g.Link.Password)
}

// MaxDownloads returns the grant's download cap (0 = unlimited).
func (g ShareGrant) MaxDownloads() int {
	if g.Link == nil {
		return 0
	}
	return g.Link.MaxDownloads
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
		switch {
		case a.ID != "":
			parts = append(parts, "id:"+strings.ToLower(strings.TrimSpace(a.ID)))
		case a.Link != "":
			parts = append(parts, "link:"+strings.ToLower(strings.TrimSpace(a.Link)))
		default:
			parts = append(parts, "group:"+strings.ToLower(strings.TrimSpace(a.Group)))
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// Canonical returns the string the owner signs. Field order is fixed;
// audience and permissions are sorted so JSON ordering doesn't matter.
//
// A grant carrying link options (E05-T4) appends three more lines — the
// marker, the password hash and the download cap — so those cannot be
// edited off a signed grant by whoever stores the file. Grants without a
// `link` object sign exactly the eight lines they always did, so adding
// link shares did not invalidate a single existing signature.
func (g ShareGrant) Canonical() string {
	perms := append([]string(nil), g.Permissions...)
	sort.Strings(perms)
	path, _ := NormalizeGrantPath(g.Path)
	fields := []string{
		"poweur-share-grant",
		strings.TrimSpace(g.ShareID),
		strings.ToLower(strings.TrimSpace(g.Owner)),
		path,
		canonicalAudience(g.Audience),
		strings.Join(perms, ","),
		g.CreatedAt,
		g.ExpiresAt,
	}
	if g.Link != nil {
		fields = append(fields,
			"poweur-share-link",
			g.Link.Password,
			strconv.Itoa(g.Link.MaxDownloads),
		)
	}
	return strings.Join(fields, "\n")
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
