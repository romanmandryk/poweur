package identity

import "testing"

func TestInboxPolicyEmailDefault(t *testing.T) {
	const op = "email.poweur.net"
	cases := []struct {
		name   string
		policy InboxPolicy
		def    string
		want   string
	}{
		{"absent block: operator default", InboxPolicy{Mode: InboxOpen}, op, op},
		{"absent block, no operator bridge", InboxPolicy{Mode: InboxOpen}, "", ""},
		{"opted out", InboxPolicy{Email: &EmailPolicy{Allow: false, Bridge: op}}, op, ""},
		{"explicit other bridge", InboxPolicy{Email: &EmailPolicy{Allow: true, Bridge: "Mail.example.org"}}, op, "mail.example.org"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.policy.EmailBridge(tc.def); got != tc.want {
				t.Fatalf("EmailBridge = %q, want %q", got, tc.want)
			}
			if tc.want != "" && !tc.policy.TrustsEmailBridgeOr(tc.want, tc.def) {
				t.Fatal("effective bridge not trusted")
			}
			if tc.policy.TrustsEmailBridgeOr("someone.poweur.net", tc.def) {
				t.Fatal("stranger trusted")
			}
		})
	}
}

func TestEmailSettings(t *testing.T) {
	s := EmailSettings{
		Version: 1, Receive: EmailReceiveContacts, UpdatedAt: "2026-10-14T12:00:00Z",
		Allow: []string{" Ann@Example.com ", "ann@example.com", "@friends.org", ""},
		Block: []string{"@spam.example"},
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(s.Allow) != 2 || s.Allow[0] != "ann@example.com" {
		t.Fatalf("allow not normalised: %v", s.Allow)
	}
	for addr, want := range map[string]bool{
		"ann@example.com": true, "ANN@example.com": true, "bob@example.com": false,
		"x@friends.org": true, "x@mail.friends.org": true, "x@notfriends.org": false, "nonsense": false,
	} {
		if got := s.Allows(addr); got != want {
			t.Errorf("Allows(%q) = %v, want %v", addr, got, want)
		}
	}
	if !s.Blocks("a@b.spam.example") || s.Blocks("a@example.com") || !s.ContactsOnly() {
		t.Fatal("block/mode wrong")
	}
	for name, bad := range map[string]EmailSettings{
		"version":  {Version: 2, UpdatedAt: "2026-10-14T12:00:00Z"},
		"mode":     {Version: 1, Receive: "nobody", UpdatedAt: "2026-10-14T12:00:00Z"},
		"time":     {Version: 1},
		"entry":    {Version: 1, UpdatedAt: "2026-10-14T12:00:00Z", Block: []string{"not an address"}},
		"domain":   {Version: 1, UpdatedAt: "2026-10-14T12:00:00Z", Block: []string{"@"}},
		"too many": {Version: 1, UpdatedAt: "2026-10-14T12:00:00Z", Block: make([]string, MaxEmailFilterEntries+1)},
	} {
		if err := bad.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
