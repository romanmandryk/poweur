package identity

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

func signInNow() time.Time { return mustTime("2026-01-15T09:30:00Z") }

func mustTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func validSignInRequest() SignInRequest {
	return SignInRequest{
		PoweurAuth:  SignInVersion,
		RequestID:   "req_001",
		Domain:      "guestbook.poweur.net",
		Audience:    "https://guestbook.poweur.net",
		Nonce:       "bm9uY2UtMDAx",
		IssuedAt:    "2026-01-15T09:29:00Z",
		ExpiresAt:   "2026-01-15T09:31:00Z",
		Action:      SignInActionSignin,
		Statement:   "Sign in to the Poweur Guestbook",
		ResponseURI: "https://guestbook.poweur.net/auth/callback",
		Scopes:      []string{"messages:send"},
	}
}

func TestNormalizeOrigin(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"plain https", "https://example.com", "https://example.com", true},
		{"uppercase host", "HTTPS://Example.COM", "https://example.com", true},
		{"default port dropped", "https://example.com:443", "https://example.com", true},
		{"custom port kept", "https://example.com:8443", "https://example.com:8443", true},
		{"http default port dropped", "http://localhost:80", "http://localhost", true},
		{"trailing slash ok", "https://example.com/", "https://example.com", true},
		{"path rejected", "https://example.com/app", "", false},
		{"query rejected", "https://example.com?a=1", "", false},
		{"fragment rejected", "https://example.com#x", "", false},
		{"userinfo rejected", "https://user@example.com", "", false},
		{"scheme rejected", "ftp://example.com", "", false},
		{"empty rejected", "", "", false},
		{"no host rejected", "https://", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeOrigin(tc.in)
			if tc.ok && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tc.ok {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestSignInAppID(t *testing.T) {
	tests := []struct {
		origin string
		want   string
		ok     bool
	}{
		{"https://guestbook.poweur.net", "net.poweur.guestbook", true},
		{"https://example.com", "com.example", true},
		{"http://localhost:8080", "", false}, // single label: no namespace
		{"https://a.b.c.d", "d.c.b.a", true},
	}
	for _, tc := range tests {
		got, err := SignInAppID(tc.origin)
		if tc.ok != (err == nil) {
			t.Fatalf("%s: ok=%v err=%v", tc.origin, tc.ok, err)
		}
		if tc.ok && got != tc.want {
			t.Fatalf("%s: got %q want %q", tc.origin, got, tc.want)
		}
	}
}

func TestNormalizeSignInScopes(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
		ok   bool
	}{
		{"sorted and deduped", []string{"profile:read", "messages:send", "profile:read"},
			[]string{"messages:send", "profile:read"}, true},
		{"dav scopes are gone", []string{"dav:rw:apps/x.y"}, nil, false},
		{"messages", []string{"messages:send"}, []string{"messages:send"}, true},
		{"empty stays empty", nil, nil, true},
		{"unknown scope", []string{"admin:everything"}, nil, false},
		{"dav without path", []string{"dav:rw:"}, nil, false},
		{"traversal", []string{"dav:rw:apps/../poweur-sys"}, nil, false},
		{"empty entry", []string{""}, nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeSignInScopes(tc.in)
			if tc.ok != (err == nil) {
				t.Fatalf("ok=%v err=%v", tc.ok, err)
			}
			if !tc.ok {
				return
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestSignInRequestValidate(t *testing.T) {
	now := signInNow()
	tests := []struct {
		name   string
		mutate func(*SignInRequest)
		errIs  error
	}{
		{"valid", func(*SignInRequest) {}, nil},
		{"bad version", func(r *SignInRequest) { r.PoweurAuth = "2" }, ErrSignInVersion},
		{"missing nonce", func(r *SignInRequest) { r.Nonce = "" }, ErrSignInMalformed},
		{"missing request id", func(r *SignInRequest) { r.RequestID = "" }, ErrSignInMalformed},
		{"bad action", func(r *SignInRequest) { r.Action = "transfer-funds" }, ErrSignInAction},
		{"multiline statement", func(r *SignInRequest) { r.Statement = "one\ntwo" }, ErrSignInMalformed},
		{"long statement", func(r *SignInRequest) { r.Statement = strings.Repeat("x", SignInMaxStatementLen+1) }, ErrSignInMalformed},
		{"expired", func(r *SignInRequest) {
			r.IssuedAt = "2026-01-15T09:00:00Z"
			r.ExpiresAt = "2026-01-15T09:03:00Z"
		}, ErrSignInExpired},
		{"ttl too long", func(r *SignInRequest) {
			r.IssuedAt = "2026-01-15T09:29:00Z"
			r.ExpiresAt = "2026-01-15T09:40:00Z"
		}, ErrSignInTTL},
		{"issued in the future", func(r *SignInRequest) {
			r.IssuedAt = "2026-01-15T09:40:00Z"
			r.ExpiresAt = "2026-01-15T09:44:00Z"
		}, ErrSignInNotYet},
		{"cross-origin response_uri", func(r *SignInRequest) {
			r.ResponseURI = "https://evil.example/collect"
		}, ErrSignInAudience},
		{"scope outside namespace", func(r *SignInRequest) {
			r.Scopes = []string{"dav:rw:apps/net.poweur.other"}
		}, ErrSignInScope},
		{"bad audience", func(r *SignInRequest) { r.Audience = "not-a-url" }, ErrSignInMalformed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := validSignInRequest()
			tc.mutate(&req)
			err := req.Validate(now)
			if tc.errIs == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if !errors.Is(err, tc.errIs) {
				t.Fatalf("got %v want %v", err, tc.errIs)
			}
		})
	}
}

func TestSignInRequestNormalizeFillsDomainAndScopes(t *testing.T) {
	req := SignInRequest{
		Audience:  "HTTPS://Guestbook.Poweur.NET:443/",
		RequestID: " req_1 ",
		Nonce:     " n ",
		Action:    "SignIn",
		IssuedAt:  "2026-01-15T09:29:00Z",
		ExpiresAt: "2026-01-15T09:31:00Z",
		Scopes:    []string{"profile:read", "messages:send"},
	}
	n, err := req.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if n.Audience != "https://guestbook.poweur.net" {
		t.Fatalf("audience %q", n.Audience)
	}
	if n.Domain != "guestbook.poweur.net" {
		t.Fatalf("domain %q", n.Domain)
	}
	if n.Action != "signin" || n.RequestID != "req_1" || n.Nonce != "n" {
		t.Fatalf("normalize: %+v", n)
	}
	if strings.Join(n.Scopes, ",") != "messages:send,profile:read" {
		t.Fatalf("scopes %v", n.Scopes)
	}
	if err := n.Validate(signInNow()); err != nil {
		t.Fatalf("normalized request should validate: %v", err)
	}
}

func TestCanonicalSignInResponseShape(t *testing.T) {
	req := validSignInRequest()
	n, err := req.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	resp, err := NewSignInResponse(n, "Alice.Poweur.NET", SignInKeyIDIdentity)
	if err != nil {
		t.Fatal(err)
	}
	got := resp.Canonical()
	want := strings.Join([]string{
		"poweur-signin",
		"1",
		"req_001",
		"alice.poweur.net",
		"https://guestbook.poweur.net",
		"bm9uY2UtMDAx",
		"2026-01-15T09:29:00Z",
		"2026-01-15T09:31:00Z",
		"signin",
		"Sign in to the Poweur Guestbook",
		"messages:send",
		"identity",
	}, "\n")
	if got != want {
		t.Fatalf("canonical mismatch\n got: %q\nwant: %q", got, want)
	}
	if strings.Count(got, "\n") != 11 {
		t.Fatalf("expected 12 lines, got %d", strings.Count(got, "\n")+1)
	}
}

func TestSignInResponseWindowCopiesRequest(t *testing.T) {
	req := validSignInRequest()
	resp, err := NewSignInResponse(req, "alice.poweur.net", "")
	if err != nil {
		t.Fatal(err)
	}
	if resp.IssuedAt != req.IssuedAt || resp.ExpiresAt != req.ExpiresAt {
		t.Fatalf("approval must not outlive the challenge: %+v", resp)
	}
	if resp.KeyID != SignInKeyIDIdentity {
		t.Fatalf("default key_id %q", resp.KeyID)
	}
}

func TestNewSignInResponseRejectsBadKeyID(t *testing.T) {
	if _, err := NewSignInResponse(validSignInRequest(), "alice.poweur.net", "device-42"); err == nil {
		t.Fatal("expected key_id rejection")
	}
	resp, err := NewSignInResponse(validSignInRequest(), "alice.poweur.net", SignInKeyIDSessionPrefix+"sess_1")
	if err != nil {
		t.Fatal(err)
	}
	if SessionIDFromKeyID(resp.KeyID) != "sess_1" {
		t.Fatalf("session id %q", SessionIDFromKeyID(resp.KeyID))
	}
	if SessionIDFromKeyID(SignInKeyIDIdentity) != "" {
		t.Fatal("identity key_id must not yield a session id")
	}
}

func TestSignInRequestEncodeRoundTrip(t *testing.T) {
	req, err := validSignInRequest().Normalize()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeSignInRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	back, err := DecodeSignInRequest(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if back.RequestID != req.RequestID || back.Audience != req.Audience {
		t.Fatalf("round trip lost data: %+v", back)
	}
	// A pasted raw-JSON request must parse too.
	rawJSON, err := DecodeSignInRequest(`{"poweur_auth":"1","request_id":"r","domain":"d.example","audience":"https://d.example","nonce":"n","issued_at":"2026-01-15T09:29:00Z","expires_at":"2026-01-15T09:31:00Z","action":"signin"}`)
	if err != nil {
		t.Fatal(err)
	}
	if rawJSON.RequestID != "r" {
		t.Fatalf("raw json parse: %+v", rawJSON)
	}
	if _, err := DecodeSignInRequest(""); err == nil {
		t.Fatal("empty request must fail")
	}
	if _, err := DecodeSignInRequest("!!!not base64!!!"); err == nil {
		t.Fatal("garbage must fail")
	}
	// Unknown fields are rejected: a signer must not silently ignore a
	// field a future version made security-relevant.
	if _, err := DecodeSignInRequest(`{"poweur_auth":"1","surprise":true}`); err == nil {
		t.Fatal("unknown field must fail")
	}
}

func TestSignInLinks(t *testing.T) {
	req, err := validSignInRequest().Normalize()
	if err != nil {
		t.Fatal(err)
	}
	deep, err := SignInDeepLink(req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(deep, "poweur://auth?request=") {
		t.Fatalf("deep link %q", deep)
	}
	web, err := SignInWebLink("https://poweur.net/app/", req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(web, "https://poweur.net/app/?auth=") {
		t.Fatalf("web link %q", web)
	}
	web2, err := SignInWebLink("https://poweur.net/app/?x=1", req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(web2, "?x=1&auth=") {
		t.Fatalf("web link with query %q", web2)
	}
}

func TestVerifySessionProof(t *testing.T) {
	priv := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	pub := priv.Public().(ed25519.PublicKey)
	sessPub, _, err := ed25519.GenerateKey(deterministicReader{})
	if err != nil {
		t.Fatal(err)
	}
	sessKey := base64.RawURLEncoding.EncodeToString(sessPub)
	issued := "2026-01-15T09:00:00Z"
	expires := "2026-01-16T09:00:00Z"
	sign := func(identity, key, iat, exp, nonce string) string {
		canonical := CanonicalSessionRegistration(identity, key, iat, exp, nonce)
		return base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, []byte(canonical)))
	}
	good := SignInSessionProof{
		SessionPublicKey:  sessKey,
		IssuedAt:          issued,
		ExpiresAt:         expires,
		Nonce:             "n1",
		IdentitySignature: sign("alice.poweur.net", sessKey, issued, expires, "n1"),
	}

	if _, err := VerifySessionProof(pub, "alice.poweur.net", good, signInNow()); err != nil {
		t.Fatalf("valid proof rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*SignInSessionProof)
		now    time.Time
	}{
		{"incomplete", func(p *SignInSessionProof) { p.Nonce = "" }, signInNow()},
		{"bad issued_at", func(p *SignInSessionProof) { p.IssuedAt = "yesterday" }, signInNow()},
		{"bad expires_at", func(p *SignInSessionProof) { p.ExpiresAt = "tomorrow" }, signInNow()},
		{"expired", func(*SignInSessionProof) {}, mustTime("2026-01-17T09:00:00Z")},
		{"ttl too long", func(p *SignInSessionProof) {
			p.ExpiresAt = "2026-01-20T09:00:00Z"
			p.IdentitySignature = sign("alice.poweur.net", sessKey, issued, "2026-01-20T09:00:00Z", "n1")
		}, signInNow()},
		{"bad key", func(p *SignInSessionProof) { p.SessionPublicKey = "not-a-key" }, signInNow()},
		{"tampered nonce", func(p *SignInSessionProof) { p.Nonce = "n2" }, signInNow()},
		{"garbage signature", func(p *SignInSessionProof) { p.IdentitySignature = "AAAA" }, signInNow()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			proof := good
			tc.mutate(&proof)
			if _, err := VerifySessionProof(pub, "alice.poweur.net", proof, tc.now); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}

	// A proof issued for a different identity must not transfer.
	if _, err := VerifySessionProof(pub, "bob.poweur.net", good, signInNow()); err == nil {
		t.Fatal("proof must be bound to the identity name")
	}
}

// deterministicReader gives ed25519.GenerateKey a fixed stream so the test
// key is stable across runs.
type deterministicReader struct{}

func (deterministicReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(i%251) + 1
	}
	return len(p), nil
}
