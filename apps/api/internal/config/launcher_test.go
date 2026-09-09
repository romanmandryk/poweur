package config

import (
	"strings"
	"testing"
)

// The default expands to both `id.<d>` and the bare `<d>` — the apex is the
// commonest thing a human types and used to get the JSON service banner
// (EPIC-015 E15-T7).
func TestLauncherHostsDefaultsToIdAndApex(t *testing.T) {
	t.Setenv("HOSTED_DOMAINS", "poweur.net,example.org")
	t.Setenv("LAUNCHER_HOST", "")
	t.Setenv("LAUNCHER_HOSTS", "")

	got := launcherHostsFromEnv()
	want := []string{"id.poweur.net", "poweur.net", "id.example.org", "example.org"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("hosts = %v, want %v", got, want)
	}
	if h := launcherHostFromEnv(); h != "id.poweur.net" {
		t.Fatalf("canonical launcher = %q, want id.poweur.net", h)
	}
}

// An operator who named one host meant one host. Adding the apex for them
// would serve the SPA on a name they never asked about.
func TestLauncherHostsExplicitSingleDoesNotExpand(t *testing.T) {
	t.Setenv("HOSTED_DOMAINS", "poweur.net")
	t.Setenv("LAUNCHER_HOSTS", "")
	t.Setenv("LAUNCHER_HOST", "Join.Poweur.Net.")

	got := launcherHostsFromEnv()
	if len(got) != 1 || got[0] != "join.poweur.net" {
		t.Fatalf("hosts = %v, want [join.poweur.net]", got)
	}
}

func TestLauncherHostsCSVWinsAndDeduplicates(t *testing.T) {
	t.Setenv("HOSTED_DOMAINS", "poweur.net")
	t.Setenv("LAUNCHER_HOST", "id.poweur.net")
	t.Setenv("LAUNCHER_HOSTS", "id.poweur.net, poweur.net ,ID.POWEUR.NET,")

	got := launcherHostsFromEnv()
	want := []string{"id.poweur.net", "poweur.net"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("hosts = %v, want %v", got, want)
	}
}

func TestLauncherHostsEmptyWithoutHostedDomains(t *testing.T) {
	t.Setenv("HOSTED_DOMAINS", "")
	t.Setenv("LAUNCHER_HOST", "")
	t.Setenv("LAUNCHER_HOSTS", "")

	if got := launcherHostsFromEnv(); len(got) != 0 {
		t.Fatalf("hosts = %v, want none", got)
	}
}

func TestIsLauncherHost(t *testing.T) {
	c := Config{
		LauncherHost:  "id.poweur.net",
		LauncherHosts: []string{"id.poweur.net", "poweur.net"},
	}
	cases := []struct {
		host string
		want bool
	}{
		{"id.poweur.net", true},
		{"poweur.net", true},
		{"ID.Poweur.NET", true},
		{"poweur.net.", true}, // a trailing root dot is the same host
		{" poweur.net ", true},
		{"bob.poweur.net", false},
		{"id.poweur.net.evil.example", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := c.IsLauncherHost(tc.host); got != tc.want {
			t.Errorf("IsLauncherHost(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}

// Every test relay and the integration harness build a Config literal; those
// set LauncherHost alone, so the set must not be the only thing consulted.
func TestIsLauncherHostFallsBackToSingleValue(t *testing.T) {
	c := Config{LauncherHost: "id.poweur.net"}
	if !c.IsLauncherHost("id.poweur.net") {
		t.Fatal("a Config literal with only LauncherHost must still match")
	}
	if c.IsLauncherHost("poweur.net") {
		t.Fatal("the apex is not implied by a bare LauncherHost")
	}
}
