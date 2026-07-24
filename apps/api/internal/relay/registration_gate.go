package relay

import (
	"strings"
	"sync"
)

// RegistrationGateMode controls who may register hosted identities.
const (
	RegistrationGateOpen   = "open"
	RegistrationGateInvite = "invite"
	// RegistrationGatePow requires a solved proof-of-work challenge
	// (EPIC-014 primitive; verification happens in the registration
	// handler, which holds the relay's challenge secret).
	RegistrationGatePow = "pow"
)

// RegistrationGate checks invite codes (and later PoW) before hosted registration.
type RegistrationGate struct {
	mode   string
	codes  map[string]struct{}
	mu     sync.RWMutex
}

func NewRegistrationGate(mode string, inviteCodes []string) *RegistrationGate {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = RegistrationGateOpen
	}
	g := &RegistrationGate{mode: mode, codes: make(map[string]struct{})}
	for _, c := range inviteCodes {
		c = strings.TrimSpace(c)
		if c != "" {
			g.codes[c] = struct{}{}
		}
	}
	return g
}

func (g *RegistrationGate) Mode() string {
	if g == nil {
		return RegistrationGateOpen
	}
	return g.mode
}

// AllowHosted returns nil if registration may proceed.
func (g *RegistrationGate) AllowHosted(inviteCode string) error {
	if g == nil || g.mode == RegistrationGateOpen {
		return nil
	}
	if g.mode == RegistrationGateInvite {
		code := strings.TrimSpace(inviteCode)
		if code == "" {
			return errInviteRequired
		}
		g.mu.RLock()
		_, ok := g.codes[code]
		g.mu.RUnlock()
		if !ok {
			return errInviteInvalid
		}
		return nil
	}
	return nil
}

var (
	errInviteRequired = gateError{code: "invite_required", detail: "registration requires a valid invite_code"}
	errInviteInvalid  = gateError{code: "invite_invalid", detail: "invite_code is not valid"}
)

type gateError struct {
	code   string
	detail string
}

func (e gateError) Error() string { return e.detail }
