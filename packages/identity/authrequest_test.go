package identity

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAuthRequestPayload(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	req := SignInRequest{
		PoweurAuth: SignInVersion, RequestID: "req_1", Audience: "https://oauth.poweur.org",
		Nonce: "nonce-nonce-nonce", Action: SignInActionSignin,
		IssuedAt: now.Format(time.RFC3339), ExpiresAt: now.Add(3 * time.Minute).Format(time.RFC3339),
		ResponseURI: "https://oauth.poweur.org/poweur/callback",
	}
	enc, err := EncodeSignInRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	good := AuthRequestPayload{Version: 1, Request: enc, Client: "Team dashboard", ClientHost: "grafana.example.org", ExpiresAt: req.ExpiresAt}
	raw, _ := json.Marshal(good)
	p, got, err := ParseAuthRequestPayload(raw, now)
	if err != nil || got.RequestID != "req_1" || p.Client != "Team dashboard" {
		t.Fatalf("parse = %+v %+v %v", p, got, err)
	}
	for name, mutate := range map[string]func(*AuthRequestPayload){
		"version":        func(p *AuthRequestPayload) { p.Version = 2 },
		"garbage":        func(p *AuthRequestPayload) { p.Request = "%%%" },
		"expiry differs": func(p *AuthRequestPayload) { p.ExpiresAt = now.Format(time.RFC3339) },
		"newline":        func(p *AuthRequestPayload) { p.Client = "a\nb" },
	} {
		bad := good
		mutate(&bad)
		raw, _ := json.Marshal(bad)
		if _, _, err := ParseAuthRequestPayload(raw, now); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, _, err := ParseAuthRequestPayload(raw, now.Add(time.Hour)); err == nil {
		t.Error("expired prompt accepted")
	}
	if _, _, err := ParseAuthRequestPayload([]byte(strings.Repeat("x", MaxAuthRequestBytes+1)), now); err == nil {
		t.Error("oversized prompt accepted")
	}
	if !IsKnownSystemType(MsgTypeAuthRequest) {
		t.Fatal("sys.auth.request is not registered")
	}
}

func TestTrustedAuthServices(t *testing.T) {
	p := InboxPolicy{Version: 1, Mode: InboxContactsOnly, TrustedAuthServices: []string{"Bridge.Poweur.org"}}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if !p.TrustsAuthService("bridge.poweur.org") || p.TrustsAuthService("evil.poweur.org") || p.TrustsAuthService("") {
		t.Fatal("TrustsAuthService is wrong")
	}
	for name, list := range map[string][]string{
		"bad name":  {"not a name"},
		"duplicate": {"bridge.poweur.org", "BRIDGE.poweur.org"},
		"too many":  strings.Split(strings.Repeat("a.example.org,", MaxTrustedAuthServices+1), ",")[:MaxTrustedAuthServices+1],
	} {
		bad := InboxPolicy{Version: 1, Mode: InboxOpen, TrustedAuthServices: list}
		if err := bad.Validate(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
