package engine

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"

	"github.com/poweur/identity/drive"
)

// Links and caps (E20-T7). A link is a share whose member is whoever holds
// its ID (and, for a password-protected link, the verifier derived from the
// password). The decryption key lives in the URL fragment and never reaches
// the relay. Caps bound what one share lets its holder add — bytes, files,
// records — and how many times a link may be opened; usage is journalled, so
// caps survive restarts.

// ErrCap reports a share's cap on files, records or opens (HTTP 429). A cap
// on bytes is ErrQuota.
var ErrCap = errors.New("share limit reached")

const kindLinkUse = "linkuse"

type linkUseOp struct {
	Link string `json:"link"`
}

// charge is what one commit spends against the share that allowed it.
type charge struct {
	Share   string `json:"share"`
	Bytes   uint64 `json:"bytes,omitempty"`
	Files   uint64 `json:"files,omitempty"`
	Records uint64 `json:"records,omitempty"`
}

// capUse is a share's spending so far.
type capUse struct {
	Bytes   uint64 `json:"bytes,omitempty"`
	Files   uint64 `json:"files,omitempty"`
	Records uint64 `json:"records,omitempty"`
}

// governing is the share that lets actor act on nodeID with need: the
// closest one on the node or an ancestor. Nil for the owner.
func (st *state) governing(actor, nodeID, need string, now nowFunc) *drive.Share {
	if strings.EqualFold(actor, st.Drive) {
		return nil
	}
	linkID, isLink := strings.CutPrefix(actor, linkActorPrefix)
	for seen, id := 0, nodeID; id != "" && seen <= len(st.Nodes); seen++ {
		var best *drive.Share
		for _, s := range st.Shares {
			if s.Node != id || s.ExpiredAt(now()) || !drive.RoleGrants(s.Role, need) {
				continue
			}
			if isLink && s.Link == linkID || !isLink && s.Member == actor {
				// Several shares on one node: take the ID order, so replay
				// charges the same one.
				if best == nil || s.ID < best.ID {
					best = s
				}
			}
		}
		if best != nil {
			return best
		}
		n := st.Nodes[id]
		if n == nil {
			break
		}
		id = n.Folder
	}
	return nil
}

// chargeFor checks a commit's spending against the governing share's caps
// and returns the charge to journal (nil for the owner or an uncapped share).
func (st *state) chargeFor(actor, nodeID, need string, c charge, now nowFunc) (*charge, error) {
	s := st.governing(actor, nodeID, need, now)
	if s == nil {
		return nil, nil
	}
	use := st.ShareUse[s.ID]
	if use == nil {
		use = &capUse{}
	}
	if s.Caps.Bytes > 0 && use.Bytes+c.Bytes > s.Caps.Bytes {
		return nil, fmt.Errorf("%w: share %s allows %d bytes", ErrQuota, s.ID, s.Caps.Bytes)
	}
	if s.Caps.Files > 0 && use.Files+c.Files > s.Caps.Files {
		return nil, fmt.Errorf("%w: share allows %d files", ErrCap, s.Caps.Files)
	}
	if s.Caps.Records > 0 && use.Records+c.Records > s.Caps.Records {
		return nil, fmt.Errorf("%w: share allows %d records", ErrCap, s.Caps.Records)
	}
	c.Share = s.ID
	return &c, nil
}

func applyCharge(st *state, c *charge) {
	if c == nil || st.Shares[c.Share] == nil {
		return
	}
	use := st.ShareUse[c.Share]
	if use == nil {
		use = &capUse{}
		st.ShareUse[c.Share] = use
	}
	use.Bytes += c.Bytes
	use.Files += c.Files
	use.Records += c.Records
}

// linkShare finds a live link's share.
func (st *state) linkShare(linkID string, now nowFunc) *drive.Share {
	for _, s := range st.Shares {
		if s.Link == linkID && !s.ExpiredAt(now()) {
			return s
		}
	}
	return nil
}

// LinkInfo is what anyone holding a link ID may learn before opening it:
// enough to derive the password key and solve proof-of-work, nothing else.
type LinkInfo struct {
	Role      string `json:"role"`
	Expires   string `json:"expires,omitempty"`
	KDF       string `json:"kdf,omitempty"`
	Salt      string `json:"salt,omitempty"`
	PoW       uint64 `json:"pow,omitempty"`
	Password  bool   `json:"password"`
	Remaining uint64 `json:"remaining_opens,omitempty"`
}

// Link describes a live link, or ErrNotFound for an unknown, expired or
// exhausted one.
func (e *Engine) Link(ctx context.Context, driveID, linkID string) (LinkInfo, error) {
	h, err := e.open(ctx, driveID)
	if err != nil {
		return LinkInfo{}, err
	}
	defer h.mu.Unlock()
	s := h.st.linkShare(linkID, e.now)
	if s == nil {
		return LinkInfo{}, ErrNotFound
	}
	info := LinkInfo{Role: s.Role, Expires: s.Expires, KDF: s.KDF, Salt: s.Salt, PoW: s.PoW, Password: s.VerifierHash != ""}
	if s.Caps.Downloads > 0 {
		used := h.st.LinkUses[linkID]
		if used >= s.Caps.Downloads {
			return LinkInfo{}, ErrNotFound
		}
		info.Remaining = s.Caps.Downloads - used
	}
	return info, nil
}

// OpenLink authenticates a link holder: the link is live, the verifier
// matches a password-protected link, and it has opens left. With count, the
// open is journalled against the link's download cap (a viewer counts once
// per visit, not per chunk).
func (e *Engine) OpenLink(ctx context.Context, driveID, linkID string, verifier []byte, count bool) error {
	h, err := e.open(ctx, driveID)
	if err != nil {
		return err
	}
	defer h.mu.Unlock()
	if count {
		if err := e.catchUp(ctx, h); err != nil {
			return err
		}
	}
	s := h.st.linkShare(linkID, e.now)
	if s == nil {
		return ErrNotFound
	}
	if s.VerifierHash != "" && subtle.ConstantTimeCompare([]byte(drive.VerifierHash(verifier)), []byte(s.VerifierHash)) != 1 {
		return fmt.Errorf("%w: wrong link password", ErrForbidden)
	}
	if s.Caps.Downloads > 0 {
		if h.st.LinkUses[linkID] >= s.Caps.Downloads {
			return fmt.Errorf("%w: link has no opens left", ErrCap)
		}
		if count {
			return e.publish(ctx, h, journalOp{Kind: kindLinkUse, At: e.now(), LinkUse: &linkUseOp{Link: linkID}})
		}
	}
	return nil
}

func applyLinkUse(st *state, op journalOp) error {
	if op.LinkUse == nil {
		return fmt.Errorf("invalid link use")
	}
	st.LinkUses[op.LinkUse.Link]++
	return nil
}
