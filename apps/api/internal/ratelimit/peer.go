package ratelimit

import (
	"math"

	"github.com/poweur/api/internal/config"
)

// PeerLimiter meters a *relay* rather than an identity (EPIC-007 E07-T5).
//
// Recipient consent stops one stranger from talking to you. It does not stop
// a thousand strangers from asking, and hosted registration makes those
// thousand strangers cheap — which is exactly why a per-identity cap cannot
// see the attack: a fresh identity per request never exceeds its own quota.
// What the requests never change is the relay that carried them, so that is
// what this meters.
//
// The unit of accountability is deliberate. A relay is a name with an
// operator, DNS, and a reputation; an identity on a hosted relay is a
// registration form. Metering the relay puts the cost where somebody can
// actually respond to it — including by policing their own users, which is
// the behaviour a blocklist ecosystem later rewards (see
// `docs/trust/relay-reputation.md`).
//
// Zero in a window means unlimited here, matching every other relay-wide
// knob (global limits, stream caps, spool TTL) — a nil *PeerLimiter* means
// the whole meter is off, and Allow on it says yes.
type PeerLimiter struct {
	inner *Limiter
}

// NewPeerLimiter builds a per-peer meter, or nil when no window is capped.
func NewPeerLimiter(limits config.RateLimits) *PeerLimiter {
	if limits.PerMinute <= 0 && limits.PerHour <= 0 && limits.PerDay <= 0 {
		return nil
	}
	return &PeerLimiter{inner: NewLimiter(config.RateLimits{
		PerMinute: orUnlimited(limits.PerMinute),
		PerHour:   orUnlimited(limits.PerHour),
		PerDay:    orUnlimited(limits.PerDay),
	}, config.GlobalRateLimits{})}
}

// Allow charges one event to peer. The nil receiver always allows, so
// callers never have to ask whether the meter is configured.
func (p *PeerLimiter) Allow(peer string) Decision {
	if p == nil || peer == "" {
		return Decision{Allowed: true}
	}
	decision := p.inner.Allow(peer)
	if !decision.Allowed {
		// The inner limiter calls every per-key rejection "sender"; here the
		// key is a relay, and a client told "sender" would go looking at the
		// wrong thing.
		decision.Scope = ScopeSenderRelay
	}
	return decision
}

// ScopeSenderRelay is the Decision.Scope a peer-relay rejection carries.
const ScopeSenderRelay = "sender_relay"

func orUnlimited(n int) int {
	if n <= 0 {
		return math.MaxInt32
	}
	return n
}
