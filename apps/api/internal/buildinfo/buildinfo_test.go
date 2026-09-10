package buildinfo

import "testing"

func TestFormatTime(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"  ", ""},
		{"2026-09-10T12:00:00Z", "2026-09-10 12:00"},
		{"2026-09-10T13:05:09+01:00", "2026-09-10 12:05"},
		{"2026-09-10 12:00", "2026-09-10 12:00"},
		{"not-a-date", "not-a-date"},
	}
	for _, tc := range cases {
		if got := FormatTime(tc.in); got != tc.want {
			t.Errorf("FormatTime(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestVersionIsSemver(t *testing.T) {
	if Version == "" {
		t.Fatal("Version must not be empty")
	}
	for _, c := range Version {
		if c != '.' && (c < '0' || c > '9') {
			t.Fatalf("Version %q is not a dotted numeric semver", Version)
		}
	}
}
