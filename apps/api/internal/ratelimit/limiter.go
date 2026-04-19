package ratelimit

import (
	"sync"
	"time"

	"github.com/eurything/api/internal/config"
)

type Limiter struct {
	mu      sync.Mutex
	entries map[string]*entry
	limits  config.RateLimits
}

type entry struct {
	minuteCount int
	hourCount   int
	dayCount    int
	minuteReset time.Time
	hourReset   time.Time
	dayReset    time.Time
}

type Decision struct {
	Allowed bool
	Window  string
	Limit   int
	ResetAt time.Time
}

func NewLimiter(limits config.RateLimits) *Limiter {
	return &Limiter{
		entries: make(map[string]*entry),
		limits:  limits,
	}
}

func (l *Limiter) Allow(identity string) Decision {
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

	e.minuteCount++
	if e.minuteCount > l.limits.PerMinute {
		return Decision{Allowed: false, Window: "minute", Limit: l.limits.PerMinute, ResetAt: e.minuteReset}
	}

	e.hourCount++
	if e.hourCount > l.limits.PerHour {
		return Decision{Allowed: false, Window: "hour", Limit: l.limits.PerHour, ResetAt: e.hourReset}
	}

	e.dayCount++
	if e.dayCount > l.limits.PerDay {
		return Decision{Allowed: false, Window: "day", Limit: l.limits.PerDay, ResetAt: e.dayReset}
	}

	return Decision{Allowed: true}
}
