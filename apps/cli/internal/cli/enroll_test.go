package cli

import "testing"

func TestNormalizeRendezvousID(t *testing.T) {
	const id = "AbC-_def0123456789xyz"
	cases := []struct {
		in, want string
	}{
		{id, id},
		{"  " + id + " \n", id},
		{"AbC\u2013_def0123456789xyz", id}, // en-dash → hyphen
		{"A\u200bbC-_def0123456789xyz", "AbC-_def0123456789xyz"},
	}
	for _, tc := range cases {
		if got := normalizeRendezvousID(tc.in); got != tc.want {
			t.Errorf("normalizeRendezvousID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// Case is load-bearing: folding it would mint a different token.
	if got := normalizeRendezvousID(id); got == "abc-_def0123456789xyz" {
		t.Fatal("must not case-fold a rendezvous id")
	}
}
