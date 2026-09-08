// Package ratelimit implements the relay's per-sender and global
// token-bucket-style rate limits. Both are evaluated on every
// open/messaging-class request before signature verification, matching
// the cheap-rejection ordering documented in the API reference.
package ratelimit

import (
	"sync"
	"time"

	"github.com/poweur/api/internal/config"
)

// Limiter tracks per-sender and global request budgets.
//
// Per-sender entries are sharded by identity. The global counters are a
// single shared bucket that ticks independently of any sender. Because the
// global bucket is a back-stop against many-sender DDoS, it is checked
// AFTER the per-sender check (i.e. one noisy sender is rejected on its own
// quota before they get a chance to consume global budget).
type Limiter struct {
	mu      sync.Mutex
	entries map[string]*entry
	limits  config.RateLimits
	gLimits config.GlobalRateLimits
	global  entry
}

type entry struct {
	minuteCount int
	hourCount   int
	dayCount    int
	minuteReset time.Time
	hourReset   time.Time
	dayReset    time.Time
}

// Decision is the outcome of an Allow() call. When Allowed is false the
// caller writes a 429 with Window/Limit/ResetAt; Scope distinguishes
// per-sender ("sender") from global ("global") so logs and clients can
// tell them apart.
type Decision struct {
	Allowed bool
	Scope   string
	Window  string
	Limit   int
	ResetAt time.Time
}

// NewLimiter builds a Limiter from per-sender and global caps.
func NewLimiter(limits config.RateLimits, globalLimits config.GlobalRateLimits) *Limiter {
	now := time.Now()
	return &Limiter{
		entries: make(map[string]*entry),
		limits:  limits,
		gLimits: globalLimits,
		global: entry{
			minuteReset: now.Add(time.Minute),
			hourReset:   now.Add(time.Hour),
			dayReset:    now.Add(24 * time.Hour),
		},
	}
}

// Allow charges one request to the given sender (and to the global
// bucket). On rejection the buckets are NOT credited back: a small
// over-count under contention is acceptable and avoids a second pass.
func (l *Limiter) Allow(identity string) Decision {
	return l.AllowCost(identity, 1)
}

// AllowCost charges `cost` units instead of one, which is how a cheap-to-probe
// endpoint gets a tighter cap than messages without a second limiter and a
// second set of tunables: the availability lookup (EPIC-018 E18-T2) is an
// enumeration oracle over registered identities, so it charges several units
// per call and exhausts its bucket proportionally sooner.
func (l *Limiter) AllowCost(identity string, cost int) Decision {
	if cost < 1 {
		cost = 1
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	e, ok := l.entries[identity]
	if !ok {
		e = &entry{
			minuteReset: now.Add(time.Minute),
			hourReset:   now.Add(time.Hour),
			dayReset:    now.Add(24 * time.Hour),
		}
		l.entries[identity] = e
	}

	rollWindows(e, now)

	e.minuteCount += cost
	if e.minuteCount > l.limits.PerMinute {
		return Decision{Allowed: false, Scope: "sender", Window: "minute", Limit: l.limits.PerMinute, ResetAt: e.minuteReset}
	}
	e.hourCount += cost
	if e.hourCount > l.limits.PerHour {
		return Decision{Allowed: false, Scope: "sender", Window: "hour", Limit: l.limits.PerHour, ResetAt: e.hourReset}
	}
	e.dayCount += cost
	if e.dayCount > l.limits.PerDay {
		return Decision{Allowed: false, Scope: "sender", Window: "day", Limit: l.limits.PerDay, ResetAt: e.dayReset}
	}

	rollWindows(&l.global, now)

	if l.gLimits.PerMinute > 0 {
		l.global.minuteCount++
		if l.global.minuteCount > l.gLimits.PerMinute {
			return Decision{Allowed: false, Scope: "global", Window: "minute", Limit: l.gLimits.PerMinute, ResetAt: l.global.minuteReset}
		}
	}
	if l.gLimits.PerHour > 0 {
		l.global.hourCount++
		if l.global.hourCount > l.gLimits.PerHour {
			return Decision{Allowed: false, Scope: "global", Window: "hour", Limit: l.gLimits.PerHour, ResetAt: l.global.hourReset}
		}
	}
	if l.gLimits.PerDay > 0 {
		l.global.dayCount++
		if l.global.dayCount > l.gLimits.PerDay {
			return Decision{Allowed: false, Scope: "global", Window: "day", Limit: l.gLimits.PerDay, ResetAt: l.global.dayReset}
		}
	}

	return Decision{Allowed: true}
}

func rollWindows(e *entry, now time.Time) {
	if now.After(e.minuteReset) {
		e.minuteCount = 0
		e.minuteReset = now.Add(time.Minute)
	}
	if now.After(e.hourReset) {
		e.hourCount = 0
		e.hourReset = now.Add(time.Hour)
	}
	if now.After(e.dayReset) {
		e.dayCount = 0
		e.dayReset = now.Add(24 * time.Hour)
	}
}
