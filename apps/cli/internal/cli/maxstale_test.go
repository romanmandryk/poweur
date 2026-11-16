package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/poweur/cli/internal/config"
	"github.com/poweur/cli/internal/identity"
)

func TestApplyMaxStale_Precedence(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("POWEUR_MAX_STALE", "")
	t.Cleanup(func() { identity.SetMaxStale(identity.DefaultMaxStale) })

	check := func(flag string, want time.Duration) {
		t.Helper()
		if err := applyMaxStale(flag); err != nil {
			t.Fatal(err)
		}
		if got := identity.MaxStale(); got != want {
			t.Fatalf("max stale = %s, want %s", got, want)
		}
	}
	check("", identity.DefaultMaxStale)

	cfg := config.DefaultConfig()
	cfg.MaxStale = "48h"
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	check("", 48*time.Hour)

	t.Setenv("POWEUR_MAX_STALE", "12h")
	check("", 12*time.Hour)
	check("90m", 90*time.Minute)
	check("0", 0)

	// Each run starts from the configured value, not the last flag.
	check("", 12*time.Hour)
}

func TestApplyMaxStale_RejectsNonsense(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("POWEUR_MAX_STALE", "")
	t.Cleanup(func() { identity.SetMaxStale(identity.DefaultMaxStale) })
	for _, bad := range []string{"soon", "-5h", "30"} {
		err := applyMaxStale(bad)
		if err == nil || !strings.Contains(err.Error(), "--max-stale") {
			t.Errorf("applyMaxStale(%q) = %v", bad, err)
		}
	}
}

func TestFetchedAgo(t *testing.T) {
	now := time.Now()
	for age, want := range map[time.Duration]string{
		10 * time.Second:   "just now",
		5 * time.Minute:    "5m ago",
		3 * time.Hour:      "3h ago",
		7 * 24 * time.Hour: "7d ago",
	} {
		if got := fetchedAgo(now.Add(-age)); got != want {
			t.Errorf("fetchedAgo(%s) = %q, want %q", age, got, want)
		}
	}
}
