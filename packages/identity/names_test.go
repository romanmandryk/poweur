package identity

import "testing"

func TestValidateClaimableName_Reserved(t *testing.T) {
	tests := []struct {
		name      string
		claimable bool
	}{
		// Mail DNS names published for poweur.net's mail server.
		{"mta-sts.poweur.net", false},
		{"autoconfig.poweur.net", false},
		{"autodiscover.poweur.net", false},
		// Email role names (EPIC-023 E23-T5).
		{"postmaster.poweur.net", false},
		{"abuse.poweur.net", false},
		{"hostmaster.poweur.net", false},
		{"webmaster.poweur.net", false},
		{"mailer-daemon.poweur.net", false},
		{"noreply.poweur.net", false},
		{"no-reply.poweur.net", false},
		{"bounce.poweur.net", false},
		{"bounces.poweur.net", false},
		{"dmarc.poweur.net", false},
		{"email.poweur.net", false},
		{"security.poweur.net", false},
		{"support.poweur.net", false},
		// Case and surrounding space do not dodge the list.
		{"MTA-STS.poweur.net", false},
		{" Autodiscover.poweur.net ", false},
		// Only the leftmost label is reserved, and only as a whole label.
		{"alice.email.poweur.net", true},
		{"emails.poweur.net", true},
		{"mta-sts2.poweur.net", true},
		{"alice.poweur.net", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateClaimableName(tt.name)
			if got := err == nil; got != tt.claimable {
				t.Fatalf("ValidateClaimableName(%q) claimable = %v, want %v (err %v)", tt.name, got, tt.claimable, err)
			}
			// Reserved names stay usable: an operator may create them.
			if err := ValidateIdentityName(tt.name); err != nil {
				t.Fatalf("ValidateIdentityName(%q): %v", tt.name, err)
			}
		})
	}
}

func TestValidateHostedHandle_ReservedMailNames(t *testing.T) {
	for _, name := range []string{"mta-sts.poweur.net", "autoconfig.poweur.net", "autodiscover.poweur.net", "mailer-daemon.poweur.net", "dmarc.poweur.net"} {
		if err := ValidateHostedHandle(name); err == nil {
			t.Fatalf("hosted handle %q accepted", name)
		}
	}
}
