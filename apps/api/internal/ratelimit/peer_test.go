package ratelimit

import (
	"testing"

	"github.com/poweur/api/internal/config"
)

func TestNewPeerLimiterDisabledWhenUncapped(t *testing.T) {
	cases := []struct {
		name   string
		limits config.RateLimits
		wantOn bool
	}{
		{"all zero is off", config.RateLimits{}, false},
		{"negatives are off", config.RateLimits{PerMinute: -1, PerHour: -5}, false},
		{"one capped window is on", config.RateLimits{PerDay: 3}, true},
		{"fully configured", config.RateLimits{PerMinute: 1, PerHour: 2, PerDay: 3}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lim := NewPeerLimiter(tc.limits)
			if (lim != nil) != tc.wantOn {
				t.Fatalf("limiter on = %v, want %v", lim != nil, tc.wantOn)
			}
			// A nil meter must be safe to call: callers should never have to
			// branch on whether the operator configured one.
			if d := lim.Allow("relay.example"); !d.Allowed && !tc.wantOn {
				t.Fatal("disabled meter must allow")
			}
		})
	}
}

func TestPeerLimiterMetersPerPeerNotGlobally(t *testing.T) {
	lim := NewPeerLimiter(config.RateLimits{PerMinute: 2})

	for i := 0; i < 2; i++ {
		if d := lim.Allow("cheap.example"); !d.Allowed {
			t.Fatalf("request %d rejected: %+v", i, d)
		}
	}
	d := lim.Allow("cheap.example")
	if d.Allowed {
		t.Fatal("third request from the same relay must be rejected")
	}
	if d.Scope != ScopeSenderRelay {
		t.Fatalf("scope = %q, want %q", d.Scope, ScopeSenderRelay)
	}
	if d.Window != "minute" || d.Limit != 2 {
		t.Fatalf("decision = %+v", d)
	}

	// A well-behaved relay is unaffected by a noisy neighbour: the whole
	// point of keying on the relay is that the blast radius is one relay.
	if d := lim.Allow("polite.example"); !d.Allowed {
		t.Fatalf("unrelated relay rejected: %+v", d)
	}
}

// An uncapped window must not behave like a cap of zero — that inversion is
// how a "leave it at the default" config silently blocks every request.
func TestPeerLimiterUncappedWindowsDoNotBlock(t *testing.T) {
	lim := NewPeerLimiter(config.RateLimits{PerDay: 2})
	for i := 0; i < 2; i++ {
		if d := lim.Allow("relay.example"); !d.Allowed {
			t.Fatalf("minute/hour windows must not cap: %+v", d)
		}
	}
	if d := lim.Allow("relay.example"); d.Allowed || d.Window != "day" {
		t.Fatalf("day cap should bite: %+v", d)
	}
}

func TestPeerLimiterEmptyPeerIsNotMetered(t *testing.T) {
	lim := NewPeerLimiter(config.RateLimits{PerMinute: 1})
	for i := 0; i < 5; i++ {
		if d := lim.Allow(""); !d.Allowed {
			t.Fatalf("an unattributable sender must not consume somebody else's bucket: %+v", d)
		}
	}
}
