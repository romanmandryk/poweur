package identity

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"
)

// Share lifecycle payloads (EPIC-005 E05-T3). They travel inside the
// end-to-end encrypted body of sys.share.* messages. The relay sees the
// message type and routing identities, never these fields.

const (
	ShareLifecycleVersion = 1
	ShareMountFile        = ".poweur-mount.json"
	MaxShareMountName     = 128
)

// ShareOffer is sent by a grant owner to each direct audience identity. The
// complete signed grant is included because the recipient cannot read the
// owner's private poweur-sys/relay/shares directory. It is evidence and a
// discovery hint, not authority: the owner's relay reloads and verifies its
// own grant document on every file request.
type ShareOffer struct {
	Version   int        `json:"version"`
	Grant     ShareGrant `json:"grant"`
	OfferedAt string     `json:"offered_at"`
}

func (o ShareOffer) ValidateFor(recipient string) error {
	if o.Version != ShareLifecycleVersion {
		return fmt.Errorf("unsupported share offer version %d", o.Version)
	}
	if err := o.Grant.Validate(); err != nil {
		return fmt.Errorf("invalid offered grant: %w", err)
	}
	if strings.TrimSpace(o.Grant.Signature) == "" {
		return fmt.Errorf("offered grant is unsigned")
	}
	if o.Grant.IsLink() {
		return fmt.Errorf("link grants are not sent as identity offers")
	}
	if _, err := time.Parse(time.RFC3339, o.OfferedAt); err != nil {
		return fmt.Errorf("invalid offered_at: %w", err)
	}
	recipient = strings.ToLower(strings.TrimSpace(recipient))
	if recipient == "" {
		return fmt.Errorf("recipient is required")
	}
	for _, audience := range o.Grant.Audience {
		if strings.EqualFold(strings.TrimSpace(audience.ID), recipient) {
			return nil
		}
	}
	return fmt.Errorf("recipient %s is not a direct audience member", recipient)
}

func ParseShareOffer(raw []byte, recipient string) (ShareOffer, error) {
	var offer ShareOffer
	if err := json.Unmarshal(raw, &offer); err != nil {
		return ShareOffer{}, fmt.Errorf("invalid share offer: %w", err)
	}
	if err := offer.ValidateFor(recipient); err != nil {
		return ShareOffer{}, err
	}
	return offer, nil
}

// ShareAccept acknowledges an offer and records the recipient-local virtual
// mount path. The sender of the enclosing message must equal Recipient and
// its recipient must equal Owner; clients verify that binding.
type ShareAccept struct {
	Version    int    `json:"version"`
	ShareID    string `json:"share_id"`
	Owner      string `json:"owner"`
	Recipient  string `json:"recipient"`
	MountPath  string `json:"mount_path"`
	AcceptedAt string `json:"accepted_at"`
}

func (a ShareAccept) Validate() error {
	if a.Version != ShareLifecycleVersion {
		return fmt.Errorf("unsupported share accept version %d", a.Version)
	}
	if err := validateLifecycleShareID(a.ShareID); err != nil {
		return err
	}
	if err := ValidateIdentityName(strings.ToLower(strings.TrimSpace(a.Owner))); err != nil {
		return fmt.Errorf("invalid owner: %w", err)
	}
	if err := ValidateIdentityName(strings.ToLower(strings.TrimSpace(a.Recipient))); err != nil {
		return fmt.Errorf("invalid recipient: %w", err)
	}
	if _, err := NormalizeShareMountPath(a.MountPath, a.Owner); err != nil {
		return err
	}
	if _, err := time.Parse(time.RFC3339, a.AcceptedAt); err != nil {
		return fmt.Errorf("invalid accepted_at: %w", err)
	}
	return nil
}

func ParseShareAccept(raw []byte) (ShareAccept, error) {
	var accept ShareAccept
	if err := json.Unmarshal(raw, &accept); err != nil {
		return ShareAccept{}, fmt.Errorf("invalid share acceptance: %w", err)
	}
	if err := accept.Validate(); err != nil {
		return ShareAccept{}, err
	}
	return accept, nil
}

// ShareRevoked tells known recipients that the owner removed a grant. Access
// is already gone at the owner's relay; this notification only lets clients
// remove or mark the now-dead local mount without waiting for a failed read.
type ShareRevoked struct {
	Version   int    `json:"version"`
	ShareID   string `json:"share_id"`
	Owner     string `json:"owner"`
	RevokedAt string `json:"revoked_at"`
}

// ShareClaim is sent by a newly claimed/signed-in identity to a link owner.
// Possession of Token proves continuity with the anonymous capability, but
// creates no authority: the owner must issue a new direct-ID grant.
type ShareClaim struct {
	Version   int    `json:"version"`
	ShareID   string `json:"share_id"`
	Owner     string `json:"owner"`
	Token     string `json:"token"`
	Claimant  string `json:"claimant"`
	Action    string `json:"action"`
	ClaimedAt string `json:"claimed_at"`
}

func (c ShareClaim) Validate() error {
	if c.Version != ShareLifecycleVersion {
		return fmt.Errorf("unsupported share claim version %d", c.Version)
	}
	if err := validateLifecycleShareID(c.ShareID); err != nil {
		return err
	}
	if err := ValidateIdentityName(strings.ToLower(strings.TrimSpace(c.Owner))); err != nil {
		return fmt.Errorf("invalid owner: %w", err)
	}
	if err := ValidateLinkToken(strings.ToLower(strings.TrimSpace(c.Token))); err != nil {
		return fmt.Errorf("invalid token: %w", err)
	}
	if err := ValidateIdentityName(strings.ToLower(strings.TrimSpace(c.Claimant))); err != nil {
		return fmt.Errorf("invalid claimant: %w", err)
	}
	switch c.Action {
	case "viewed", "downloaded", "uploaded":
	default:
		return fmt.Errorf("invalid claim action %q", c.Action)
	}
	if _, err := time.Parse(time.RFC3339, c.ClaimedAt); err != nil {
		return fmt.Errorf("invalid claimed_at: %w", err)
	}
	return nil
}

