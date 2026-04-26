package ratelimit

import (
	"testing"
	"time"

	"github.com/eurything/api/internal/config"
)

func TestLimiterAllowAndGlobalRejection(t *testing.T) {
	lim := NewLimiter(
		config.RateLimits{PerMinute: 100, PerHour: 1000, PerDay: 10000},
		config.GlobalRateLimits{PerMinute: 2, PerHour: 0, PerDay: 0},
	)
	if d := lim.Allow("alice"); !d.Allowed {
		t.Fatalf("first: got %+v", d)
	}
	if d := lim.Allow("alice"); !d.Allowed {
		t.Fatalf("second: got %+v", d)
	}
	d := lim.Allow("bob")
	if d.Allowed {
		t.Fatalf("expected global minute rejection, got %+v", d)
	}
	if d.Scope != "global" || d.Window != "minute" {
		t.Fatalf("scope/window: %+v", d)
	}
}

func TestLimiterPerSenderRejectsFirst(t *testing.T) {
	lim := NewLimiter(
		config.RateLimits{PerMinute: 1, PerHour: 1000, PerDay: 10000},
		config.GlobalRateLimits{PerMinute: 0, PerHour: 0, PerDay: 0},
	)
	if !lim.Allow("u").Allowed {
		t.Fatal("first")
	}
	d := lim.Allow("u")
	if d.Allowed || d.Scope != "sender" {
		t.Fatalf("got %+v", d)
	}
}

func TestRollWindowsResetsOldMinute(t *testing.T) {
	e := &entry{
		minuteCount: 5,
		minuteReset: time.Now().Add(-time.Second),
		hourCount:   0,
		hourReset:   time.Now().Add(time.Hour),
		dayCount:    0,
		dayReset:    time.Now().Add(24 * time.Hour),
	}
	rollWindows(e, time.Now())
	if e.minuteCount != 0 {
		t.Fatalf("minute count not reset: %d", e.minuteCount)
	}
}
