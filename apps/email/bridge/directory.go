package bridge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/poweur/identity"
)

// Directory answers "who is this address?" from the network itself: the
// address names an identity, and that identity's signed email.json says
// whether it takes mail through this bridge. There is no user table.
type Directory struct {
	cfg  Config
	now  func() time.Time
	mu   sync.Mutex
	hits map[string]dirEntry
}

type dirEntry struct {
	r   Recipient
	err error
	at  time.Time
}

// Recipient is a routable mailbox.
type Recipient struct {
	Address  string // the address as written (lowercased, without the tag)
	Identity string
	Tag      string
	OptIn    identity.EmailOptIn
	// PublicKey is the identity's signing key, checked against the opt-in.
	PublicKey []byte
}

// Lookup errors. ErrNoMailbox and ErrNotServed are permanent (5xx); anything
// else is a temporary failure the mail server should retry (4xx).
var (
	ErrNoMailbox = errors.New("no such mailbox")
	ErrNotServed = errors.New("domain not served by this bridge")
)

// NewDirectory builds a directory over cfg.
func NewDirectory(cfg Config) *Directory {
	return &Directory{cfg: cfg, now: time.Now, hits: map[string]dirEntry{}}
}

// Route maps an address to the identity it would deliver to, without any
// network lookup.
func (d *Directory) Route(address string) (id string, route identity.EmailRoute, err error) {
	route, err = identity.ParseEmailAddress(address)
	if err != nil {
		if _, ok := d.cfg.IdentityDomain(domainOf(address)); ok {
			return "", route, ErrNoMailbox
		}
		return "", route, ErrNotServed
	}
	if target, ok := d.cfg.Aliases[route.Local+"@"+route.Domain]; ok {
		return target, route, nil
	}
	idDomain, ok := d.cfg.IdentityDomain(route.Domain)
	if !ok {
		return "", route, ErrNotServed
	}
	return route.Local + "." + idDomain, route, nil
}

func domainOf(address string) string {
	address = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(address), "<"), ">")
	if at := strings.LastIndex(address, "@"); at >= 0 {
		return strings.ToLower(address[at+1:])
	}
	return ""
}

// Lookup resolves an address to an opted-in recipient. Results — including
// "no such mailbox" — are cached for OptInCacheTTL, so opting out takes
// effect within that window and a flood of RCPTs to one name costs one
// lookup. Temporary failures are not cached.
func (d *Directory) Lookup(ctx context.Context, address string) (Recipient, error) {
	id, route, err := d.Route(address)
	if err != nil {
		return Recipient{}, err
	}
	r, err := d.Opted(ctx, id)
	return withRoute(r, route), err
}

// Opted returns an identity's opt-in through this bridge, cached like Lookup.
func (d *Directory) Opted(ctx context.Context, id string) (Recipient, error) {
	id = strings.ToLower(strings.TrimSpace(id))
	if id == strings.ToLower(d.cfg.BridgeID) {
		return Recipient{}, ErrNoMailbox // the bridge has no mailbox of its own
	}
	d.mu.Lock()
	if e, ok := d.hits[id]; ok && d.now().Sub(e.at) < d.cfg.OptInCacheTTL {
		d.mu.Unlock()
		return e.r, e.err
	}
	d.mu.Unlock()

	r, err := d.lookup(ctx, id)
	if err == nil || errors.Is(err, ErrNoMailbox) {
		d.mu.Lock()
		if len(d.hits) > 100000 {
			d.hits = map[string]dirEntry{}
		}
		d.hits[id] = dirEntry{r: r, err: err, at: d.now()}
		d.mu.Unlock()
	}
	return r, err
}

func withRoute(r Recipient, route identity.EmailRoute) Recipient {
	if r.Identity == "" {
		return r
	}
	r.Address = route.Local + "@" + route.Domain
	r.Tag = route.Tag
	return r
}

// Forget drops a cached answer (after a delivery saw the opt-in change).
func (d *Directory) Forget(id string) {
	d.mu.Lock()
	delete(d.hits, strings.ToLower(id))
	d.mu.Unlock()
}

func (d *Directory) lookup(ctx context.Context, id string) (Recipient, error) {
	if err := identity.ValidateIdentityName(id); err != nil {
		return Recipient{}, ErrNoMailbox
	}
	res, err := identity.Resolve(ctx, id, d.cfg.Resolve)
	if err != nil {
		if isNotFound(err) {
			return Recipient{}, ErrNoMailbox
		}
		return Recipient{}, fmt.Errorf("resolve %s: %w", id, err)
	}
	pub, err := identity.ParseEd25519PublicKey(res.Document.PublicKey)
	if err != nil {
		return Recipient{}, ErrNoMailbox
	}
	raw, err := identity.FetchPublicFile(ctx, id, identity.WellKnownEmailPath, d.cfg.Resolve)
	if errors.Is(err, identity.ErrPublicFileAbsent) {
		return Recipient{}, ErrNoMailbox
	}
	if err != nil {
		return Recipient{}, fmt.Errorf("fetch email.json of %s: %w", id, err)
	}
	optIn, err := identity.ParseEmailOptIn(raw)
	if err != nil || !strings.EqualFold(optIn.Identity, id) || !strings.EqualFold(optIn.Bridge, d.cfg.BridgeID) {
		return Recipient{}, ErrNoMailbox
	}
	if err := optIn.VerifySignature(pub); err != nil {
		return Recipient{}, ErrNoMailbox
	}
	return Recipient{Identity: id, OptIn: optIn, PublicKey: pub}, nil
}

// isNotFound reports a resolver answer that means "no such identity" rather
// than "could not ask": the identity's host answered 404/410, or its name
// does not exist. A timeout or refused connection is not one — those must
// stay temporary, or a relay outage would bounce mail.
func isNotFound(err error) bool {
	msg := strings.ToLower(err.Error())
	for _, s := range []string{"well-known returned 404", "well-known returned 410", "no such host"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}