func ParseShareClaim(raw []byte) (ShareClaim, error) {
	var claim ShareClaim
	if err := json.Unmarshal(raw, &claim); err != nil {
		return ShareClaim{}, fmt.Errorf("invalid share claim: %w", err)
	}
	if err := claim.Validate(); err != nil {
		return ShareClaim{}, err
	}
	return claim, nil
}

func (r ShareRevoked) Validate() error {
	if r.Version != ShareLifecycleVersion {
		return fmt.Errorf("unsupported share revoked version %d", r.Version)
	}
	if err := validateLifecycleShareID(r.ShareID); err != nil {
		return err
	}
	if err := ValidateIdentityName(strings.ToLower(strings.TrimSpace(r.Owner))); err != nil {
		return fmt.Errorf("invalid owner: %w", err)
	}
	if _, err := time.Parse(time.RFC3339, r.RevokedAt); err != nil {
		return fmt.Errorf("invalid revoked_at: %w", err)
	}
	return nil
}

func ParseShareRevoked(raw []byte) (ShareRevoked, error) {
	var revoked ShareRevoked
	if err := json.Unmarshal(raw, &revoked); err != nil {
		return ShareRevoked{}, fmt.Errorf("invalid share revocation: %w", err)
	}
	if err := revoked.Validate(); err != nil {
		return ShareRevoked{}, err
	}
	return revoked, nil
}

// ShareMount is a recipient-local pointer stored at
// /shared/<owner>/<name>/.poweur-mount.json. It contains no credential and
// grants no authority. A Poweur-aware client resolves Owner and mints a fresh
// visitor token for SourcePath; a vanilla DAV client sees an ordinary small
// JSON file rather than a misleading local copy of remote data.
type ShareMount struct {
	Version     int      `json:"version"`
	ShareID     string   `json:"share_id"`
	Owner       string   `json:"owner"`
	SourcePath  string   `json:"source_path"`
	Permissions []string `json:"permissions"`
	AcceptedAt  string   `json:"accepted_at"`
	ExpiresAt   string   `json:"expires_at,omitempty"`
}

func (m ShareMount) Validate() error {
	if m.Version != ShareLifecycleVersion {
		return fmt.Errorf("unsupported share mount version %d", m.Version)
	}
	if err := validateLifecycleShareID(m.ShareID); err != nil {
		return err
	}
	if err := ValidateIdentityName(strings.ToLower(strings.TrimSpace(m.Owner))); err != nil {
		return fmt.Errorf("invalid owner: %w", err)
	}
	normalized, err := NormalizeGrantPath(m.SourcePath)
	if err != nil {
		return fmt.Errorf("invalid source_path: %w", err)
	}
	if normalized != strings.Trim(m.SourcePath, "/") {
		return fmt.Errorf("source_path is not normalized")
	}
	if len(m.Permissions) == 0 {
		return fmt.Errorf("permissions is empty")
	}
	for _, permission := range m.Permissions {
		if permission != PermRead && permission != PermWrite {
			return fmt.Errorf("unknown permission %q", permission)
		}
	}
	if _, err := time.Parse(time.RFC3339, m.AcceptedAt); err != nil {
		return fmt.Errorf("invalid accepted_at: %w", err)
	}
	if m.ExpiresAt != "" {
		if _, err := time.Parse(time.RFC3339, m.ExpiresAt); err != nil {
			return fmt.Errorf("invalid expires_at: %w", err)
		}
	}
	return nil
}

func ParseShareMount(raw []byte) (ShareMount, error) {
	var mount ShareMount
	if err := json.Unmarshal(raw, &mount); err != nil {
		return ShareMount{}, fmt.Errorf("invalid share mount: %w", err)
	}
	if err := mount.Validate(); err != nil {
		return ShareMount{}, err
	}
	return mount, nil
}

// NormalizeShareMountPath returns the recipient-local directory in which a
// mount document lives. The owner is a path segment, so a pointer cannot be
// moved under another identity's label without validation failing.
func NormalizeShareMountPath(raw, owner string) (string, error) {
	p := strings.Trim(strings.TrimSpace(raw), "/")
	if p == "" || path.Clean(p) != p {
		return "", fmt.Errorf("invalid mount_path")
	}
	parts := strings.Split(p, "/")
	if len(parts) != 3 || parts[0] != "shared" {
		return "", fmt.Errorf("mount_path must be shared/<owner>/<name>")
	}
	if !strings.EqualFold(parts[1], strings.TrimSpace(owner)) {
		return "", fmt.Errorf("mount_path owner does not match %s", owner)
	}
	if len(parts[2]) == 0 || len(parts[2]) > MaxShareMountName || parts[2] == "." || parts[2] == ".." {
		return "", fmt.Errorf("invalid mount name")
	}
	return p, nil
}

func validateLifecycleShareID(shareID string) error {
	shareID = strings.TrimSpace(shareID)
	if shareID == "" {
		return fmt.Errorf("share_id is required")
	}
	if strings.ContainsAny(shareID, "/\\") || shareID == "." || shareID == ".." {
		return fmt.Errorf("invalid share_id")
	}
	return nil
}
