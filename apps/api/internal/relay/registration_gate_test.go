package relay

import "testing"

func TestRegistrationGateOpen(t *testing.T) {
	g := NewRegistrationGate("open", nil)
	if err := g.AllowHosted(""); err != nil {
		t.Fatal(err)
	}
}

func TestRegistrationGateInvite(t *testing.T) {
	g := NewRegistrationGate("invite", []string{"secret-code"})
	if err := g.AllowHosted(""); err == nil {
		t.Fatal("expected invite required")
	}
	if err := g.AllowHosted("wrong"); err == nil {
		t.Fatal("expected invalid invite")
	}
	if err := g.AllowHosted("secret-code"); err != nil {
		t.Fatal(err)
	}
}
