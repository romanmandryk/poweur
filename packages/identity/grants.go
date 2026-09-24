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
	// PermCreate is currently valid only for file-request link grants. It is
	// the strict v1 subset of the granular create permission planned by
	// EPIC-020: create a new object, with no list/read/overwrite/delete.
	PermCreate = "create"
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
	// FileRequest turns the capability into an upload-only drop box. Its
	// presence is signed and requires permissions=["create"].
	FileRequest *ShareFileRequest `json:"file_request,omitempty"`
}

// ShareFileRequest contains owner-selected upload limits. Zero means the
// relay default/unlimited within the relay's own hard safety limits.
type ShareFileRequest struct {
	MaxUploads     int      `json:"max_uploads,omitempty"`
	MaxBytes       int64    `json:"max_bytes,omitempty"`
	MaxObjectBytes int64    `json:"max_object_bytes,omitempty"`
	AllowedTypes   []string `json:"allowed_types,omitempty"`
	Notify         bool     `json:"notify,omitempty"`
}

// ShareGrant is the signed grant document.
type ShareGrant struct {
	ShareID string `json:"share_id"`
	// SourceShareID binds a direct grant created from a public capability to
	// that capability. It is owner-signed provenance used for aggregate
	// conversion accounting; it is never authority by itself.
	SourceShareID string          `json:"source_share_id,omitempty"`
	Owner         string          `json:"owner"`
	Path          string          `json:"path"` // clean tree path, e.g. "shared/project-x"
	Audience      []ShareAudience `json:"audience"`
	Permissions   []string        `json:"permissions"`
	CreatedAt     string          `json:"created_at"`
	ExpiresAt     string          `json:"expires_at,omitempty"`
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
	if g.SourceShareID != "" {
		if strings.TrimSpace(g.SourceShareID) == "" || strings.TrimSpace(g.SourceShareID) != g.SourceShareID || strings.ContainsAny(g.SourceShareID, "/\\") || g.SourceShareID == "." || g.SourceShareID == ".." {
			return fmt.Errorf("invalid source_share_id")
		}
		if g.SourceShareID == g.ShareID {
			return fmt.Errorf("source_share_id must differ from share_id")
		}
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
	if g.SourceShareID != "" {
		if links != 0 || len(g.Audience) != 1 || strings.TrimSpace(g.Audience[0].ID) == "" {
			return fmt.Errorf("source_share_id requires exactly one direct identity recipient")
		}
	}
	if len(g.Permissions) == 0 {
		return fmt.Errorf("permissions is empty")
	}
	for _, p := range g.Permissions {
		if p != PermRead && p != PermWrite && p != PermCreate {
			return fmt.Errorf("unknown permission %q (v1 vocabulary: read, write, create)", p)
		}
		if p == PermCreate && !g.IsFileRequest() {
			return fmt.Errorf("create is currently limited to file-request links")
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
// different code paths). Ordinary links are read-only; file requests are a
// separately marked create-only subset that cannot inspect existing objects.
func (g ShareGrant) validateLink(links int) error {
	switch {
	case links > 1:
		return fmt.Errorf("a grant carries at most one link token")
	case links == 1:
		if len(g.Audience) != 1 {
			return fmt.Errorf("a link grant's audience is the link alone (no ids or groups)")
		}
		if g.AllowsWrite() {
			return fmt.Errorf("link shares are read-only or create-only and cannot grant write")
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
	request := g.Link.FileRequest
	if request == nil {
		if len(g.Permissions) != 1 || g.Permissions[0] != PermRead {
			return fmt.Errorf("download links require exactly the read permission")
		}
		return nil
	}
	if len(g.Permissions) != 1 || g.Permissions[0] != PermCreate {
		return fmt.Errorf("file-request links require exactly the create permission")
	}
	if g.Link.MaxDownloads != 0 {
		return fmt.Errorf("file-request links cannot set max_downloads")
	}
	if request.MaxUploads < 0 || request.MaxUploads > MaxLinkDownloads {
		return fmt.Errorf("max_uploads must be between 0 (unlimited) and %d", MaxLinkDownloads)
	}
	if request.MaxBytes < 0 || request.MaxObjectBytes < 0 {
		return fmt.Errorf("file-request byte limits cannot be negative")
	}
	for _, mediaType := range request.AllowedTypes {
		mediaType = strings.TrimSpace(strings.ToLower(mediaType))
		if mediaType == "" || strings.ContainsAny(mediaType, " \t\r\n;") || !strings.Contains(mediaType, "/") {
			return fmt.Errorf("invalid allowed media type %q", mediaType)
		}
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

// IsFileRequest reports whether the link is an upload-only capability.
func (g ShareGrant) IsFileRequest() bool {
	return g.Link != nil && g.Link.FileRequest != nil
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
// A grant carrying link options (E05-T4) appends the marker, password hash
// and download cap. A file request appends its marker and five limit fields.
// A direct grant upgraded from a public capability appends its source marker.
// Grants without either extension retain the original eight-line format.
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
		if g.Link.FileRequest != nil {
			types := append([]string(nil), g.Link.FileRequest.AllowedTypes...)
			for i := range types {
				types[i] = strings.ToLower(strings.TrimSpace(types[i]))
			}
			sort.Strings(types)
			fields = append(fields,
				"poweur-file-request",
				strconv.Itoa(g.Link.FileRequest.MaxUploads),
				strconv.FormatInt(g.Link.FileRequest.MaxBytes, 10),
				strconv.FormatInt(g.Link.FileRequest.MaxObjectBytes, 10),
				strings.Join(types, ","),
				strconv.FormatBool(g.Link.FileRequest.Notify),
			)
		}
	}
	if g.SourceShareID != "" {
		fields = append(fields, "poweur-share-source", strings.TrimSpace(g.SourceShareID))
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

// GroupSelfDoc is where a group identity keeps its own membership: the
// group is a hosted identity, and this is the one document in its tree
// that makes it a group (E05-T5).
const GroupSelfDoc = "poweur-sys/relay/groups/self.json"

// ShareGroup is a signed member list. It covers both kinds of group,
// because they are the same document:
//
//   - **Owner-local groups** (v1, shipped): `Group` is a bare name like
//     "team", the document lives at poweur-sys/relay/groups/<name>.json in
//     the owner's tree, and it is signed by the owner. It means something
//     only inside that owner's grants.
//   - **Group identities** (E05-T5): `Group` and `Owner` are both the
//     group's own Poweur ID, the document lives at GroupSelfDoc in the
//     *group's* tree, and it is signed by the group's identity key. It is
//     addressable — any owner can name it in a grant, and (EPIC-009) it can
//     receive messages.
//
// A group identity additionally carries Admins and Epoch. Those two fields
// append to the canonical string only when Admins is set, so every
// owner-local group signs exactly the five lines it always did.
type ShareGroup struct {
	Group   string   `json:"group"`
	Owner   string   `json:"owner"`
	Members []string `json:"members"`
	// Admins are the identities entitled to update this group's membership.
	// Present only on group identities; its presence is what marks the
	// document as one.
	Admins []string `json:"admins,omitempty"`
	// Epoch is a monotonic membership version. It exists so a relay can
	// refuse a rollback to an older member list, and so EPIC-009's group
	// key agreement (E09-T5) has a membership version to bind keys to
	// without inventing a second counter.
	Epoch     int    `json:"epoch,omitempty"`
	UpdatedAt string `json:"updated_at"`
	Signature string `json:"signature"`
}

// IsGroupIdentity reports whether this document describes an addressable
// group identity rather than an owner-local group.
func (gr ShareGroup) IsGroupIdentity() bool { return len(gr.Admins) > 0 }

// IsGroupIdentityName reports whether a grant audience's group name refers
// to a group *identity* rather than an owner-local group.
//
// The rule is the presence of a dot: a Poweur ID is a domain name and
// always has one, and owner-local group names are forbidden from having
// one (see Validate). So the two namespaces cannot collide, and an existing
// grant naming "team" keeps meaning alice's own "team" forever.
func IsGroupIdentityName(name string) bool {
	return strings.Contains(strings.TrimSpace(name), ".")
}

// HasAdmin reports whether identity may update this group's membership.
func (gr ShareGroup) HasAdmin(identity string) bool {
	for _, a := range gr.Admins {
		if strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(identity)) {
			return true
		}
	}
	return false
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
	return gr.validateIdentityFields()
}

// validateIdentityFields applies the group-identity rules (E05-T5).
func (gr ShareGroup) validateIdentityFields() error {
	name := strings.TrimSpace(gr.Group)
	if !gr.IsGroupIdentity() {
		// An owner-local group name must stay out of the identity
		// namespace, or a grant saying {"group": "team.acme.poweur.net"}
		// would be ambiguous.
		if IsGroupIdentityName(name) {
			return fmt.Errorf("an owner-local group name must not look like a Poweur ID (no dots); a group identity needs an admins list")
		}
		if gr.Epoch != 0 {
			return fmt.Errorf("epoch belongs to group identities, which need an admins list")
		}
		return nil
	}
	// A group identity is its own owner: the membership document lives in
	// the group's tree and is signed by the group's identity key, so there
	// is nobody else it could belong to.
	if !strings.EqualFold(name, strings.TrimSpace(gr.Owner)) {
		return fmt.Errorf("a group identity's group and owner must both be its own Poweur ID")
	}
	if !IsGroupIdentityName(name) {
		return fmt.Errorf("a group identity's name must be a Poweur ID")
	}
	if len(gr.Admins) > MaxGroupMembers {
		return fmt.Errorf("group exceeds %d admins", MaxGroupMembers)
	}
	if gr.Epoch < 0 {
		return fmt.Errorf("epoch must not be negative")
	}
	for _, a := range gr.Admins {
		if strings.TrimSpace(a) == "" {
			return fmt.Errorf("admins must not contain an empty entry")
		}
	}
	return nil
}

// Canonical returns the string the signer commits to (members sorted).
//
// A group identity appends three more lines — the marker, the sorted admin
// list and the epoch — so an admin list or a membership version cannot be
// edited off a signed document by whoever stores it. A group with no
// admins signs exactly the five lines it always did, so introducing group
// identities invalidated no existing owner-local group signature.
func (gr ShareGroup) Canonical() string {
	members := make([]string, 0, len(gr.Members))
	for _, m := range gr.Members {
		members = append(members, strings.ToLower(strings.TrimSpace(m)))
	}
	sort.Strings(members)
	fields := []string{
		"poweur-share-group",
		strings.ToLower(strings.TrimSpace(gr.Group)),
		strings.ToLower(strings.TrimSpace(gr.Owner)),
		strings.Join(members, ","),
		gr.UpdatedAt,
	}
	if gr.IsGroupIdentity() {
		admins := make([]string, 0, len(gr.Admins))
		for _, a := range gr.Admins {
			admins = append(admins, strings.ToLower(strings.TrimSpace(a)))
		}
		sort.Strings(admins)
		fields = append(fields,
			"poweur-group-identity",
			strings.Join(admins, ","),
			strconv.Itoa(gr.Epoch),
		)
	}
	return strings.Join(fields, "\n")
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
