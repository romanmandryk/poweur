package drive

import (
	"errors"
	"strings"
	"time"
)

// Share offers (PCP-0008, E20-T7). A share lives on the owner's relay; the
// member learns about it from an end-to-end encrypted message. The offer
// carries the complete signed share (which the member verifies against the
// issuer's key before trusting), where the drive is hosted, and the shared
// node's name and kind — the member cannot decrypt a shared root's own name,
// which is sealed to its parent. Accepting records a mount in the member's
// own encrypted drive and tells the owner; a revocation notice lets the
// member drop the mount. None of these messages grants anything: access is
// always decided by the share on the relay.

// OfferFormat is the body format of the v2 share messages.
const OfferFormat = 2

// ShareOffer is the decrypted body of sys.share.offer.
type ShareOffer struct {
	Format    int    `json:"format"`
	Share     Share  `json:"share"`
	Relay     string `json:"relay"`
	Name      string `json:"name,omitempty"`
	Kind      string `json:"kind"`
	OfferedAt string `json:"offered_at"`
}

// ShareAccept is the decrypted body of sys.share.accept.
type ShareAccept struct {
	Format     int    `json:"format"`
	Drive      string `json:"drive"`
	ShareID    string `json:"share_id"`
	AcceptedAt string `json:"accepted_at"`
}

// ShareRevoked is the decrypted body of sys.share.revoked.
type ShareRevoked struct {
	Format    int    `json:"format"`
	Drive     string `json:"drive"`
	ShareID   string `json:"share_id"`
	RevokedAt string `json:"revoked_at"`
}

func validRelay(relay string) bool {
	relay = strings.TrimPrefix(strings.TrimPrefix(relay, "https://"), "http://")
	return relay != "" && len(relay) <= 253 && !strings.ContainsAny(relay, "/ \\\x00?#@")
}

// Validate checks an offer's shape; the share's signature and the issuer's
// authority are checked by the recipient's client against resolved keys.
func (o ShareOffer) Validate() error {
	if o.Format != OfferFormat {
		return errors.New("unsupported share offer format")
	}
	if err := o.Share.Validate(); err != nil {
		return err
	}
	if o.Share.Member == "" {
		return errors.New("an offer is for a member; links travel as URLs")
	}
	if !validRelay(o.Relay) {
		return errors.New("invalid relay")
	}
	if o.Kind != KindFile && o.Kind != KindFolder {
		return errors.New("kind is file or folder")
	}
	if o.Name != "" {
		if _, err := NormalizeName(o.Name); err != nil {
			return err
		}
	}
	if !validTime(o.OfferedAt) {
		return errors.New("invalid offer time")
	}
	return nil
}

func (a ShareAccept) Validate() error {
	if a.Format != OfferFormat || !validIdentity(a.Drive) || !validHex(a.ShareID, 16) || !validTime(a.AcceptedAt) {
		return errors.New("invalid share accept")
	}
	return nil
}

func (r ShareRevoked) Validate() error {
	if r.Format != OfferFormat || !validIdentity(r.Drive) || !validHex(r.ShareID, 16) || !validTime(r.RevokedAt) {
		return errors.New("invalid share revocation notice")
	}
	return nil
}

// Mount is one accepted share in a member's mounts file.
type Mount struct {
	Drive   string `json:"drive"`
	Relay   string `json:"relay"`
	Node    string `json:"node"`
	ShareID string `json:"share_id"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Role    string `json:"role"`
	Mounted string `json:"mounted"`
}

// Mounts is `.poweur/private/mounts.json` in a member's drive: the shares
// they accepted, shown as `Shared/<owner>/<name>`. It is encrypted like any
// private file; the relay never sees it.
type Mounts struct {
	Format int     `json:"format"`
	Mounts []Mount `json:"mounts"`
}

// Accept adds (or refreshes) the mount for an offer and returns the accept
// message body to send to the owner.
func (m *Mounts) Accept(o ShareOffer, now time.Time) (ShareAccept, error) {
	if err := o.Validate(); err != nil {
		return ShareAccept{}, err
	}
	m.Format = 1
	stamp := now.UTC().Format(time.RFC3339)
	mount := Mount{Drive: o.Share.Drive, Relay: o.Relay, Node: o.Share.Node, ShareID: o.Share.ID, Name: o.Name, Kind: o.Kind, Role: o.Share.Role, Mounted: stamp}
	kept := m.Mounts[:0]
	for _, existing := range m.Mounts {
		if existing.Drive != mount.Drive || existing.Node != mount.Node {
			kept = append(kept, existing)
		}
	}
	m.Mounts = append(kept, mount)
	return ShareAccept{Format: OfferFormat, Drive: o.Share.Drive, ShareID: o.Share.ID, AcceptedAt: stamp}, nil
}

// Drop removes the mount a revocation notice names; it reports whether one
// was removed.
func (m *Mounts) Drop(r ShareRevoked) bool {
	kept := m.Mounts[:0]
	removed := false
	for _, existing := range m.Mounts {
		if existing.Drive == r.Drive && existing.ShareID == r.ShareID {
			removed = true
			continue
		}
		kept = append(kept, existing)
	}
	m.Mounts = kept
	return removed
}
